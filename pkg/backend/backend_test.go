package backend

import (
	"testing"

	"github.com/frinknet/caddy-gitproxy/pkg/access"
	"github.com/frinknet/caddy-gitproxy/pkg/repo"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"
	"github.com/go-git/go-git/v6/plumbing/transport"
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
