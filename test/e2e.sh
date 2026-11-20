#!/bin/sh
# e2e test runner: starts caddy-gitproxy:test with the fixture Caddyfile,
# publishes the SSH port, and exercises clone, fetch, and push over SSH.

set -eu

IMAGE=caddy-gitproxy:test
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

mkdir -p "$WORK/acme" "$WORK/other" "$WORK/data" "$WORK/cache" "$WORK/seed"

git init -q --bare "$WORK/acme/app.git"
git -C "$WORK/acme/app.git" symbolic-ref HEAD refs/heads/main
git init -q --bare "$WORK/other/secret.git"
git -C "$WORK/seed" init -q
printf 'hello gitproxy\n' >"$WORK/seed/README"
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
} >"$WORK/users"
printf '@alice owner\n@bob guest\n@carol coder\n@mallory block\n' >"$WORK/acme.access"

CID=$(timeout 60s docker run -d \
	-u "$(id -u):$(id -g)" \
	-p "127.0.0.1:$PORT:2222" \
	-v "$FIXTURES/Caddyfile:/etc/caddy/Caddyfile:ro" \
	-v "$WORK:/srv/git" \
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

GIT_SSH_COMMAND="$(ssh_for "$WORK/id_alice")" timeout 60s git clone -q "$URL" "$WORK/clone"
[ "$(cat "$WORK/clone/README")" = "hello gitproxy" ] || fail "clone content mismatch"

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

if GIT_SSH_COMMAND="$(ssh_for "$WORK/id_mallory")" timeout 30s git ls-remote "$URL" >/dev/null 2>&1; then
	fail "blocked handle read the repository"
fi

if GIT_SSH_COMMAND="$(ssh_for "$WORK/id_alice")" timeout 30s git ls-remote "ssh://git@127.0.0.1:$PORT/other/secret.git" >/dev/null 2>&1; then
	fail "repository without an org ACL was readable"
fi

if command -v ssh >/dev/null 2>&1; then
	if timeout 30s ssh -i "$WORK/id_alice" $SSH_OPTS -p "$PORT" git@127.0.0.1 "git-upload-pack '../acme/app.git'" >/dev/null 2>&1; then
		fail "traversal path was accepted"
	fi
	if timeout 30s ssh -i "$WORK/id_alice" $SSH_OPTS -p "$PORT" git@127.0.0.1 "echo hi" >/dev/null 2>&1; then
		fail "shell command was accepted"
	fi
fi

printf 'e2e: ok\n'
