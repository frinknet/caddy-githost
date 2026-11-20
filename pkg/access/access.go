package access

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	gossh "golang.org/x/crypto/ssh"
)

type Role string

const (
	RoleNone  Role = ""
	RoleOwner Role = "owner"
	RoleCoder Role = "coder"
	RoleGuest Role = "guest"
	RoleBlock Role = "block"
)

var handlePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func ParseRole(s string) (Role, error) {
	switch Role(s) {
	case RoleOwner, RoleCoder, RoleGuest, RoleBlock:
		return Role(s), nil
	default:
		return RoleNone, fmt.Errorf("unknown role %q", s)
	}
}

func (r Role) CanRead() bool {
	return r == RoleOwner || r == RoleCoder || r == RoleGuest
}

func (r Role) CanPush() bool {
	return r == RoleOwner || r == RoleCoder
}

func ParseHandle(s string) (string, error) {
	if !strings.HasPrefix(s, "@") {
		return "", fmt.Errorf("handle %q must start with @", s)
	}
	body := strings.TrimPrefix(s, "@")
	if !handlePattern.MatchString(body) {
		return "", fmt.Errorf("invalid handle %q", s)
	}
	return s, nil
}

func HandleName(handle string) string {
	return strings.TrimPrefix(handle, "@")
}

type User struct {
	Handle string
	Email  string
	Key    gossh.PublicKey
}

type Snapshot struct {
	users    []*User
	byHandle map[string]*User
	acls     map[string]map[string]Role
}

func (s *Snapshot) Authenticate(key gossh.PublicKey) (string, bool) {
	if s == nil {
		return "", false
	}
	for _, u := range s.users {
		if bytes.Equal(key.Marshal(), u.Key.Marshal()) {
			return u.Handle, true
		}
	}
	return "", false
}

func (s *Snapshot) Role(org, handle string) Role {
	if s == nil {
		return RoleNone
	}
	orgACL, ok := s.acls[org]
	if !ok {
		return RoleNone
	}
	return orgACL[handle]
}

func (s *Snapshot) CanRead(org, handle string) bool {
	return s.Role(org, handle).CanRead()
}

func (s *Snapshot) CanPush(org, handle string) bool {
	return s.Role(org, handle).CanPush()
}
