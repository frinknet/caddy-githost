package gitproxy

import (
	"os"
	"path/filepath"

	"github.com/caddyserver/caddy/v2"

	"github.com/frinknet/caddy-gitproxy/pkg/access"
	"github.com/frinknet/caddy-gitproxy/pkg/backend"
	"github.com/frinknet/caddy-gitproxy/pkg/config"
	"github.com/frinknet/caddy-gitproxy/pkg/transport"
)

func init() {
	caddy.RegisterModule(GitProxy{})
}

type GitProxy struct {
	config.Config
	server *transport.Server
}

func (GitProxy) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "gitproxy",
		New: func() caddy.Module { return new(GitProxy) },
	}
}

func (g *GitProxy) Provision(ctx caddy.Context) error {
	g.ApplyDefaults()
	if err := g.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(g.RepoRoot, 0o755); err != nil {
		return err
	}

	store, err := access.Open(g.RepoRoot)
	if err != nil {
		return err
	}
	hostKey, err := transport.LoadOrCreateHostKey(ctx, ctx.Storage(), filepath.FromSlash(g.HostKeyPath))
	if err != nil {
		return err
	}

	g.server = transport.NewServer(transport.Options{
		ListenAddr: g.ListenAddr,
		RepoRoot:   g.RepoRoot,
		Store:      store,
		Backend:    backend.New(),
		HostKey:    hostKey,
		Logger:     ctx.Logger(),
	})
	return nil
}

func (g *GitProxy) Start() error {
	if g.server == nil {
		return nil
	}
	return g.server.Start()
}

func (g *GitProxy) Stop() error {
	if g.server == nil {
		return nil
	}
	return g.server.Stop()
}

var (
	_ caddy.Module      = (*GitProxy)(nil)
	_ caddy.App         = (*GitProxy)(nil)
	_ caddy.Provisioner = (*GitProxy)(nil)
)
