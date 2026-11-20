package access

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"
)

func testUser(t *testing.T, handle, email string) (string, gossh.PublicKey) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(gossh.MarshalAuthorizedKey(key)))
	fields := strings.Fields(line)
	return fmt.Sprintf("%s %s %s %s", fields[0], fields[1], handle, email), key
}

func TestParseUsers(t *testing.T) {
	lineA, keyA := testUser(t, "@alice", "alice@example.com")
	lineB, keyB := testUser(t, "@bob", "bob@example.com")
	input := "# comment\n\n" + lineA + "\n" + lineB + "\n"

	users, err := parseUsers(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 {
		t.Fatalf("got %d users, want 2", len(users))
	}
	if users[0].Handle != "@alice" || users[0].Email != "alice@example.com" {
		t.Errorf("unexpected first user: %+v", users[0])
	}
	if !bytes.Equal(users[0].Key.Marshal(), keyA.Marshal()) {
		t.Error("first user key mismatch")
	}
	if !bytes.Equal(users[1].Key.Marshal(), keyB.Marshal()) {
		t.Error("second user key mismatch")
	}
}

func TestParseUsersRejectsDuplicates(t *testing.T) {
	lineA, _ := testUser(t, "@alice", "alice@example.com")
	lineB, _ := testUser(t, "@alice", "other@example.com")
	if _, err := parseUsers(strings.NewReader(lineA + "\n" + lineB + "\n")); err == nil {
		t.Fatal("expected duplicate handle error")
	}

	lineC, keyC := testUser(t, "@carol", "carol@example.com")
	fields := strings.Fields(lineC)
	lineD := strings.Join([]string{fields[0], fields[1], "@dave", "dave@example.com"}, " ")
	if _, err := parseUsers(strings.NewReader(lineC + "\n" + lineD + "\n")); err == nil {
		t.Fatal("expected duplicate key error")
	}
	_ = keyC
}

func TestParseUsersRejectsBadLines(t *testing.T) {
	lineA, _ := testUser(t, "@alice", "alice@example.com")
	fields := strings.Fields(lineA)

	cases := map[string]string{
		"three fields":  strings.Join(fields[:3], " "),
		"bad handle":    strings.Join([]string{fields[0], fields[1], "alice", "a@b"}, " "),
		"bad key":       "ssh-ed25519 notbase64 @alice a@b",
		"wrong type":    strings.Join([]string{"ssh-rsa", fields[1], "@alice", "a@b"}, " "),
		"five fields":   lineA + " extra",
		"missing email": strings.Join(fields[:3], " "),
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseUsers(strings.NewReader(line + "\n")); err == nil {
				t.Fatal("expected parse error")
			}
		})
	}
}

func TestParseACL(t *testing.T) {
	known := map[string]bool{"@alice": true, "@bob": true}
	input := "# owners\n@alice owner\n@bob guest # visitor\n"
	acl, err := parseACL("acme", strings.NewReader(input), func(h string) bool { return known[h] })
	if err != nil {
		t.Fatal(err)
	}
	if acl["@alice"] != RoleOwner {
		t.Errorf("@alice role = %q, want owner", acl["@alice"])
	}
	if acl["@bob"] != RoleGuest {
		t.Errorf("@bob role = %q, want guest", acl["@bob"])
	}
}

func TestParseACLRejectsInvalidEntries(t *testing.T) {
	known := map[string]bool{"@alice": true}
	cases := map[string]string{
		"unknown handle": "@nobody owner",
		"unknown role":   "@alice admin",
		"duplicate":      "@alice owner\n@alice guest",
		"missing role":   "@alice",
		"extra field":    "@alice owner extra",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseACL("acme", strings.NewReader(input), func(h string) bool { return known[h] }); err == nil {
				t.Fatal("expected parse error")
			}
		})
	}
}

func TestStore(t *testing.T) {
	root := t.TempDir()
	lineA, keyA := testUser(t, "@alice", "alice@example.com")
	lineB, keyB := testUser(t, "@bob", "bob@example.com")
	if err := os.WriteFile(filepath.Join(root, "users"), []byte(lineA+"\n"+lineB+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "acme.access"), []byte("@alice owner\n@bob guest\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	snap := store.Snapshot()

	if handle, ok := snap.Authenticate(keyA); !ok || handle != "@alice" {
		t.Errorf("Authenticate(alice) = %q, %v", handle, ok)
	}
	if handle, ok := snap.Authenticate(keyB); !ok || handle != "@bob" {
		t.Errorf("Authenticate(bob) = %q, %v", handle, ok)
	}
	if snap.Role("acme", "@alice") != RoleOwner {
		t.Errorf("alice role = %q, want owner", snap.Role("acme", "@alice"))
	}
	if !snap.CanRead("acme", "@alice") || !snap.CanPush("acme", "@alice") {
		t.Error("owner should read and push")
	}
	if !snap.CanRead("acme", "@bob") || snap.CanPush("acme", "@bob") {
		t.Error("guest should read but not push")
	}
	if snap.CanRead("other", "@alice") {
		t.Error("missing org ACL should deny")
	}
}

func TestStoreReload(t *testing.T) {
	root := t.TempDir()
	lineA, _ := testUser(t, "@alice", "alice@example.com")
	if err := os.WriteFile(filepath.Join(root, "users"), []byte(lineA+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	aclPath := filepath.Join(root, "acme.access")
	if err := os.WriteFile(aclPath, []byte("@alice guest\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if snap := store.Snapshot(); snap.CanPush("acme", "@alice") {
		t.Fatal("alice should not push yet")
	}

	if err := os.WriteFile(aclPath, []byte("@alice owner\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Reload(); err != nil {
		t.Fatal(err)
	}
	if snap := store.Snapshot(); !snap.CanPush("acme", "@alice") {
		t.Fatal("alice should push after reload")
	}
}

func TestStoreFailClosed(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	lineA, keyA := testUser(t, "@alice", "alice@example.com")
	_ = lineA
	if _, ok := store.Snapshot().Authenticate(keyA); ok {
		t.Error("missing users file must authenticate nobody")
	}

	if err := os.WriteFile(filepath.Join(root, "users"), []byte("not a valid line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root); err == nil {
		t.Error("malformed users file must fail to load")
	}
}

func TestParseFiles(t *testing.T) {
	lineA, keyA := testUser(t, "@alice", "alice@example.com")
	files := map[string][]byte{
		"users":       []byte(lineA + "\n"),
		"admins":      []byte("# keep this handle\n@alice\n"),
		"acme.access": []byte("@alice owner\n"),
	}

	snap, err := ParseFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	if handle, ok := snap.Authenticate(keyA); !ok || handle != "@alice" {
		t.Errorf("Authenticate(alice) = %q, %v", handle, ok)
	}
	if snap.Role("acme", "@alice") != RoleOwner {
		t.Errorf("alice role = %q, want owner", snap.Role("acme", "@alice"))
	}
	if !snap.HasHandle("@alice") || snap.HasHandle("@bob") {
		t.Error("HasHandle mismatch")
	}
	if !snap.IsAdmin("@alice") || snap.IsAdmin("@bob") {
		t.Error("IsAdmin mismatch")
	}

	if _, err := ParseFiles(map[string][]byte{
		"users":       []byte(lineA + "\n"),
		"acme.access": []byte("@nobody owner\n"),
	}); err == nil {
		t.Error("unknown handle must fail")
	}
	if _, err := ParseFiles(map[string][]byte{"notes.txt": []byte("x")}); err == nil {
		t.Error("unexpected file must fail")
	}
	if _, err := ParseFiles(map[string][]byte{
		"users":  []byte(lineA + "\n"),
		"admins": []byte("@nobody\n"),
	}); err == nil {
		t.Error("unknown admin handle must fail")
	}
}

func TestStoreApply(t *testing.T) {
	root := t.TempDir()
	lineA, _ := testUser(t, "@alice", "alice@example.com")
	if err := os.WriteFile(filepath.Join(root, "users"), []byte(lineA+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "acme.access"), []byte("@alice guest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "old.access"), []byte("@alice guest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "admins"), []byte("@alice\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if store.Snapshot().CanPush("acme", "@alice") {
		t.Fatal("alice should not push yet")
	}

	files := map[string][]byte{
		"users":       []byte(lineA + "\n"),
		"admins":      []byte("@alice\n"),
		"acme.access": []byte("@alice owner\n"),
	}
	if err := store.Apply(files); err != nil {
		t.Fatal(err)
	}
	if !store.Snapshot().CanPush("acme", "@alice") {
		t.Error("alice should push after apply")
	}
	if !store.Snapshot().IsAdmin("@alice") {
		t.Error("alice should be admin after apply")
	}
	if _, err := os.Stat(filepath.Join(root, "old.access")); !os.IsNotExist(err) {
		t.Error("stale old.access was not removed")
	}
	data, err := os.ReadFile(filepath.Join(root, "acme.access"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "@alice owner\n" {
		t.Errorf("acme.access = %q", data)
	}

	bad := map[string][]byte{
		"users":       []byte(lineA + "\n"),
		"acme.access": []byte("@nobody owner\n"),
	}
	if err := store.Apply(bad); err == nil {
		t.Fatal("expected apply error")
	}
	if !store.Snapshot().CanPush("acme", "@alice") {
		t.Error("failed apply must keep the previous snapshot")
	}
	data, err = os.ReadFile(filepath.Join(root, "acme.access"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "@alice owner\n" {
		t.Errorf("failed apply changed acme.access to %q", data)
	}
}

func TestStoreRejectsUnknownACLHandle(t *testing.T) {
	root := t.TempDir()
	lineA, _ := testUser(t, "@alice", "alice@example.com")
	if err := os.WriteFile(filepath.Join(root, "users"), []byte(lineA+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "acme.access"), []byte("@nobody owner\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root); err == nil {
		t.Fatal("unknown handle in ACL must fail")
	}
}
