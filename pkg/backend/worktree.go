package backend

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/frinknet/caddy-githost/pkg/repo"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
)

const gitDirName = ".git"

func SyncWorktree(repoDir, configDir string) error {
	if !repo.Exists(repoDir) {
		return nil
	}
	if !repo.IsBare(repoDir) {
		return fmt.Errorf("config repository: %s is not a bare repository", repoDir)
	}

	st, err := openStore(repoDir)
	if err != nil {
		return err
	}
	head, err := st.Reference(plumbing.HEAD)
	if err != nil {
		return fmt.Errorf("config repository: read HEAD: %w", err)
	}
	target := head.Name()
	if head.Type() == plumbing.SymbolicReference {
		target = head.Target()
	}
	ref, err := st.Reference(target)
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return nil
		}
		return fmt.Errorf("config repository: read %s: %w", target, err)
	}

	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return err
	}
	gitFile := filepath.Join(configDir, gitDirName)
	if err := os.WriteFile(gitFile, []byte("gitdir: "+repoDir+"\n"), 0o644); err != nil {
		return err
	}

	r, err := git.PlainOpen(configDir)
	if err != nil {
		return fmt.Errorf("config worktree: open: %w", err)
	}
	wt, err := r.Worktree()
	if err != nil {
		return fmt.Errorf("config worktree: worktree: %w", err)
	}
	if err := wt.Reset(&git.ResetOptions{Commit: ref.Hash(), Mode: git.HardReset}); err != nil {
		return fmt.Errorf("config worktree: reset: %w", err)
	}

	commit, err := object.GetCommit(st, ref.Hash())
	if err != nil {
		return err
	}
	return removeStaleConfigFiles(configDir, commit)
}

func removeStaleConfigFiles(configDir string, commit *object.Commit) error {
	tree, err := commit.Tree()
	if err != nil {
		return err
	}
	keep := make(map[string]bool)
	for _, entry := range tree.Entries {
		keep[entry.Name] = true
	}
	entries, err := os.ReadDir(configDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == gitDirName {
			continue
		}
		if !isConfigFile(entry.Name()) || keep[entry.Name()] {
			continue
		}
		if err := os.Remove(filepath.Join(configDir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func isConfigFile(name string) bool {
	return name == "users" || name == "admins" || strings.HasSuffix(name, ".access")
}
