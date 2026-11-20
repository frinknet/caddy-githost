package backend

import (
	"strings"
	"testing"

	"github.com/frinknet/caddy-gitproxy/pkg/access"
	"github.com/frinknet/caddy-gitproxy/pkg/repo"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/go-git/go-git/v6/storage/memory"
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
