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
	"github.com/go-git/go-git/v6/plumbing/cache"
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

	return transport.ReceivePack(ctx, st,
		io.NopCloser(stdin),
		ioutil.WriteNopCloser(stdout),
		&transport.ReceivePackRequest{
			GitProtocol:   req.GitProtocol,
			AdvertiseRefs: false,
			StatelessRPC:  false,
			Hooks: transport.ReceivePackHooks{
				PreReceive: func(_ context.Context, info *transport.PreReceiveInfo) error {
					return checkPushPolicy(req, info)
				},
			},
		})
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
