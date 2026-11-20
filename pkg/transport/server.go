package transport

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/frinknet/caddy-githost/pkg/access"
	"github.com/frinknet/caddy-githost/pkg/backend"
	"github.com/frinknet/caddy-githost/pkg/repo"
	"github.com/gliderlabs/ssh"
	"go.uber.org/zap"
	gossh "golang.org/x/crypto/ssh"
)

const (
	handleKey         = "githost.handle"
	defaultConfigRepo = "server-access.git"
)

type Options struct {
	ListenAddr string
	RepoRoot   string
	ConfigRepo string
	Store      *access.Store
	Backend    backend.Backend
	HostKey    gossh.Signer
	Logger     *zap.Logger
}

type Server struct {
	addr       string
	repoRoot   string
	configRepo string
	store      *access.Store
	backend    backend.Backend
	logger     *zap.Logger
	srv        *ssh.Server
	ln         net.Listener
	wg         sync.WaitGroup
}

func NewServer(opts Options) *Server {
	configRepo := opts.ConfigRepo
	if configRepo == "" {
		configRepo = defaultConfigRepo
	}
	s := &Server{
		addr:       opts.ListenAddr,
		repoRoot:   opts.RepoRoot,
		configRepo: configRepo,
		store:      opts.Store,
		backend:    opts.Backend,
		logger:     opts.Logger,
	}
	s.srv = &ssh.Server{
		Addr:             opts.ListenAddr,
		Handler:          s.handleSession,
		PublicKeyHandler: s.handlePublicKey,
		PtyCallback: func(ssh.Context, ssh.Pty) bool {
			return false
		},
	}
	s.srv.AddHostKey(opts.HostKey)
	return s
}

func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("githost: listen %s: %w", s.addr, err)
	}
	s.ln = ln

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, ssh.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			s.logError("githost ssh server stopped", err)
		}
	}()
	return nil
}

func (s *Server) Stop() error {
	if s.srv != nil {
		_ = s.srv.Close()
	}
	if s.ln != nil {
		_ = s.ln.Close()
	}
	s.wg.Wait()
	return nil
}

func (s *Server) Addr() string {
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

func (s *Server) handlePublicKey(ctx ssh.Context, key ssh.PublicKey) bool {
	handle, ok := s.store.Snapshot().Authenticate(key)
	if !ok {
		return false
	}
	ctx.SetValue(handleKey, handle)
	return true
}

func (s *Server) handleSession(sess ssh.Session) {
	handle, _ := sess.Context().Value(handleKey).(string)
	if handle == "" {
		s.fail(sess, "authentication required")
		return
	}

	args := sess.Command()
	if len(args) == 0 || (len(args) == 1 && args[0] == "githost-list") {
		s.listRepositories(sess, handle)
		return
	}

	if action, ok, err := parseAdminCommand(args, s.configRepo); ok {
		if err != nil {
			s.fail(sess, err.Error())
			return
		}
		s.handleAdmin(sess, handle, action)
		return
	}

	action, r, err := parseGitCommand(args)
	if err != nil {
		s.fail(sess, err.Error())
		return
	}

	role := s.store.Snapshot().Role(r.Org, handle)
	dir, err := r.Dir(s.repoRoot)
	if err != nil {
		s.fail(sess, "invalid repository path")
		return
	}

	req := backend.Request{Repo: r, Root: s.repoRoot, Dir: dir, Handle: handle, Role: role, GitProtocol: gitProtocolEnv(sess.Environ())}
	switch action {
	case actionUploadPack:
		if !role.CanRead() {
			s.fail(sess, "access denied")
			return
		}
		if !repo.Exists(dir) {
			s.fail(sess, "repository not found")
			return
		}
		s.writeListingHint(sess, handle)
		err = s.backend.UploadPack(sess.Context(), req, sess, sess, sess.Stderr())
	case actionReceivePack:
		if !role.CanPush() {
			s.fail(sess, "access denied")
			return
		}
		if !repo.Exists(dir) {
			if role != access.RoleOwner {
				s.fail(sess, "access denied")
				return
			}
			if err := repo.CreateBare(dir); err != nil {
				s.fail(sess, "create repository: "+err.Error())
				return
			}
		}
		err = s.backend.ReceivePack(sess.Context(), req, sess, sess, sess.Stderr())
	}

	if err != nil {
		s.fail(sess, err.Error())
		return
	}
	_ = sess.Exit(0)
}

func (s *Server) handleAdmin(sess ssh.Session, handle string, act action) {
	if !s.isAdmin(handle) {
		s.fail(sess, "access denied")
		return
	}

	dir := filepath.Join(s.repoRoot, s.configRepo)
	if act == actionReceivePack && !repo.Exists(dir) {
		if err := repo.CreateBare(dir); err != nil {
			s.fail(sess, "create repository: "+err.Error())
			return
		}
	}
	if !repo.Exists(dir) {
		s.fail(sess, "repository not found")
		return
	}

	req := backend.Request{
		Root:        s.repoRoot,
		Dir:         dir,
		Handle:      handle,
		GitProtocol: gitProtocolEnv(sess.Environ()),
		Admin:       true,
		Access:      s.store,
	}
	var err error
	switch act {
	case actionUploadPack:
		err = s.backend.UploadPack(sess.Context(), req, sess, sess, sess.Stderr())
	case actionReceivePack:
		err = s.backend.ReceivePack(sess.Context(), req, sess, sess, sess.Stderr())
	}
	if err != nil {
		s.fail(sess, err.Error())
		return
	}
	_ = sess.Exit(0)
}

func (s *Server) isAdmin(handle string) bool {
	return s.store.Snapshot().IsAdmin(handle)
}

func parseAdminCommand(args []string, configRepo string) (action, bool, error) {
	if len(args) != 2 {
		return "", false, nil
	}
	var act action
	switch args[0] {
	case "git-upload-pack":
		act = actionUploadPack
	case "git-receive-pack":
		act = actionReceivePack
	default:
		return "", false, nil
	}
	if !isAdminPath(args[1], configRepo) {
		return "", false, nil
	}
	return act, true, nil
}

func isAdminPath(arg, configRepo string) bool {
	raw := strings.Trim(strings.TrimSpace(arg), `"'`)
	raw = strings.TrimPrefix(raw, "/")
	return raw == configRepo || raw == strings.TrimSuffix(configRepo, ".git")
}

func (s *Server) listRepositories(sess ssh.Session, handle string) {
	lines, err := s.repositoryListing(handle)
	if err != nil {
		s.fail(sess, "list repositories: "+err.Error())
		return
	}
	for _, line := range lines {
		if _, err := fmt.Fprintln(sess, line); err != nil {
			s.fail(sess, "list repositories: "+err.Error())
			return
		}
	}
	_ = sess.Exit(0)
}

func (s *Server) writeListingHint(sess ssh.Session, handle string) {
	lines, err := s.repositoryListing(handle)
	if err != nil {
		return
	}
	for _, line := range lines {
		if _, err := fmt.Fprintln(sess.Stderr(), line); err != nil {
			return
		}
	}
}

func (s *Server) repositoryListing(handle string) ([]string, error) {
	repos, err := repo.List(s.repoRoot)
	if err != nil {
		return nil, err
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].String() < repos[j].String() })

	snapshot := s.store.Snapshot()
	lines := make([]string, 0, len(repos))
	for _, r := range repos {
		role := snapshot.Role(r.Org, handle)
		if !role.CanRead() {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s  %s", r, role))
	}
	return lines, nil
}

func (s *Server) fail(sess ssh.Session, msg string) {
	_, _ = fmt.Fprintln(sess.Stderr(), "githost: "+msg)
	_ = sess.Exit(1)
}

func (s *Server) logError(msg string, err error) {
	if s.logger != nil {
		s.logger.Error(msg, zap.Error(err))
	}
}

func gitProtocolEnv(env []string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "GIT_PROTOCOL="); ok {
			return v
		}
	}
	return ""
}

type action string

const (
	actionUploadPack  action = "upload-pack"
	actionReceivePack action = "receive-pack"
)

func parseGitCommand(args []string) (action, repo.Repo, error) {
	if len(args) != 2 {
		return "", repo.Repo{}, fmt.Errorf("command not permitted")
	}

	var act action
	switch args[0] {
	case "git-upload-pack":
		act = actionUploadPack
	case "git-receive-pack":
		act = actionReceivePack
	default:
		return "", repo.Repo{}, fmt.Errorf("command not permitted")
	}

	r, err := repo.Parse(args[1])
	if err != nil {
		return "", repo.Repo{}, fmt.Errorf("invalid repository path")
	}
	return act, r, nil
}
