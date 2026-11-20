package config

import (
	"encoding/json"
	"testing"

	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
)

func TestUnmarshalCaddyfile(t *testing.T) {
	d := caddyfile.NewTestDispenser(`gitproxy {
		listen_addr :2222
		repo_root /srv/git
		host_key_path keys/ssh_host_ed25519
	}`)

	var cfg Config
	if err := cfg.UnmarshalCaddyfile(d); err != nil {
		t.Fatal(err)
	}

	if cfg.ListenAddr != ":2222" {
		t.Errorf("listen_addr = %q, want %q", cfg.ListenAddr, ":2222")
	}
	if cfg.RepoRoot != "/srv/git" {
		t.Errorf("repo_root = %q, want %q", cfg.RepoRoot, "/srv/git")
	}
	if cfg.HostKeyPath != "keys/ssh_host_ed25519" {
		t.Errorf("host_key_path = %q, want %q", cfg.HostKeyPath, "keys/ssh_host_ed25519")
	}
}

func TestUnmarshalCaddyfileRejectsUnknown(t *testing.T) {
	d := caddyfile.NewTestDispenser(`gitproxy {
		users_file /srv/git/users
	}`)

	var cfg Config
	if err := cfg.UnmarshalCaddyfile(d); err == nil {
		t.Fatal("expected error for unknown subdirective")
	}
}

func TestUnmarshalCaddyfileRejectsExtraArgs(t *testing.T) {
	d := caddyfile.NewTestDispenser(`gitproxy unexpected`)

	var cfg Config
	if err := cfg.UnmarshalCaddyfile(d); err == nil {
		t.Fatal("expected error for extra arguments")
	}
}

func TestParseGlobalOption(t *testing.T) {
	d := caddyfile.NewTestDispenser(`gitproxy {
		listen_addr :2222
	}`)

	val, err := parseGlobalOption(d, nil)
	if err != nil {
		t.Fatal(err)
	}

	app, ok := val.(httpcaddyfile.App)
	if !ok {
		t.Fatalf("global option returned %T, want httpcaddyfile.App", val)
	}
	if app.Name != "gitproxy" {
		t.Errorf("app name = %q, want %q", app.Name, "gitproxy")
	}

	var cfg Config
	if err := json.Unmarshal(app.Value, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != ":2222" {
		t.Errorf("listen_addr = %q, want %q", cfg.ListenAddr, ":2222")
	}
}

func TestValidateRejectsRelativeRepoRoot(t *testing.T) {
	cfg := Config{
		ListenAddr:  ":22",
		RepoRoot:    "repos",
		HostKeyPath: DefaultHostKeyPath,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for relative repo_root")
	}
}

func TestApplyDefaults(t *testing.T) {
	var cfg Config
	cfg.ApplyDefaults()

	if cfg.ListenAddr != DefaultListenAddr {
		t.Errorf("listen_addr = %q, want %q", cfg.ListenAddr, DefaultListenAddr)
	}
	if cfg.RepoRoot != DefaultRepoRoot {
		t.Errorf("repo_root = %q, want %q", cfg.RepoRoot, DefaultRepoRoot)
	}
	if cfg.HostKeyPath != DefaultHostKeyPath {
		t.Errorf("host_key_path = %q, want %q", cfg.HostKeyPath, DefaultHostKeyPath)
	}
}
