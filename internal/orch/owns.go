package orch

import (
	"path/filepath"
	"regexp"
	"strings"
)

// A worker's owned paths: inside its
// folder it may change only what these patterns name; outside the folder it
// is not held back. The patterns live on its pane as @stui-owns (comma
// separated) with the folder as @stui-root, so the hook that checks an edit
// reads them from tmux without asking anyone.
//
// A pattern is a path relative to the folder (or absolute): `*` is any run
// of characters but `/`, `**` any run including `/`, `?` one character. A
// pattern without a wildcard names that file, or everything under it when it
// is a directory ("src/auth" and "src/auth/" both own the whole directory).

// The pane options.
const (
	OptOwns = "@stui-owns"
	OptRoot = "@stui-root"
)

// ParseOwns splits a comma separated list, dropping empty entries.
func ParseOwns(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, filepath.ToSlash(p))
		}
	}
	return out
}

// MayEdit says whether a worker owning patterns under root may change path,
// and when not, a reason to give its agent. No patterns: anything.
func MayEdit(patterns []string, root, path string) (bool, string) {
	if len(patterns) == 0 || path == "" {
		return true, ""
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path = filepath.ToSlash(filepath.Clean(path))
	base := filepath.ToSlash(filepath.Clean(root))
	if root != "" && path != base && !strings.HasPrefix(path, base+"/") {
		return true, ""
	}
	for _, p := range patterns {
		if ownsPath(p, base, path) {
			return true, ""
		}
	}
	return false, "stui: this worker may change only " + strings.Join(patterns, ", ") +
		" in " + base + "; " + strings.TrimPrefix(strings.TrimPrefix(path, base), "/") +
		" is another worker's or the coordinator's. Ask the coordinator in your answer instead of editing it."
}

func ownsPath(pattern, base, path string) bool {
	if !filepath.IsAbs(pattern) && !strings.HasPrefix(pattern, "/") {
		pattern = base + "/" + strings.TrimPrefix(pattern, "./")
	}
	pattern = strings.TrimSuffix(pattern, "/")
	if !strings.ContainsAny(pattern, "*?") {
		return path == pattern || strings.HasPrefix(path, pattern+"/")
	}
	re, err := globRE(pattern)
	return err == nil && re.MatchString(path)
}

func globRE(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				// "**/" also matches no directory at all.
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}
