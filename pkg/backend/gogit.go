package backend

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/frinknet/caddy-githost/pkg/access"
	"github.com/frinknet/caddy-githost/pkg/repo"
	"github.com/go-git/go-billy/v6/osfs"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/cache"
	"github.com/go-git/go-git/v6/plumbing/object"
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
	var adminFiles map[string][]byte
	var adminApplyErr error
	err = transport.ReceivePack(ctx, st,
		io.NopCloser(stdin),
		ioutil.WriteNopCloser(stdout),
		&transport.ReceivePackRequest{
			GitProtocol:   req.GitProtocol,
			AdvertiseRefs: false,
			StatelessRPC:  false,
			Hooks: transport.ReceivePackHooks{
				PreReceive: func(_ context.Context, info *transport.PreReceiveInfo) error {
					if req.Admin {
						files, err := proposedAdminFiles(info.Storer, info.Commands)
						if err != nil {
							return err
						}
						if err := validateAdminPush(req, files); err != nil {
							return err
						}
						adminFiles = files
						return nil
					}

					if err := checkPushPolicy(req, info); err != nil {
						return err
					}
					remove, err := checkDeletion(req, info)
					deleteRepo = remove && err == nil
					return err
				},
				PostReceive: func(context.Context, *transport.PostReceiveInfo) error {
					if !req.Admin {
						return nil
					}
					if req.Access == nil {
						adminApplyErr = fmt.Errorf("config repository: access store is not configured")
						return adminApplyErr
					}
					adminApplyErr = req.Access.Apply(adminFiles)
					return adminApplyErr
				},
			},
		})
	if err != nil {
		return err
	}
	if adminApplyErr != nil {
		return adminApplyErr
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

func validateAdminPush(req Request, files map[string][]byte) error {
	snap, err := access.ParseFiles(files)
	if err != nil {
		return err
	}
	if !snap.IsAdmin(req.Handle) {
		return fmt.Errorf("config repository: pushed admins file removes %s", req.Handle)
	}
	return nil
}

func proposedAdminFiles(st storage.Storer, commands []*packp.Command) (map[string][]byte, error) {
	head, err := st.Reference(plumbing.HEAD)
	if err != nil {
		return nil, err
	}
	target := plumbing.NewBranchReferenceName("main")
	if head.Type() == plumbing.SymbolicReference {
		target = head.Target()
	} else {
		target = head.Name()
	}

	var hash plumbing.Hash
	found := false
	for _, cmd := range commands {
		if cmd.Name != target {
			continue
		}
		if cmd.Action() == packp.Delete {
			return nil, fmt.Errorf("config repository: refusing to delete %s", target)
		}
		hash = cmd.New
		found = true
		break
	}
	if !found {
		return nil, fmt.Errorf("config repository: push must update %s", target)
	}

	commit, err := object.GetCommit(st, hash)
	if err != nil {
		return nil, err
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, err
	}

	files := make(map[string][]byte)
	err = tree.Files().ForEach(func(file *object.File) error {
		if strings.Contains(file.Name, "/") {
			return fmt.Errorf("config repository: nested path %q is not allowed", file.Name)
		}
		if !file.Mode.IsFile() {
			return fmt.Errorf("config repository: %q is not a regular file", file.Name)
		}
		contents, err := file.Contents()
		if err != nil {
			return err
		}
		files[file.Name] = []byte(contents)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
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
