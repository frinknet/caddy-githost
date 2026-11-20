package backend

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/frinknet/caddy-gitproxy/pkg/access"
	"github.com/frinknet/caddy-gitproxy/pkg/repo"
	"github.com/go-git/go-billy/v6/osfs"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/cache"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/go-git/go-git/v6/storage"
	"github.com/go-git/go-git/v6/storage/filesystem"
	"github.com/go-git/go-git/v6/utils/ioutil"
)

type GoGit struct {
	locks sync.Map
}

func New() *GoGit {
	return &GoGit{}
}

func (b *GoGit) UploadPack(ctx context.Context, req Request, stdin io.Reader, stdout, _ io.Writer) error {
	st, err := openStore(req.Dir)
	if err != nil {
		return err
	}

	lock := b.repoLock(req.Dir)
	lock.RLock()
	defer lock.RUnlock()

	return transport.UploadPack(ctx, st,
		io.NopCloser(stdin),
		ioutil.WriteNopCloser(stdout),
		&transport.UploadPackRequest{
			GitProtocol:   req.GitProtocol,
			AdvertiseRefs: false,
			StatelessRPC:  false,
		})
}

func (b *GoGit) ReceivePack(ctx context.Context, req Request, stdin io.Reader, stdout, _ io.Writer) error {
	st, err := openStore(req.Dir)
	if err != nil {
		return err
	}

	lock := b.repoLock(req.Dir)
	lock.Lock()
	defer lock.Unlock()

	var deleteRepo bool
	err = transport.ReceivePack(ctx, st,
		io.NopCloser(stdin),
		ioutil.WriteNopCloser(stdout),
		&transport.ReceivePackRequest{
			GitProtocol:   req.GitProtocol,
			AdvertiseRefs: false,
			StatelessRPC:  false,
			Hooks: transport.ReceivePackHooks{
				PreReceive: func(_ context.Context, info *transport.PreReceiveInfo) error {
					if err := checkPushPolicy(req, info); err != nil {
						return err
					}
					remove, err := checkDeletion(req, info)
					deleteRepo = remove && err == nil
					return err
				},
			},
		})
	if err != nil {
		return err
	}
	if deleteRepo {
		return repo.RemoveBare(req.Root, req.Dir)
	}
	return nil
}

const confirmDeleteOption = "confirm-delete"

func checkDeletion(req Request, info *transport.PreReceiveInfo) (bool, error) {
	head, err := info.Storer.Reference(plumbing.HEAD)
	if err != nil || head.Type() != plumbing.SymbolicReference {
		return false, nil
	}
	target := head.Target()

	deletesHead := false
	for _, cmd := range info.Commands {
		if cmd.Action() == packp.Delete && cmd.Name == target {
			deletesHead = true
			break
		}
	}
	if !deletesHead {
		return false, nil
	}

	if req.Role != access.RoleOwner {
		return false, fmt.Errorf("only the owner may delete repository %s", req.Repo.String())
	}
	if !repo.IsBare(req.Dir) {
		return false, fmt.Errorf("refusing to delete %s: not a bare repository", req.Repo.String())
	}
	for _, opt := range info.PushOptions {
		if opt == confirmDeleteOption {
			return true, nil
		}
	}
	return false, fmt.Errorf("deleting %s deletes repository %s; retry with: git push -o %s <remote> :%s",
		target, req.Repo.String(), confirmDeleteOption, target)
}

func (b *GoGit) repoLock(dir string) *sync.RWMutex {
	lock, _ := b.locks.LoadOrStore(dir, new(sync.RWMutex))
	return lock.(*sync.RWMutex)
}

func openStore(dir string) (storage.Storer, error) {
	if !repo.Exists(dir) {
		return nil, fmt.Errorf("repository not found")
	}
	return filesystem.NewStorage(osfs.New(dir), cache.NewObjectLRUDefault()), nil
}

func checkPushPolicy(req Request, info *transport.PreReceiveInfo) error {
	if req.Role != access.RoleCoder {
		return nil
	}
	prefix := "refs/heads/" + access.HandleName(req.Handle) + "-"
	for _, cmd := range info.Commands {
		if !strings.HasPrefix(cmd.Name.String(), prefix) {
			return fmt.Errorf("coder may only push refs starting with %s", prefix)
		}
	}
	return nil
}
