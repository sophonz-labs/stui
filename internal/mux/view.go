package mux

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Viewer is what a UI client needs from a multiplexer with a view per
// client: the client's own view without tmux's status line and with tmux's
// mouse on, and a pane's history to scroll in the client instead of in
// copy-mode.
type Viewer interface {
	// SetUIMode turns UI mode on or off for client's view of session. Only
	// that view changes; every other client keeps its status line.
	SetUIMode(ctx context.Context, session, client string, on bool) bool
	// PaneHistory is pane's last lines (its history and its screen) as
	// tmux would draw them, with its colors, and the state that decides
	// whether the app scrolls it or hands the wheel to the program.
	PaneHistory(ctx context.Context, session, pane string, lines int) (PaneHistory, bool)
}

// PaneHistory is GET /api/panes/history's answer.
type PaneHistory struct {
	Pane   string `json:"pane"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	// The program is on the alternate screen (vim, less, htop): it has no
	// history to scroll, and the wheel is the program's.
	Alt bool `json:"alt"`
	// The program asked for the mouse: the wheel is its own.
	Mouse bool `json:"mouse"`
	// The pane is in copy-mode (or another tmux mode) already.
	InMode bool `json:"inMode"`
	// Lines of history above the screen, all of them, not just the ones sent.
	HistorySize int `json:"historySize"`
	// The cursor on the screen, from its top left.
	CursorX int `json:"cursorX"`
	CursorY int `json:"cursorY"`
	// The lines, oldest first, the screen's last: capture-pane -e -J, so
	// with SGR colors and wrapped lines joined. Lines end in "\n".
	Text string `json:"text"`
}

// MaxHistoryLines is the most one request reads.
const MaxHistoryLines = 10000

var _ Viewer = (*Tmux)(nil)

func (t *Tmux) SetUIMode(ctx context.Context, session, client string, on bool) bool {
	// Without groups there is no view of one's own: changing the session
	// would take the status line from every client.
	if !t.grouped || client == "" || !ValidName(session) || !ValidClient(client) {
		return false
	}
	view := ViewNameFor(session, client)
	var args []string
	if on {
		args = []string{"set-option", "-t", view, "status", "off", ";", "set-option", "-t", view, "mouse", "on"}
	} else {
		args = []string{"set-option", "-u", "-t", view, "status", ";", "set-option", "-u", "-t", view, "mouse"}
	}
	_, ok := t.sh(ctx, args...)
	return ok
}

const historyFields = "#{pane_width} #{pane_height} #{alternate_on} #{mouse_any_flag} #{pane_in_mode} #{history_size} #{cursor_x} #{cursor_y}"

func (t *Tmux) PaneHistory(ctx context.Context, session, pane string, lines int) (PaneHistory, bool) {
	if !ValidName(session) || !paneIDRE.MatchString(pane) {
		return PaneHistory{}, false
	}
	// Only a pane of this session: the id alone would reach any.
	if s, _ := SplitPaneID(pane); s != "" && s != session {
		return PaneHistory{}, false
	}
	_, bare := SplitPaneID(pane)
	ids, ok := t.sh(ctx, "list-panes", "-s", "-t", "="+session+":", "-F", "#{pane_id}")
	if !ok || !hasLine(ids, bare) {
		return PaneHistory{}, false
	}
	target := t.paneTarget(session, pane)
	state, ok := t.ask(ctx, target, historyFields)
	if !ok {
		return PaneHistory{}, false
	}
	h, ok := parseHistoryState(state)
	if !ok {
		return PaneHistory{}, false
	}
	h.Pane = pane
	lines = min(max(lines, 1), MaxHistoryLines)
	// From `lines` above the screen's top to its last line.
	start := strconv.Itoa(-min(lines, h.HistorySize))
	text, ok := t.sh(ctx, "capture-pane", "-p", "-e", "-J", "-S", start, "-t", target)
	if !ok {
		return PaneHistory{}, false
	}
	h.Text = text
	return h, true
}

func parseHistoryState(s string) (PaneHistory, bool) {
	f := strings.Fields(s)
	if len(f) != 8 {
		return PaneHistory{}, false
	}
	n := make([]int, len(f))
	for i, x := range f {
		v, err := strconv.Atoi(x)
		if err != nil {
			return PaneHistory{}, false
		}
		n[i] = v
	}
	return PaneHistory{
		Width: n[0], Height: n[1], Alt: n[2] == 1, Mouse: n[3] == 1, InMode: n[4] == 1,
		HistorySize: n[5], CursorX: n[6], CursorY: n[7],
	}, true
}

func hasLine(out, want string) bool {
	for _, l := range lines(out) {
		if strings.TrimSpace(l) == want {
			return true
		}
	}
	return false
}

// String is for logs.
func (h PaneHistory) String() string {
	return fmt.Sprintf("%s %dx%d alt=%v mouse=%v history=%d", h.Pane, h.Width, h.Height, h.Alt, h.Mouse, h.HistorySize)
}
