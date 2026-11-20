package gitproxy

import (
	"testing"

	"github.com/frinknet/caddy-gitproxy/pkg/config"
)

func TestCaddyModule(t *testing.T) {
	info := GitProxy{}.CaddyModule()
	if info.ID != "gitproxy" {
		t.Errorf("module ID = %q, want %q", info.ID, "gitproxy")
	}
	if info.New == nil {
		t.Fatal("module New is nil")
	}
	if _, ok := info.New().(*GitProxy); !ok {
		t.Fatalf("module New returned %T, want *GitProxy", info.New())
	}
}

func TestApplyDefaults(t *testing.T) {
	g := new(GitProxy)
	g.ApplyDefaults()

	if g.ListenAddr != config.DefaultListenAddr {
		t.Errorf("listen_addr = %q, want %q", g.ListenAddr, config.DefaultListenAddr)
	}
	if g.RepoRoot != config.DefaultRepoRoot {
		t.Errorf("repo_root = %q, want %q", g.RepoRoot, config.DefaultRepoRoot)
	}
	if g.HostKeyPath != config.DefaultHostKeyPath {
		t.Errorf("host_key_path = %q, want %q", g.HostKeyPath, config.DefaultHostKeyPath)
	}
}
