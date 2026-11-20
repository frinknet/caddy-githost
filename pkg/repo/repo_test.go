package repo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		input string
		want  Repo
	}{
		{"acme/app.git", Repo{Org: "acme", Name: "app"}},
		{"acme/app", Repo{Org: "acme", Name: "app"}},
		{"'acme/app.git'", Repo{Org: "acme", Name: "app"}},
		{"\"acme/app.git\"", Repo{Org: "acme", Name: "app"}},
		{"/acme/app.git", Repo{Org: "acme", Name: "app"}},
		{" acme/app.git ", Repo{Org: "acme", Name: "app"}},
		{"org/My.Repo_1-2.git", Repo{Org: "org", Name: "My.Repo_1-2"}},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got, err := Parse(tc.input)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("Parse(%q) = %+v, want %+v", tc.input, got, tc.want)
			}
		})
	}
}

func TestParseRejectsInvalid(t *testing.T) {
	cases := []string{
		"",
		"repo.git",
		"a/b/c.git",
		"../app.git",
		"acme/../app.git",
		"acme/..",
		"acme/.",
		"acme/.git",
		`acme\app.git`,
		"acme/users.git",
		"acme/server-access.git",
		"acme/secret.access.git",
		"acme/-bad.git",
		"acme ga/app.git",
	}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			if _, err := Parse(input); err == nil {
				t.Errorf("Parse(%q) should fail", input)
			}
		})
	}
}

func TestDirStaysUnderRoot(t *testing.T) {
	root := t.TempDir()
	r := Repo{Org: "acme", Name: "app"}
	dir, err := r.Dir(root)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "acme", "app.git")
	if dir != want {
		t.Errorf("Dir = %q, want %q", dir, want)
	}
}

func TestCreateAndList(t *testing.T) {
	root := t.TempDir()
	r := Repo{Org: "acme", Name: "app"}
	dir, err := r.Dir(root)
	if err != nil {
		t.Fatal(err)
	}
	if Exists(dir) {
		t.Fatal("repo should not exist yet")
	}
	if err := CreateBare(dir); err != nil {
		t.Fatal(err)
	}
	if !Exists(dir) {
		t.Fatal("repo should exist")
	}
	for _, name := range []string{"HEAD", "config", "description"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s missing: %v", name, err)
		}
	}

	other := Repo{Org: "acme", Name: "other"}
	otherDir, err := other.Dir(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := CreateBare(otherDir); err != nil {
		t.Fatal(err)
	}

	repos, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 {
		t.Fatalf("List returned %d repos, want 2: %+v", len(repos), repos)
	}
	seen := map[string]bool{}
	for _, listed := range repos {
		seen[listed.String()] = true
	}
	if !seen["acme/app.git"] || !seen["acme/other.git"] {
		t.Errorf("unexpected listing: %+v", repos)
	}
}

func TestRemoveBare(t *testing.T) {
	root := t.TempDir()
	dir, err := (Repo{Org: "acme", Name: "app"}).Dir(root)
	if err != nil {
		t.Fatal(err)
	}
	sibling, err := (Repo{Org: "acme", Name: "other"}).Dir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{dir, sibling} {
		if IsBare(d) {
			t.Fatalf("%s should not be bare before creation", d)
		}
		if err := CreateBare(d); err != nil {
			t.Fatal(err)
		}
		if !IsBare(d) {
			t.Fatalf("%s should be bare", d)
		}
	}

	if err := RemoveBare(root, dir); err != nil {
		t.Fatal(err)
	}
	if Exists(dir) {
		t.Error("removed repo still exists")
	}
	if !Exists(sibling) {
		t.Error("sibling repo was removed")
	}
	repos, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].String() != "acme/other.git" {
		t.Errorf("unexpected listing after removal: %+v", repos)
	}
}
