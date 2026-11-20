package transport

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"

	"github.com/frinknet/caddy-gitproxy/pkg/access"
	"github.com/frinknet/caddy-gitproxy/pkg/backend"
	"github.com/frinknet/caddy-gitproxy/pkg/repo"
	"github.com/gliderlabs/ssh"
	"go.uber.org/zap"
	gossh "golang.org/x/crypto/ssh"
)

const handleKey = "gitproxy.handle"

type Options struct {
	ListenAddr string
	RepoRoot   string
	Store      *access.Store
	Backend    backend.Backend
	HostKey    gossh.Signer
	Logger     *zap.Logger
}

type Server struct {
	addr     string
	repoRoot string
	store    *access.Store
	backend  backend.Backend
	logger   *zap.Logger
	srv      *ssh.Server
	ln       net.Listener
	wg       sync.WaitGroup
}

func NewServer(opts Options) *Server {
	s := &Server{
		addr:     opts.ListenAddr,
		repoRoot: opts.RepoRoot,
		store:    opts.Store,
		backend:  opts.Backend,
		logger:   opts.Logger,
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
		return fmt.Errorf("gitproxy: listen %s: %w", s.addr, err)
	}
	s.ln = ln

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, ssh.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			s.logError("gitproxy ssh server stopped", err)
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

	action, r, err := parseGitCommand(sess.Command())
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

func (s *Server) fail(sess ssh.Session, msg string) {
	_, _ = fmt.Fprintln(sess.Stderr(), "gitproxy: "+msg)
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
