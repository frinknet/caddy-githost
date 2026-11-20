package githost

import (
	"testing"

	"github.com/frinknet/caddy-githost/pkg/config"
)

func TestCaddyModule(t *testing.T) {
	info := GitHost{}.CaddyModule()
	if info.ID != "githost" {
		t.Errorf("module ID = %q, want %q", info.ID, "githost")
	}
	if info.New == nil {
		t.Fatal("module New is nil")
	}
	if _, ok := info.New().(*GitHost); !ok {
		t.Fatalf("module New returned %T, want *GitHost", info.New())
	}
}

func TestApplyDefaults(t *testing.T) {
	g := new(GitHost)
	g.ApplyDefaults()

	if g.ListenAddr != config.DefaultListenAddr {
		t.Errorf("listen_addr = %q, want %q", g.ListenAddr, config.DefaultListenAddr)
	}
	if g.RepoRoot != config.DefaultRepoRoot {
		t.Errorf("repo_root = %q, want %q", g.RepoRoot, config.DefaultRepoRoot)
	}
	if g.ConfigDir != config.DefaultConfigDir {
		t.Errorf("config_dir = %q, want %q", g.ConfigDir, config.DefaultConfigDir)
	}
}
