package mux

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// RepoOf is the GitHub repository ("owner/name") that folder dir is in, from
// the origin remote of the git work tree around it; "" when it is in none or
// origin is not on GitHub (GET /api/windows). It reads the
// files git keeps and runs no git, and remembers each folder for a while:
// the window list asks every few seconds.
func RepoOf(dir string) string {
	return originOf(dir).repo
}

// ForgeOf is the GitHub or GitLab project folder dir is in, from the same
// origin remote as RepoOf; nil when it is in none or origin is on neither.
// What it returns is remembered as seen (ForgeSeen), so GET /api/avatar
// asks only forges stui has shown in a window.
func ForgeOf(dir string) *Forge {
	f := originOf(dir).forge
	if f != nil {
		noteForge(f)
		c := *f
		return &c
	}
	return nil
}

func originOf(dir string) repoEntry {
	if dir == "" || !filepath.IsAbs(dir) {
		return repoEntry{}
	}
	repoCache.Lock()
	e, ok := repoCache.m[dir]
	repoCache.Unlock()
	if ok && time.Since(e.at) < repoTTL {
		return e
	}
	e = repoEntry{at: time.Now()}
	if config := gitConfig(dir); config != "" {
		url := originURL(config)
		e.repo, e.forge = GitHubRepo(url), ParseForge(url)
	}
	repoCache.Lock()
	if len(repoCache.m) > 512 {
		repoCache.m = map[string]repoEntry{}
	}
	repoCache.m[dir] = e
	repoCache.Unlock()
	return e
}

const repoTTL = 30 * time.Second

type repoEntry struct {
	repo  string
	forge *Forge
	at    time.Time
}

var repoCache = struct {
	sync.Mutex
	m map[string]repoEntry
}{m: map[string]repoEntry{}}

// gitConfig is the config file of the work tree dir is in: .git/config, or
// for a linked worktree (.git is a file) its main repository's.
func gitConfig(dir string) string {
	for d := dir; ; {
		dot := filepath.Join(d, ".git")
		if fi, err := os.Stat(dot); err == nil {
			if fi.IsDir() {
				return filepath.Join(dot, "config")
			}
			return linkedConfig(d, dot)
		}
		up := filepath.Dir(d)
		if up == d {
			return ""
		}
		d = up
	}
}

func linkedConfig(dir, dot string) string {
	b, err := os.ReadFile(dot)
	if err != nil {
		return ""
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
	if !ok {
		return ""
	}
	gitdir = strings.TrimSpace(gitdir)
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(dir, gitdir)
	}
	common := gitdir
	if c, err := os.ReadFile(filepath.Join(gitdir, "commondir")); err == nil {
		common = strings.TrimSpace(string(c))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitdir, common)
		}
	}
	return filepath.Join(common, "config")
}

// originURL is remote "origin"'s url in a git config file.
func originURL(config string) string {
	f, err := os.Open(config)
	if err != nil {
		return ""
	}
	defer f.Close()
	in := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(l, "[") {
			in = strings.ReplaceAll(l, " ", "") == `[remote"origin"]`
			continue
		}
		if !in {
			continue
		}
		if k, v, ok := strings.Cut(l, "="); ok && strings.TrimSpace(k) == "url" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

var githubURL = regexp.MustCompile(`^(?:https?://(?:[^@/]+@)?github\.com/|ssh://(?:[^@/]+@)?github\.com(?::\d+)?/|(?:[^@/]+@)?github\.com:)([A-Za-z0-9-]+)/([A-Za-z0-9._-]+?)(?:\.git)?/?$`)

// GitHubRepo is "owner/name" for a GitHub remote URL, "" for any other.
func GitHubRepo(url string) string {
	m := githubURL.FindStringSubmatch(url)
	if m == nil {
		return ""
	}
	return m[1] + "/" + m[2]
}
