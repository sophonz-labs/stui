package mux

import (
	"regexp"
	"strings"
	"sync"
	"time"
)

// Forge is the GitHub or GitLab project a window's folder is in, from its
// git origin (GET /api/windows). Path is "<owner>/<name>" on
// GitHub and "<group>/.../<name>" on GitLab, subgroups included.
type Forge struct {
	// "github" or "gitlab".
	Kind string `json:"kind"`
	// "github.com", "gitlab.com" or a self-hosted GitLab's host, with its
	// port when an https origin names one.
	Host string `json:"host"`
	Path string `json:"path"`
}

// Namespace is the owner or group the project is under: Path without its
// last segment.
func (f Forge) Namespace() string {
	i := strings.LastIndexByte(f.Path, '/')
	if i < 0 {
		return ""
	}
	return f.Path[:i]
}

// Key is how GET /api/avatar names the owner's picture:
// "<kind>:<host>/<namespace>", lower case.
func (f Forge) Key() string {
	return strings.ToLower(f.Kind + ":" + f.Host + "/" + f.Namespace())
}

var (
	forgeHTTP = regexp.MustCompile(`^https?://(?:[^@/]+@)?([A-Za-z0-9.-]+(?::\d{1,5})?)/(.+)$`)
	forgeSSH  = regexp.MustCompile(`^ssh://(?:[^@/]+@)?([A-Za-z0-9.-]+)(?::\d{1,5})?/(.+)$`)
	forgeSCP  = regexp.MustCompile(`^(?:[^@/:]+@)?([A-Za-z0-9.-]+):([^/].*)$`)
	forgeSeg  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)
	ghOwner   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`)
)

// ParseForge is the forge project of a remote URL (https, ssh:// or
// scp-style "git@host:path"), nil when it is not on GitHub or GitLab or
// does not look like a project there. GitLab is gitlab.com or any host whose
// name has "gitlab" in it.
func ParseForge(url string) *Forge {
	var host, path string
	for _, re := range []*regexp.Regexp{forgeHTTP, forgeSSH, forgeSCP} {
		if m := re.FindStringSubmatch(url); m != nil {
			host, path = strings.ToLower(m[1]), m[2]
			break
		}
	}
	if host == "" {
		return nil
	}
	path = strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git")
	segs := strings.Split(path, "/")
	for _, s := range segs {
		if !forgeSeg.MatchString(s) || strings.Contains(s, "..") {
			return nil
		}
	}
	name := host
	if i := strings.IndexByte(name, ':'); i >= 0 {
		name = name[:i]
	}
	if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") || strings.Contains(name, "..") {
		return nil
	}
	switch {
	case name == "github.com":
		if len(segs) != 2 || !ghOwner.MatchString(segs[0]) || len(segs[0]) > 39 {
			return nil
		}
		return &Forge{Kind: "github", Host: name, Path: path}
	case strings.Contains(name, "gitlab"):
		if len(segs) < 2 || len(segs) > 20 {
			return nil
		}
		return &Forge{Kind: "gitlab", Host: host, Path: path}
	}
	return nil
}

// How long a forge stays askable after a window last showed it.
const forgeSeenTTL = 24 * time.Hour

var forgesSeen = struct {
	sync.Mutex
	m map[string]time.Time
}{m: map[string]time.Time{}}

func noteForge(f *Forge) {
	k := f.Key()
	forgesSeen.Lock()
	defer forgesSeen.Unlock()
	if len(forgesSeen.m) > 1024 {
		now := time.Now()
		for k, at := range forgesSeen.m {
			if now.Sub(at) > forgeSeenTTL {
				delete(forgesSeen.m, k)
			}
		}
		if len(forgesSeen.m) > 1024 {
			forgesSeen.m = map[string]time.Time{}
		}
	}
	forgesSeen.m[k] = time.Now()
}

// ForgeSeen reports whether a window stui listed in the last day was in a
// project under key (Forge.Key): the only owners GET /api/avatar fetches.
func ForgeSeen(key string) bool {
	forgesSeen.Lock()
	defer forgesSeen.Unlock()
	at, ok := forgesSeen.m[strings.ToLower(key)]
	return ok && time.Since(at) < forgeSeenTTL
}
