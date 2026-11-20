package config

import (
	"github.com/caddyserver/caddy/v2/caddyconfig"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
)

func init() {
	httpcaddyfile.RegisterGlobalOption("gitproxy", parseGlobalOption)
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
			case "host_key_path":
				if !d.Args(&c.HostKeyPath) {
					return d.ArgErr()
				}
			default:
				return d.Errf("unrecognized gitproxy subdirective %q", d.Val())
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
	return httpcaddyfile.App{Name: "gitproxy", Value: caddyconfig.JSON(cfg, nil)}, nil
}
