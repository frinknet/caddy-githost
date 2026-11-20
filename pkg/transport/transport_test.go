package transport

import (
	"bytes"
	"context"
	"testing"

	"github.com/caddyserver/certmagic"
)

func TestParseGitCommand(t *testing.T) {
	act, r, err := parseGitCommand([]string{"git-upload-pack", "'acme/app.git'"})
	if err != nil {
		t.Fatal(err)
	}
	if act != actionUploadPack || r.String() != "acme/app.git" {
		t.Errorf("got %q %q", act, r.String())
	}

	act, r, err = parseGitCommand([]string{"git-receive-pack", "acme/app"})
	if err != nil {
		t.Fatal(err)
	}
	if act != actionReceivePack || r.String() != "acme/app.git" {
		t.Errorf("got %q %q", act, r.String())
	}
}

func TestParseGitCommandRejects(t *testing.T) {
	cases := [][]string{
		{},
		{"git-upload-pack"},
		{"git-upload-pack", "acme/app.git", "extra"},
		{"git-upload-archive", "acme/app.git"},
		{"git-upload-pack", "../app.git"},
		{"sh", "-c", "echo hi"},
	}
	for _, args := range cases {
		if _, _, err := parseGitCommand(args); err == nil {
			t.Errorf("parseGitCommand(%q) should fail", args)
		}
	}
}

func TestGitProtocolEnv(t *testing.T) {
	if got := gitProtocolEnv([]string{"HOME=/tmp", "GIT_PROTOCOL=version=2"}); got != "version=2" {
		t.Errorf("got %q", got)
	}
	if got := gitProtocolEnv([]string{"HOME=/tmp"}); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestHostKeyPersists(t *testing.T) {
	storage := &certmagic.FileStorage{Path: t.TempDir()}
	ctx := context.Background()

	first, err := LoadOrCreateHostKey(ctx, storage, "gitproxy/ssh_host_ed25519")
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateHostKey(ctx, storage, "gitproxy/ssh_host_ed25519")
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(first.PublicKey().Marshal(), second.PublicKey().Marshal()) {
		t.Error("host key changed between loads")
	}
}
