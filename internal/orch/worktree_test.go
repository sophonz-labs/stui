package orch

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func sh(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func repo(t *testing.T) string {
	t.Helper()
	t.Setenv("STUI_HOME", t.TempDir())
	dir := t.TempDir()
	sh(t, dir, "init", "-q", "-b", "main")
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	os.WriteFile(filepath.Join(dir, "src", "a.txt"), []byte("one\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".env.local\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".env.local"), []byte("SECRET=1\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".worktreeinclude"), []byte("# untracked files a worktree needs\n.env.local\n"), 0o644)
	sh(t, dir, "add", "-A")
	sh(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func TestAWorkersWorktreeMergesBack(t *testing.T) {
	dir := repo(t)
	w, err := MakeWorktree(filepath.Join(dir, "src"), "r-12345678", "api")
	if err != nil {
		t.Fatal(err)
	}
	if w.Branch != "stui/r-12345678/api" || filepath.Base(w.Dir) != "src" || !strings.HasPrefix(w.Path, WorktreesDir()) {
		t.Fatalf("%+v", w)
	}
	if b, _ := os.ReadFile(filepath.Join(w.Path, ".env.local")); string(b) != "SECRET=1\n" {
		t.Fatal(".worktreeinclude was not copied")
	}
	os.WriteFile(filepath.Join(w.Dir, "b.txt"), []byte("new\n"), 0o644)
	if d, _ := w.Dirty(); !d {
		t.Fatal("dirty")
	}
	status, diff, err := w.Changes(false)
	if err != nil || !strings.Contains(status, "b.txt") {
		t.Fatalf("%q %q %v", status, diff, err)
	}
	if err := w.Commit("stui: api"); err != nil {
		t.Fatal(err)
	}
	r, err := w.Merge(false)
	if err != nil || !r.Merged || r.Into != "main" || r.Commits != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "src", "b.txt")); err != nil {
		t.Fatal("not merged in")
	}
	if err := w.Remove(false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(w.Path); !os.IsNotExist(err) {
		t.Fatal("worktree left")
	}
}

func TestAConflictLeavesTheCheckoutAsItWas(t *testing.T) {
	dir := repo(t)
	w, err := MakeWorktree(dir, "r-12345678", "ui")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(w.Path, "src", "a.txt"), []byte("worker\n"), 0o644)
	w.Commit("worker")
	os.WriteFile(filepath.Join(dir, "src", "a.txt"), []byte("main\n"), 0o644)
	sh(t, dir, "commit", "-q", "-am", "main")
	r, err := w.Merge(false)
	if err != nil || r.Merged || len(r.Conflicts) != 1 || r.Conflicts[0] != "src/a.txt" {
		t.Fatalf("%+v %v", r, err)
	}
	if s := sh(t, dir, "status", "--porcelain"); s != "" {
		t.Fatalf("left half merged: %q", s)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "src", "a.txt")); string(b) != "main\n" {
		t.Fatal("the checkout changed")
	}
}

// A folder git does not know is not refused: stui keeps a shadow
// repository for it in its own home (never a .git in the folder), takes a
// snapshot of the folder as the base, and the worktree, diff and merge are
// the same git as anywhere.
func TestNotGitGoesThroughAShadow(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	os.MkdirAll(filepath.Join(dir, "node_modules", "x"), 0o755)
	os.WriteFile(filepath.Join(dir, "src", "a.txt"), []byte("one\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "node_modules", "x", "i.js"), []byte("junk\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "run.log"), []byte("log\n"), 0o644)

	w, err := MakeWorktree(dir, "r-12345678", "api")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		t.Fatal("a .git was made in the folder")
	}
	if w.Shadow == "" || !strings.HasPrefix(w.Shadow, ShadowsDir()) || w.Snapshot == 0 || w.BaseKind() != "snapshot" {
		t.Fatalf("%+v", w)
	}
	if w.TreeID() != "r-12345678/api" || w.Dir != w.Path {
		t.Fatalf("%+v", w)
	}
	// What git would leave out is not copied in.
	for _, f := range []string{"node_modules/x/i.js", "run.log"} {
		if _, err := os.Stat(filepath.Join(w.Path, f)); err == nil {
			t.Fatalf("%s was copied into the worktree", f)
		}
	}
	os.WriteFile(filepath.Join(w.Dir, "src", "a.txt"), []byte("one\ntwo\n"), 0o644)
	status, diff, err := w.Changes(false)
	if err != nil || !strings.Contains(status, "a.txt") || !strings.Contains(diff, "+two") {
		t.Fatalf("%q %q %v", status, diff, err)
	}
	if err := w.Commit("stui: api"); err != nil {
		t.Fatal(err)
	}
	r, err := w.Merge(false)
	if err != nil || !r.Merged || r.Commits != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "src", "a.txt")); string(b) != "one\ntwo\n" {
		t.Fatalf("not merged into the folder: %q", b)
	}
	// The last worktree removed takes the shadow with it.
	if err := w.Remove(false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(w.Shadow)); err == nil {
		t.Fatal("the shadow was left behind")
	}
	if _, err := os.Stat(filepath.Join(dir, "src", "a.txt")); err != nil {
		t.Fatal("removing the shadow took the folder's files")
	}
}

// A second worktree of the same folder shares its shadow; the folder's
// later changes are a new snapshot, and the shadow stays while any
// worktree does.
func TestShadowIsSharedAndKeptWhileUsed(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644)
	a, err := MakeWorktree(dir, "r-12345678", "a")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("changed by hand\n"), 0o644)
	b, err := MakeWorktree(dir, "r-12345678", "b")
	if err != nil {
		t.Fatal(err)
	}
	if a.Shadow != b.Shadow || a.Base == b.Base {
		t.Fatalf("%+v %+v", a, b)
	}
	// A folder inside one with a shadow uses that shadow, from where it is.
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	c, err := MakeWorktree(filepath.Join(dir, "sub"), "r-12345678", "c")
	if err != nil || c.Shadow != a.Shadow || filepath.Base(c.Dir) != "sub" {
		t.Fatalf("%+v %v", c, err)
	}
	if err := c.Remove(true, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(b.Path, "a.txt")); string(got) != "changed by hand\n" {
		t.Fatalf("b did not start from the folder as it is: %q", got)
	}
	if err := a.Remove(true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(b.Shadow); err != nil {
		t.Fatal("the shadow went while b still uses it")
	}
	if err := b.Remove(true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(b.Shadow)); err == nil {
		t.Fatal("the shadow was left behind")
	}
}

// Too much to snapshot is refused before anything is written, with the
// numbers; AnySize takes it anyway.
func TestShadowRefusesAFolderTooBig(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	dir := t.TempDir()
	for i := 0; i < 12; i++ {
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d.txt", i)), []byte("0123456789"), 0o644)
	}
	defer func(f int, b int64) { shadowMaxFiles, shadowMaxBytes = f, b }(shadowMaxFiles, shadowMaxBytes)
	shadowMaxFiles, shadowMaxBytes = 10, 1<<30

	_, err := MakeWorktree(dir, "r-12345678", "x")
	var big *TooBigError
	if !errors.As(err, &big) || big.Files != 12 || big.Bytes != 120 {
		t.Fatalf("%v", err)
	}
	for _, want := range []string{"12 files", "120 B", "--any-size"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("%q does not say %q", err, want)
		}
	}
	if es, _ := os.ReadDir(ShadowsDir()); len(es) != 0 {
		t.Fatalf("a shadow was left: %v", es)
	}
	shadowMaxFiles, shadowMaxBytes = 1<<20, 100
	if _, err := MakeWorktree(dir, "r-12345678", "y"); !errors.As(err, &big) || big.Bytes != 120 {
		t.Fatalf("bytes: %v", err)
	}
	if _, err := MakeWorktreeWith(dir, "r-12345678", "z", TreeOptions{AnySize: true}); err != nil {
		t.Fatal(err)
	}
}
