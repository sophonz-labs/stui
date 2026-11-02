package plugin

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Plugin is one manifest as found on disk.
type Plugin struct {
	M        *Manifest // nil when it did not parse
	Name     string    // M.Name, or the directory's name
	Path     string    // the plugin.json
	Shared   bool      // from the shared directory, not the tool's own
	Err      error     // why it did not load
	Problems []string  // what loaded but is left out: conflicts, missing programs
}

// Dirs is where a tool's plugins live, first match wins:
// $XDG_CONFIG_HOME/<tool>/plugins (default ~/.config), then the shared
// $XDG_CONFIG_HOME/tui-plugins that every TUI using this spec reads.
func Dirs(tool string) []string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return []string{filepath.Join(base, tool, "plugins"), filepath.Join(base, "tui-plugins")}
}

// Load reads <dir>/<name>/plugin.json in each dir. A name already found
// in an earlier dir shadows the later one, which is listed with a problem.
func Load(dirs []string) []*Plugin {
	var out []*Plugin
	byName := map[string]*Plugin{}
	for i, d := range dirs {
		ents, _ := os.ReadDir(d)
		for _, e := range ents {
			path := filepath.Join(d, e.Name(), "plugin.json")
			data, err := os.ReadFile(path)
			if err != nil {
				continue // not a plugin directory
			}
			p := &Plugin{Name: e.Name(), Path: path, Shared: i > 0}
			if p.M, p.Err = Parse(data); p.M != nil {
				p.Name = p.M.Name
				if p.M.Name != e.Name() {
					p.Problems = append(p.Problems, fmt.Sprintf("its name is %q but its directory is %q", p.M.Name, e.Name()))
				}
				for _, r := range p.M.Requires {
					if _, err := exec.LookPath(r); err != nil {
						p.Problems = append(p.Problems, r+" is not on PATH")
					}
				}
			}
			if first := byName[p.Name]; first != nil {
				p.Problems = append(p.Problems, "shadowed by "+first.Path)
				p.M = nil
			} else {
				byName[p.Name] = p
			}
			out = append(out, p)
		}
	}
	return out
}

// Builtins is what a tool has already: its : commands, its views'
// names (as action scopes), and the keys it uses, by scope ("" is keys
// every view has).
type Builtins struct {
	Commands []string
	Scopes   []string
	Keys     map[string][]string
}

// ViewRef, ActionRef and CommandRef are a plugin's parts that made it in.
type ViewRef struct {
	P *Plugin
	V *View
}
type ActionRef struct {
	P *Plugin
	A *Action
}
type CommandRef struct {
	P *Plugin
	C *Command
}

// Set is the plugins that loaded, with every name and key resolved.
type Set struct {
	Views    map[string]*ViewRef
	Commands map[string]*CommandRef
	Actions  map[string]map[string]*ActionRef // scope, key
	Order    []string                         // view and command names, as found
}

// Resolve puts the plugins together. Built-ins always win: a plugin's
// view, command or key that takes one is left out, as is the second
// plugin to claim a name or a key, and each is a problem on its plugin.
func Resolve(ps []*Plugin, b Builtins) *Set {
	s := &Set{Views: map[string]*ViewRef{}, Commands: map[string]*CommandRef{}, Actions: map[string]map[string]*ActionRef{}}
	builtinCmd := map[string]bool{}
	for _, c := range b.Commands {
		builtinCmd[c] = true
	}
	owner := map[string]string{} // view or command name: plugin
	claim := func(p *Plugin, kind, name string) bool {
		switch {
		case builtinCmd[name]:
			p.Problems = append(p.Problems, fmt.Sprintf("%s :%s is built in, left out", kind, name))
		case owner[name] != "":
			p.Problems = append(p.Problems, fmt.Sprintf("%s :%s is taken by plugin %s, left out", kind, name, owner[name]))
		default:
			owner[name] = p.Name
			s.Order = append(s.Order, name)
			return true
		}
		return false
	}
	for _, p := range ps {
		if p.M == nil {
			continue
		}
		for i := range p.M.Views {
			if v := &p.M.Views[i]; claim(p, "view", v.Name) {
				s.Views[v.Name] = &ViewRef{p, v}
			}
		}
		for i := range p.M.Commands {
			if c := &p.M.Commands[i]; claim(p, "command", c.Name) {
				s.Commands[c.Name] = &CommandRef{p, c}
			}
		}
	}
	scopes := append([]string(nil), b.Scopes...)
	for n := range s.Views {
		scopes = append(scopes, n)
	}
	sort.Strings(scopes)
	for _, p := range ps {
		if p.M == nil {
			continue
		}
		for i := range p.M.Actions {
			a := &p.M.Actions[i]
			if a.Open != "" && s.Views[a.Open] == nil {
				p.Problems = append(p.Problems, fmt.Sprintf("key %s opens :%s, which no plugin has", a.Key, a.Open))
				continue
			}
			want := a.Scopes
			if contains(want, "all") {
				want = scopes
			}
			for _, sc := range want {
				if !contains(scopes, sc) {
					if !contains(TmuxScopes, sc) { // a TUI without a windows view just has no such key
						p.Problems = append(p.Problems, fmt.Sprintf("key %s: no view %q", a.Key, sc))
					}
					continue
				}
				if contains(b.Keys[""], a.Key) || contains(b.Keys[sc], a.Key) {
					p.Problems = append(p.Problems, fmt.Sprintf("key %s on %s is built in, left out", a.Key, sc))
					continue
				}
				if s.Actions[sc] == nil {
					s.Actions[sc] = map[string]*ActionRef{}
				}
				if o := s.Actions[sc][a.Key]; o != nil {
					p.Problems = append(p.Problems, fmt.Sprintf("key %s on %s is taken by plugin %s, left out", a.Key, sc, o.P.Name))
					continue
				}
				s.Actions[sc][a.Key] = &ActionRef{p, a}
			}
		}
	}
	return s
}

// Keys is a scope's plugin actions, sorted by key.
func (s *Set) Keys(scope string) []*ActionRef {
	var out []*ActionRef
	for _, a := range s.Actions[scope] {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].A.Key < out[j].A.Key })
	return out
}

// Describe is what a manifest will run, one line each, for a person to
// read before installing it.
func Describe(m *Manifest) []string {
	var out []string
	q := func(argv []string) string {
		parts := make([]string, len(argv))
		for i, a := range argv {
			if a == "" || strings.ContainsAny(a, " \t\"'\\$`;|&<>()*?!#~") {
				a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
			}
			parts[i] = a
		}
		return strings.Join(parts, " ")
	}
	for _, v := range m.Views {
		extra := ""
		if v.Refresh != "" {
			extra = ", every " + v.Refresh
		}
		out = append(out, fmt.Sprintf("view :%s runs   %s  (%s%s)", v.Name, q(v.Run), v.Format, extra))
	}
	for _, a := range m.Actions {
		what := "opens :" + a.Open
		if a.Open == "" {
			what = "runs   " + q(a.Run) + "  (" + mode(a.Mode) + confirmText(a.Confirm) + ")"
		}
		out = append(out, fmt.Sprintf("key %s on %s %s", a.Key, strings.Join(a.Scopes, ","), what))
	}
	for _, c := range m.Commands {
		out = append(out, fmt.Sprintf("command :%s runs   %s  (%s%s)", c.Name, q(c.Run), mode(c.Mode), confirmText(c.Confirm)))
	}
	return out
}

func mode(m string) string {
	if m == "" {
		return "background"
	}
	return m
}

func confirmText(c bool) string {
	if c {
		return ", asks first"
	}
	return ""
}
