package orch

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

func status(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "status", "--short", "--untracked-files=all").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// A checkout: the copy is under .git/st, and the tree stays clean.
func TestProjectCopyCheckout(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	repo := gitRepo(t)
	sub := filepath.Join(repo, "web")
	os.Mkdir(sub, 0o755)
	p := ProjectCopy(sub, "r-1a2b3c4d", "w1.md", []byte("task"))
	if want := filepath.Join(repo, ".git", "stui", "r-1a2b3c4d", "w1.md"); p != want {
		t.Fatalf("%q, want %q", p, want)
	}
	if b, _ := os.ReadFile(p); string(b) != "task" {
		t.Errorf("%q", b)
	}
	s := ProjectCopy(repo, "r-1a2b3c4d", "sent/20261025-000000-w1.md", []byte("brief"))
	if !strings.HasSuffix(s, filepath.Join(".git", "stui", "r-1a2b3c4d", "sent", "20261025-000000-w1.md")) {
		t.Errorf("%q", s)
	}
	if st := status(t, repo); st != "" {
		t.Errorf("git status: %q", st)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "info", "exclude")); err == nil {
		if b, _ := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude")); strings.Contains(string(b), excludeLine) {
			t.Error("a checkout needs no exclude")
		}
	}
}

// A worktree's git directory is outside it: the copy goes in .st there,
// excluded once in the info/exclude the worktree shares.
func TestProjectCopyWorktree(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	repo := gitRepo(t)
	wt := filepath.Join(filepath.Dir(repo), "wt-"+filepath.Base(repo))
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-q", wt, "-b", "w").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	t.Cleanup(func() { os.RemoveAll(wt) })
	for i := 0; i < 2; i++ {
		p := ProjectCopy(wt, "r-1a2b3c4d", "w1.md", []byte("task"))
		if want := filepath.Join(wt, ".st", "r-1a2b3c4d", "w1.md"); p != want {
			t.Fatalf("%q, want %q", p, want)
		}
	}
	if st := status(t, wt); st != "" {
		t.Errorf("worktree status: %q", st)
	}
	b, _ := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if n := strings.Count(string(b), "\n"+excludeLine+"\n"); n != 1 {
		t.Errorf("exclude has /.st/ %d times:\n%s", n, b)
	}
	if st := status(t, repo); st != "" {
		t.Errorf("main status: %q", st)
	}
}

func TestProjectCopyNone(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	plain, _ := filepath.EvalSymlinks(t.TempDir())
	if p := ProjectCopy(plain, "r-1a2b3c4d", "w1.md", []byte("x")); p != "" {
		t.Errorf("not a repository: %q", p)
	}
	repo := gitRepo(t)
	for _, c := range []struct{ run, rel string }{{"nope", "w.md"}, {"r-1a2b3c4d", "../x.md"}, {"r-1a2b3c4d", "/etc/x"}, {"r-1a2b3c4d", ""}} {
		if p := ProjectCopy(repo, c.run, c.rel, []byte("x")); p != "" {
			t.Errorf("%v: %q", c, p)
		}
	}
	if Readable("", "/canon") != "/canon" || Readable("/c", "/canon") != "/c" {
		t.Error("Readable")
	}
}

// A run that retires takes its copies out of its workers' projects, and
// only those: a copy someone changed, a file of someone else's and a
// forged note stay. When it comes back, the copies are written again.
func TestArchiveRemovesCopies(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	repo := gitRepo(t)
	r := &Run{ID: NewID(), Coordinator: "%9", CoordinatorKey: "k-c", Created: Now()}
	r.Save()
	task, err := WriteTask(r.ID, "w1", "do it")
	if err != nil {
		t.Fatal(err)
	}
	tb, _ := os.ReadFile(task)
	cTask := ProjectCopy(repo, r.ID, filepath.Base(task), tb)
	sent, _ := WriteSent(r.ID, "w1", []byte("brief"), time.Unix(100, 0))
	cSent := ProjectCopy(repo, r.ID, "sent/"+filepath.Base(sent), []byte("brief"))
	sent2, _ := WriteSent(r.ID, "w1", []byte("second"), time.Unix(200, 0))
	cChanged := ProjectCopy(repo, r.ID, "sent/"+filepath.Base(sent2), []byte("second"))
	if cTask == "" || cSent == "" || cChanged == "" {
		t.Fatal("no copies")
	}
	os.WriteFile(cChanged, []byte("second, with my notes"), 0o600)
	theirs := filepath.Join(repo, ".git", "stui", "notes.md")
	os.WriteFile(theirs, []byte("mine"), 0o600)
	victim := filepath.Join(repo, "keep.md")
	os.WriteFile(victim, []byte("brief"), 0o600)
	f, _ := os.OpenFile(filepath.Join(RunDir(r.ID), copiesFile), os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"path":"` + victim + `","rel":"sent/` + filepath.Base(sent) + `"}` + "\n")
	f.Close()

	if err := Archive(r.ID, "test"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{cTask, cSent} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s is still there: %v", p, err)
		}
	}
	for _, p := range []string{cChanged, theirs, victim} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s went: %v", p, err)
		}
	}

	if err := Unarchive(r.ID); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(cTask); !bytes.Equal(b, tb) {
		t.Errorf("task copy: %q", b)
	}
	if b, _ := os.ReadFile(cSent); string(b) != "brief" {
		t.Errorf("sent copy: %q", b)
	}
	if b, _ := os.ReadFile(cChanged); string(b) != "second, with my notes" {
		t.Errorf("changed copy: %q", b)
	}
	if st := status(t, repo); st != "?? keep.md" {
		t.Errorf("git status: %q", st)
	}
}

// Nothing left: .git/st itself goes, and a worktree's .st with it.
func TestArchiveRemovesEmptyCopyDirs(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	repo := gitRepo(t)
	wt := filepath.Join(filepath.Dir(repo), "wt-"+filepath.Base(repo))
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-q", wt, "-b", "w").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	t.Cleanup(func() { os.RemoveAll(wt) })
	r := &Run{ID: NewID(), Coordinator: "%9", CoordinatorKey: "k-c", Created: Now()}
	r.Save()
	task, _ := WriteTask(r.ID, "w1", "do it")
	b, _ := os.ReadFile(task)
	ProjectCopy(repo, r.ID, filepath.Base(task), b)
	ProjectCopy(wt, r.ID, filepath.Base(task), b)
	if err := Archive(r.ID, "test"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(repo, ".git", "stui"), filepath.Join(wt, ".st")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s is still there: %v", p, err)
		}
	}
}
