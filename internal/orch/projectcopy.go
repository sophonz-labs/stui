package orch

// A copy of a worker's task (or a brief sent to it) inside the folder it
// works in, and the line typed into the worker names the copy.
//
// The task itself stays in ~/.stui/orch/<run>/ -- stui task reads it, the
// history and sent/ are there. But that is outside the worker's project, and
// opencode asks before it reads outside its project ("Access external
// directory"), once per opencode process and in memory only: every opencode
// worker would wait on a person for its first task. Inside the project it
// does not ask (measured, opencode 1.19.4).
//
// Where inside:
//   - a checkout whose .git is a directory in it: .git/st/<run>/. git never
//     tracks its own directory, so the user's tree and git status stay as
//     they were.
//   - a git worktree (stui worker start --worktree): its git directory is the
//     main checkout's .git/worktrees/<name>, outside the worktree, and
//     opencode asks there. So .st/<run>/ in the worktree, and "/.st/" in
//     info/exclude -- which a worktree shares with the main checkout (git
//     rev-parse --git-path info/exclude), so it says /.st/ for all of them.
//     Not .gitignore: that is a file of the user's repository.
//   - not a git repository: no copy; the line names ~/.stui's file.
//
// Every agent reads the copy, so the history, the docs and what was typed
// say one thing; Claude Code and Codex read .git/st/ as they read ~/.stui
// (measured: claude -p, codex exec).
//
// Each copy is noted in the run's copies.jsonl. When the run retires
// (Archive) stui deletes the copies it wrote, and when it comes back
// (Unarchive) stui writes them again from the run's own files, so the path a
// worker was given works again. It deletes only a file it can prove it
// wrote: the path is <.git/st or .st>/<run>/<the run's own file>, and the
// file still holds what that file holds. Anything else stays.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// ProjectCopy writes content as rel (a task's "<name>.md", a brief's
// "sent/<file>") under run, inside the project cwd is in, and returns its
// path; "" when there is no such place or it could not be written.
func ProjectCopy(cwd, run, rel string, content []byte) string {
	if cwd == "" || !runID.MatchString(run) || rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, "..") {
		return ""
	}
	top, err := git(cwd, "rev-parse", "--show-toplevel")
	if err != nil || top == "" {
		return ""
	}
	gitDir, err := git(cwd, "rev-parse", "--absolute-git-dir")
	if err != nil || gitDir == "" {
		return ""
	}
	var base string
	if inside(gitDir, top) {
		base = filepath.Join(gitDir, "stui")
	} else {
		base = filepath.Join(top, ".st")
		if !excludeST(cwd) {
			return ""
		}
	}
	p := filepath.Join(base, run, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return ""
	}
	if err := os.WriteFile(p, content, 0o600); err != nil {
		return ""
	}
	noteCopy(run, rel, p)
	return p
}

const copiesFile = "copies.jsonl"

// copyNote is one line of copies.jsonl: the copy, and the run's own file
// it is a copy of (relative to the run's directory).
type copyNote struct {
	Path string `json:"path"`
	Rel  string `json:"rel"`
}

func noteCopy(run, rel, p string) {
	b, err := json.Marshal(copyNote{Path: p, Rel: rel})
	if err != nil || os.MkdirAll(RunDir(run), 0o700) != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(RunDir(run), copiesFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(b, '\n'))
}

// readCopies is the copies noted in a run's directory, each once.
func readCopies(dir string) []copyNote {
	b, _ := os.ReadFile(filepath.Join(dir, copiesFile))
	var out []copyNote
	seen := map[string]bool{}
	for _, l := range bytes.Split(b, []byte("\n")) {
		var c copyNote
		if len(l) == 0 || json.Unmarshal(l, &c) != nil || seen[c.Path] {
			continue
		}
		seen[c.Path] = true
		out = append(out, c)
	}
	return out
}

// copyBase is the .git/st or .st directory a noted copy of run is under,
// when its path has the shape ProjectCopy gives it.
func copyBase(c copyNote, run string) (string, bool) {
	if c.Rel == "" || filepath.IsAbs(c.Rel) || strings.Contains(c.Rel, "..") || !filepath.IsAbs(c.Path) || filepath.Clean(c.Path) != c.Path {
		return "", false
	}
	tail := string(filepath.Separator) + filepath.Join(run, c.Rel)
	if !strings.HasSuffix(c.Path, tail) {
		return "", false
	}
	base := strings.TrimSuffix(c.Path, tail)
	if filepath.Base(base) == ".st" || (filepath.Base(base) == "stui" && filepath.Base(filepath.Dir(base)) == ".git") {
		return base, true
	}
	return "", false
}

// removeCopies deletes the copies noted in dir (run's directory) that are
// still what stui wrote, and the directories they leave empty, up to and
// including .git/st or .st.
func removeCopies(run, dir string) {
	for _, c := range readCopies(dir) {
		base, ok := copyBase(c, run)
		if !ok {
			continue
		}
		fi, err := os.Lstat(c.Path)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		want, err := os.ReadFile(filepath.Join(dir, c.Rel))
		if err != nil {
			continue
		}
		if got, err := os.ReadFile(c.Path); err != nil || !bytes.Equal(got, want) {
			continue
		}
		if os.Remove(c.Path) != nil {
			continue
		}
		for d := filepath.Dir(c.Path); inside(d, base); d = filepath.Dir(d) {
			if os.Remove(d) != nil || d == base {
				break
			}
		}
	}
}

// restoreCopies writes again the copies of run that removeCopies deleted,
// from the run's own files, while the project they were in is still there.
func restoreCopies(run string) {
	dir := RunDir(run)
	for _, c := range readCopies(dir) {
		base, ok := copyBase(c, run)
		if !ok {
			continue
		}
		if _, err := os.Lstat(c.Path); !os.IsNotExist(err) {
			continue
		}
		if fi, err := os.Stat(filepath.Dir(base)); err != nil || !fi.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, c.Rel))
		if err != nil {
			continue
		}
		if filepath.Base(base) == ".st" && !excludeST(filepath.Dir(base)) {
			continue
		}
		if os.MkdirAll(filepath.Dir(c.Path), 0o700) == nil {
			os.WriteFile(c.Path, b, 0o600)
		}
	}
}

// inside is whether path is dir or under it, links resolved.
func inside(path, dir string) bool {
	if p, err := filepath.EvalSymlinks(path); err == nil {
		path = p
	}
	if d, err := filepath.EvalSymlinks(dir); err == nil {
		dir = d
	}
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

const excludeLine = "/.st/"

// excludeST makes sure the repository's info/exclude has /.st/; false when
// it could not be written.
func excludeST(cwd string) bool {
	ex, err := git(cwd, "rev-parse", "--git-path", "info/exclude")
	if err != nil || ex == "" {
		return false
	}
	if !filepath.IsAbs(ex) {
		ex = filepath.Join(cwd, ex)
	}
	b, err := os.ReadFile(ex)
	if err != nil && !os.IsNotExist(err) {
		return false
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == excludeLine {
			return true
		}
	}
	add := "# stui: copies of workers' tasks, inside their worktree (stui worker start)\n" + excludeLine + "\n"
	if len(b) > 0 && !strings.HasSuffix(string(b), "\n") {
		add = "\n" + add
	}
	if err := os.MkdirAll(filepath.Dir(ex), 0o755); err != nil {
		return false
	}
	f, err := os.OpenFile(ex, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return false
	}
	defer f.Close()
	_, err = f.WriteString(add)
	return err == nil
}

// Readable is the file a worker is told to read: the copy, or the one in
// ~/.stui when there is no copy.
func Readable(copy, canonical string) string {
	if copy != "" {
		return copy
	}
	return canonical
}
