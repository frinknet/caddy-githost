package backend

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/frinknet/caddy-githost/pkg/access"
	"github.com/frinknet/caddy-githost/pkg/repo"
	"github.com/go-git/go-billy/v6/osfs"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/cache"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/go-git/go-git/v6/storage/filesystem"
	"github.com/go-git/go-git/v6/storage/memory"
	gossh "golang.org/x/crypto/ssh"
)

func command(ref string) *packp.Command {
	return &packp.Command{Name: plumbing.ReferenceName(ref)}
}

func TestCheckPushPolicy(t *testing.T) {
	coder := Request{Handle: "@alice", Role: access.RoleCoder}

	if err := checkPushPolicy(coder, &transport.PreReceiveInfo{
		Commands: []*packp.Command{command("refs/heads/alice-topic")},
	}); err != nil {
		t.Errorf("coder prefixed ref rejected: %v", err)
	}

	if err := checkPushPolicy(coder, &transport.PreReceiveInfo{
		Commands: []*packp.Command{command("refs/heads/main")},
	}); err == nil {
		t.Error("coder unprefixed ref accepted")
	}

	owner := Request{Handle: "@alice", Role: access.RoleOwner}
	if err := checkPushPolicy(owner, &transport.PreReceiveInfo{
		Commands: []*packp.Command{command("refs/heads/main")},
	}); err != nil {
		t.Errorf("owner push rejected: %v", err)
	}
}

func TestOpenStore(t *testing.T) {
	root := t.TempDir()
	dir, err := repo.Repo{Org: "acme", Name: "app"}.Dir(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateBare(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := openStore(dir); err != nil {
		t.Fatal(err)
	}

	missing, err := repo.Repo{Org: "acme", Name: "missing"}.Dir(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openStore(missing); err == nil {
		t.Fatal("missing repository should fail to open")
	}
}

func deleteCommand(ref string) *packp.Command {
	return &packp.Command{Name: plumbing.ReferenceName(ref), Old: plumbing.NewHash(strings.Repeat("1", 40))}
}

func headStore(t *testing.T, target plumbing.ReferenceName) *memory.Storage {
	t.Helper()
	st := memory.NewStorage()
	if err := st.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, target)); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestCheckDeletion(t *testing.T) {
	root := t.TempDir()
	r := repo.Repo{Org: "acme", Name: "app"}
	dir, err := r.Dir(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateBare(dir); err != nil {
		t.Fatal(err)
	}
	owner := Request{Repo: r, Root: root, Dir: dir, Handle: "@alice", Role: access.RoleOwner}
	mainDelete := []*packp.Command{deleteCommand("refs/heads/main")}

	if remove, err := checkDeletion(owner, &transport.PreReceiveInfo{
		Storer:   headStore(t, "refs/heads/main"),
		Commands: []*packp.Command{deleteCommand("refs/heads/topic")},
	}); err != nil || remove {
		t.Errorf("non-default deletion: remove=%v err=%v", remove, err)
	}

	remove, err := checkDeletion(owner, &transport.PreReceiveInfo{
		Storer:   headStore(t, "refs/heads/main"),
		Commands: mainDelete,
	})
	if remove || err == nil || !strings.Contains(err.Error(), "confirm-delete") {
		t.Errorf("unconfirmed default deletion: remove=%v err=%v", remove, err)
	}

	remove, err = checkDeletion(owner, &transport.PreReceiveInfo{
		Storer:      headStore(t, "refs/heads/main"),
		Commands:    mainDelete,
		PushOptions: []string{"confirm-delete"},
	})
	if !remove || err != nil {
		t.Errorf("confirmed default deletion: remove=%v err=%v", remove, err)
	}

	coder := owner
	coder.Role = access.RoleCoder
	if remove, err := checkDeletion(coder, &transport.PreReceiveInfo{
		Storer:      headStore(t, "refs/heads/main"),
		Commands:    mainDelete,
		PushOptions: []string{"confirm-delete"},
	}); remove || err == nil {
		t.Errorf("coder deletion: remove=%v err=%v", remove, err)
	}

	nonBare := owner
	nonBare.Dir = t.TempDir()
	if remove, err := checkDeletion(nonBare, &transport.PreReceiveInfo{
		Storer:      headStore(t, "refs/heads/main"),
		Commands:    mainDelete,
		PushOptions: []string{"confirm-delete"},
	}); remove || err == nil {
		t.Errorf("non-bare deletion: remove=%v err=%v", remove, err)
	}

	detached := memory.NewStorage()
	if err := detached.SetReference(plumbing.NewHashReference(plumbing.HEAD, plumbing.NewHash(strings.Repeat("2", 40)))); err != nil {
		t.Fatal(err)
	}
	if remove, err := checkDeletion(owner, &transport.PreReceiveInfo{
		Storer:   detached,
		Commands: mainDelete,
	}); err != nil || remove {
		t.Errorf("detached HEAD deletion: remove=%v err=%v", remove, err)
	}
}
func adminStorage(t *testing.T, files map[string]string) (*memory.Storage, plumbing.Hash) {
	t.Helper()
	st := memory.NewStorage()
	if err := st.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("main"))); err != nil {
		t.Fatal(err)
	}

	var entries []object.TreeEntry
	for name, content := range files {
		obj := st.NewEncodedObject()
		obj.SetType(plumbing.BlobObject)
		w, err := obj.Writer()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		hash, err := st.SetEncodedObject(obj)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, object.TreeEntry{Name: name, Mode: filemode.Regular, Hash: hash})
	}
	sort.Sort(object.TreeEntrySorter(entries))

	treeObj := st.NewEncodedObject()
	tree := &object.Tree{Entries: entries}
	if err := tree.Encode(treeObj); err != nil {
		t.Fatal(err)
	}
	treeHash, err := st.SetEncodedObject(treeObj)
	if err != nil {
		t.Fatal(err)
	}

	sig := object.Signature{Name: "e2e", Email: "e2e@example.com", When: time.Unix(0, 0)}
	commit := &object.Commit{Author: sig, Committer: sig, Message: "acl", TreeHash: treeHash}
	commitObj := st.NewEncodedObject()
	if err := commit.Encode(commitObj); err != nil {
		t.Fatal(err)
	}
	hash, err := st.SetEncodedObject(commitObj)
	if err != nil {
		t.Fatal(err)
	}
	return st, hash
}

func TestProposedAdminFiles(t *testing.T) {
	st, hash := adminStorage(t, map[string]string{
		"users":       "ssh-ed25519 AAAA @alice alice@example.com\n",
		"acme.access": "@alice owner\n",
	})

	files, err := proposedAdminFiles(st, []*packp.Command{{
		Name: plumbing.NewBranchReferenceName("main"),
		New:  hash,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if string(files["users"]) != "ssh-ed25519 AAAA @alice alice@example.com\n" {
		t.Errorf("users = %q", files["users"])
	}
	if string(files["acme.access"]) != "@alice owner\n" {
		t.Errorf("acme.access = %q", files["acme.access"])
	}

	if _, err := proposedAdminFiles(st, []*packp.Command{{
		Name: plumbing.NewBranchReferenceName("topic"),
		New:  hash,
	}}); err == nil {
		t.Error("non-default branch push should be rejected")
	}

	if _, err := proposedAdminFiles(st, []*packp.Command{{
		Name: plumbing.NewBranchReferenceName("main"),
		New:  plumbing.ZeroHash,
	}}); err == nil {
		t.Error("default branch deletion should be rejected")
	}
}

func TestValidateAdminPush(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(gossh.MarshalAuthorizedKey(key))) + " @alice alice@example.com\n"
	req := Request{Handle: "@alice"}

	files := map[string][]byte{
		"users":  []byte(line),
		"admins": []byte("@alice\n"),
	}
	if err := validateAdminPush(req, files); err != nil {
		t.Fatalf("admin retained: %v", err)
	}

	delete(files, "admins")
	if err := validateAdminPush(req, files); err == nil {
		t.Error("push dropping the admins file should be rejected")
	}

	files["admins"] = []byte("@bob\n")
	if err := validateAdminPush(req, files); err == nil {
		t.Error("push removing the pushing admin should be rejected")
	}
}

func TestSyncWorktree(t *testing.T) {
	root := t.TempDir()
	repoDir := filepath.Join(root, "config.git")
	if err := repo.CreateBare(repoDir); err != nil {
		t.Fatal(err)
	}
	st := filesystem.NewStorage(osfs.New(repoDir), cache.NewObjectLRUDefault())

	blob := st.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	w, err := blob.Writer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("users\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	blobHash, err := st.SetEncodedObject(blob)
	if err != nil {
		t.Fatal(err)
	}

	tree := &object.Tree{Entries: []object.TreeEntry{{Name: "users", Mode: filemode.Regular, Hash: blobHash}}}
	treeObj := st.NewEncodedObject()
	if err := tree.Encode(treeObj); err != nil {
		t.Fatal(err)
	}
	treeHash, err := st.SetEncodedObject(treeObj)
	if err != nil {
		t.Fatal(err)
	}

	sig := object.Signature{Name: "test", Email: "test@example.com", When: time.Unix(0, 0)}
	commit := &object.Commit{Author: sig, Committer: sig, Message: "init", TreeHash: treeHash}
	commitObj := st.NewEncodedObject()
	if err := commit.Encode(commitObj); err != nil {
		t.Fatal(err)
	}
	commitHash, err := st.SetEncodedObject(commitObj)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName("main"), commitHash)); err != nil {
		t.Fatal(err)
	}

	configDir := filepath.Join(root, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "stale.access"), []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SyncWorktree(repoDir, configDir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(configDir, "users"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "users\n" {
		t.Fatalf("users = %q", data)
	}
	gitFile, err := os.ReadFile(filepath.Join(configDir, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gitFile), "gitdir: "+repoDir) {
		t.Fatalf(".git = %q", gitFile)
	}
	if _, err := os.Stat(filepath.Join(configDir, "stale.access")); !os.IsNotExist(err) {
		t.Fatalf("stale.access should have been removed, err = %v", err)
	}

	if err := SyncWorktree(filepath.Join(root, "missing.git"), filepath.Join(root, "missing")); err != nil {
		t.Fatal(err)
	}
}
