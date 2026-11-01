package orch

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sophonz-labs/stui/internal/home"
	"github.com/sophonz-labs/stui/internal/proc"
)

// A worker's own git worktree: its
// branch st/<run>/<name> from the commit the coordinator's checkout is on,
// in a folder outside the repository, so workers never see each other's
// files, builds or tests. The coordinator reads the result with stui worker
// diff, takes it with stui worker merge, and removes it with stui worker clean.
//
// A repository's .worktreeinclude (one pattern a line, as Orca reads it)
// names files git does not keep that a worktree needs too, such as
// .env.local; they are copied in.

// Worktree is what was made for a worker.
type Worktree struct {
	Repo   string `json:"repo"`   // the main checkout's top
	Path   string `json:"path"`   // the worktree's top
	Branch string `json:"branch"` // st/<run>/<name>
	Base   string `json:"base"`   // the commit it started from
	// The folder the worker works in: Path, plus where in the repository
	// the coordinator was.
	Dir string `json:"dir"`
	// Its own name, <run>/<worker>: a worktree is opened by it after its
	// worker is gone (stui worker diff/ask/merge/clean, /api/orch/trees).
	// Records from before have none; TreeID reads it from the branch.
	ID string `json:"id,omitempty"`
	// For a folder git does not know (shadow.go): the repository stui keeps
	// for it in its own home, and when it took the snapshot Base is (ms).
	// Repo is then the folder itself, which has no .git.
	Shadow   string `json:"shadow,omitempty"`
	Snapshot int64  `json:"snapshot,omitempty"`
}

// TreeID is the worktree's own name, <run>/<worker>.
func (w *Worktree) TreeID() string {
	if w.ID != "" {
		return w.ID
	}
	return strings.TrimPrefix(w.Branch, "stui/")
}

// BaseKind is what Base is: "commit", one the person knows, or "snapshot",
// the folder as stui copied it at Snapshot (a folder git does not know).
func (w *Worktree) BaseKind() string {
	if w.Shadow != "" {
		return "snapshot"
	}
	return "commit"
}

// repo runs git on the main checkout: the folder with its shadow
// repository, or the checkout itself.
func (w *Worktree) repo(args ...string) (string, error) {
	if w.Shadow != "" {
		return gitWith([]string{"--git-dir=" + w.Shadow, "--work-tree=" + w.Repo}, w.Repo, args...)
	}
	return git(w.Repo, args...)
}

// ErrNotGit is a folder that is not in a git repository. MakeWorktree no
// longer returns it: such a folder gets a shadow repository (shadow.go).
var ErrNotGit = errors.New("not in a git repository")

func git(dir string, args ...string) (string, error) {
	return gitWith([]string{"-C", dir}, dir, args...)
}

// gitWith runs git with pre before args; dir is where its identity is
// looked for.
func gitWith(pre []string, dir string, args ...string) (string, error) {
	// A commit stui makes (a worker's leftovers, a merge) needs a name; a
	// machine that never set one (a CI runner, a fresh account) gets stui's.
	if args[0] == "commit" || args[0] == "merge" {
		if !hasIdentity(dir) {
			pre = append(pre, "-c", "user.name=stui", "-c", "user.email=stui@localhost")
		}
	}
	cmd := exec.Command("git", append(pre, args...)...)
	proc.Hide(cmd)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return strings.TrimSpace(string(out)), fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(string(ee.Stderr)))
		}
		return strings.TrimSpace(string(out)), err
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// WorktreesDir is where workers' worktrees go.
func WorktreesDir() string { return filepath.Join(home.Dir(), "worktrees") }

// TreeOptions are how a worktree is made.
type TreeOptions struct {
	// AnySize snapshots a folder git does not know however big it is
	// (stui worker start --any-size); see shadowMaxFiles.
	AnySize bool
}

// MakeWorktree makes worker name's worktree of the repository dir is in,
// or of the folder through a shadow repository when git does not know it.
func MakeWorktree(dir, run, name string) (*Worktree, error) {
	return MakeWorktreeWith(dir, run, name, TreeOptions{})
}

// MakeWorktreeWith is MakeWorktree with o.
func MakeWorktreeWith(dir, run, name string, o TreeOptions) (*Worktree, error) {
	top, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil || top == "" {
		return makeShadowWorktree(dir, run, name, o)
	}
	base, err := git(top, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("the repository has no commit to start from: %w", err)
	}
	sum := sha256.Sum256([]byte(top))
	path := filepath.Join(WorktreesDir(), filepath.Base(top)+"-"+hex.EncodeToString(sum[:3]), run+"-"+name)
	branch := "stui/" + run + "/" + name
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if _, err := git(top, "worktree", "add", "-b", branch, path, base); err != nil {
		return nil, err
	}
	sub, _ := filepath.Rel(realPath(top), realPath(dir))
	w := &Worktree{Repo: top, Path: path, Branch: branch, Base: base, Dir: path, ID: run + "/" + name}
	if sub != "." && sub != "" && !strings.HasPrefix(sub, "..") {
		w.Dir = filepath.Join(path, sub)
	}
	copyIncluded(top, path)
	return w, nil
}

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// copyIncluded copies the files .worktreeinclude names, when they exist and
// git does not keep them.
func copyIncluded(top, path string) {
	f, err := os.Open(filepath.Join(top, ".worktreeinclude"))
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		pat := strings.TrimSpace(sc.Text())
		if pat == "" || strings.HasPrefix(pat, "#") || strings.Contains(pat, "..") {
			continue
		}
		matches, _ := filepath.Glob(filepath.Join(top, pat))
		for _, m := range matches {
			rel, err := filepath.Rel(top, m)
			if err != nil || strings.HasPrefix(rel, "..") {
				continue
			}
			if fi, err := os.Stat(m); err != nil || fi.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(path, rel)); err == nil {
				continue // git keeps it: already there
			}
			copyFile(m, filepath.Join(path, rel))
		}
	}
}

func copyFile(from, to string) {
	in, err := os.Open(from)
	if err != nil {
		return
	}
	defer in.Close()
	fi, _ := in.Stat()
	os.MkdirAll(filepath.Dir(to), 0o755)
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fi.Mode().Perm())
	if err != nil {
		return
	}
	defer out.Close()
	io.Copy(out, in)
}

// Changes is what the worker changed: files not committed (git status
// --short), and the diff from where it started, committed or not.
func (w *Worktree) Changes(stat bool) (status, diff string, err error) {
	status, err = git(w.Path, "status", "--short")
	if err != nil {
		return "", "", err
	}
	args := []string{"diff", w.Base}
	if stat {
		args = append(args, "--stat")
	}
	diff, err = git(w.Path, args...)
	return status, diff, err
}

// Dirty says whether the worktree has changes not committed.
func (w *Worktree) Dirty() (bool, error) {
	s, err := git(w.Path, "status", "--porcelain")
	return s != "", err
}

// Commit commits everything in the worktree with message.
func (w *Worktree) Commit(message string) error {
	if _, err := git(w.Path, "add", "-A"); err != nil {
		return err
	}
	_, err := git(w.Path, "-c", "user.name=stui worker", "-c", "user.email=stui@localhost", "commit", "-q", "-m", message)
	return err
}

// MergeResult is how a merge went.
type MergeResult struct {
	Merged    bool     `json:"merged"`
	Conflicts []string `json:"conflicts,omitempty"`
	Into      string   `json:"into"` // the main checkout's branch
	Commits   int      `json:"commits"`
}

// Merge brings the worker's branch into the main checkout's current branch.
// On a conflict nothing is left half done: the merge is aborted and the files
// are named. squash makes one change to commit by hand instead.
func (w *Worktree) Merge(squash bool) (*MergeResult, error) {
	into, _ := w.repo("rev-parse", "--abbrev-ref", "HEAD")
	if w.Shadow != "" {
		// Its shadow's branch means nothing to the person: the folder.
		into = w.Repo
	}
	r := &MergeResult{Into: into}
	n, _ := w.repo("rev-list", "--count", "HEAD.."+w.Branch)
	fmt.Sscan(n, &r.Commits)
	if r.Commits == 0 {
		return r, fmt.Errorf("%s has no commits that %s does not have", w.Branch, into)
	}
	args := []string{"merge", "--no-ff", "--no-edit", w.Branch}
	if squash {
		args = []string{"merge", "--squash", w.Branch}
	}
	if _, err := w.repo(args...); err != nil {
		u, _ := w.repo("diff", "--name-only", "--diff-filter=U")
		if u != "" {
			r.Conflicts = strings.Split(u, "\n")
			if squash {
				w.repo("reset", "--merge")
			} else {
				w.repo("merge", "--abort")
			}
			return r, nil
		}
		return r, err
	}
	r.Merged = true
	return r, nil
}

// Remove removes the worktree and, unless keepBranch, its branch. force
// removes it with changes not committed.
func (w *Worktree) Remove(force, keepBranch bool) error {
	args := []string{"worktree", "remove", w.Path}
	if force {
		args = []string{"worktree", "remove", "--force", w.Path}
	}
	if _, err := w.repo(args...); err != nil {
		return err
	}
	if !keepBranch {
		flag := "-d"
		if force {
			flag = "-D"
		}
		if _, err := w.repo("branch", flag, w.Branch); err != nil {
			return err
		}
	}
	// A kept branch lives in the shadow, so the shadow stays with it.
	if w.Shadow != "" && !keepBranch {
		w.dropShadowIfUnused()
	}
	return nil
}

// hasIdentity is whether git has a committer email for dir (its config,
// the user's, or the environment).
func hasIdentity(dir string) bool {
	if os.Getenv("GIT_COMMITTER_EMAIL") != "" || os.Getenv("EMAIL") != "" {
		return true
	}
	cmd := exec.Command("git", "-C", dir, "config", "user.email")
	proc.Hide(cmd)
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}
