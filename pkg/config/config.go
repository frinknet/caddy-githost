package config

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	DefaultListenAddr = ":22"
	DefaultRepoRoot   = "/var/lib/githost/repos"
	DefaultConfigDir  = "/var/caddy/config"
	DefaultConfigRepo = "server-access.git"
)

var configRepoPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.git$`)

type Config struct {
	ListenAddr string `json:"listen_addr,omitempty"`
	RepoRoot   string `json:"repo_root,omitempty"`
	ConfigDir  string `json:"config_dir,omitempty"`
	ConfigRepo string `json:"config_repo,omitempty"`
	Secret     string `json:"secret,omitempty"`
}

func (c *Config) ApplyDefaults() {
	if c.ListenAddr == "" {
		c.ListenAddr = DefaultListenAddr
	}
	if c.RepoRoot == "" {
		c.RepoRoot = DefaultRepoRoot
	}
	if c.ConfigDir == "" {
		c.ConfigDir = DefaultConfigDir
	}
	if c.ConfigRepo == "" {
		c.ConfigRepo = DefaultConfigRepo
	}
	if !strings.HasSuffix(c.ConfigRepo, ".git") {
		c.ConfigRepo += ".git"
	}
}

func (c Config) Validate() error {
	if c.ListenAddr == "" {
		return fmt.Errorf("githost: listen_addr is required")
	}
	if c.RepoRoot == "" {
		return fmt.Errorf("githost: repo_root is required")
	}
	if !filepath.IsAbs(c.RepoRoot) {
		return fmt.Errorf("githost: repo_root must be absolute: %q", c.RepoRoot)
	}
	if c.ConfigDir == "" {
		return fmt.Errorf("githost: config_dir is required")
	}
	if !filepath.IsAbs(c.ConfigDir) {
		return fmt.Errorf("githost: config_dir must be absolute: %q", c.ConfigDir)
	}
	if !configRepoPattern.MatchString(c.ConfigRepo) {
		return fmt.Errorf("githost: config_repo must be a single name ending in .git: %q", c.ConfigRepo)
	}
	if c.Secret != "" && strings.TrimSpace(c.Secret) == "" {
		return fmt.Errorf("githost: secret must not be blank")
	}
	return nil
}
