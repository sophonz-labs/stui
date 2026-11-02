// Package plugin reads plugin.json manifests: other CLIs' commands and
// output brought into a tmux TUI. The format is the tui-plugins spec, v 1
// (github.com/sophonz-labs/tui-plugins/blob/main/SPEC.md). Nothing here
// knows about the UI, so any TUI implementing the spec can use it as is.
package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Version is the manifest version this package reads.
const Version = 1

// Manifest is one plugin.json.
type Manifest struct {
	V           int       `json:"v"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Homepage    string    `json:"homepage,omitempty"`
	Requires    []string  `json:"requires,omitempty"` // programs that must be on PATH
	Views       []View    `json:"views,omitempty"`
	Actions     []Action  `json:"actions,omitempty"`
	Commands    []Command `json:"commands,omitempty"`
}

// View is a table made from a command's output, opened with :name.
type View struct {
	Name    string   `json:"name"`
	Title   string   `json:"title,omitempty"`
	Run     []string `json:"run"`
	Format  string   `json:"format"` // json, tsv, lines
	Columns []Column `json:"columns,omitempty"`
	ID      string   `json:"id,omitempty"`      // the field that names a row, to keep the cursor on it
	Refresh string   `json:"refresh,omitempty"` // "10s"; empty is only on open and ctrl-r
	Timeout string   `json:"timeout,omitempty"` // default 10s
	Cwd     string   `json:"cwd,omitempty"`     // "", "pane", "home"
}

// Column is one column of a view: a field of each record.
type Column struct {
	Title string `json:"title,omitempty"`
	Field string `json:"field"`
}

// Exec is how an action or command runs.
type Exec struct {
	Run     []string `json:"run,omitempty"`
	Mode    string   `json:"mode,omitempty"` // background (default), foreground, popup
	Confirm bool     `json:"confirm,omitempty"`
	Wait    bool     `json:"wait,omitempty"` // foreground/popup: keep the output until a key
	Cwd     string   `json:"cwd,omitempty"`
}

// Action is a key on rows of the views in Scopes.
type Action struct {
	Key    string   `json:"key"`
	Title  string   `json:"title"`
	Scopes []string `json:"scopes"`
	Open   string   `json:"open,omitempty"` // a plugin view to open on the row, instead of run
	Exec
}

// Command is :name [args].
type Command struct {
	Name  string `json:"name"`
	Title string `json:"title,omitempty"`
	Exec
}

// TmuxScopes are the views every implementation has.
var TmuxScopes = []string{"sessions", "windows", "panes"}

// Reserved are the keys no plugin action may take, on any view, in any
// TUI: moving, filtering, commands, help, and the digits that switch views.
var Reserved = []string{"j", "k", "g", "G", "h", "l", "q", "/", ":", "?", "enter", "esc", "ctrl-b", "ctrl-f", "ctrl-r", "ctrl-c", "ctrl-e",
	"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"}

// TmuxVars are the placeholders every action and command can use.
var TmuxVars = []string{"session", "window", "pane", "pane_cwd", "pane_command"}

var (
	nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	// shortcut: keys are one character or ctrl-a..z, which is what the
	// key readers name; function keys can come with a v 2.
	ctrlRe = regexp.MustCompile(`^ctrl-[a-z]$`)
)

// Parse reads and checks a manifest. Unknown fields are an error: a
// misspelt "confirm" must not quietly run without asking.
func Parse(data []byte) (*Manifest, error) {
	var probe struct {
		V int `json:"v"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, err
	}
	if probe.V > Version {
		return nil, fmt.Errorf("manifest v %d needs a newer version (this reads v %d)", probe.V, Version)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate is every rule of the spec that one manifest can check alone.
func (m *Manifest) Validate() error {
	var errs []error
	bad := func(f string, a ...any) { errs = append(errs, fmt.Errorf(f, a...)) }
	if m.V != Version {
		bad(`"v" must be %d`, Version)
	}
	if !nameRe.MatchString(m.Name) {
		bad("name %q: lower-case letters, digits and -, at most 32", m.Name)
	}
	if len(m.Views)+len(m.Actions)+len(m.Commands) == 0 {
		bad("no views, actions or commands")
	}
	views := map[string]bool{}
	for i, v := range m.Views {
		at := fmt.Sprintf("views[%d] %q", i, v.Name)
		if !nameRe.MatchString(v.Name) {
			bad("%s: name: lower-case letters, digits and -", at)
		}
		if views[v.Name] {
			bad("%s: two views with this name", at)
		}
		views[v.Name] = true
		if err := checkRun(v.Run, nil); err != nil {
			bad("%s: run: %v", at, err)
		}
		switch v.Format {
		case "json":
			if len(v.Columns) == 0 {
				bad("%s: a json view needs columns", at)
			}
		case "tsv", "lines":
		default:
			bad("%s: format %q: json, tsv or lines", at, v.Format)
		}
		for j, c := range v.Columns {
			if c.Field == "" {
				bad("%s: columns[%d]: no field", at, j)
			}
		}
		if d, err := dur(v.Refresh); err != nil || (v.Refresh != "" && d < time.Second) {
			bad("%s: refresh %q: a duration of 1s or more", at, v.Refresh)
		}
		if d, err := dur(v.Timeout); err != nil || (v.Timeout != "" && d <= 0) {
			bad("%s: timeout %q: a duration", at, v.Timeout)
		}
		if !okCwd(v.Cwd) {
			bad("%s: cwd %q: pane, home or nothing", at, v.Cwd)
		}
	}
	for i, x := range m.Actions {
		at := fmt.Sprintf("actions[%d] %q", i, x.Key)
		if !ValidKey(x.Key) {
			bad("%s: key: one character or ctrl-a..z", at)
		} else if contains(Reserved, x.Key) {
			bad("%s: key: reserved for moving around", at)
		}
		if x.Title == "" {
			bad("%s: no title", at)
		}
		if len(x.Scopes) == 0 {
			bad("%s: no scopes", at)
		}
		tmuxOnly := true
		for _, s := range x.Scopes {
			if s != "all" && !nameRe.MatchString(s) {
				bad("%s: scope %q", at, s)
			}
			if !contains(TmuxScopes, s) {
				tmuxOnly = false
			}
		}
		var known []string
		if tmuxOnly {
			known = TmuxVars // no row fields on tmux's own views
		}
		switch {
		case x.Open != "" && len(x.Run) > 0:
			bad("%s: run or open, not both", at)
		case x.Open != "":
			if !nameRe.MatchString(x.Open) {
				bad("%s: open %q", at, x.Open)
			}
		default:
			if err := checkRun(x.Run, known); err != nil {
				bad("%s: run: %v", at, err)
			}
		}
		checkExec(x.Exec, at, bad)
	}
	cmds := map[string]bool{}
	for i, c := range m.Commands {
		at := fmt.Sprintf("commands[%d] %q", i, c.Name)
		if !nameRe.MatchString(c.Name) {
			bad("%s: name: lower-case letters, digits and -", at)
		}
		if cmds[c.Name] || views[c.Name] {
			bad("%s: the name is taken in this plugin", at)
		}
		cmds[c.Name] = true
		if err := checkRun(c.Run, nil); err != nil {
			bad("%s: run: %v", at, err)
		}
		checkExec(c.Exec, at, bad)
	}
	return errors.Join(errs...)
}

func checkExec(e Exec, at string, bad func(string, ...any)) {
	switch e.Mode {
	case "", "background", "foreground", "popup":
	default:
		bad("%s: mode %q: background, foreground or popup", at, e.Mode)
	}
	if !okCwd(e.Cwd) {
		bad("%s: cwd %q: pane, home or nothing", at, e.Cwd)
	}
}

// checkRun is a non-empty argv with well-formed placeholders; when known
// is set, only those names (and args) may appear.
func checkRun(run []string, known []string) error {
	if len(run) == 0 || run[0] == "" {
		return errors.New("needs a program")
	}
	for _, s := range run {
		for _, n := range Placeholders(s) {
			if known != nil && n != "args" && !contains(known, n) {
				return fmt.Errorf("{%s}: tmux's views have only %s", n, strings.Join(known, " "))
			}
		}
	}
	return nil
}

// ValidKey is a key an action can take.
func ValidKey(k string) bool {
	return ctrlRe.MatchString(k) || (utf8.RuneCountInString(k) == 1 && k > " " && k != "\x7f")
}

func okCwd(c string) bool { return c == "" || c == "pane" || c == "home" }

func dur(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	return time.ParseDuration(s)
}

// RefreshEvery and TimeoutAfter are the view's durations, parsed.
func (v *View) RefreshEvery() time.Duration { d, _ := dur(v.Refresh); return d }
func (v *View) TimeoutAfter() time.Duration {
	if d, _ := dur(v.Timeout); d > 0 {
		return d
	}
	return 10 * time.Second
}

// Heading is a column's title, its field in capitals when it has none.
func (c Column) Heading() string {
	if c.Title != "" {
		return c.Title
	}
	return strings.ToUpper(c.Field)
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
