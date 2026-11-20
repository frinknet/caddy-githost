Fair point—defaulting `listen_addr` to **`:22`** matches real SSH (`git@host:repo.git` without custom port flags) while treating elevated port privileges as expected in a Docker environment.

Here is the updated **spec**, **Caddyfile**, **minimal Go implementation**, and **antipatterns** with `:22` as the default port and Docker privilege handling built in.

---

## 1. Updated Specification

### Goals & Reuse Boundaries

* **Reuse from Go & Caddy:**
* `crypto/ed25519` & `crypto/rand` to generate the SSH host key.
* `ctx.Storage()` (`certmagic.Storage`) to persist and reload the host key across container restarts and Caddy reloads.
* Existing in-process `Backend` shared with the HTTP Git handler (no loopback HTTP calls, no OS user accounts).


* **Use from SSH packages (`golang.org/x/crypto/ssh` + `gliderlabs/ssh`):**
* OpenSSH key parsing/matching (`ParseAuthorizedKey`, `KeysEqual`, `MarshalPrivateKey`, `ParsePrivateKey`).
* SSH transport and session handling for `exec` channels.


* **Out of scope:** Interactive shells, PTYs, port forwarding, agent forwarding, SFTP, `/etc/ssh`, and host `sshd`.

### Module, Port & Docker Lifecycle Contract

* **Module ID:** `gitproxy` (`caddy.Module`, `caddy.App`, `caddy.Provisioner`, `caddyfile.Unmarshaler`).
* **Default port:** `listen_addr` defaults to **`:22`** (or whatever port is configured, e.g. `:2222`). Binding `:22` inside Docker assumes elevated privileges (running as root or with `CAP_NET_BIND_SERVICE` / `cap_add: [NET_BIND_SERVICE]`) and publishing port `22` (`-p 22:22` or custom host port `-p 2222:22`).
* **Config fields:**
* `listen_addr` (string, default `:22`): TCP listen address.
* `repo_root` (string, default `/var/lib/gitproxy/repos`): Bare repository root shared with HTTP.
* `host_key_path` (string, default `gitproxy/ssh_host_ed25519`): Path inside Caddy `Storage`.
* `users` (`[]UserRule`): Logical users, authorized keys, allowed repo patterns, and `read_only` flag.


* **Lifecycle:**
1. `Provision(ctx)`: Applies defaults (`:22`, `/var/lib/gitproxy/repos`), builds `Backend`, loads/creates the host signer via `ctx.Storage()`, and configures `ssh.Server`.
2. `Start()`: Binds `net.Listen("tcp", a.ListenAddr)` synchronously (surfacing permission or bind errors immediately) and serves in a tracked goroutine.
3. `Stop()`: Closes `ssh.Server` and `net.Listener`, then waits for active session goroutines.



### Session & Command Contract

* **Auth (`PublicKeyHandler`):** Match presented public key against `users` via `gossh.KeysEqual`; store matched user in session context.
* **Allowlist:** Accept only `git-upload-pack '<repo>'` and `git-receive-pack '<repo>'` (two tokens, unquoted non-empty repo). Reject shells, port forwards, SFTP, and other commands with an error on `s.Stderr()` and exit `1`.
* **Path safety:** Clean path, strip leading slashes, normalize `.git` suffix consistently with HTTP, and reject `..` segments or paths escaping `repo_root`.
* **Execution:** Enforce read/write and repo rules; pass `s` (stdin/stdout) and `s.Stderr()` straight to `backend.UploadPack` or `backend.ReceivePack`; write errors to `s.Stderr()` and exit `1` on error or `0` on success.

---

## 2. Caddyfile Example

```caddyfile
{
	gitproxy {
		# Defaults to :22; explicit here for clarity in Docker
		listen_addr :22
		repo_root /var/lib/gitproxy/repos

		user alice {
			key ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleKeyAlice1111111111111111 alice@workstation
			repos app/* docs/handbook.git
		}

		user bob {
			key ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleKeyBob22222222222222222 bob@laptop
			repos app/web.git
			read_only true
		}
	}
}

git.example.com {
	route /repos/* {
		respond "HTTP git endpoint for {path}" 200
	}
}

```

---

## 3. Minimal Go Implementation

```go
package gitproxy

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"path"
	"strings"
	"sync"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/certmagic"
	"github.com/gliderlabs/ssh"
	gossh "golang.org/x/crypto/ssh"
)

func init() {
	caddy.RegisterModule(GitProxyApp{})
}

type Backend interface {
	UploadPack(ctx context.Context, repo string, stdin io.Reader, stdout, stderr io.Writer) error
	ReceivePack(ctx context.Context, repo string, stdin io.Reader, stdout, stderr io.Writer) error
}

type UserRule struct {
	Name     string
	Keys     []gossh.PublicKey
	Repos    []string
	ReadOnly bool
}

type GitProxyApp struct {
	ListenAddr   string     `json:"listen_addr,omitempty"`
	RepoRoot     string     `json:"repo_root,omitempty"`
	HostKeyPath  string     `json:"host_key_path,omitempty"`
	Users        []UserRule `json:"users,omitempty"`

	storage certmagic.Storage
	backend Backend
	server  *ssh.Server
	ln      net.Listener
	wg      sync.WaitGroup
}

func (GitProxyApp) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "gitproxy",
		New: func() caddy.Module { return new(GitProxyApp) },
	}
}

func (a *GitProxyApp) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	for d.Next() {
		if d.NextArg() {
			return d.ArgErr()
		}
		for d.NextBlock(0) {
			switch d.Val() {
			case "listen_addr":
				if !d.Args(&a.ListenAddr) {
					return d.ArgErr()
				}
			case "repo_root":
				if !d.Args(&a.RepoRoot) {
					return d.ArgErr()
				}
			case "host_key_path":
				if !d.Args(&a.HostKeyPath) {
					return d.ArgErr()
				}
			case "user":
				rule, err := parseUserBlock(d)
				if err != nil {
					return err
				}
				a.Users = append(a.Users, rule)
			default:
				return d.Errf("unknown subdirective %q", d.Val())
			}
		}
	}
	return nil
}

func parseUserBlock(d *caddyfile.Dispenser) (UserRule, error) {
	var rule UserRule
	if !d.Args(&rule.Name) {
		return rule, d.ArgErr()
	}
	for d.NextBlock(1) {
		switch d.Val() {
		case "key":
			raw := strings.TrimSpace(strings.TrimPrefix(d.ResetToCurrentLine(), "key"))
			pub, _, _, _, err := gossh.ParseAuthorizedKey([]byte(raw))
			if err != nil {
				return rule, d.Errf("invalid ssh key for user %s: %v", rule.Name, err)
			}
			rule.Keys = append(rule.Keys, pub)
		case "repos":
			rule.Repos = append(rule.Repos, d.RemainingArgs()...)
		case "read_only":
			if !d.Args(&rule.ReadOnly) {
				return rule, d.ArgErr()
			}
		default:
			return rule, d.Errf("unknown user option %q", d.Val())
		}
	}
	return rule, nil
}

func (a *GitProxyApp) Provision(ctx caddy.Context) error {
	if a.ListenAddr == "" {
		a.ListenAddr = ":22"
	}
	if a.RepoRoot == "" {
		a.RepoRoot = "/var/lib/gitproxy/repos"
	}
	if a.HostKeyPath == "" {
		a.HostKeyPath = "gitproxy/ssh_host_ed25519"
	}

	a.storage = ctx.Storage()
	a.backend = newLocalGitBackend(a.RepoRoot)

	signer, err := loadOrCreateHostSigner(context.Background(), a.storage, a.HostKeyPath)
	if err != nil {
		return err
	}

	a.server = &ssh.Server{
		Addr: a.ListenAddr,
		PublicKeyHandler: func(sctx ssh.Context, key gossh.PublicKey) bool {
			user, ok := a.matchKey(key)
			if !ok {
				return false
			}
			sctx.SetValue("git_user", user)
			return true
		},
		Handler: a.handle,
	}
	a.server.AddHostKey(signer)
	return nil
}

func (a *GitProxyApp) Start() error {
	ln, err := net.Listen("tcp", a.ListenAddr)
	if err != nil {
		return fmt.Errorf("gitproxy listen %s (check privileges/port binding): %w", a.ListenAddr, err)
	}
	a.ln = ln
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		if err := a.server.Serve(ln); err != nil && !errors.Is(err, net.ErrClosed) {
			// Log via Caddy logger in production
		}
	}()
	return nil
}

func (a *GitProxyApp) Stop() error {
	if a.server != nil {
		_ = a.server.Close()
	}
	if a.ln != nil {
		_ = a.ln.Close()
	}
	a.wg.Wait()
	return nil
}

func loadOrCreateHostSigner(ctx context.Context, storage certmagic.Storage, keyPath string) (gossh.Signer, error) {
	if storage == nil {
		return nil, errors.New("caddy storage is nil")
	}
	if raw, err := storage.Load(ctx, keyPath); err == nil {
		return gossh.ParsePrivateKey(raw)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("load ssh host key: %w", err)
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ed25519 host key: %w", err)
	}
	pemBlock, err := gossh.MarshalPrivateKey(priv, "gitproxy-host-key")
	if err != nil {
		return nil, fmt.Errorf("marshal openssh host key: %w", err)
	}
	if err := storage.Store(ctx, keyPath, gossh.MarshalPrivateKeyToPEM(pemBlock)); err != nil {
		return nil, fmt.Errorf("store ssh host key: %w", err)
	}
	return gossh.NewSignerFromKey(priv)
}

func (a *GitProxyApp) handle(s ssh.Session) {
	action, rawRepo, ok := parseGitCommand(s.Command())
	if !ok {
		_, _ = fmt.Fprintln(s.Stderr(), "fatal: command not permitted")
		_ = s.Exit(1)
		return
	}

	repo, ok := normalizeRepoPath(rawRepo)
	if !ok {
		_, _ = fmt.Fprintln(s.Stderr(), "fatal: invalid repository path")
		_ = s.Exit(1)
		return
	}

	user, _ := s.Context().Value("git_user").(UserRule)
	if !a.canAccess(user, repo, action == "receive-pack") {
		_, _ = fmt.Fprintln(s.Stderr(), "fatal: access denied")
		_ = s.Exit(1)
		return
	}

	var err error
	switch action {
	case "upload-pack":
		err = a.backend.UploadPack(s.Context(), repo, s, s, s.Stderr())
	case "receive-pack":
		err = a.backend.ReceivePack(s.Context(), repo, s, s, s.Stderr())
	}
	if err != nil {
		_, _ = fmt.Fprintf(s.Stderr(), "fatal: %v\n", err)
		_ = s.Exit(1)
		return
	}
	_ = s.Exit(0)
}

func parseGitCommand(cmd []string) (action, repo string, ok bool) {
	if len(cmd) != 2 {
		return "", "", false
	}
	if cmd[0] != "git-upload-pack" && cmd[0] != "git-receive-pack" {
		return "", "", false
	}
	target := strings.TrimSpace(strings.Trim(cmd[1], `"'`))
	if target == "" {
		return "", "", false
	}
	return strings.TrimPrefix(cmd[0], "git-"), target, true
}

func normalizeRepoPath(p string) (string, bool) {
	raw := strings.Trim(strings.TrimSpace(p), `"'`)
	if raw == "" {
		return "", false
	}
	for _, seg := range strings.Split(raw, "/") {
		if seg == ".." {
			return "", false
		}
	}
	cleaned := strings.TrimPrefix(path.Clean("/"+strings.TrimPrefix(raw, "/")), "/")
	if cleaned == "" || cleaned == "." || strings.Contains(cleaned, "..") {
		return "", false
	}
	if !strings.HasSuffix(cleaned, ".git") {
		cleaned += ".git"
	}
	return cleaned, true
}

func (a *GitProxyApp) matchKey(presented gossh.PublicKey) (UserRule, bool) {
	for _, u := range a.Users {
		for _, k := range u.Keys {
			if gossh.KeysEqual(presented, k) {
				return u, true
			}
		}
	}
	return UserRule{}, false
}

func (a *GitProxyApp) canAccess(u UserRule, repo string, isWrite bool) bool {
	if u.Name == "" || (isWrite && u.ReadOnly) {
		return false
	}
	for _, pattern := range u.Repos {
		if pattern == "*" || pattern == repo {
			return true
		}
		if strings.HasSuffix(pattern, "/*") && strings.HasPrefix(repo, strings.TrimSuffix(pattern, "*")) {
			return true
		}
	}
	return false
}

type localGitBackend struct {
	root string
}

func newLocalGitBackend(root string) *localGitBackend {
	return &localGitBackend{root: root}
}

func (b *localGitBackend) UploadPack(_ context.Context, repo string, _ io.Reader, stdout, _ io.Writer) error {
	_, _ = fmt.Fprintf(stdout, "upload-pack %s/%s\n", b.root, repo)
	return nil
}

func (b *localGitBackend) ReceivePack(_ context.Context, repo string, _ io.Reader, stdout, _ io.Writer) error {
	_, _ = fmt.Fprintf(stdout, "receive-pack %s/%s\n", b.root, repo)
	return nil
}

var (
	_ caddy.Module          = (*GitProxyApp)(nil)
	_ caddy.App             = (*GitProxyApp)(nil)
	_ caddy.Provisioner     = (*GitProxyApp)(nil)
	_ caddyfile.Unmarshaler = (*GitProxyApp)(nil)
)

```

---

## 4. Antipatterns to Avoid

* **Allowing shell or arbitrary `exec` fallback:** Reject interactive shells (`len(cmd) == 0`) and non-git commands immediately; never shell out arbitrary strings.
* **Trusting `s.User()` as the user identity:** Clients connect as `git@host`, so `s.User()` is `"git"`; always map verified public keys to logical users.
* **Allowing `..` or escaped paths in repo arguments:** Reject traversal segments before joining under `repo_root`.
* **Silent exit codes:** Always write a readable error to `s.Stderr()` before `s.Exit(1)`.
* **Looping over HTTP (`localhost`) from SSH:** Call the shared `Backend` directly in-process.
* **Ephemeral host keys or leaked listeners on reload:** Persist the Ed25519 host key in Caddy's `ctx.Storage()`, and close both `server` and `listener` cleanly in `Stop()`.
* **Ignoring host port `22` conflicts in Docker:** If the Docker host already runs `sshd` on port `22`, publish `:22` from the container onto an unused host port (e.g. `-p 2222:22`) rather than colliding on the host's port `22`.
