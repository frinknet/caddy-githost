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

func TestParseAdminCommand(t *testing.T) {
	cases := []struct {
		args []string
		act  action
		ok   bool
	}{
		{[]string{"git-upload-pack", "server-access.git"}, actionUploadPack, true},
		{[]string{"git-receive-pack", "'server-access'"}, actionReceivePack, true},
		{[]string{"git-upload-pack", "/server-access.git"}, actionUploadPack, true},
		{[]string{"git-upload-pack", "acme/app.git"}, "", false},
		{[]string{"git-receive-pack", "acme/server-access.git"}, "", false},
		{[]string{"sh", "-c", "server-access.git"}, "", false},
		{[]string{"git-upload-pack"}, "", false},
	}
	for _, tc := range cases {
		act, ok, err := parseAdminCommand(tc.args, "server-access.git")
		if err != nil {
			t.Errorf("parseAdminCommand(%q): %v", tc.args, err)
			continue
		}
		if ok != tc.ok || act != tc.act {
			t.Errorf("parseAdminCommand(%q) = %q, %v; want %q, %v", tc.args, act, ok, tc.act, tc.ok)
		}
	}
}

func TestParseAdminCommandCustomRepo(t *testing.T) {
	act, ok, err := parseAdminCommand([]string{"git-receive-pack", "acme-config.git"}, "acme-config.git")
	if err != nil || !ok || act != actionReceivePack {
		t.Fatalf("custom config repo: act=%q ok=%v err=%v", act, ok, err)
	}
	if _, ok, _ := parseAdminCommand([]string{"git-receive-pack", "server-access.git"}, "acme-config.git"); ok {
		t.Error("default name must not match a custom config repo")
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

func TestHostKeyFromPhrase(t *testing.T) {
	first, err := HostKeyFromPhrase("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	second, err := HostKeyFromPhrase("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.PublicKey().Marshal(), second.PublicKey().Marshal()) {
		t.Error("same phrase produced different host keys")
	}

	other, err := HostKeyFromPhrase("another phrase")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first.PublicKey().Marshal(), other.PublicKey().Marshal()) {
		t.Error("different phrases produced the same host key")
	}
}

func TestHostKeyPersists(t *testing.T) {
	storage := &certmagic.FileStorage{Path: t.TempDir()}
	ctx := context.Background()

	first, err := LoadOrCreateHostKey(ctx, storage, "githost/ssh_host_ed25519")
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateHostKey(ctx, storage, "githost/ssh_host_ed25519")
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(first.PublicKey().Marshal(), second.PublicKey().Marshal()) {
		t.Error("host key changed between loads")
	}
}
