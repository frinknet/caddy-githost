package githost

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/frinknet/caddy-githost/pkg/backend"
	"github.com/frinknet/caddy-githost/pkg/repo"
)

func init() {
	caddy.RegisterModule(HTTPHandler{})
	httpcaddyfile.RegisterHandlerDirective("githost", parseHTTPHandler)
	httpcaddyfile.RegisterDirectiveOrder("githost", httpcaddyfile.After, "basic_auth")
}

// HTTPHandler serves read-only Git smart HTTP for repositories managed by the
// githost app. Push is refused; only clone and fetch are available.
type HTTPHandler struct {
	app *GitHost
}

// CaddyModule returns the Caddy module information.
func (HTTPHandler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.githost",
		New: func() caddy.Module { return new(HTTPHandler) },
	}
}

// Provision resolves the githost app that owns the access store and backend.
func (h *HTTPHandler) Provision(ctx caddy.Context) error {
	app, err := ctx.AppIfConfigured("githost")
	if err != nil {
		return err
	}
	g, ok := app.(*GitHost)
	if !ok {
		return errors.New("githost: app module has an unexpected type")
	}
	h.app = g
	return nil
}

// UnmarshalCaddyfile parses the githost directive. It takes no arguments.
func (h *HTTPHandler) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	d.Next()
	if d.NextArg() {
		return d.ArgErr()
	}
	if d.NextBlock(0) {
		return d.Err("githost: the directive takes no arguments")
	}
	return nil
}

// ServeHTTP serves Git smart HTTP. It accepts only the read-only upload-pack
// service; receive-pack and push are refused.
func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request, _ caddyhttp.Handler) error {
	if h.app == nil {
		return caddyhttp.Error(http.StatusInternalServerError, errors.New("githost: app is not provisioned"))
	}

	handle := requestHandle(r)
	if handle == "" {
		w.Header().Set("WWW-Authenticate", `Basic realm="githost"`)
		return caddyhttp.Error(http.StatusUnauthorized, errors.New("githost: authentication required"))
	}

	repoPath, action, ok := splitHTTPPath(r.URL.Path)
	if !ok {
		return caddyhttp.Error(http.StatusNotFound, errors.New("githost: not found"))
	}
	if action == "receive-pack" {
		return caddyhttp.Error(http.StatusForbidden, errors.New("githost: push is not available over HTTP"))
	}

	req, err := h.requestFor(r, handle, repoPath)
	if err != nil {
		return err
	}

	tw := &trackingWriter{ResponseWriter: w}
	switch action {
	case "info-refs":
		if r.Method != http.MethodGet {
			return caddyhttp.Error(http.StatusMethodNotAllowed, errors.New("githost: info/refs requires GET"))
		}
		if service := r.URL.Query().Get("service"); service != "git-upload-pack" {
			return caddyhttp.Error(http.StatusForbidden, errors.New("githost: only the upload-pack service is available"))
		}
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		w.Header().Set("Cache-Control", "no-cache")
		if err := h.app.backend.UploadPackInfoRefs(r.Context(), req, tw); err != nil {
			if tw.wrote {
				return nil
			}
			return caddyhttp.Error(http.StatusInternalServerError, err)
		}
		return nil

	case "upload-pack":
		if r.Method != http.MethodPost {
			return caddyhttp.Error(http.StatusMethodNotAllowed, errors.New("githost: upload-pack requires POST"))
		}
		w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
		w.Header().Set("Cache-Control", "no-cache")
		if err := h.app.backend.UploadPackHTTP(r.Context(), req, r.Body, tw); err != nil {
			if tw.wrote {
				return nil
			}
			return caddyhttp.Error(http.StatusInternalServerError, err)
		}
		return nil

	default:
		return caddyhttp.Error(http.StatusNotFound, errors.New("githost: not found"))
	}
}

type trackingWriter struct {
	http.ResponseWriter
	wrote bool
}

func (t *trackingWriter) WriteHeader(code int) {
	t.wrote = true
	t.ResponseWriter.WriteHeader(code)
}

func (t *trackingWriter) Write(p []byte) (int, error) {
	t.wrote = true
	return t.ResponseWriter.Write(p)
}

func requestHandle(r *http.Request) string {
	repl, ok := r.Context().Value(caddy.ReplacerCtxKey).(*caddy.Replacer)
	if !ok {
		return ""
	}
	handle, _ := repl.GetString("http.auth.user.id")
	if handle == "" {
		return ""
	}
	if !strings.HasPrefix(handle, "@") {
		handle = "@" + handle
	}
	return handle
}

func splitHTTPPath(path string) (repoPath, action string, ok bool) {
	path = strings.TrimPrefix(path, "/")
	switch {
	case strings.HasSuffix(path, "/info/refs"):
		return strings.TrimSuffix(path, "/info/refs"), "info-refs", true
	case strings.HasSuffix(path, "/git-upload-pack"):
		return strings.TrimSuffix(path, "/git-upload-pack"), "upload-pack", true
	case strings.HasSuffix(path, "/git-receive-pack"):
		return strings.TrimSuffix(path, "/git-receive-pack"), "receive-pack", true
	default:
		return "", "", false
	}
}

func (h *HTTPHandler) requestFor(r *http.Request, handle, repoPath string) (backend.Request, error) {
	req := backend.Request{
		Handle:      handle,
		GitProtocol: r.Header.Get("Git-Protocol"),
	}

	snap := h.app.store.Snapshot()
	if snap == nil || !snap.HasHandle(handle) {
		return req, caddyhttp.Error(http.StatusForbidden, errors.New("githost: unknown handle"))
	}

	if repoPath == h.app.ConfigRepo || repoPath == strings.TrimSuffix(h.app.ConfigRepo, ".git") {
		if !snap.IsAdmin(handle) {
			return req, caddyhttp.Error(http.StatusForbidden, errors.New("githost: config repository requires admin"))
		}
		req.Root = h.app.RepoRoot
		req.Dir = filepath.Join(h.app.RepoRoot, h.app.ConfigRepo)
		req.Admin = true
		req.Access = h.app.store
		if !repo.Exists(req.Dir) {
			return req, caddyhttp.Error(http.StatusNotFound, errors.New("githost: repository not found"))
		}
		return req, nil
	}

	rp, err := repo.Parse(repoPath)
	if err != nil {
		return req, caddyhttp.Error(http.StatusNotFound, errors.New("githost: repository not found"))
	}
	role := snap.Role(rp.Org, handle)
	if !role.CanRead() {
		return req, caddyhttp.Error(http.StatusForbidden, errors.New("githost: access denied"))
	}
	dir, err := rp.Dir(h.app.RepoRoot)
	if err != nil {
		return req, caddyhttp.Error(http.StatusNotFound, errors.New("githost: repository not found"))
	}
	req.Repo = rp
	req.Root = h.app.RepoRoot
	req.Dir = dir
	req.Role = role
	if !repo.Exists(req.Dir) {
		return req, caddyhttp.Error(http.StatusNotFound, errors.New("githost: repository not found"))
	}
	return req, nil
}

func parseHTTPHandler(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var handler HTTPHandler
	if err := handler.UnmarshalCaddyfile(h.Dispenser); err != nil {
		return nil, err
	}
	return &handler, nil
}

var (
	_ caddy.Module                = (*HTTPHandler)(nil)
	_ caddy.Provisioner           = (*HTTPHandler)(nil)
	_ caddyhttp.MiddlewareHandler = (*HTTPHandler)(nil)
	_ caddyfile.Unmarshaler       = (*HTTPHandler)(nil)
)
