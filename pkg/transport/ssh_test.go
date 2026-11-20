package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/frinknet/caddy-gitproxy/pkg/access"
	"github.com/frinknet/caddy-gitproxy/pkg/backend"
	"github.com/frinknet/caddy-gitproxy/pkg/repo"
	gossh "golang.org/x/crypto/ssh"
)

type fakeBackend struct {
	mu    sync.Mutex
	calls int
	req   backend.Request
}

func (f *fakeBackend) UploadPack(_ context.Context, req backend.Request, _ io.Reader, stdout, _ io.Writer) error {
	f.record(req)
	_, err := io.WriteString(stdout, "upload-ok")
	return err
}

func (f *fakeBackend) ReceivePack(_ context.Context, req backend.Request, _ io.Reader, stdout, _ io.Writer) error {
	f.record(req)
	_, err := io.WriteString(stdout, "receive-ok")
	return err
}

func (f *fakeBackend) record(req backend.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.req = req
}

func (f *fakeBackend) snapshot() (int, backend.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.req
}

func newSigner(t *testing.T) (gossh.Signer, []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	authorized := gossh.MarshalAuthorizedKey(sshPub)
	return signer, authorized
}

func startTestServer(t *testing.T, role string, withRepo bool) (*Server, *access.Store, *fakeBackend, gossh.Signer) {
	t.Helper()
	root := t.TempDir()

	clientSigner, clientAuthorized := newSigner(t)
	fields := strings.Fields(strings.TrimSpace(string(clientAuthorized)))
	users := fmt.Sprintf("%s %s @alice alice@example.com\n", fields[0], fields[1])
	if err := os.WriteFile(filepath.Join(root, "users"), []byte(users), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "acme.access"), []byte("@alice "+role+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if withRepo {
		r := repo.Repo{Org: "acme", Name: "app"}
		dir, err := r.Dir(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.CreateBare(dir); err != nil {
			t.Fatal(err)
		}
	}

	store, err := access.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	hostKey, _ := newSigner(t)
	fake := &fakeBackend{}
	srv := NewServer(Options{
		ListenAddr: "127.0.0.1:0",
		RepoRoot:   root,
		Store:      store,
		Backend:    fake,
		HostKey:    hostKey,
	})
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Stop() })
	return srv, store, fake, clientSigner
}

func dial(t *testing.T, srv *Server, signer gossh.Signer) *gossh.Client {
	t.Helper()
	cfg := &gossh.ClientConfig{
		User:            "git",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	client, err := gossh.Dial("tcp", srv.Addr(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func runCommand(t *testing.T, client *gossh.Client, command string) (int, string, string) {
	t.Helper()
	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	var stdout, stderr bytes.Buffer
	sess.Stdout = &stdout
	sess.Stderr = &stderr
	err = sess.Run(command)

	code := 0
	if err != nil {
		var exitErr *gossh.ExitError
		if !isExitError(err, &exitErr) {
			t.Fatalf("run %q: %v", command, err)
		}
		code = exitErr.ExitStatus()
	}
	return code, stdout.String(), stderr.String()
}

func isExitError(err error, target **gossh.ExitError) bool {
	if e, ok := err.(*gossh.ExitError); ok {
		*target = e
		return true
	}
	return false
}

func TestSSHUploadPack(t *testing.T) {
	srv, _, fake, clientSigner := startTestServer(t, "owner", true)
	client := dial(t, srv, clientSigner)

	code, stdout, stderr := runCommand(t, client, `git-upload-pack 'acme/app.git'`)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if stdout != "upload-ok" {
		t.Errorf("stdout = %q", stdout)
	}
	calls, req := fake.snapshot()
	if calls != 1 {
		t.Fatalf("backend calls = %d, want 1", calls)
	}
	if req.Repo.String() != "acme/app.git" || req.Handle != "@alice" || req.Role != access.RoleOwner {
		t.Errorf("unexpected request: %+v", req)
	}
}

func TestSSHReceivePack(t *testing.T) {
	srv, _, fake, clientSigner := startTestServer(t, "coder", true)
	client := dial(t, srv, clientSigner)

	code, stdout, stderr := runCommand(t, client, `git-receive-pack 'acme/app.git'`)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if stdout != "receive-ok" {
		t.Errorf("stdout = %q", stdout)
	}
	calls, req := fake.snapshot()
	if calls != 1 || req.Role != access.RoleCoder {
		t.Errorf("unexpected backend call: calls=%d req=%+v", calls, req)
	}
}

func TestSSHGuestCannotPush(t *testing.T) {
	srv, _, fake, clientSigner := startTestServer(t, "guest", true)
	client := dial(t, srv, clientSigner)

	code, _, stderr := runCommand(t, client, `git-receive-pack 'acme/app.git'`)
	if code == 0 {
		t.Fatal("guest push succeeded")
	}
	if !strings.Contains(stderr, "access denied") {
		t.Errorf("stderr = %q", stderr)
	}
	if calls, _ := fake.snapshot(); calls != 0 {
		t.Errorf("backend called %d times", calls)
	}
}

func TestSSHMissingRepo(t *testing.T) {
	srv, _, _, clientSigner := startTestServer(t, "owner", false)
	client := dial(t, srv, clientSigner)

	code, _, stderr := runCommand(t, client, `git-upload-pack 'acme/app.git'`)
	if code == 0 || !strings.Contains(stderr, "repository not found") {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
}

func TestSSHRejectsShell(t *testing.T) {
	srv, _, _, clientSigner := startTestServer(t, "owner", true)
	client := dial(t, srv, clientSigner)

	code, _, stderr := runCommand(t, client, "echo hi")
	if code == 0 || !strings.Contains(stderr, "command not permitted") {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
}

func TestSSHRejectsUnknownKey(t *testing.T) {
	srv, _, _, _ := startTestServer(t, "owner", true)
	other, _ := newSigner(t)

	cfg := &gossh.ClientConfig{
		User:            "git",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(other)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	if client, err := gossh.Dial("tcp", srv.Addr(), cfg); err == nil {
		_ = client.Close()
		t.Fatal("unauthenticated client connected")
	}
}
