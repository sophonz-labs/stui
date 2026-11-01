package mux

import (
	"context"
	"strconv"
	"strings"

	"github.com/sophonz-labs/stui/internal/cmdicon"
	"github.com/sophonz-labs/stui/internal/tmuxlayout"
)

// SelectPane selects pane <arg> (its index) in the window the client is
// looking at, and leaves the window's zoom alone: where the apps' share
// target and compose picker take you.
const SelectPane Action = "select-pane"

// PaneRow is one pane of a session, for GET /api/panes: what an app's picker
// shows under a window.
type PaneRow struct {
	// The window it is in, by the index goto-window expects, and its id.
	Window     int    `json:"window"`
	WindowID   string `json:"windowId"`
	WindowName string `json:"windowName"`
	// The index select-pane and zoom-pane take, and the pane's own id (%3).
	Index  int    `json:"index"`
	ID     string `json:"id"`
	Active bool   `json:"active"`
	// What it runs (internal/cmdicon); empty for a shell at its prompt.
	Command string `json:"command,omitempty"`
	Icon    string `json:"icon,omitempty"`
	// The agent in it and agent watch's state for it, when it has one.
	Agent string `json:"agent,omitempty"`
	State string `json:"state,omitempty"`
	Title string `json:"title,omitempty"`
	// Where the pane sits in its window, in cells, for the apps' layout
	// minimap. Absent when the multiplexer does not say.
	Geometry *PaneGeometry `json:"geometry,omitempty"`
	// The window is zoomed (on whichever pane is active).
	Zoomed bool `json:"zoomed,omitempty"`
	proc   string
}

// PaneGeometry is a pane's box and its window's size, in cells. A zoomed
// window still reports the layout under the zoom, which is what the
// minimap draws.
type PaneGeometry struct {
	Left         int `json:"left"`
	Top          int `json:"top"`
	Width        int `json:"width"`
	Height       int `json:"height"`
	WindowWidth  int `json:"windowWidth"`
	WindowHeight int `json:"windowHeight"`
}

// layoutGeometry is pane id's box in a window_layout string (either form,
// internal/tmuxlayout), and the window's size from its root. While the
// window is zoomed tmux gives the layout under the zoom here, so the box is
// where the pane sits when unzoomed. nil when the layout cannot be read or
// does not hold the pane.
func layoutGeometry(layout, id string) *PaneGeometry {
	b, win, ok := tmuxlayout.Pane(layout, id)
	if !ok {
		return nil
	}
	return parseGeometry([]string{strconv.Itoa(b.X), strconv.Itoa(b.Y), strconv.Itoa(b.W), strconv.Itoa(b.H),
		strconv.Itoa(win.W), strconv.Itoa(win.H)})
}

// parseGeometry reads the six numbers, or nil when any is missing or the
// box does not fit in the window: an older tmux, or psmux leaving a format
// it does not know empty.
func parseGeometry(f []string) *PaneGeometry {
	n := make([]int, 6)
	for i := range n {
		v, err := strconv.Atoi(strings.TrimSpace(f[i]))
		if err != nil || v < 0 {
			return nil
		}
		n[i] = v
	}
	g := &PaneGeometry{Left: n[0], Top: n[1], Width: n[2], Height: n[3], WindowWidth: n[4], WindowHeight: n[5]}
	if g.Width == 0 || g.Height == 0 || g.WindowWidth == 0 || g.WindowHeight == 0 ||
		g.Left+g.Width > g.WindowWidth || g.Top+g.Height > g.WindowHeight {
		return nil
	}
	return g
}

// PaneLister is a backend that can list a session's panes and say where a
// pane is. Only tmux (and psmux, which answers its formats) is one.
type PaneLister interface {
	ListPanes(ctx context.Context, session string) ([]PaneRow, bool)
	LocatePane(ctx context.Context, id string) (PaneLocation, bool)
}

// PaneLocation is where a pane is: what stui doc records with a document.
type PaneLocation struct {
	Session    string `json:"session"`
	Window     int    `json:"window"`
	WindowName string `json:"windowName"`
	Pane       int    `json:"pane"`
	ID         string `json:"paneId"`
}

var paneRowFields = strings.Join([]string{
	"#{window_index}", "#{window_id}", "#{pane_index}", "#{pane_id}", "#{pane_active}",
	"#{pane_current_command}", "#{pane_tty}", "#{pane_dead}",
	"#{pane_left}", "#{pane_top}", "#{pane_width}", "#{pane_height}",
	"#{window_width}", "#{window_height}", "#{window_zoomed_flag}", "#{window_layout}",
	"#{window_name}",
}, sep)

const paneRowCount = 17

func parsePaneRows(out string) []PaneRow {
	list := []PaneRow{}
	for _, line := range lines(out) {
		// The window name can contain the separator: it is whatever is left.
		f := strings.SplitN(line, sep, paneRowCount)
		for len(f) < paneRowCount {
			f = append(f, "")
		}
		if f[7] == "1" {
			continue
		}
		w, _ := strconv.Atoi(f[0])
		i, _ := strconv.Atoi(f[2])
		r := PaneRow{Window: w, WindowID: f[1], Index: i, ID: f[3], Active: f[4] == "1", WindowName: f[16], proc: f[5],
			Geometry: layoutGeometry(f[15], f[3]), Zoomed: f[14] == "1"}
		// window_zoomed_flag is 1 on every pane of a zoomed window, and the
		// zoomed one reports the whole window as its box (measured, tmux
		// 3.8). The layout under the zoom is in window_layout, which tmux
		// 2.6 and psmux may not give; without it a zoomed window has none.
		if r.Geometry == nil && !r.Zoomed {
			r.Geometry = parseGeometry(f[8:14])
		}
		if !IsShell(r.proc) {
			m := resolve(r.proc, f[6])
			r.Command, r.Icon = m.Command, m.Icon
		}
		list = append(list, r)
	}
	return list
}

// ListPanes reads the base session: its windows are every view's windows,
// and a pane's index is the same in each of them.
func (t *Tmux) ListPanes(ctx context.Context, session string) ([]PaneRow, bool) {
	if !ValidName(session) {
		return nil, false
	}
	out, ok := t.sh(ctx, "list-panes", "-s", "-t", "="+session+":", "-F", paneRowFields)
	if !ok {
		return nil, false
	}
	list := parsePaneRows(out)
	for i := range list {
		list[i].ID = t.paneOut(session, list[i].ID)
	}
	return list, true
}

var locateFields = strings.Join([]string{
	"#{pane_id}", "#{window_index}", "#{pane_index}", "#{session_name}", "#{window_name}",
}, sep)

// LocatePane finds a pane by its id across every session, preferring the
// base session over a client's view of it.
func (t *Tmux) LocatePane(ctx context.Context, id string) (PaneLocation, bool) {
	if !paneIDRE.MatchString(id) {
		return PaneLocation{}, false
	}
	out, ok := t.sh(ctx, "list-panes", "-a", "-F", locateFields)
	if !ok {
		return PaneLocation{}, false
	}
	return pickLocation(out, id, t.paneOut)
}

// pickLocation finds id in `list-panes -a -F locateFields`; name gives each
// line's pane as stui reports it (psmux: by its session). A bare id on psmux
// is the first session's with that %n.
func pickLocation(out, id string, name func(session, id string) string) (PaneLocation, bool) {
	var found PaneLocation
	have := false
	for _, line := range lines(out) {
		f := strings.SplitN(line, sep, 5)
		if len(f) < 5 {
			continue
		}
		if f[0] != id && name(f[3], f[0]) != id {
			continue
		}
		id := name(f[3], f[0])
		w, _ := strconv.Atoi(f[1])
		p, _ := strconv.Atoi(f[2])
		loc := PaneLocation{Session: f[3], Window: w, Pane: p, WindowName: f[4], ID: id}
		if base, _, view := strings.Cut(loc.Session, ClientSep); view {
			// A client's view: its base session holds the same window.
			if !have {
				loc.Session = base
				found, have = loc, true
			}
			continue
		}
		return loc, true
	}
	return found, have
}

// PanesOfWindow keeps the rows of window index w, in order.
func PanesOfWindow(list []PaneRow, w int) []PaneRow {
	out := []PaneRow{}
	for _, r := range list {
		if r.Window == w {
			out = append(out, r)
		}
	}
	return out
}

// PaneAgents fills in the agent and state agent watch has for each row.
func PaneAgents(list []PaneRow, agentOf func(pane string) string, stateOf func(pane string) string) {
	for i := range list {
		if agentOf != nil {
			if a := agentOf(list[i].ID); a != "" {
				list[i].Agent = a
				m := cmdicon.Resolve(a, list[i].proc, nil)
				list[i].Icon, list[i].Command = m.Icon, m.Command
			}
		}
		if stateOf != nil {
			list[i].State = stateOf(list[i].ID)
		}
	}
}

// PaneCwd is the folder pane id's shell or program is in (tmux's
// pane_current_path): where a path printed in it is read from.
func (t *Tmux) PaneCwd(ctx context.Context, id string) (string, bool) {
	if !paneIDRE.MatchString(id) {
		return "", false
	}
	return t.ask(ctx, t.paneTarget("", id), "#{pane_current_path}")
}

// PaneKey is a pane's lasting key (@stui-key, orch.OptKey), "" when it has
// none.
func (t *Tmux) PaneKey(ctx context.Context, id string) string {
	if !paneIDRE.MatchString(id) {
		return ""
	}
	k, _ := t.ask(ctx, t.paneTarget("", id), "#{@stui-key}")
	return k
}
