package mux

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitHubRepo(t *testing.T) {
	for url, want := range map[string]string{
		"git@github.com:acme/desktop-app.git":  "acme/desktop-app",
		"https://github.com/someuser/web-sdk":  "someuser/web-sdk",
		"https://x-token@github.com/a/b.c.git": "a/b.c",
		"ssh://git@github.com/a/b/":            "a/b",
		"ssh://git@github.com:22/a/b.git":      "a/b",
		"https://gitlab.example.com/a/b.git":   "",
		"git@github.com.evil.example:a/b.git":  "",
		"https://github.com/a/b/c":             "",
		"":                                     "",
	} {
		if got := GitHubRepo(url); got != want {
			t.Errorf("GitHubRepo(%q) = %q, want %q", url, got, want)
		}
	}
}

func TestRepoOf(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "main")
	write := func(p, s string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(main, ".git", "config"), "[core]\n\tbare = false\n[remote \"upstream\"]\n\turl = git@github.com:up/x.git\n[remote \"origin\"]\n\turl = git@github.com:org/app.git\n")
	if err := os.MkdirAll(filepath.Join(main, "src", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := RepoOf(filepath.Join(main, "src", "deep")); got != "org/app" {
		t.Errorf("subfolder: %q", got)
	}
	// A linked worktree: .git is a file, the config is the main one's.
	wt := filepath.Join(root, "wt")
	write(filepath.Join(main, ".git", "worktrees", "wt", "commondir"), "../..\n")
	write(filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(main, ".git", "worktrees", "wt")+"\n")
	if got := RepoOf(wt); got != "org/app" {
		t.Errorf("worktree: %q", got)
	}
	if got := RepoOf(filepath.Join(root, "none")); got != "" {
		t.Errorf("outside: %q", got)
	}
	if got := RepoOf("relative"); got != "" {
		t.Errorf("relative: %q", got)
	}
}
