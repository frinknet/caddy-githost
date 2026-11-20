package config

import (
	"strings"

	"github.com/caddyserver/caddy/v2/caddyconfig"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
)

func init() {
	httpcaddyfile.RegisterGlobalOption("githost", parseGlobalOption)
}

func (c *Config) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	for d.Next() {
		if d.NextArg() {
			return d.ArgErr()
		}
		for d.NextBlock(0) {
			switch d.Val() {
			case "listen_addr":
				if !d.Args(&c.ListenAddr) {
					return d.ArgErr()
				}
			case "repo_root":
				if !d.Args(&c.RepoRoot) {
					return d.ArgErr()
				}
			case "config_dir":
				if !d.Args(&c.ConfigDir) {
					return d.ArgErr()
				}
			case "config_repo":
				if !d.Args(&c.ConfigRepo) {
					return d.ArgErr()
				}
			case "secret":
				args := d.RemainingArgs()
				if len(args) == 0 {
					return d.ArgErr()
				}
				c.Secret = strings.Join(args, " ")
			default:
				return d.Errf("unrecognized githost subdirective %q", d.Val())
			}
		}
	}
	return nil
}

func parseGlobalOption(d *caddyfile.Dispenser, _ any) (any, error) {
	cfg := new(Config)
	if err := cfg.UnmarshalCaddyfile(d); err != nil {
		return nil, err
	}
	return httpcaddyfile.App{Name: "githost", Value: caddyconfig.JSON(cfg, nil)}, nil
}
