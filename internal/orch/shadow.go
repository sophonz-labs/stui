package orch

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sophonz-labs/stui/internal/home"
)

// A folder git does not know still gets a worktree. stui keeps a shadow repository for it in its own home,
//
//	~/.stui/shadow/<first 6 bytes of sha256(the folder's real path)>/git
//
// with the folder as its work tree, and commits the folder as it is: that
// snapshot is the worktree's base. From there it is the same git as
// anywhere -- the worktree, its diff, the merge back into the folder and the
// removal are the code in worktree.go. Nothing is written into the folder:
// a .git there would make every tool in it believe it is a repository.
//
// The worktree is a copy of the folder (it has no history to branch from),
// so what git would leave out is left out: the folder's .gitignore, which
// git reads without a repository, and stui's own list in the shadow's
// info/exclude. Before the first snapshot stui counts what it would take, and
// past shadowMaxFiles or shadowMaxBytes it refuses with the numbers unless
// told --any-size. The shadow goes with the last worktree made from it.

// ShadowsDir is where the shadow repositories are.
func ShadowsDir() string { return filepath.Join(home.Dir(), "shadow") }

// What a snapshot takes without --any-size.
var (
	shadowMaxFiles       = 20000
	shadowMaxBytes int64 = 2 << 30
)

// shadowExclude is left out of every snapshot, as a .gitignore would:
// dependencies, build output and caches, which a worktree rebuilds.
var shadowExclude = []string{
	".git/", "/.st/", "node_modules/", "target/", ".build/", "DerivedData/", "__pycache__/", ".venv/",
	"dist/", "build/", ".next/", ".turbo/", "*.o", "*.a", ".DS_Store",
}

// TooBigError is a folder with more in it than a snapshot takes unasked.
type TooBigError struct {
	Folder string
	Files  int
	Bytes  int64
}

func (e *TooBigError) Error() string {
	return fmt.Sprintf("%s is not in a git repository, so stui would snapshot it to make the worktree: that is %s files, %s, more than the %s files or %s it takes unasked. Start in a smaller folder, leave things out with a .gitignore there, or add --any-size",
		e.Folder, count(e.Files), Bytes(e.Bytes), count(shadowMaxFiles), Bytes(shadowMaxBytes))
}

// shadowMu keeps two worktrees of one folder from snapshotting it at once.
var shadowMu sync.Mutex

func shadowOf(folder string) string {
	sum := sha256.Sum256([]byte(folder))
	return filepath.Join(ShadowsDir(), hex.EncodeToString(sum[:6]), "git")
}

// shadowFor is the folder an existing shadow already covers dir with (dir
// or a folder above it), else dir itself.
func shadowFor(dir string) (folder, gitDir string) {
	for d := dir; ; d = filepath.Dir(d) {
		if g := shadowOf(d); exists(filepath.Join(g, "HEAD")) {
			return d, g
		}
		if filepath.Dir(d) == d {
			return dir, shadowOf(dir)
		}
	}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func makeShadowWorktree(dir, run, name string, o TreeOptions) (*Worktree, error) {
	dir = realPath(dir)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("no folder %s", dir)
	}
	shadowMu.Lock()
	defer shadowMu.Unlock()
	folder, gitDir := shadowFor(dir)
	w := &Worktree{Repo: folder, Shadow: gitDir, ID: run + "/" + name}
	fresh := !exists(filepath.Join(gitDir, "HEAD"))
	if fresh {
		if err := os.MkdirAll(filepath.Dir(gitDir), 0o700); err != nil {
			return nil, err
		}
		if _, err := git(filepath.Dir(gitDir), "init", "-q", "--bare", "-b", "main", gitDir); err != nil {
			os.RemoveAll(filepath.Dir(gitDir))
			return nil, err
		}
		// Not bare: it has a work tree, the folder, given on every call.
		w.repo("config", "core.bare", "false")
		os.MkdirAll(filepath.Join(gitDir, "info"), 0o700)
		os.WriteFile(filepath.Join(gitDir, "info", "exclude"), []byte("# stui leaves these out of a snapshot (internal/orch/shadow.go)\n"+strings.Join(shadowExclude, "\n")+"\n"), 0o600)
		os.WriteFile(filepath.Join(filepath.Dir(gitDir), "folder"), []byte(folder+"\n"), 0o600)
	}
	drop := func() {
		if fresh {
			os.RemoveAll(filepath.Dir(gitDir))
		}
	}
	// Counted before anything is taken in.
	if !o.AnySize {
		files, bytes := w.untracked()
		if files > shadowMaxFiles || bytes > shadowMaxBytes {
			drop()
			return nil, &TooBigError{Folder: folder, Files: files, Bytes: bytes}
		}
	}
	if err := w.snapshot(fresh); err != nil {
		drop()
		return nil, fmt.Errorf("could not snapshot %s: %w", folder, err)
	}
	base, err := w.repo("rev-parse", "HEAD")
	if err != nil {
		drop()
		return nil, err
	}
	w.Base = base
	if ct, err := w.repo("log", "-1", "--format=%ct", "HEAD"); err == nil {
		if s, err := strconv.ParseInt(strings.TrimSpace(ct), 10, 64); err == nil {
			w.Snapshot = s * 1000
		}
	}
	sum := sha256.Sum256([]byte(folder))
	w.Path = filepath.Join(WorktreesDir(), filepath.Base(folder)+"-"+hex.EncodeToString(sum[:3]), run+"-"+name)
	w.Branch = "stui/" + run + "/" + name
	w.Dir = w.Path
	if err := os.MkdirAll(filepath.Dir(w.Path), 0o700); err != nil {
		drop()
		return nil, err
	}
	if _, err := w.repo("worktree", "add", "-q", "-b", w.Branch, w.Path, base); err != nil {
		drop()
		return nil, err
	}
	if sub, err := filepath.Rel(folder, dir); err == nil && sub != "." && !strings.HasPrefix(sub, "..") {
		w.Dir = filepath.Join(w.Path, sub)
	}
	copyIncluded(folder, w.Path)
	return w, nil
}

// untracked is how many files, and bytes, a snapshot would take in that
// the shadow does not have yet.
func (w *Worktree) untracked() (files int, bytes int64) {
	out, _ := w.repo("ls-files", "-z", "--others", "--exclude-standard")
	for _, f := range strings.Split(out, "\x00") {
		if f == "" {
			continue
		}
		files++
		if fi, err := os.Lstat(filepath.Join(w.Repo, f)); err == nil && fi.Mode().IsRegular() {
			bytes += fi.Size()
		}
	}
	return files, bytes
}

// snapshot commits the folder as it is now, when it changed since the
// last one (always for a fresh shadow).
func (w *Worktree) snapshot(fresh bool) error {
	if _, err := w.repo("add", "-A"); err != nil {
		return err
	}
	if !fresh {
		if _, err := w.repo("diff", "--cached", "--quiet"); err == nil {
			return nil // unchanged: the last snapshot is still the folder
		}
	}
	_, err := w.repo("-c", "user.name=stui", "-c", "user.email=stui@localhost", "commit", "-q", "--allow-empty", "--no-verify",
		"-m", "stui snapshot "+time.Now().Format(time.RFC3339))
	return err
}

// dropShadowIfUnused removes the shadow once no worktree is made from it.
func (w *Worktree) dropShadowIfUnused() {
	out, err := w.repo("worktree", "list", "--porcelain")
	if err != nil {
		return
	}
	n := 0
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "worktree ") {
			n++
		}
	}
	// The first is the shadow itself.
	if n <= 1 && strings.HasPrefix(w.Shadow, ShadowsDir()+string(filepath.Separator)) {
		os.RemoveAll(filepath.Dir(w.Shadow))
	}
}

// Bytes is n as a person reads it: 120 B, 3.4 KiB, 2.0 GiB.
func Bytes(n int64) string {
	const k = 1024
	if n < k {
		return fmt.Sprintf("%d B", n)
	}
	v, units := float64(n)/k, []string{"KiB", "MiB", "GiB", "TiB"}
	i := 0
	for v >= k && i < len(units)-1 {
		v /= k
		i++
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// count is n with thousands separated: 20,000.
func count(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
