package repo

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

const trashDir = ".trash"

type Repo struct {
	Org  string
	Name string
}

func Parse(arg string) (Repo, error) {
	raw := strings.Trim(strings.TrimSpace(arg), `"'`)
	raw = strings.TrimPrefix(raw, "/")
	if raw == "" {
		return Repo{}, fmt.Errorf("empty repository path")
	}
	if strings.Contains(raw, `\`) {
		return Repo{}, fmt.Errorf("invalid repository path %q", arg)
	}

	parts := strings.Split(raw, "/")
	if len(parts) != 2 {
		return Repo{}, fmt.Errorf("repository path must be <org>/<name>.git: %q", arg)
	}

	org := parts[0]
	name := strings.TrimSuffix(parts[1], ".git")
	if org == "" || name == "" {
		return Repo{}, fmt.Errorf("invalid repository path %q", arg)
	}
	if !namePattern.MatchString(org) || !namePattern.MatchString(name) {
		return Repo{}, fmt.Errorf("invalid repository path %q", arg)
	}
	if org == "." || org == ".." || name == "." || name == ".." {
		return Repo{}, fmt.Errorf("invalid repository path %q", arg)
	}
	if isReserved(name) {
		return Repo{}, fmt.Errorf("reserved repository name %q", name)
	}

	return Repo{Org: org, Name: name}, nil
}

func isReserved(name string) bool {
	return name == "users" || name == "server-access" || strings.HasSuffix(name, ".access")
}

func (r Repo) String() string {
	return r.Org + "/" + r.Name + ".git"
}

func (r Repo) Dir(root string) (string, error) {
	dir := filepath.Join(root, r.Org, r.Name+".git")
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("repository path escapes root: %q", r.String())
	}
	return dir, nil
}

func Exists(dir string) bool {
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

func IsBare(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "objects"))
	if err != nil || !info.IsDir() {
		return false
	}
	_, err = os.Stat(filepath.Join(dir, "HEAD"))
	return err == nil
}

func RemoveBare(root, dir string) error {
	trash := filepath.Join(root, trashDir)
	if err := os.MkdirAll(trash, 0o755); err != nil {
		return err
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return err
	}
	name := strconv.FormatInt(time.Now().UnixNano(), 10) + "-" + strings.ReplaceAll(filepath.ToSlash(rel), "/", "-")
	target := filepath.Join(trash, name)
	if err := os.Rename(dir, target); err != nil {
		return err
	}
	return os.RemoveAll(target)
}

func CreateBare(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, sub := range []string{"refs/heads", "refs/tags", "objects/info", "objects/pack", "hooks", "info"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(sub)), 0o755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		return err
	}
	config := "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = true\n"
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(config), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "description"), []byte("gitproxy bare repository\n"), 0o644)
}

func List(root string) ([]Repo, error) {
	orgs, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}

	var repos []Repo
	for _, orgEntry := range orgs {
		if !orgEntry.IsDir() || !namePattern.MatchString(orgEntry.Name()) {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(root, orgEntry.Name()))
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasSuffix(entry.Name(), ".git") {
				continue
			}
			name := strings.TrimSuffix(entry.Name(), ".git")
			if name == "" || !namePattern.MatchString(name) || isReserved(name) {
				continue
			}
			repos = append(repos, Repo{Org: orgEntry.Name(), Name: name})
		}
	}
	return repos, nil
}
