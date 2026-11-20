package config

import (
	"encoding/json"
	"testing"

	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
)

func TestUnmarshalCaddyfile(t *testing.T) {
	d := caddyfile.NewTestDispenser(`githost {
		listen_addr :2222
		repo_root /srv/git
		config_dir /srv/config
		config_repo acme-config.git
		secret correct horse battery staple
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
	if cfg.ConfigDir != "/srv/config" {
		t.Errorf("config_dir = %q, want %q", cfg.ConfigDir, "/srv/config")
	}
	if cfg.ConfigRepo != "acme-config.git" {
		t.Errorf("config_repo = %q, want %q", cfg.ConfigRepo, "acme-config.git")
	}
	if cfg.Secret != "correct horse battery staple" {
		t.Errorf("secret = %q", cfg.Secret)
	}
}

func TestUnmarshalCaddyfileRejectsUnknown(t *testing.T) {
	d := caddyfile.NewTestDispenser(`githost {
		users_file /srv/git/users
	}`)

	var cfg Config
	if err := cfg.UnmarshalCaddyfile(d); err == nil {
		t.Fatal("expected error for unknown subdirective")
	}
}

func TestUnmarshalCaddyfileRejectsExtraArgs(t *testing.T) {
	d := caddyfile.NewTestDispenser(`githost unexpected`)

	var cfg Config
	if err := cfg.UnmarshalCaddyfile(d); err == nil {
		t.Fatal("expected error for extra arguments")
	}
}

func TestParseGlobalOption(t *testing.T) {
	d := caddyfile.NewTestDispenser(`githost {
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
	if app.Name != "githost" {
		t.Errorf("app name = %q, want %q", app.Name, "githost")
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
		ListenAddr: ":22",
		RepoRoot:   "repos",
		ConfigDir:  DefaultConfigDir,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for relative repo_root")
	}
}

func TestValidateRejectsBlankSecret(t *testing.T) {
	cfg := Config{
		ListenAddr: ":22",
		RepoRoot:   DefaultRepoRoot,
		ConfigDir:  DefaultConfigDir,
		ConfigRepo: DefaultConfigRepo,
		Secret:     "   ",
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for blank secret")
	}
}

func TestValidateConfigRepo(t *testing.T) {
	for _, repo := range []string{"infra/config.git", "../x.git", ".git", "server-access"} {
		cfg := Config{
			ListenAddr: ":22",
			RepoRoot:   DefaultRepoRoot,
			ConfigDir:  DefaultConfigDir,
			ConfigRepo: repo,
		}
		if err := cfg.Validate(); err == nil {
			t.Errorf("config_repo %q should fail validation", repo)
		}
	}
}

func TestValidateRejectsRelativeConfigDir(t *testing.T) {
	cfg := Config{
		ListenAddr: ":22",
		RepoRoot:   DefaultRepoRoot,
		ConfigDir:  "config",
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for relative config_dir")
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
	if cfg.ConfigDir != DefaultConfigDir {
		t.Errorf("config_dir = %q, want %q", cfg.ConfigDir, DefaultConfigDir)
	}
	if cfg.ConfigRepo != DefaultConfigRepo {
		t.Errorf("config_repo = %q, want %q", cfg.ConfigRepo, DefaultConfigRepo)
	}
}
