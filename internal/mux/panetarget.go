package mux

import (
	"context"
	"errors"
	"regexp"
	"strconv"
)

// PaneTargeter carries out select-pane, zoom-pane and unzoom on a pane named
// outright, by its id (%12) or as <window>.<pane>, instead of by an index in
// whatever window a client's view happens to be on (/api/version "panes": 3).
//
// The index form raced a client's own attach: a client sends goto-window and
// zoom-pane as soon as its socket opens, before ttyd has made the client's
// view, and with no view the index went to the base session's current window
// -- the one the desk was on. A named pane is in one window, and windows (and
// their zoom) are shared by every view, so no view is needed at all.
type PaneTargeter interface {
	RunPane(ctx context.Context, session string, action Action, target, client string) (PaneResult, error)
}

// PaneResult is how the pane's window stands after the action, read back
// from the multiplexer rather than assumed.
type PaneResult struct {
	ID     string `json:"id"`
	Window int    `json:"window"`
	Pane   int    `json:"pane"`
	// The window is zoomed on this pane.
	Zoomed bool `json:"zoomed"`
	// The window could not be read back; nothing was toggled blind.
	Unknown bool `json:"unknown,omitempty"`
}

// ErrNoPane is a target that names no pane of the session: it closed, or the
// indexes moved since the app read them. The app reads /api/panes again.
var ErrNoPane = errors.New("no such pane")

var paneTargetRE = regexp.MustCompile(`^(?:(?:[A-Za-z0-9_-]{1,64}:)?%[0-9]{1,9}|[0-9]{1,6}\.[0-9]{1,6})$`)

// ValidPaneTarget is a pane id (%<id>, or psmux's <session>:%<id>) or
// <window>.<pane>.
func ValidPaneTarget(s string) bool { return paneTargetRE.MatchString(s) }

// findPane is target among a session's panes.
func findPane(rows []PaneRow, target string) (PaneRow, bool) {
	if IsPaneID(target) {
		_, bare := SplitPaneID(target)
		for _, r := range rows {
			// A bare %n finds psmux's <session>:%n among this session's
			// rows; a qualified one only its own.
			if _, rb := SplitPaneID(r.ID); r.ID == target || (bare == target && rb == bare) {
				return r, true
			}
		}
		return PaneRow{}, false
	}
	var w, p int
	for i := 0; i < len(target); i++ {
		if target[i] == '.' {
			w, _ = strconv.Atoi(target[:i])
			p, _ = strconv.Atoi(target[i+1:])
			break
		}
	}
	for _, r := range rows {
		if r.Window == w && r.Index == p {
			return r, true
		}
	}
	return PaneRow{}, false
}

// zoomedOn is whether window's zoom is on pane id: the window is zoomed and
// id is its active pane.
func zoomedOn(rows []PaneRow, id string) (PaneRow, bool) {
	for _, r := range rows {
		if r.ID == id {
			return r, r.Zoomed && r.Active
		}
	}
	return PaneRow{}, false
}

func (t *Tmux) RunPane(ctx context.Context, session string, action Action, target, client string) (PaneResult, error) {
	if action != SelectPane && action != ZoomPane && action != Unzoom {
		return PaneResult{}, errors.New("not a pane action")
	}
	rows, ok := t.ListPanes(ctx, session)
	if !ok {
		return PaneResult{Unknown: true}, nil
	}
	pane, ok := findPane(rows, target)
	if !ok {
		return PaneResult{}, ErrNoPane
	}
	res := PaneResult{ID: pane.ID, Window: pane.Window, Pane: pane.Index}

	switch action {
	case SelectPane, ZoomPane:
		// The client's view onto the same window, when it has one; when
		// it does not yet, the view it gets copies the base session's
		// current window, so that is moved too -- which is what the app
		// asked for in any case.
		if t.grouped && client != "" {
			view := ViewNameFor(session, client)
			if t.HasSession(ctx, view) {
				t.sh(ctx, "select-window", "-t", "="+view+":"+strconv.Itoa(pane.Window))
			}
		}
		// The pane's window becomes its session's current one: select-pane
		// alone leaves an attach landing on another window.
		t.sh(ctx, "select-window", "-t", pane.ID)
		// select-pane onto another pane unzooms a zoomed window; onto the
		// zoomed pane itself it leaves the zoom alone.
		if _, ok := t.sh(ctx, "select-pane", "-t", pane.ID); !ok {
			return PaneResult{}, ErrNoPane
		}
		if action == ZoomPane {
			// resize-pane -Z toggles: only when the window is read as
			// unzoomed. An unreadable window is left alone.
			z, ok := t.ask(ctx, pane.ID, "#{window_zoomed_flag}")
			if ok && z == "0" {
				t.sh(ctx, "resize-pane", "-Z", "-t", pane.ID)
			} else if !ok {
				res.Unknown = true
			}
		}
	case Unzoom:
		// Only the zoom the client made, on the pane it names: if someone
		// has since zoomed another pane of the window, that zoom is theirs.
		if _, on := zoomedOn(rows, pane.ID); on {
			t.sh(ctx, "resize-pane", "-Z", "-t", pane.ID)
		}
	}

	after, ok := t.ListPanes(ctx, session)
	if !ok {
		res.Unknown = true
		return res, nil
	}
	if _, on := zoomedOn(after, pane.ID); on {
		res.Zoomed = true
	}
	return res, nil
}
