// Package restore writes down what a tmux server holds, and sets it up again
// after the server has died.
//
// A tmux server that dies takes every shell and program in it along. None of
// that comes back. What can come back is the arrangement: sessions, windows
// and their names and layouts, each pane's directory, and -- for a coding
// agent -- the conversation, which the agent keeps on disk and resumes by an
// id stui already receives from its hooks.
//
// Nothing is ever run on the user's behalf. A pane that was running a
// command gets a note saying what it was; an agent pane gets its resume line
// typed at the prompt, not entered (unless restore.json says resumeAgents).
package restore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/sophonz-labs/stui/internal/proc"
	"sync"
	"time"
)

// Snapshot is one tmux server's sessions at a moment.
type Snapshot struct {
	V      int    `json:"v"`
	ID     string `json:"id"`
	Taken  int64  `json:"taken"` // unix ms, last confirmed
	First  int64  `json:"first"` // unix ms, when this arrangement was first seen
	Server Server `json:"server"`
	// The socket the server listened on, for saying which one it was.
	Socket   string    `json:"socket,omitempty"`
	Sessions []Session `json:"sessions"`
}

// Server identifies a tmux server: a new one after a death has another pid
// and start time.
type Server struct {
	PID   int   `json:"pid"`
	Start int64 `json:"start"` // unix seconds
}

// Same is whether s and o are one server. A record from before start times
// were required (Start 0) is matched by its pid alone.
func (s Server) Same(o Server) bool {
	if s.Start == 0 || o.Start == 0 {
		return s.PID != 0 && s.PID == o.PID
	}
	return s.PID == o.PID && s.Start == o.Start
}

type Session struct {
	Name    string `json:"name"`
	Created int64  `json:"created"`
	// Another session of the same tmux group, which holds the windows this
	// one shares; its own Windows is then empty.
	GroupOf string   `json:"groupOf,omitempty"`
	Windows []Window `json:"windows"`
}

type Window struct {
	Index  int    `json:"index"`
	Name   string `json:"name"`
	Layout string `json:"layout"`
	Active bool   `json:"active"`
	Panes  []Pane `json:"panes"`
	// Set on a window restore made, so the web and phone can say so.
	Restored string `json:"restored,omitempty"`
}

type Pane struct {
	Index   int    `json:"index"`
	ID      string `json:"id"` // tmux's %N, meaningful only in its own server
	Active  bool   `json:"active"`
	Cwd     string `json:"cwd"`
	Command string `json:"command"` // pane_current_command
	// The foreground program's whole command line, when it is not a shell.
	Args  string `json:"args,omitempty"`
	Title string `json:"title,omitempty"`
	// A coding agent and the id its conversation resumes by.
	Agent *Agent `json:"agent,omitempty"`
	// Where the pane's last lines were saved, when restore.json asks.
	Scrollback string `json:"scrollback,omitempty"`
	// The pane's lasting key (@stui-key), which a coordinator's and its
	// workers' records name it by (internal/orch, rebind.go): set on the
	// pane that takes its place, so they find each other again.
	Key string `json:"key,omitempty"`
}

type Agent struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
}

// Counts is sessions, windows and panes.
func (s *Snapshot) Counts() (sessions, windows, panes int) {
	for _, se := range s.Sessions {
		sessions++
		for _, w := range se.Windows {
			windows++
			panes += len(w.Panes)
		}
	}
	return
}

// Tmux runs tmux commands against one server.
type Tmux interface {
	Run(ctx context.Context, args ...string) (string, error)
}

// Exec is Tmux by running the binary. Prefix goes before every command
// (-S <socket> in tests); Env replaces the environment when set.
type Exec struct {
	Bin    string
	Prefix []string
	Env    []string
}

func (e Exec) Run(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.Bin, append(append([]string{}, e.Prefix...), args...)...)
	if e.Env != nil {
		cmd.Env = e.Env
	}
	// Snapshots are taken every ten seconds, and on Windows this binary is
	// psmux. Without this each one flashes a console window open and shut
	// (internal/mux/exec.go says the rest).
	proc.Hide(cmd)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return string(out), fmt.Errorf("%s: %s", strings.Join(args[:min(2, len(args))], " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return string(out), err
	}
	return string(out), nil
}

// sep separates -F fields. Not a tab or a control character: tmux rewrites
// those in format output. Free text (names, paths, titles) could hold a
// plain "|", so this is three characters, and the title is last.
const sep = "|&|"

var paneFields = []string{
	"#{session_name}", "#{session_group}", "#{session_created}",
	"#{window_index}", "#{window_active}", "#{window_layout}",
	"#{pane_index}", "#{pane_id}", "#{pane_active}", "#{pane_tty}",
	"#{pane_current_command}", "#{@stui_restored}", "#{@stui-key}",
	"#{window_name}", "#{pane_current_path}", "#{pane_title}",
}

// What Probe found.
type ServerState int

const (
	// No server answers: none is running.
	NoServer ServerState = iota
	// One answered, and said which it is.
	Known
	// One may be there, but which could not be told: tmux did not answer in
	// time, or answered without its start time. Nothing is to be concluded
	// from that -- not a death, and not a new server to take a record of.
	Unknown
)

// Probe is the running server's identity, and whether there is one.
func Probe(ctx context.Context, t Tmux) (Server, ServerState) {
	out, err := t.Run(ctx, "list-sessions", "-F", "#{pid}"+sep+"#{start_time}")
	if err != nil {
		if unsure(ctx, err) {
			return Server{}, Unknown
		}
		return Server{}, NoServer
	}
	line, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	f := strings.Split(line, sep)
	if len(f) != 2 {
		return Server{}, Unknown
	}
	pid, _ := strconv.Atoi(f[0])
	start, _ := strconv.ParseInt(f[1], 10, 64)
	if pid == 0 || start == 0 {
		// A record with start 0 reads as another server next time (Same),
		// which would be a death that never happened (seen under heavy load).
		return Server{}, Unknown
	}
	return Server{PID: pid, Start: start}, Known
}

// unsure is an error that says nothing about whether a server is there:
// tmux was killed for taking too long, or the caller gave up.
func unsure(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	m := err.Error()
	return strings.Contains(m, "signal: killed") || strings.Contains(m, "deadline exceeded")
}

// ServerOf is the running server's identity; ok false when none answers or
// it could not be told which one it is.
func ServerOf(ctx context.Context, t Tmux) (Server, bool) {
	srv, state := Probe(ctx, t)
	return srv, state == Known
}

// IsView says whether a session is one stui made for a single browser (a
// per-client view in the base session's group); those are not restored.
type IsView func(name, group string, peers []string) bool

// Capture reads the whole server. ok false when no server answers.
func Capture(ctx context.Context, t Tmux, isView IsView) (*Snapshot, bool) {
	srv, ok := ServerOf(ctx, t)
	if !ok {
		return nil, false
	}
	out, err := t.Run(ctx, "list-panes", "-a", "-F", strings.Join(paneFields, sep))
	if err != nil {
		return nil, false
	}
	var rows [][]string
	groups := map[string][]string{}
	seenSession := map[string]bool{}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, sep, len(paneFields))
		if len(f) < len(paneFields) {
			continue
		}
		rows = append(rows, f)
		if f[1] != "" && !seenSession[f[0]] {
			groups[f[1]] = append(groups[f[1]], f[0])
		}
		seenSession[f[0]] = true
	}
	snap := &Snapshot{V: 1, Server: srv}
	view := func(name, group string) bool { return isView != nil && isView(name, group, groups[group]) }
	// A group's windows are listed once per member; one member -- the one
	// named for the group, else the first by name -- carries them.
	primary := map[string]string{}
	for _, f := range rows {
		name, group := f[0], f[1]
		if group == "" || view(name, group) {
			continue
		}
		if cur, ok := primary[group]; !ok || (cur != group && (name == group || name < cur)) {
			primary[group] = name
		}
	}
	bySession := map[string]*Session{}
	var order []string
	for _, f := range rows {
		name, group := f[0], f[1]
		if view(name, group) {
			continue
		}
		se := bySession[name]
		if se == nil {
			created, _ := strconv.ParseInt(f[2], 10, 64)
			se = &Session{Name: name, Created: created}
			if p := primary[group]; group != "" && p != name {
				se.GroupOf = p
			}
			bySession[name] = se
			order = append(order, name)
		}
		if se.GroupOf != "" {
			continue
		}
		widx, _ := strconv.Atoi(f[3])
		var w *Window
		for i := range se.Windows {
			if se.Windows[i].Index == widx {
				w = &se.Windows[i]
			}
		}
		if w == nil {
			se.Windows = append(se.Windows, Window{Index: widx, Active: f[4] == "1", Layout: f[5], Name: f[13], Restored: f[11]})
			w = &se.Windows[len(se.Windows)-1]
		}
		pidx, _ := strconv.Atoi(f[6])
		w.Panes = append(w.Panes, Pane{
			Index: pidx, ID: f[7], Active: f[8] == "1", Command: f[10],
			Cwd: f[14], Title: f[15], Args: foregroundArgs(ctx, f[9], f[10]), Key: f[12],
		})
	}
	for _, name := range order {
		snap.Sessions = append(snap.Sessions, *bySession[name])
	}
	sort.SliceStable(snap.Sessions, func(i, j int) bool { return snap.Sessions[i].Name < snap.Sessions[j].Name })
	for i := range snap.Sessions {
		ws := snap.Sessions[i].Windows
		sort.SliceStable(ws, func(a, b int) bool { return ws[a].Index < ws[b].Index })
		for j := range ws {
			ps := ws[j].Panes
			sort.SliceStable(ps, func(a, b int) bool { return ps[a].Index < ps[b].Index })
		}
	}
	return snap, true
}

var shells = map[string]bool{
	"bash": true, "zsh": true, "fish": true, "sh": true, "dash": true, "ksh": true, "mksh": true,
	"tcsh": true, "csh": true, "nu": true, "xonsh": true, "elvish": true, "pwsh": true, "login": true,
}

// IsShell is whether a pane's current command is just its shell.
func IsShell(cmd string) bool { return shells[strings.TrimPrefix(filepath.Base(cmd), "-")] }

type argsEntry struct {
	args string
	at   time.Time
}

// argsCache keeps foregroundArgs for a minute per (tty, command): a snapshot
// every few seconds should not mean a ps per busy pane every few seconds.
var (
	argsMu    sync.Mutex
	argsCache = map[string]argsEntry{}
)

// foregroundArgs is the whole command line of what a pane is running, when
// that is not its shell: "npm run dev" rather than "node". "" when unknown.
func foregroundArgs(ctx context.Context, tty, cmd string) string {
	if tty == "" || cmd == "" || IsShell(cmd) {
		return ""
	}
	key := tty + "\x00" + cmd
	argsMu.Lock()
	e, ok := argsCache[key]
	if len(argsCache) > 500 {
		argsCache = map[string]argsEntry{}
	}
	argsMu.Unlock()
	if ok && time.Since(e.at) < time.Minute {
		return e.args
	}
	args := psForeground(ctx, tty)
	argsMu.Lock()
	argsCache[key] = argsEntry{args, time.Now()}
	argsMu.Unlock()
	return args
}

func psForeground(ctx context.Context, tty string) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-t", strings.TrimPrefix(tty, "/dev/"), "-o", "stat=,args=").Output()
	if err != nil {
		return ""
	}
	var last string
	for _, line := range strings.Split(string(out), "\n") {
		stat, args, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || !strings.Contains(stat, "+") {
			continue
		}
		args = strings.TrimSpace(args)
		first, _, _ := strings.Cut(args, " ")
		if IsShell(first) {
			continue
		}
		last = args
	}
	if len(last) > 300 {
		last = last[:300]
	}
	return last
}

// Attach fills in each pane's agent from what hooks said, keyed by pane id,
// and guesses the kind from the command where no hook ever came.
func (s *Snapshot) Attach(agents map[string]Agent) {
	for i := range s.Sessions {
		for j := range s.Sessions[i].Windows {
			for k := range s.Sessions[i].Windows[j].Panes {
				p := &s.Sessions[i].Windows[j].Panes[k]
				if a, ok := agents[p.ID]; ok && a.Kind != "" {
					a := a
					p.Agent = &a
					continue
				}
				switch p.Command {
				case "claude", "codex", "gemini":
					p.Agent = &Agent{Kind: p.Command}
				}
			}
		}
	}
}

// fingerprint is what makes two snapshots the same arrangement: everything
// but when they were taken, the server, pane ids and scrollback paths.
func (s *Snapshot) fingerprint() string {
	c := *s
	c.ID, c.Taken, c.First, c.Server, c.Socket = "", 0, 0, Server{}, ""
	c.Sessions = append([]Session{}, s.Sessions...)
	for i := range c.Sessions {
		c.Sessions[i].Created = 0
		ws := append([]Window{}, c.Sessions[i].Windows...)
		for j := range ws {
			ps := append([]Pane{}, ws[j].Panes...)
			for k := range ps {
				ps[k].ID, ps[k].Scrollback, ps[k].Title = "", "", ""
			}
			ws[j].Panes = ps
		}
		c.Sessions[i].Windows = ws
	}
	b, _ := json.Marshal(c)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}
