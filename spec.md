# caddy-githost design

This is the target design for an SSH Git server packaged as a Caddy v2 app. It
describes the implemented design and the remaining aspirational pieces. The
current status lives in `README.md`; known gaps live in `BACKLOG.md`.

## Status

Implemented:

- One Caddy app module, `githost`, configured by a global Caddyfile option.
- SSH listener with public-key authentication against a users file.
- Per-org ACLs and role policy: owner, coder, guest, block.
- Clone, fetch, push, repository creation on owner push, and confirmed
  repository deletion.
- Repository listing over SSH with `githost-list` and a best-effort
  `git ls-remote` hint on stderr.
- A config repository, `server-access.git` by default, whose default branch is
  materialized into `config_dir` and reloaded in process.

Not implemented:

- An HTTP Git endpoint. The backend is shared by design so an HTTP handler can
  be added without loopback calls or OS accounts.

## Goals and boundaries

Reuse:

- `crypto/ed25519` and `crypto/rand` for the SSH host key.
- Caddy `ctx.Storage()` (`certmagic.Storage`) for the fallback host key.
- A single in-process `Backend` that any future HTTP handler can share.
- `golang.org/x/crypto/ssh` for key parsing, signatures, and transport types.
- `github.com/gliderlabs/ssh` for the TCP server and session handling.
- `github.com/go-git/go-git/v6` for the Git wire protocol, so the image needs
  no external `git` binary.

Out of scope:

- Interactive shells, PTYs, port forwarding, agent forwarding, SFTP.
- `/etc/ssh`, an OS `sshd`, or per-user OS accounts.
- Fake refs as a repository listing.

## Module and configuration

Module ID: `githost` (`caddy.Module`, `caddy.App`, `caddy.Provisioner`).

Caddyfile global option:

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
```

Fields:

| Field | JSON key | Default | Rules |
| --- | --- | --- | --- |
| Listen address | `listen_addr` | `:22` | `host:port`; binds the given interface, not only wildcard |
| Repository root | `repo_root` | `/var/lib/githost/repos` | absolute path, created on provision |
| Config directory | `config_dir` | `/var/caddy/config` | absolute path, created on provision |
| Config repository | `config_repo` | `server-access.git` | one path component ending in `.git`, under `repo_root` |
| Host key secret | `secret` | empty | optional; derives the host key instead of storing it |

Unknown subdirectives and extra arguments are rejected. There is no user list
in Caddy config; identity and authorization live in files.

Defaults are applied before validation. `Provision` creates `repo_root` and
`config_dir`, opens the access store, resolves the host key, and builds the
transport and backend.

## Access model

### Users

`<config_dir>/users`, one line per user:

```text
<key-type> <base64-key> @<handle> <email>
```

Blank lines and `#` comments are ignored. Handles match
`[A-Za-z0-9][A-Za-z0-9._-]*` and carry a leading `@`. Duplicate handles and
duplicate keys are load errors. A presented key authenticates as its handle. A
missing users file authenticates nobody.

### Admins

`<config_dir>/admins`, one `@handle` per line, comments allowed:

```text
@alice  # primary admin
```

Every listed handle must exist in `users`; unknown handles and duplicates are
load errors. A missing or empty file means nobody may push or clone the config
repository. The pushing handle must remain in the pushed `admins` file, so an
admin cannot lock itself out.

### Org ACLs

`<config_dir>/<org>.access`, one line per handle:

```text
@<handle> <role> [# comment]
```

| Role | Read | Push | Notes |
| --- | --- | --- | --- |
| `owner` | yes | any ref | may create missing repos in the org |
| `coder` | yes | `refs/heads/<handle>-*` only | checked before ref updates |
| `guest` | yes | no | |
| `block` | no | no | explicit denial |

Unknown handles, unknown roles, and duplicate entries are load errors. A
missing or empty ACL file denies every repository in its org.

### Loading

The access store reads `users`, `admins`, and `*.access` from `config_dir`,
parses them into an immutable snapshot, and swaps the snapshot atomically.
Loading fails closed. Snapshots are refreshed on `caddy reload` and after a
successful push to the config repository.

## Repositories

A repository path is exactly `<org>/<name>.git`. A missing `.git` suffix is
added; quotes and a leading slash are stripped. Reject deeper nesting, `..`,
invalid characters, reserved names (`users`, `server-access`, `*.access`), and
paths escaping `repo_root`.

Repositories are bare directories under `repo_root`. An `owner` pushing to a
missing repository creates it with `HEAD` at `refs/heads/main`. Other roles
cannot create repositories.

### Listing

`githost-list`, or a session that requests no command and no PTY, prints one
line per repository the handle may read:

```text
acme/app.git  owner
acme/tools.git  guest
```

The second column is the role in that org. Repos are sorted by path. Orgs the
handle cannot read, including `block`, contribute nothing. The command exits
`0` even when the listing is empty.

Before `git-upload-pack` starts, the same lines are written to the session's
stderr. The SSH client inherits that stream, so `git clone` and `git fetch`
print the readable repositories among their remote output without changing
stdout, the ref advertisement, or the exit code. Writing to stderr is best
effort: a client that disconnects first sees nothing, and the write error is
ignored. No fake refs are ever advertised.

### Deletion

An owner deletes a repository by deleting its default branch, resolved from
`HEAD`:

```sh
git push origin :refs/heads/main
```

The first push is refused and names the retry, because the deletion would
destroy every other ref with it. Confirm with the `confirm-delete` push option:

```sh
git push -o confirm-delete origin :refs/heads/main
```

Only an owner may delete. Deletion is refused for reserved names and paths that
are not bare repositories. After the ref update, the directory moves into
`<repo_root>/.trash/` and is removed, so a push either leaves the repository
untouched or removes it completely. A later owner push recreates it empty.

## Config repository

`<repo_root>/<config_repo>` is a top-level bare repository with a special
session route. Only handles listed in the current `admins` snapshot may read or
push it. A missing repository is created on the first admin push.

The default branch is the config tree. Every root entry must be `users`,
`admins`, or `<org>.access`; nested paths, non-regular files, unknown names,
and deletion of the default branch are rejected. The push must update the
default branch. The tree is parsed and validated before any ref moves.

Validation rejects a tree whose ACL references an unknown handle, whose
`admins` names an unknown handle, or whose `admins` no longer contains the
pushing handle. A rejected push leaves the installed files and the live
snapshot unchanged.

On success, `users`, `admins`, and `*.access` are written to `config_dir` with
temporary files and renames, any stale access file is removed, and the store
swaps in the new snapshot. No `caddy reload` is needed. The config repository
is not an org repository and never appears in listings.

## SSH transport

Authentication:

- Match the presented public key against the users snapshot. `KeysEqual` is
  the comparison; the SSH username is not identity.
- Store the authenticated handle in the session context.
- Deny sessions with no authenticated handle.

Allowed commands:

- `git-upload-pack '<repo>'`
- `git-receive-pack '<repo>'`
- `githost-list`
- no command and no PTY (same listing)

Everything else, including shells, `git-upload-archive`, extra arguments,
PTYs, and forwarding, is rejected with a message on stderr and exit `1`.
Errors are written to stderr before exiting `1`; success exits `0`.

Path safety rejects empty paths, traversal segments, backslashes, deeper
nesting, and paths escaping `repo_root`.

## Host key

The server presents one Ed25519 host key.

- With `secret`, the key is derived with scrypt (fixed salt
  `caddy-githost/ssh-host-key/v1`, `N=32768`, `r=8`, `p=1`) and
  `ed25519.NewKeyFromSeed`. Nothing is written to storage, and the same secret
  yields the same key across restarts.
- Without `secret`, the key is loaded from or generated into Caddy storage at
  `githost/ssh_host_ed25519` and reused across restarts and reloads.

The secret is as sensitive as a private key. It must not be blank when set.

## Lifecycle

1. `Provision`: apply defaults, validate, create `repo_root` and `config_dir`,
   open the access store, resolve the host key, construct the backend and
   server.
2. `Start`: bind `net.Listen("tcp", listen_addr)` synchronously so bind and
   permission errors surface immediately, then serve in a tracked goroutine.
3. `Stop`: close the SSH server and listener, then wait for session goroutines.

Reloading Caddy provisions a new instance and stops the old one; the host key
persists through storage or the configured secret.

## Backend

`Backend` is an in-process interface:

```go
type Backend interface {
	UploadPack(ctx context.Context, req Request, stdin io.Reader, stdout, stderr io.Writer) error
	ReceivePack(ctx context.Context, req Request, stdin io.Reader, stdout, stderr io.Writer) error
}
```

`Request` carries the parsed repository, root, directory, handle, role, Git
protocol version, and, for the config repository, the access store and admin
flag.

The go-git implementation:

- Opens only bare repositories that already exist.
- Takes a per-directory read lock for upload-pack and a write lock for
  receive-pack, so deletion and pushes do not overlap active sessions.
- Enforces coder ref prefixes and owner-only deletion in pre-receive hooks.
- Validates and applies config repository pushes through the same hooks.

## Package layout

| Package | Responsibility |
| --- | --- |
| root `githost.go` | package clause and blank imports of `pkg/config` and `pkg/githost` |
| `pkg/config` | `Config`, defaults, validation, global Caddyfile option |
| `pkg/githost` | app module, provisioning, listener lifecycle |
| `pkg/access` | users, admins, ACL parsing, snapshots, authentication, roles |
| `pkg/repo` | path parsing, safety checks, bare repository creation and removal, listing |
| `pkg/backend` | `Backend` interface, go-git adapter, locking, push policy, config tree |
| `pkg/transport` | SSH server, host key, authentication, command dispatch, listing |

## Future work

### HTTP Git endpoint

A future HTTP handler may share the same `Backend` and access snapshot. It
must not call back through localhost HTTP and must not use OS accounts. Git
smart HTTP routes would enforce the same role policy and repository rules as
SSH. Until it exists, the SSH transport is the only Git transport.

### `git ls-remote` listing hint

The listing is written to the session stderr before `upload-pack` starts, so a
client still waiting for the advertisement receives it and `git ls-remote`
shows the readable repositories on stderr, before or after the refs. The write
is best effort, its error is ignored, and its assertion tolerates an absent
hint. It must never emit fake refs.

## Antipatterns

- Trusting the SSH username or `s.User()` as identity. Map verified keys to
  handles from the users file.
- Rejecting only literal `..` without checking the joined path still stays
  under `repo_root`.
- Treating `githost-list` or a no-command session as a shell.
- Emitting fake refs to advertise repositories.
- Storing or logging the `secret`.
- Writing errors only to logs. Always write a readable message to stderr
  before an interactive failure, and exit non-zero.
- Calling the backend over loopback HTTP from the SSH path.
- Leaving the listener or server goroutines running on reload.
- Publishing the container's `:22` onto a host port already used by `sshd`.
