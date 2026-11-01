package mux

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseForge(t *testing.T) {
	gh := func(p string) *Forge { return &Forge{Kind: "github", Host: "github.com", Path: p} }
	gl := func(h, p string) *Forge { return &Forge{Kind: "gitlab", Host: h, Path: p} }
	for url, want := range map[string]*Forge{
		"git@github.com:acme/desktop-app.git":                  gh("acme/desktop-app"),
		"https://github.com/someuser/web-sdk":                  gh("someuser/web-sdk"),
		"https://x-token@github.com/a/b.c.git":                 gh("a/b.c"),
		"ssh://git@github.com:22/a/b.git":                      gh("a/b"),
		"https://GitHub.com/A/b/":                              gh("A/b"),
		"git@gitlab.com:group/sub/project.git":                 gl("gitlab.com", "group/sub/project"),
		"https://gitlab.example.com/infra/cluster.git":         gl("gitlab.example.com", "infra/cluster"),
		"https://oauth2:tok@gitlab.example.org:8443/a/b/c.git": gl("gitlab.example.org:8443", "a/b/c"),
		"ssh://git@gitlab.example.com:2222/team/app.git":       gl("gitlab.example.com", "team/app"),
		"git@my-gitlab.corp:team/app":                          gl("my-gitlab.corp", "team/app"),
		"https://github.com/a/b/c":                             nil,
		"https://github.com/a.b/c":                             nil,
		"git@github.com.evil.example:a/b.git":                  nil,
		"https://gitlab.com/only":                              nil,
		"https://gitlab.com/a/../b":                            nil,
		"https://gitlab.com/a/%2e%2e/b":                        nil,
		"https://gitlab.com/a/b?x=1":                           nil,
		"https://bitbucket.org/a/b.git":                        nil,
		"git@gitlab.com:/abs/path.git":                         nil,
		"/srv/git/repo.git":                                    nil,
		"":                                                     nil,
	} {
		got := ParseForge(url)
		if (got == nil) != (want == nil) || (got != nil && *got != *want) {
			t.Errorf("ParseForge(%q) = %+v, want %+v", url, got, want)
		}
	}
}

func TestForgeKey(t *testing.T) {
	f := Forge{Kind: "gitlab", Host: "GitLab.com", Path: "Group/Sub/app"}
	if f.Namespace() != "Group/Sub" || f.Key() != "gitlab:gitlab.com/group/sub" {
		t.Errorf("%q %q", f.Namespace(), f.Key())
	}
}

func TestForgeOfMarksSeen(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("[remote \"origin\"]\n\turl = git@gitlab.com:seen-group/x.git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ForgeSeen("gitlab:gitlab.com/seen-group") {
		t.Fatal("seen before any window")
	}
	f := ForgeOf(dir)
	if f == nil || f.Path != "seen-group/x" || RepoOf(dir) != "" {
		t.Fatalf("ForgeOf = %+v, RepoOf = %q", f, RepoOf(dir))
	}
	if !ForgeSeen("gitlab:gitlab.com/seen-group") || ForgeSeen("gitlab:gitlab.com/other") {
		t.Error("seen list")
	}
}
