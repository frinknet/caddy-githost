package access

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

type Store struct {
	root string
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
	snap, err := loadSnapshot(s.root)
	if err != nil {
		return err
	}
	s.snap.Store(snap)
	return nil
}

func loadSnapshot(root string) (*Snapshot, error) {
	users, err := loadUsers(filepath.Join(root, "users"))
	if err != nil {
		return nil, err
	}

	snap := &Snapshot{
		users:    users,
		byHandle: make(map[string]*User, len(users)),
		acls:     make(map[string]map[string]Role),
	}
	for _, u := range users {
		snap.byHandle[u.Handle] = u
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read repo root: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".access") {
			continue
		}
		org := strings.TrimSuffix(name, ".access")
		if !handlePattern.MatchString(org) {
			return nil, fmt.Errorf("%s: invalid org name", name)
		}

		f, err := os.Open(filepath.Join(root, name))
		if err != nil {
			return nil, err
		}
		acl, parseErr := parseACL(org, f, func(handle string) bool {
			_, ok := snap.byHandle[handle]
			return ok
		})
		closeErr := f.Close()
		if parseErr != nil {
			return nil, parseErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		snap.acls[org] = acl
	}

	return snap, nil
}

func loadUsers(path string) ([]*User, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	users, parseErr := parseUsers(f)
	closeErr := f.Close()
	if parseErr != nil {
		return nil, parseErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return users, nil
}
