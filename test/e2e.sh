#!/bin/sh
# e2e test runner: starts caddy-githost:test with the fixture Caddyfile,
# publishes the SSH port, and exercises clone, fetch, and push over SSH.

set -eu

IMAGE=caddy-githost:test
PORT=${E2E_SSH_PORT:-2222}
FIXTURES=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/e2e/fixtures
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
WORK=$(mktemp -d)
CID=
SSH_OPTS="-o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR"

cleanup() {
	if [ -n "$CID" ]; then
		timeout 20s docker rm -f "$CID" >/dev/null 2>&1 || true
	fi
	rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

fail() {
	printf 'e2e: %s\n' "$*" >&2
	exit 1
}

command -v git >/dev/null 2>&1 || fail "git is required on the host"
command -v ssh-keygen >/dev/null 2>&1 || fail "ssh-keygen is required on the host"

image_version=$(timeout 20s docker run --rm "$IMAGE" version | awk '{print $1}')
[ -n "$image_version" ] || fail "could not read the image's Caddy version"
grep -q "github.com/caddyserver/caddy/v2 $image_version" "$ROOT/go.mod" ||
	fail "image Caddy $image_version does not match go.mod"

timeout 30s docker run --rm "$IMAGE" validate \
	--config /srv/www/Caddyfile --adapter caddyfile >/dev/null ||
	fail "shipped Caddyfile does not validate"

for broken in "$FIXTURES"/broken/*.Caddyfile; do
	[ -e "$broken" ] || fail "no broken Caddyfile fixtures"
	if timeout 30s docker run --rm \
		-v "$broken:/etc/caddy/broken.Caddyfile:ro" \
		"$IMAGE" validate --config /etc/caddy/broken.Caddyfile --adapter caddyfile \
		>/dev/null 2>&1; then
		fail "$(basename "$broken") unexpectedly validated"
	fi
done

mkdir -p "$WORK/acme" "$WORK/other" "$WORK/config" "$WORK/data" "$WORK/cache" "$WORK/seed"

git init -q --bare "$WORK/acme/app.git"
git -C "$WORK/acme/app.git" symbolic-ref HEAD refs/heads/main
git init -q --bare "$WORK/acme/tools.git"
git init -q --bare "$WORK/other/secret.git"
git -C "$WORK/seed" init -q
printf 'hello githost\n' >"$WORK/seed/README"
git -C "$WORK/seed" add README
git -C "$WORK/seed" -c user.name=e2e -c user.email=e2e@example.com commit -qm init
git -C "$WORK/seed" push -q "$WORK/acme/app.git" HEAD:refs/heads/main

for user in alice bob carol mallory; do
	ssh-keygen -q -t ed25519 -N '' -f "$WORK/id_$user"
done

user_line() {
	key=$1
	handle=$2
	line=$(ssh-keygen -y -f "$key")
	printf '%s %s %s %s\n' "$(printf '%s' "$line" | awk '{print $1}')" \
		"$(printf '%s' "$line" | awk '{print $2}')" "$handle" "$handle@example.com"
}

{
	user_line "$WORK/id_alice" @alice
	user_line "$WORK/id_bob" @bob
	user_line "$WORK/id_carol" @carol
	user_line "$WORK/id_mallory" @mallory
} >"$WORK/config/users"
printf '@alice owner\n@bob guest\n@carol coder\n@mallory block\n' >"$WORK/config/acme.access"
printf '@alice\n' >"$WORK/config/admins"

CID=$(timeout 60s docker run -d \
	-u "$(id -u):$(id -g)" \
	-p "127.0.0.1:$PORT:2222" \
	-v "$FIXTURES/Caddyfile:/etc/caddy/Caddyfile:ro" \
	-v "$WORK:/srv/git" \
	-v "$WORK/config:/srv/config" \
	-v "$WORK/data:/var/lib/caddy" \
	-v "$WORK/cache:/var/cache/caddy" \
	"$IMAGE" run --config /etc/caddy/Caddyfile --adapter caddyfile)
[ -n "$CID" ] || fail "docker run produced no container id"

URL="ssh://git@127.0.0.1:$PORT/acme/app.git"

ssh_for() {
	printf 'ssh -i %s %s -p %s' "$1" "$SSH_OPTS" "$PORT"
}

wait_for_ssh() {
	tries=0
	while [ "$tries" -lt 30 ]; do
		if GIT_SSH_COMMAND="$(ssh_for "$WORK/id_alice")" timeout 5s git ls-remote "$URL" >/dev/null 2>&1; then
			return 0
		fi
		tries=$((tries + 1))
		sleep 1
	done
	return 1
}

if ! wait_for_ssh; then
	printf 'e2e: SSH endpoint did not become ready\n' >&2
	timeout 5s git ls-remote "$URL" 2>&1 || true
	timeout 5s ssh -vv -i "$WORK/id_alice" $SSH_OPTS -p "$PORT" git@127.0.0.1 "git-upload-pack 'acme/app.git'" 2>&1 | tail -20 || true
	timeout 10s docker logs "$CID" 2>&1 | tail -20 || true
	timeout 20s docker run --rm --entrypoint /bin/caddy \
		-v "$FIXTURES/Caddyfile:/etc/caddy/Caddyfile:ro" \
		"$IMAGE" adapt --config /etc/caddy/Caddyfile --adapter caddyfile 2>&1 | head -40 || true
	fail "SSH endpoint did not become ready; see diagnostics above"
fi

[ -z "$(find "$WORK/data" -name ssh_host_ed25519 -print -quit 2>/dev/null)" ] ||
	fail "host key was stored despite secret"

GIT_SSH_COMMAND="$(ssh_for "$WORK/id_alice")" timeout 60s git clone -q "$URL" "$WORK/clone" 2>"$WORK/clone.err"
[ "$(cat "$WORK/clone/README")" = "hello githost" ] || fail "clone content mismatch"
# The hint is best effort and git may prefix or reorder it, so match its lines
# rather than the whole stderr stream.
grep -q 'acme/app.git  owner' "$WORK/clone.err" || {
	cat "$WORK/clone.err" >&2
	fail "clone stderr did not carry the owner repository hint"
}
grep -q 'acme/tools.git  owner' "$WORK/clone.err" || {
	cat "$WORK/clone.err" >&2
	fail "clone stderr did not carry the sibling repository hint"
}

printf 'push from owner\n' >>"$WORK/clone/README"
git -C "$WORK/clone" add README
git -C "$WORK/clone" -c user.name=e2e -c user.email=e2e@example.com commit -qm owner-push
GIT_SSH_COMMAND="$(ssh_for "$WORK/id_alice")" timeout 60s git -C "$WORK/clone" push -q origin HEAD:refs/heads/main
remote_head=$(GIT_SSH_COMMAND="$(ssh_for "$WORK/id_alice")" timeout 30s git ls-remote "$URL" refs/heads/main | awk '{print $1}')
[ "$(git -C "$WORK/clone" rev-parse HEAD)" = "$remote_head" ] || fail "owner push was not visible remotely"

GIT_SSH_COMMAND="$(ssh_for "$WORK/id_bob")" timeout 60s git clone -q "$URL" "$WORK/bob-clone"
printf 'guest push\n' >>"$WORK/bob-clone/README"
git -C "$WORK/bob-clone" add README
git -C "$WORK/bob-clone" -c user.name=e2e -c user.email=e2e@example.com commit -qm guest-push
if GIT_SSH_COMMAND="$(ssh_for "$WORK/id_bob")" timeout 60s git -C "$WORK/bob-clone" push -q origin HEAD:refs/heads/main 2>/dev/null; then
	fail "guest push succeeded"
fi

GIT_SSH_COMMAND="$(ssh_for "$WORK/id_carol")" timeout 60s git clone -q "$URL" "$WORK/carol-clone"
printf 'coder push\n' >>"$WORK/carol-clone/README"
git -C "$WORK/carol-clone" add README
git -C "$WORK/carol-clone" -c user.name=e2e -c user.email=e2e@example.com commit -qm coder-push
GIT_SSH_COMMAND="$(ssh_for "$WORK/id_carol")" timeout 60s git -C "$WORK/carol-clone" push -q origin HEAD:refs/heads/carol-topic
if GIT_SSH_COMMAND="$(ssh_for "$WORK/id_carol")" timeout 60s git -C "$WORK/carol-clone" push -q origin HEAD:refs/heads/main 2>/dev/null; then
	fail "coder push to main succeeded"
fi

if GIT_SSH_COMMAND="$(ssh_for "$WORK/id_mallory")" timeout 30s git ls-remote "$URL" >/dev/null 2>"$WORK/mallory.err"; then
	fail "blocked handle read the repository"
fi
grep -q 'acme/app.git  ' "$WORK/mallory.err" && fail "blocked handle saw the repository hint"

if GIT_SSH_COMMAND="$(ssh_for "$WORK/id_alice")" timeout 30s git ls-remote "ssh://git@127.0.0.1:$PORT/other/secret.git" >/dev/null 2>&1; then
	fail "repository without an org ACL was readable"
fi

ssh_exec() {
	timeout 30s ssh -T -i "$WORK/id_$1" $SSH_OPTS -p "$PORT" git@127.0.0.1 "$2" </dev/null
}

owner_listing=$(printf 'acme/app.git  owner\nacme/tools.git  owner')
guest_listing=$(printf 'acme/app.git  guest\nacme/tools.git  guest')
coder_listing=$(printf 'acme/app.git  coder\nacme/tools.git  coder')
[ "$(ssh_exec alice githost-list)" = "$owner_listing" ] || fail "owner listing mismatch"
[ "$(ssh_exec bob githost-list)" = "$guest_listing" ] || fail "guest listing mismatch"
[ "$(ssh_exec carol githost-list)" = "$coder_listing" ] || fail "coder listing mismatch"
[ -z "$(ssh_exec mallory githost-list)" ] || fail "blocked handle saw a repository listing"
[ "$(ssh_exec alice '')" = "$owner_listing" ] || fail "no-command listing mismatch"

if command -v ssh >/dev/null 2>&1; then
	if timeout 30s ssh -i "$WORK/id_alice" $SSH_OPTS -p "$PORT" git@127.0.0.1 "git-upload-pack '../acme/app.git'" >/dev/null 2>&1; then
		fail "traversal path was accepted"
	fi
	if timeout 30s ssh -i "$WORK/id_alice" $SSH_OPTS -p "$PORT" git@127.0.0.1 "echo hi" >/dev/null 2>&1; then
		fail "shell command was accepted"
	fi
fi

ADMIN_URL="ssh://git@127.0.0.1:$PORT/githost-config.git"
mkdir "$WORK/admin"
git -C "$WORK/admin" init -q
cp "$WORK/config/users" "$WORK/config/admins" "$WORK/config/acme.access" "$WORK/admin/"
git -C "$WORK/admin" add users admins acme.access
git -C "$WORK/admin" -c user.name=e2e -c user.email=e2e@example.com commit -qm acl

if GIT_SSH_COMMAND="$(ssh_for "$WORK/id_bob")" timeout 60s \
	git -C "$WORK/admin" push -q "$ADMIN_URL" HEAD:refs/heads/main 2>/dev/null; then
	fail "non-admin pushed server-access.git"
fi

GIT_SSH_COMMAND="$(ssh_for "$WORK/id_alice")" timeout 60s \
	git -C "$WORK/admin" push -q "$ADMIN_URL" HEAD:refs/heads/main
[ -d "$WORK/githost-config.git" ] || fail "config_repo path was not created"

printf '@alice owner\n@bob owner\n@carol coder\n@mallory block\n' >"$WORK/admin/acme.access"
printf '@alice\n@bob\n' >"$WORK/admin/admins"
git -C "$WORK/admin" add acme.access admins
git -C "$WORK/admin" -c user.name=e2e -c user.email=e2e@example.com commit -qm promote-bob
GIT_SSH_COMMAND="$(ssh_for "$WORK/id_alice")" timeout 60s \
	git -C "$WORK/admin" push -q "$ADMIN_URL" HEAD:refs/heads/main

GIT_SSH_COMMAND="$(ssh_for "$WORK/id_bob")" timeout 60s \
	git -C "$WORK/bob-clone" push -q origin HEAD:refs/heads/main ||
	fail "ACL push did not take effect"

GIT_SSH_COMMAND="$(ssh_for "$WORK/id_bob")" timeout 30s \
	git ls-remote "$ADMIN_URL" >/dev/null ||
	fail "admins file in the repo did not grant admin access"

printf '@nobody owner\n' >"$WORK/admin/acme.access"
git -C "$WORK/admin" add acme.access
git -C "$WORK/admin" -c user.name=e2e -c user.email=e2e@example.com commit -qm malformed
if GIT_SSH_COMMAND="$(ssh_for "$WORK/id_alice")" timeout 60s \
	git -C "$WORK/admin" push -q "$ADMIN_URL" HEAD:refs/heads/main 2>/dev/null; then
	fail "malformed ACL push was accepted"
fi
grep -q '@nobody' "$WORK/config/acme.access" && fail "malformed ACL was installed"
[ "$(ssh_exec bob githost-list)" = "$owner_listing" ] || fail "malformed ACL push changed access"

printf '@alice owner\n@bob owner\n@carol coder\n@mallory block\n' >"$WORK/admin/acme.access"
printf '@bob\n' >"$WORK/admin/admins"
git -C "$WORK/admin" add acme.access admins
git -C "$WORK/admin" -c user.name=e2e -c user.email=e2e@example.com commit -qm lockout
if GIT_SSH_COMMAND="$(ssh_for "$WORK/id_alice")" timeout 60s \
	git -C "$WORK/admin" push -q "$ADMIN_URL" HEAD:refs/heads/main 2>/dev/null; then
	fail "push removing the pushing admin was accepted"
fi
grep -q '@alice' "$WORK/config/admins" || fail "admin lockout was installed"

if GIT_SSH_COMMAND="$(ssh_for "$WORK/id_alice")" timeout 60s \
	git -C "$WORK/clone" push -q origin :refs/heads/main 2>"$WORK/delete.out"; then
	fail "unconfirmed default branch deletion succeeded"
fi
grep -q "confirm-delete" "$WORK/delete.out" || fail "rejection did not print the confirmation command"
[ -d "$WORK/acme/app.git" ] || fail "unconfirmed deletion removed the repository"
GIT_SSH_COMMAND="$(ssh_for "$WORK/id_alice")" timeout 30s git ls-remote "$URL" refs/heads/main |
	grep -q . || fail "unconfirmed deletion removed the default branch"

GIT_SSH_COMMAND="$(ssh_for "$WORK/id_alice")" timeout 60s \
	git -C "$WORK/clone" push -q -o confirm-delete origin :refs/heads/main
[ ! -e "$WORK/acme/app.git" ] || fail "confirmed deletion left the repository in place"
[ -d "$WORK/other/secret.git" ] || fail "confirmed deletion removed a sibling repository"

printf 'e2e: ok\n'
