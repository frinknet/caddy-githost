package githost

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/caddyserver/caddy/v2"
	gossh "golang.org/x/crypto/ssh"

	"github.com/frinknet/caddy-githost/pkg/access"
	"github.com/frinknet/caddy-githost/pkg/backend"
	"github.com/frinknet/caddy-githost/pkg/config"
	"github.com/frinknet/caddy-githost/pkg/transport"
)

const hostKeyPath = "githost/ssh_host_ed25519"

func init() {
	caddy.RegisterModule(GitHost{})
}

type GitHost struct {
	config.Config
	server  *transport.Server
	store   *access.Store
	backend backend.Backend
}

func (GitHost) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "githost",
		New: func() caddy.Module { return new(GitHost) },
	}
}

func (g *GitHost) Provision(ctx caddy.Context) error {
	g.ApplyDefaults()
	if err := g.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(g.RepoRoot, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(g.ConfigDir, 0o755); err != nil {
		return err
	}
	if err := backend.SyncWorktree(filepath.Join(g.RepoRoot, g.ConfigRepo), g.ConfigDir); err != nil {
		return err
	}

	store, err := access.Open(g.ConfigDir)
	if err != nil {
		return err
	}
	g.store = store
	g.backend = backend.New()
	var hostKey gossh.Signer
	if secret := strings.TrimSpace(g.Secret); secret != "" {
		hostKey, err = transport.HostKeyFromPhrase(secret)
	} else {
		hostKey, err = transport.LoadOrCreateHostKey(ctx, ctx.Storage(), hostKeyPath)
	}
	if err != nil {
		return err
	}

	g.server = transport.NewServer(transport.Options{
		ListenAddr: g.ListenAddr,
		RepoRoot:   g.RepoRoot,
		ConfigRepo: g.ConfigRepo,
		ConfigDir:  g.ConfigDir,
		Store:      g.store,
		Backend:    g.backend,
		HostKey:    hostKey,
		Logger:     ctx.Logger(),
	})
	return nil
}

func (g *GitHost) Start() error {
	if g.server == nil {
		return nil
	}
	return g.server.Start()
}

func (g *GitHost) Stop() error {
	if g.server == nil {
		return nil
	}
	return g.server.Stop()
}

var (
	_ caddy.Module      = (*GitHost)(nil)
	_ caddy.App         = (*GitHost)(nil)
	_ caddy.Provisioner = (*GitHost)(nil)
)
