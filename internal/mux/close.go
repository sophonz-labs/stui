package mux

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/sophonz-labs/stui/internal/cmdicon"
)

// Closing a window or a session from a phone or a browser.
//
// Agents open windows and sessions faster than anybody closes them, so the
// apps can close one. Before they do they ask what is still running there,
// and say so: a window that is only a shell at its prompt goes with a plain
// "are you sure", one with a build or an agent in it with a warning that
// names them.

// ErrNoWindow is a window that is not (or no longer) in the session.
var ErrNoWindow = errors.New("no such window")

// Closer is a backend that can close one window of a session by its id, and
// list the session's panes with what each is running.
type Closer interface {
	// Panes is every pane of session's windows, in display order. processes
	// is false when the backend cannot tell what a pane is running: Command
	// is then empty, and the caller says it does not know rather than "idle".
	Panes(ctx context.Context, session string) (panes []PaneInfo, processes bool, ok bool)
	// KillWindow closes the window id (WindowInfo.ID) of session. It is
	// ErrNoWindow when session has no such window.
	KillWindow(ctx context.Context, session, id string) error
}

// PaneInfo is one pane, as the close preview needs it.
type PaneInfo struct {
	// The window it is in: its id, index and name.
	Window      string
	WindowIndex int
	WindowName  string
	// The pane's own id (%3), the key agent watch knows it by.
	ID string
	// What it runs (internal/cmdicon), "" for a shell at its prompt or
	// when the backend cannot tell.
	Command string
	Icon    string
	// The pane's foreground program, raw, before icon resolution.
	Proc string
	Dead bool
}

// AgentState is what agent watch knows about the agent in a pane.
type AgentState struct {
	Agent string
	State string
}

// CanClose reports whether m can close a window, and so whether the apps
// may offer to.
func CanClose(m Multiplexer) bool {
	c, ok := m.(Closer)
	if !ok {
		return false
	}
	if t, ok := c.(*Tmux); ok && t.bin == "" {
		return false
	}
	return true
}

// Window ids reach a command line, so they are matched like names: tmux's
// "@12", zellij's tab id "3".
var windowIDRE = regexp.MustCompile(`^@?[0-9]{1,9}$`)

func ValidWindowID(id string) bool { return windowIDRE.MatchString(id) }

var shells = map[string]bool{
	"bash": true, "zsh": true, "fish": true, "sh": true, "dash": true, "ksh": true, "mksh": true,
	"tcsh": true, "csh": true, "nu": true, "xonsh": true, "elvish": true, "login": true,
	"pwsh": true, "powershell": true, "cmd": true,
}

// IsShell is whether a pane's current command is only its shell: nothing
// would be lost by closing it but the shell itself.
func IsShell(cmd string) bool {
	if strings.TrimSpace(cmd) == "" {
		return true
	}
	c := strings.TrimPrefix(filepath.Base(strings.ReplaceAll(cmd, `\`, "/")), "-")
	c = strings.TrimSuffix(strings.ToLower(c), ".exe")
	return c == "" || shells[c]
}

// ---- tmux

var paneFields = strings.Join([]string{
	"#{window_id}", "#{window_index}", "#{pane_id}", "#{pane_current_command}",
	"#{pane_tty}", "#{pane_dead}", "#{window_name}",
}, sep)

func parsePanes(out string) []PaneInfo {
	list := []PaneInfo{}
	for _, line := range lines(out) {
		f := strings.SplitN(line, sep, 7)
		for len(f) < 7 {
			f = append(f, "")
		}
		idx, _ := strconv.Atoi(f[1])
		p := PaneInfo{Window: f[0], WindowIndex: idx, ID: f[2], Proc: f[3], Dead: f[5] == "1", WindowName: f[6]}
		if !p.Dead && !IsShell(p.Proc) {
			m := resolve(p.Proc, f[4])
			p.Command, p.Icon = m.Command, m.Icon
		}
		list = append(list, p)
	}
	return list
}

// Panes reads the base session: its windows are every view's windows.
func (t *Tmux) Panes(ctx context.Context, session string) ([]PaneInfo, bool, bool) {
	if !ValidName(session) {
		return nil, false, false
	}
	out, ok := t.sh(ctx, "list-panes", "-s", "-t", "="+session+":", "-F", paneFields)
	if !ok {
		return nil, false, false
	}
	list := parsePanes(out)
	for i := range list {
		list[i].ID = t.paneOut(session, list[i].ID)
	}
	// psmux answers the same formats, but what it says a pane runs has not
	// been checked against a real Windows console: better to say so.
	return list, t.grouped, true
}

// KillWindow takes the window out of every session it is linked to -- the
// base session and each client's view of it. Closing a session's last
// window ends the session, as it would at the keyboard.
func (t *Tmux) KillWindow(ctx context.Context, session, id string) error {
	if !ValidName(session) || !ValidWindowID(id) || !strings.HasPrefix(id, "@") {
		return ErrNoWindow
	}
	out, ok := t.sh(ctx, "list-windows", "-t", "="+session+":", "-F", "#{window_id}")
	if !ok || !containsLine(out, id) {
		return ErrNoWindow
	}
	if _, ok := t.sh(ctx, "kill-window", "-t", id); !ok {
		return errors.New("tmux would not close the window")
	}
	return nil
}

func containsLine(out, want string) bool {
	for _, l := range lines(out) {
		if strings.TrimSpace(l) == want {
			return true
		}
	}
	return false
}

// ---- zellij

// Panes lists the tabs only: zellij says which command a pane was started
// with, not what runs in it now, so processes is false.
func (z *Zellij) Panes(ctx context.Context, session string) ([]PaneInfo, bool, bool) {
	if !ValidName(session) {
		return nil, false, false
	}
	out, ok := z.action(ctx, session, "list-tabs")
	if !ok {
		return nil, false, false
	}
	var list []PaneInfo
	for _, w := range parseTabs(out) {
		list = append(list, PaneInfo{Window: w.ID, WindowIndex: w.Index, WindowName: w.Name})
	}
	return list, false, true
}

// KillWindow needs a zellij that closes a tab by its id (0.44 and later);
// an older one answers with an error, and so does this.
func (z *Zellij) KillWindow(ctx context.Context, session, id string) error {
	if !ValidName(session) || !ValidWindowID(id) || strings.HasPrefix(id, "@") {
		return ErrNoWindow
	}
	found := false
	for _, w := range z.ListWindows(ctx, session, "") {
		found = found || w.ID == id
	}
	if !found {
		return ErrNoWindow
	}
	if _, ok := z.action(ctx, session, "close-tab-by-id", id); !ok {
		return errors.New("this zellij cannot close a tab by id")
	}
	return nil
}

// ---- the preview

// Running is one pane that is not a shell at its prompt.
type Running struct {
	Pane    string `json:"pane"`
	Command string `json:"command"`
	Icon    string `json:"icon,omitempty"`
	// The coding agent in it and its state (agent watch), when one is known.
	Agent string `json:"agent,omitempty"`
	State string `json:"state,omitempty"`
}

// PreviewWindow is a window as the close preview gives it.
type PreviewWindow struct {
	ID      string    `json:"id"`
	Index   int       `json:"index"`
	Name    string    `json:"name"`
	Panes   int       `json:"panes"`
	Running []Running `json:"running"`
}

// ClosePreview is GET /api/close: what closing would stop.
type ClosePreview struct {
	Session string `json:"session"`
	// False when the multiplexer cannot tell what a pane runs (zellij):
	// every running list is then empty, and means "not known", not "idle".
	Processes bool `json:"processes"`
	// Terminals open on the session right now, on any device.
	Clients int             `json:"clients"`
	Windows []PreviewWindow `json:"windows"`
}

// Preview groups panes into their windows, in order, and keeps the ones
// running something. agents is agent watch's view by pane id: a pane with an
// agent says which, and whether it is working or waiting on someone.
func Preview(session string, panes []PaneInfo, processes bool, agents map[string]AgentState) ClosePreview {
	out := ClosePreview{Session: session, Processes: processes, Windows: []PreviewWindow{}}
	at := map[string]int{}
	for _, p := range panes {
		i, ok := at[p.Window]
		if !ok {
			i = len(out.Windows)
			at[p.Window] = i
			out.Windows = append(out.Windows, PreviewWindow{ID: p.Window, Index: p.WindowIndex, Name: p.WindowName, Running: []Running{}})
		}
		w := &out.Windows[i]
		if p.ID == "" {
			// A zellij tab: no panes to count.
			continue
		}
		w.Panes++
		if p.Command == "" {
			continue
		}
		r := Running{Pane: p.ID, Command: p.Command, Icon: p.Icon}
		if a, ok := agents[p.ID]; ok && a.Agent != "" {
			m := cmdicon.Resolve(a.Agent, p.Proc, nil)
			r.Command, r.Icon, r.Agent, r.State = m.Command, m.Icon, a.Agent, a.State
		}
		w.Running = append(w.Running, r)
	}
	return out
}
