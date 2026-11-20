# caddy-githost

SSH Git server packaged as a Caddy v2 app. `spec.md` is the target design;
this file describes only what exists in the tree today.

## Status

Core server works. Caddy starts an SSH listener, authenticates public keys
against a users file, authorizes per-org ACLs, and serves clone, fetch, and
push with an in-process Git implementation. Repositories are bare directories
under `repo_root`; the users file and org ACLs live in `config_dir`.

- Module ID: `githost` (a Caddy app, not an HTTP handler).
- Caddyfile global option: `githost { ... }` with `listen_addr`, `repo_root`,
  `config_dir`, `config_repo`, and `secret`. Unknown subdirectives and extra
  arguments are rejected.
- `git-upload-pack '<org>/<name>.git'`, `git-receive-pack '<org>/<name>.git'`,
  and `githost-list` are accepted; a session with no command (and no PTY)
  prints the same listing. Shells, PTYs, port forwarding, agent forwarding,
  and SFTP are denied.
- Host key: an Ed25519 key. With `secret` it is deterministically derived from
  the secret and never stored; otherwise one key is persisted at
  `githost/ssh_host_ed25519` in Caddy storage, generated on first use and
  reused across restarts and reloads.
- Git backend: `github.com/go-git/go-git/v6` (`v6.0.0-beta.1`), so the image
  needs no external `git` binary.
- `sh test/unit.sh` and `./test/e2e.sh` pass.

Not implemented yet: an HTTP Git endpoint. See `BACKLOG.md` (local,
.git-ignored).

## Configuration

Caddyfile:

```caddyfile
{
	githost {
		listen_addr :22
		repo_root /var/lib/githost/repos
		config_dir /var/caddy/config
		config_repo server-access.git
		secret "correct horse battery staple"
	}
}

:80 {
	root * /srv/www
	file_server
}
```

Defaults: `:22`, `/var/lib/githost/repos`, and `/var/caddy/config`.
`repo_root` and `config_dir` must be absolute. The app creates both directories
during provisioning.

`secret` is optional. When set, the SSH host key is derived from it with scrypt
and Ed25519 and is never written to disk, so the same secret yields the same
host key across restarts. The secret is as sensitive as a private key: keep it
out of shell history and world-readable files. When unset, the key is persisted
in Caddy storage instead.

`config_repo` names the bare repository under `repo_root` that holds the config
tree. It defaults to `server-access.git`, must be a single path component, and
may omit the `.git` suffix. Admins reach it as `ssh://git@host/<config_repo>`.

The JSON module ID is `githost`, with fields `listen_addr`, `repo_root`,
`config_dir`, `config_repo`, and `secret`. There is no user list in JSON;
access lives in files.

### Users file

`<config_dir>/users`, one line per user:

```text
<key-type> <base64-key> @<handle> <email>
```

Comments starting with `#` and blank lines are ignored. Duplicate keys and
duplicate handles are load errors. A presented key authenticates as its
handle. A missing users file means nobody can authenticate.

### Admins file

`<config_dir>/admins`, one `@handle` per line, comments with `#`:

```text
@alice  # primary admin
@bob
```

Listed handles must exist in `users`; unknown handles and duplicates are load
errors. A missing or empty admins file means nobody can push the config
repository. This file lives in the config tree, so it is edited by pushing to
`config_repo`, not in the Caddyfile.

### Org ACLs

`<config_dir>/<org>.access`, one line per handle:

```text
@<handle> <role> [# comment]
```

Roles:

| Role | Read | Push | Notes |
| --- | --- | --- | --- |
| `owner` | yes | any ref | may create missing repos in the org |
| `coder` | yes | `refs/heads/<handle>-*` only | policy enforced before ref updates |
| `guest` | yes | no | |
| `block` | no | no | explicitly denied |

Unknown handles, unknown roles, and duplicate entries are load errors. A
missing or empty ACL file denies every repo in that org.

Access files are loaded during `Provision`, fail closed, and are not watched.
Edit them and run `caddy reload` for changes to take effect, or push them to
the config repository.

### Config repository

Handles listed in `admins` may read and push the top-level bare repository
`<repo_root>/<config_repo>`. A push to its default branch is the config
tree: every root file must be `users`, `admins`, or `<org>.access`, and the
whole pushed tree is validated before any ref moves. A malformed push, a
nested path, a deletion of the default branch, or an `admins` file that drops
the pushing handle is rejected and the installed files stay untouched.

On success the pushed tree replaces `users`, `admins`, and `*.access` in
`config_dir` and reloads the in-memory ACL snapshot, with no `caddy reload`
needed. Any `users`, `admins`, or `*.access` file not in the pushed tree is
removed from `config_dir`. Only handles in the current `admins` file can push
or clone this repository; a missing repository is created on the first admin
push.

```sh
# locally, in a clone of the config repository
git add users admins acme.access
git commit -m 'grant bob owner and admin'
git push origin HEAD:refs/heads/main
```

### Repositories

A repo path is exactly `<org>/<name>.git`; a missing `.git` suffix is added.
Quotes and a leading slash are stripped. Deeper nesting, `..`, invalid
characters, reserved names (`users`, `server-access`, `*.access`), and paths
escaping `repo_root` are rejected. The top-level `config_repo` is not an org
repository and cannot be addressed this way.

An `owner` pushing to a missing repo creates a bare repo with `HEAD` pointing
at `refs/heads/main`. Other roles cannot create repos.

### Listing repositories

`ssh git@host githost-list`, or an SSH session that requests no command and
no PTY, prints one line per repository the authenticated handle can read:

```text
acme/app.git  owner
acme/tools.git  owner
```

The first column is the repo path and the second its role in that org. Repos
are sorted by path. An org whose ACL gives the handle no read role, including
`block`, contributes nothing, and the command still exits `0`. There is no Git
protocol for listing repositories, so `git ls-remote` cannot advertise them.

### Deleting a repository

An `owner` deletes a repository by pushing a deletion of its default branch,
resolved from the repo's `HEAD` (not hardcoded to `main`):

```sh
git push origin :refs/heads/main
```

The first push is refused and names the exact retry, because the ref deletion
would destroy every other ref with it. Confirm with the `confirm-delete` push
option:

```sh
git push -o confirm-delete origin :refs/heads/main
```

The server advertises the `push-options` capability, so the client sends the
option only when asked to. Only `owner` may delete; the push is refused for
reserved names and for paths that are not bare repositories. After the ref
update succeeds the directory is renamed into `<repo_root>/.trash/` and then
removed, so a push either leaves the repo untouched or removes it completely.
Deletion never touches `<org>.access`. A later owner push to the same path
recreates the repo, empty, because creation-on-push is enabled.

Deletion holds the per-repo lock, so a push and active clone/fetch sessions on
the same repository do not overlap in-process. A client already connected to a
repo that is deleted sees its session fail.

### Bootstrap

An empty `repo_root` and `config_dir` authenticate nobody. Before the first
connection:

1. Create `<config_dir>/users` with at least one key line.
2. Create `<config_dir>/<org>.access` with that handle as `owner`.
3. Create `<config_dir>/admins` with that handle so it can push the config
   repository.
4. Create the bare repo, or let the first owner push create it.
5. Run `caddy reload` after adding files to a running server.

No users file means no authentication; no org ACL means every repo in that
org is denied.

## Layout

| Path | Purpose |
| --- | --- |
| `githost.go` | Root Go file. Holds the `package` clause and blank imports of `pkg/config` and `pkg/githost`. |
| `pkg/config/` | `Config` struct, defaults, validation, and the `githost` Caddyfile global option parser. |
| `pkg/githost/` | `GitHost` app module: module registration, provisioning, listener lifecycle. |
| `pkg/access/` | Users file parsing, org ACL parsing, immutable access snapshots, key authentication, role resolution. |
| `pkg/repo/` | Repo path parsing and safety checks, bare repo creation, repo listing over the filesystem. |
| `pkg/backend/` | `Backend` interface, go-git server adapter, per-repo locking, push policy, config-repository tree validation. |
| `pkg/transport/` | SSH server, host key persistence, public-key auth, command allowlist, session dispatch, repository listing, admin repository routing. |
| `www/Caddyfile` | Shipped Caddyfile, copied to `/srv/www` in the image. Serves `/srv/www` on `:80` and starts `githost` on `:22`. |
| `test/unit.sh` | Canonical unit layer: one bounded builder container running `gofmt -l . && go vet ./... && go test -timeout=120s ./...`. |
| `test/e2e.sh` | Canonical e2e layer: starts `caddy-githost:test`, seeds a bare repo and access files in the config dir, then runs clone, push, admin ACL push, and denial checks over SSH. |
| `test/e2e/fixtures/` | Fixture Caddyfile mounted into the e2e container. |
| `build.sh` | Builds `caddy-githost:test` from the `NOTICE` slug. |
| `Dockerfile` | Three stages: fetch toybox/certs/tzdata, build with `xcaddy`, compose a scratch image. |
| `spec.md` | Aspirational target; do not trim it to match the code. |
| `AGENTS.md` | Contributor rules. |

## Build

```sh
./build.sh
```

Produces `caddy-githost:test`. The image is scratch plus Caddy, toybox,
CA certificates, timezone data, collected licenses, and `/srv/www`. The
healthcheck probes the admin endpoint at `127.0.0.1:2019`; `80`, `443`, and
`443/udp` are exposed. No `USER` is set, so the container runs as root, which
is what the default `:22` listener needs. Publish SSH explicitly, for example
`-p 2222:22`.

`go.mod` tracks Caddy `v2.11.9`; its `go` directive is `1.26.0`, the minimum
Caddy requires, while the builder image ships Go 1.27.4.

## Test

```sh
sh test/unit.sh
./build.sh
./test/e2e.sh
```

No Go toolchain runs on the host for unit tests. The e2e harness runs the
image as the invoking uid, mounts the fixture Caddyfile, a temporary repo
root, a temporary config dir, and Caddy data/cache directories, then asserts:

- clone returns the seeded bytes;
- an owner push becomes visible remotely;
- a guest push is denied;
- a coder push to `refs/heads/carol-topic` succeeds and to `refs/heads/main`
  fails;
- a blocked handle cannot read;
- a repo in an org without an ACL is unreadable;
- `githost-list`, and a session with no command, print exactly the readable
  repos with each role's label, and nothing for a blocked handle;
- traversal paths and shell commands are rejected;
- a non-admin push to the config repository is denied; an admin push installs
  the ACLs and takes effect without a reload; adding a handle to the pushed
  `admins` file grants admin access; a malformed ACL push and a push that
  removes the pushing admin are both rejected and leave the installed files
  unchanged;
- deleting the default branch without `-o confirm-delete` is refused with the
  retry command in the error, leaves the repo and branch intact, and the
  confirmed push removes the repo while a sibling repo survives.

It also checks that the image's Caddy version matches `go.mod`, that the
shipped `www/Caddyfile` validates, and that deliberately broken Caddyfiles
(unknown subdirective, relative `repo_root`, relative `config_dir`) fail
validation.

## License

Apache-2.0, per `NOTICE` and `LICENSE`.
