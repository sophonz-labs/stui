// Package cmdicon picks the icon for the program running in a pane, from
// the catalogue in registry.json and paths.json.
package cmdicon

import (
	_ "embed"
	"encoding/json"
	"regexp"
	"strings"
)

//go:embed registry.json
var registryJSON []byte

//go:embed paths.json
var pathsJSON []byte

type entry struct {
	Icon     string   `json:"icon"`
	Label    string   `json:"label"`
	Match    []string `json:"match"`
	Category string   `json:"category"`
}

type registry struct {
	IconsVersion int `json:"iconsVersion"`
	Icons        map[string]struct {
		Title string `json:"title"`
	} `json:"icons"`
	Commands []entry `json:"commands"`
}

var (
	reg    registry
	byName = map[string]*entry{}
	paths  map[string]string
)

func init() {
	if err := json.Unmarshal(registryJSON, &reg); err != nil {
		panic("cmdicon: " + err.Error())
	}
	if err := json.Unmarshal(pathsJSON, &paths); err != nil {
		panic("cmdicon: " + err.Error())
	}
	for i := range reg.Commands {
		for _, m := range reg.Commands[i].Match {
			byName[m] = &reg.Commands[i]
		}
	}
}

// Version is the registry's iconsVersion.
func Version() int { return reg.IconsVersion }

// Fallback is the icon of a program the registry does not know.
const Fallback = "generic"

// Match is what a window is running, as far as the APIs say: an icon slug
// and a short command name, both safe to send anywhere.
type Match struct {
	Icon    string `json:"icon,omitempty"`
	Command string `json:"command,omitempty"`
}

var (
	versionOnly   = regexp.MustCompile(`^\d+(\.\d+)+$`)
	versionSuffix = regexp.MustCompile(`^(.*[a-z+])[-_v]?\d+(\.\d+)*$`)
	unsafe        = regexp.MustCompile(`[^a-z0-9._+-]`)
)

// Normalize is the process name as matching sees it: the last path
// segment, lower case, no login-shell dash, no .exe.
func Normalize(command string) string {
	c := strings.TrimSpace(command)
	if i := strings.LastIndexAny(c, `/\`); i >= 0 {
		c = c[i+1:]
	}
	c = strings.ToLower(c)
	c = strings.TrimPrefix(c, "-")
	c = strings.TrimSuffix(c, ".exe")
	return c
}

// lookup finds name as it is, then without a version suffix.
func lookup(name string) *entry {
	if e := byName[name]; e != nil {
		return e
	}
	if m := versionSuffix.FindStringSubmatch(name); m != nil {
		return byName[m[1]]
	}
	return nil
}

// wrappers run something else, named in their arguments.
var wrappers = map[string]bool{
	"npx": true, "pnpx": true, "bunx": true, "uvx": true, "pipx": true,
	"npm": true, "pnpm": true, "yarn": true, "uv": true, "bun": true, "deno": true,
	"node": true, "nodejs": true, "python": true, "ruby": true, "perl": true, "php": true,
	"sudo": true, "doas": true, "env": true, "nohup": true, "time": true, "caffeinate": true,
}

// IsWrapper says whether argv could change the answer for command, so a
// caller reads argv only then.
func IsWrapper(command string) bool {
	n := Normalize(command)
	if wrappers[n] {
		return true
	}
	if m := versionSuffix.FindStringSubmatch(n); m != nil {
		return wrappers[m[1]]
	}
	return false
}

var subcommands = map[string]bool{"exec": true, "x": true, "dlx": true, "run": true, "tool": true, "--": true}

var scriptExt = []string{".js", ".mjs", ".cjs", ".ts", ".py", ".rb", ".pl", ".php"}

// target is the registry name a wrapper's argv runs, or "".
func target(argv []string) string {
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		if a == "-m" && i+1 < len(argv) { // python -m module
			return candidate(argv[i+1])
		}
		if a == "-e" || a == "-c" || a == "-p" || a == "--eval" || a == "--print" {
			return "" // inline code: nothing to name
		}
		if strings.HasPrefix(a, "-") || subcommands[a] || strings.Contains(a, "=") && !strings.Contains(a, "/") {
			continue
		}
		return candidate(a)
	}
	return ""
}

func candidate(a string) string {
	a = strings.ReplaceAll(a, `\`, "/")
	if i := strings.LastIndex(a, "node_modules/"); i >= 0 {
		pkg := a[i+len("node_modules/"):]
		parts := strings.Split(pkg, "/")
		if strings.HasPrefix(parts[0], "@") && len(parts) > 1 {
			a = parts[1]
		} else {
			a = parts[0]
		}
	} else {
		if strings.HasPrefix(a, "@") { // @scope/name[@version]
			if _, rest, ok := strings.Cut(a, "/"); ok {
				a = rest
			}
		}
		if i := strings.LastIndex(a, "/"); i >= 0 {
			a = a[i+1:]
		}
	}
	if i := strings.Index(a, "@"); i > 0 {
		a = a[:i]
	}
	a = strings.ToLower(a)
	for _, ext := range scriptExt {
		if strings.HasSuffix(a, ext) {
			a = strings.TrimSuffix(a, ext)
			break
		}
	}
	if e := lookup(a); e != nil && !wrappers[a] {
		return a
	}
	return ""
}

// Resolve picks the icon for a pane: agent is agent watch's kind for it
// ("" when not an agent pane), command its pane_current_command, and argv,
// if not nil, reads the foreground process's arguments -- called only for a
// wrapper. argv is never kept or returned.
func Resolve(agent, command string, argv func() []string) Match {
	if agent != "" {
		if e := byName[agent]; e != nil {
			return Match{Icon: e.Icon, Command: agent}
		}
	}
	n := Normalize(command)
	if n == "" {
		return Match{}
	}
	if versionOnly.MatchString(n) {
		return Match{Icon: byName["claude"].Icon, Command: "claude"}
	}
	e := lookup(n)
	if argv != nil && IsWrapper(n) {
		if t := target(argv()); t != "" {
			return Match{Icon: lookup(t).Icon, Command: t}
		}
	}
	short := unsafe.ReplaceAllString(n, "")
	if len(short) > 40 {
		short = short[:40]
	}
	if e == nil {
		return Match{Icon: Fallback, Command: short}
	}
	return Match{Icon: e.Icon, Command: short}
}

var slugRE = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)

// SVG is the icon's document, or false for a slug there is none for.
func SVG(slug string) ([]byte, bool) {
	if !slugRE.MatchString(slug) {
		return nil, false
	}
	d, ok := paths[slug]
	if !ok {
		return nil, false
	}
	title := slug
	if i, ok := reg.Icons[slug]; ok {
		title = i.Title
	}
	title = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(title)
	return []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor"><title>` +
		title + `</title><path d="` + d + `"/></svg>` + "\n"), true
}
