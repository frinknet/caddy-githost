package access

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

type Store struct {
	root string
	mu   sync.Mutex
	snap atomic.Pointer[Snapshot]
}

func Open(root string) (*Store, error) {
	s := &Store{root: root}
	if err := s.Reload(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Snapshot() *Snapshot {
	return s.snap.Load()
}

func (s *Store) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reload()
}

func (s *Store) reload() error {
	files, err := readFiles(s.root)
	if err != nil {
		return err
	}
	snap, err := ParseFiles(files)
	if err != nil {
		return err
	}
	s.snap.Store(snap)
	return nil
}

func (s *Store) Apply(files map[string][]byte) error {
	snap, err := ParseFiles(files)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	prev, err := readFiles(s.root)
	if err != nil {
		prev = nil
	}
	if err := writeFiles(s.root, files); err != nil {
		if prev != nil {
			_ = writeFiles(s.root, prev)
		}
		return err
	}
	s.snap.Store(snap)
	return nil
}

func isConfigFile(name string) bool {
	return name == "users" || name == "admins" || strings.HasSuffix(name, ".access")
}

func readFiles(root string) (map[string][]byte, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read config dir: %w", err)
	}

	files := make(map[string][]byte)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !isConfigFile(name) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return nil, err
		}
		files[name] = data
	}
	return files, nil
}

func writeFiles(root string, files map[string][]byte) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}

	for name, data := range files {
		f, err := os.CreateTemp(root, ".config-*")
		if err != nil {
			return err
		}
		tmp := f.Name()
		_, writeErr := f.Write(data)
		closeErr := f.Close()
		if writeErr == nil {
			writeErr = closeErr
		}
		if writeErr == nil {
			writeErr = os.Chmod(tmp, 0o644)
		}
		if writeErr == nil {
			writeErr = os.Rename(tmp, filepath.Join(root, name))
		}
		if writeErr != nil {
			_ = os.Remove(tmp)
			return writeErr
		}
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !isConfigFile(name) {
			continue
		}
		if _, ok := files[name]; !ok {
			if err := os.Remove(filepath.Join(root, name)); err != nil {
				return err
			}
		}
	}
	return nil
}

func ParseFiles(files map[string][]byte) (*Snapshot, error) {
	users, err := parseUsers(bytes.NewReader(files["users"]))
	if err != nil {
		return nil, err
	}
	admins, err := parseAdmins(bytes.NewReader(files["admins"]))
	if err != nil {
		return nil, err
	}

	snap := &Snapshot{
		users:    users,
		byHandle: make(map[string]*User, len(users)),
		acls:     make(map[string]map[string]Role),
		admins:   admins,
	}
	for _, u := range users {
		snap.byHandle[u.Handle] = u
	}

	for name, data := range files {
		switch {
		case name == "users" || name == "admins":
		case strings.HasSuffix(name, ".access"):
			org := strings.TrimSuffix(name, ".access")
			if !handlePattern.MatchString(org) {
				return nil, fmt.Errorf("%s: invalid org name", name)
			}
			acl, err := parseACL(org, bytes.NewReader(data), snap.HasHandle)
			if err != nil {
				return nil, err
			}
			snap.acls[org] = acl
		default:
			return nil, fmt.Errorf("unexpected config file %q", name)
		}
	}

	for handle := range snap.admins {
		if !snap.HasHandle(handle) {
			return nil, fmt.Errorf("admins: unknown handle %q", handle)
		}
	}

	return snap, nil
}
