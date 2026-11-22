package githost

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/frinknet/caddy-githost/pkg/access"
	"github.com/frinknet/caddy-githost/pkg/backend"
	"github.com/frinknet/caddy-githost/pkg/config"
	gossh "golang.org/x/crypto/ssh"
)

type fakeHTTPBackend struct {
	infoRefs bool
	upload   bool
	lastReq  backend.Request
}

func (f *fakeHTTPBackend) UploadPack(context.Context, backend.Request, io.Reader, io.Writer, io.Writer) error {
	return nil
}

func (f *fakeHTTPBackend) ReceivePack(context.Context, backend.Request, io.Reader, io.Writer, io.Writer) error {
	return nil
}

func (f *fakeHTTPBackend) UploadPackInfoRefs(_ context.Context, req backend.Request, w io.Writer) error {
	f.infoRefs = true
	f.lastReq = req
	_, err := io.WriteString(w, "info-refs")
	return err
}

func (f *fakeHTTPBackend) UploadPackHTTP(_ context.Context, req backend.Request, _ io.Reader, w io.Writer) error {
	f.upload = true
	f.lastReq = req
	_, err := io.WriteString(w, "upload-pack")
	return err
}

func userLine(t *testing.T, handle string) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(gossh.MarshalAuthorizedKey(key)))
	fields := strings.Fields(line)
	if len(fields) != 2 {
		t.Fatalf("unexpected authorized key format: %q", line)
	}
	return fields[0] + " " + fields[1] + " " + handle + " " + handle[1:] + "@example.com"
}

func testStore(t *testing.T) *access.Store {
	t.Helper()
	dir := t.TempDir()

	users := userLine(t, "@alice") + "\n" + userLine(t, "@bob") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "users"), []byte(users), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "admins"), []byte("@alice\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "acme.access"), []byte("@alice owner\n@bob block\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := access.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func newTestHandler(t *testing.T, store *access.Store, backend backend.Backend) *HTTPHandler {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"acme/app.git", "server-access.git"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return &HTTPHandler{
		app: &GitHost{
			Config: config.Config{
				RepoRoot:   root,
				ConfigRepo: "server-access.git",
			},
			store:   store,
			backend: backend,
		},
	}
}

func requestWithHandle(method, target, handle string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	repl := caddy.NewReplacer()
	repl.Set("http.auth.user.id", handle)
	return req.WithContext(context.WithValue(req.Context(), caddy.ReplacerCtxKey, repl))
}

func TestSplitHTTPPath(t *testing.T) {
	tests := []struct {
		path   string
		repo   string
		action string
		ok     bool
	}{
		{"/acme/app.git/info/refs", "acme/app.git", "info-refs", true},
		{"/acme/app.git/git-upload-pack", "acme/app.git", "upload-pack", true},
		{"/acme/app.git/git-receive-pack", "acme/app.git", "receive-pack", true},
		{"/acme/app.git", "", "", false},
		{"/", "", "", false},
	}
	for _, tt := range tests {
		repo, action, ok := splitHTTPPath(tt.path)
		if repo != tt.repo || action != tt.action || ok != tt.ok {
			t.Errorf("splitHTTPPath(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.path, repo, action, ok, tt.repo, tt.action, tt.ok)
		}
	}
}

func TestRequestHandle(t *testing.T) {
	req := requestWithHandle(http.MethodGet, "/", "alice")
	if got := requestHandle(req); got != "@alice" {
		t.Errorf("requestHandle = %q, want @alice", got)
	}
	if got := requestHandle(httptest.NewRequest(http.MethodGet, "/", nil)); got != "" {
		t.Errorf("requestHandle without replacer = %q, want empty", got)
	}
}

func TestHTTPHandlerInfoRefs(t *testing.T) {
	store := testStore(t)
	fake := &fakeHTTPBackend{}
	h := newTestHandler(t, store, fake)

	req := requestWithHandle(http.MethodGet, "/acme/app.git/info/refs?service=git-upload-pack", "alice")
	rec := httptest.NewRecorder()
	if err := h.ServeHTTP(rec, req, nil); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "info-refs" {
		t.Errorf("body = %q, want info-refs", got)
	}
	if !fake.infoRefs {
		t.Error("backend UploadPackInfoRefs was not called")
	}
	if fake.lastReq.Handle != "@alice" {
		t.Errorf("handle = %q, want @alice", fake.lastReq.Handle)
	}
	if want := filepath.Join(h.app.RepoRoot, "acme", "app.git"); fake.lastReq.Dir != want {
		t.Errorf("dir = %q, want %q", fake.lastReq.Dir, want)
	}
}

func TestHTTPHandlerUploadPack(t *testing.T) {
	store := testStore(t)
	fake := &fakeHTTPBackend{}
	h := newTestHandler(t, store, fake)

	req := requestWithHandle(http.MethodPost, "/acme/app.git/git-upload-pack", "alice")
	rec := httptest.NewRecorder()
	if err := h.ServeHTTP(rec, req, nil); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "upload-pack" {
		t.Errorf("body = %q, want upload-pack", got)
	}
	if !fake.upload {
		t.Error("backend UploadPackHTTP was not called")
	}
}

func TestHTTPHandlerRejectsPush(t *testing.T) {
	store := testStore(t)
	h := newTestHandler(t, store, &fakeHTTPBackend{})

	for _, target := range []string{
		"/acme/app.git/info/refs?service=git-receive-pack",
		"/acme/app.git/git-receive-pack",
	} {
		req := requestWithHandle(http.MethodGet, target, "alice")
		rec := httptest.NewRecorder()
		err := h.ServeHTTP(rec, req, nil)
		if err == nil {
			t.Fatalf("%s: expected error", target)
		}
		if e, ok := err.(caddyhttp.HandlerError); !ok || e.StatusCode != http.StatusForbidden {
			t.Errorf("%s: error = %v, want 403", target, err)
		}
	}
}

func TestHTTPHandlerBlocked(t *testing.T) {
	store := testStore(t)
	h := newTestHandler(t, store, &fakeHTTPBackend{})

	req := requestWithHandle(http.MethodGet, "/acme/app.git/info/refs?service=git-upload-pack", "bob")
	rec := httptest.NewRecorder()
	err := h.ServeHTTP(rec, req, nil)
	if e, ok := err.(caddyhttp.HandlerError); !ok || e.StatusCode != http.StatusForbidden {
		t.Errorf("error = %v, want 403", err)
	}
}

func TestHTTPHandlerUnknownHandle(t *testing.T) {
	store := testStore(t)
	h := newTestHandler(t, store, &fakeHTTPBackend{})

	req := requestWithHandle(http.MethodGet, "/acme/app.git/info/refs?service=git-upload-pack", "carol")
	rec := httptest.NewRecorder()
	err := h.ServeHTTP(rec, req, nil)
	if e, ok := err.(caddyhttp.HandlerError); !ok || e.StatusCode != http.StatusForbidden {
		t.Errorf("error = %v, want 403", err)
	}
}

func TestHTTPHandlerConfigRepo(t *testing.T) {
	store := testStore(t)
	fake := &fakeHTTPBackend{}
	h := newTestHandler(t, store, fake)

	req := requestWithHandle(http.MethodGet, "/server-access.git/info/refs?service=git-upload-pack", "alice")
	rec := httptest.NewRecorder()
	if err := h.ServeHTTP(rec, req, nil); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}
	if !fake.infoRefs {
		t.Error("config repo info/refs was not served")
	}
	if want := filepath.Join(h.app.RepoRoot, "server-access.git"); fake.lastReq.Dir != want {
		t.Errorf("dir = %q, want %q", fake.lastReq.Dir, want)
	}

	req = requestWithHandle(http.MethodGet, "/server-access.git/info/refs?service=git-upload-pack", "bob")
	rec = httptest.NewRecorder()
	err := h.ServeHTTP(rec, req, nil)
	if e, ok := err.(caddyhttp.HandlerError); !ok || e.StatusCode != http.StatusForbidden {
		t.Errorf("non-admin config repo error = %v, want 403", err)
	}
}
