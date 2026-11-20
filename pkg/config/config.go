package config

import (
	"fmt"
	"path/filepath"
)

const (
	DefaultListenAddr  = ":22"
	DefaultRepoRoot    = "/var/lib/gitproxy/repos"
	DefaultHostKeyPath = "gitproxy/ssh_host_ed25519"
)

type Config struct {
	ListenAddr  string `json:"listen_addr,omitempty"`
	RepoRoot    string `json:"repo_root,omitempty"`
	HostKeyPath string `json:"host_key_path,omitempty"`
}

func (c *Config) ApplyDefaults() {
	if c.ListenAddr == "" {
		c.ListenAddr = DefaultListenAddr
	}
	if c.RepoRoot == "" {
		c.RepoRoot = DefaultRepoRoot
	}
	if c.HostKeyPath == "" {
		c.HostKeyPath = DefaultHostKeyPath
	}
}

func (c Config) Validate() error {
	if c.ListenAddr == "" {
		return fmt.Errorf("gitproxy: listen_addr is required")
	}
	if c.RepoRoot == "" {
		return fmt.Errorf("gitproxy: repo_root is required")
	}
	if !filepath.IsAbs(c.RepoRoot) {
		return fmt.Errorf("gitproxy: repo_root must be absolute: %q", c.RepoRoot)
	}
	if c.HostKeyPath == "" {
		return fmt.Errorf("gitproxy: host_key_path is required")
	}
	return nil
}
