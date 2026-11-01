// Package mux is what the rest of the app needs from a terminal multiplexer.
//
// The phone-facing layer -- key bar, session list, swipe -- is written against
// this, not against tmux. Backends differ in what they can do, so each
// declares its own actions and the UI renders only those.
package mux

import (
	"context"
	"regexp"
	"strings"
)

type Action string

const (
	NextWindow  Action = "next-window"
	PrevWindow  Action = "prev-window"
	NewWindow   Action = "new-window"
	CloseWindow Action = "close-window"
	GotoWindow  Action = "goto-window"
	// RenameWindow renames the window the client is looking at. It takes no
	// target on purpose: zellij can only rename the focused tab, so both
	// backends mean the same thing by it.
	RenameWindow Action = "rename-window"
	NextPane     Action = "next-pane"
	Zoom         Action = "zoom"
	SplitRight   Action = "split-right"
	SplitDown    Action = "split-down"
	ClosePane    Action = "close-pane"
	ScrollUp     Action = "scroll-up"
	ScrollDown   Action = "scroll-down"
	ScrollEnd    Action = "scroll-end"
	Detach       Action = "detach"
	// ZoomPane selects pane <arg> (its index) in the window the client is
	// looking at and zooms it: where the apps take a teammate's pane.
	ZoomPane Action = "zoom-pane"
	// Unzoom unzooms window <arg> (its index; the client's window without
	// one) when it is zoomed, and does nothing when it is not: what the
	// apps send on leaving a pane they zoomed.
	Unzoom Action = "unzoom"
)

// allActions is the order the key bar has always received them in.
var allActions = []Action{
	NextWindow, PrevWindow, NewWindow, CloseWindow, GotoWindow,
	RenameWindow,
	NextPane, Zoom, SplitRight, SplitDown, ClosePane,
	ScrollUp, ScrollDown, ScrollEnd, Detach,
	ZoomPane, SelectPane, Unzoom,
}

type WindowInfo struct {
	// The index goto-window expects.
	Index int `json:"index"`
	// What DELETE /api/windows takes: tmux's window id ("@3"), zellij's tab
	// id ("2"). It stays put while windows are moved and renumbered.
	ID     string `json:"id,omitempty"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
	// Columns the multiplexer is drawing this window at, or 0 when the
	// backend will not say. It is what lets the page notice another client
	// holding the window wider than it can show, and shrink to fit.
	Width int `json:"width"`
	// How many panes it has, and whether it is zoomed; 0 and false when
	// the backend will not say. The apps open a window of two or more
	// panes on its layout minimap.
	Panes  int  `json:"panes,omitempty"`
	Zoomed bool `json:"zoomed,omitempty"`
	// What the window's active pane is running (internal/cmdicon).
	Icon    string `json:"icon,omitempty"`
	Command string `json:"command,omitempty"`
	// When stui set this window up again after the multiplexer died
	// (RFC 3339), so the page can say "restored"; absent otherwise.
	Restored string `json:"restored,omitempty"`
	// The agent ("claude", "codex") whose resume line stui typed into this
	// restored window; absent otherwise.
	RestoredAgent string `json:"restoredAgent,omitempty"`
	// The GitHub repository ("owner/name") the active pane's folder is in,
	// from its git origin; absent outside one. A client can show the
	// owner's avatar beside the window (GET /api/windows).
	Repo string `json:"repo,omitempty"`
	// The GitHub or GitLab project the active pane's folder is in, for the
	// owner's or group's avatar (GET /api/avatar); absent outside one.
	Forge *Forge `json:"forge,omitempty"`
	// The active pane, for agent watch to correct the icon; not sent.
	Pane string `json:"-"`
	proc string
	path string
}

type SessionInfo struct {
	Name     string `json:"name"`
	Windows  int    `json:"windows"`
	Attached int    `json:"attached"`
	// Unix seconds, or 0 when the backend does not report it.
	Created       int64  `json:"created"`
	CurrentWindow string `json:"currentWindow"`
	// What the current window's active pane is running (internal/cmdicon).
	Icon    string `json:"icon,omitempty"`
	Command string `json:"command,omitempty"`
	// How many panes in this session are running a coding agent, counted
	// across every window -- not only the one on screen. Claude Code's agent
	// teams put each teammate in its own pane, so a session can hold several
	// at once and the count is the only way to see that from a list.
	Agents int    `json:"agents,omitempty"`
	Pane   string `json:"-"`
	proc   string
}

type Multiplexer interface {
	ID() string
	Label() string
	// Actions this backend can carry out; the key bar renders exactly these.
	Actions() []Action
	// Whether a session outlives a dropped websocket. False means the phone
	// loses its work whenever the screen locks, which the UI says out loud.
	Persistent() bool
	// True when a second client gets its own size instead of clamping the first.
	IndependentClientSize() bool
	// The program ttyd runs. Fixed for the life of the process.
	Command() string

	Available() bool
	ListSessions(ctx context.Context) []SessionInfo
	HasSession(ctx context.Context, name string) bool
	CreateSession(ctx context.Context, name string) error
	KillSession(ctx context.Context, name string) error
	// AttachArgs attaches a client to session. client identifies the browser
	// ("" when it sent none); backends that can size each one apart use it.
	AttachArgs(session, client string) []string
	// Windows (tmux) or tabs (zellij) in a session, in display order.
	ListWindows(ctx context.Context, session, client string) []WindowInfo
	// Run carries out a semantic action. arg is "" when none was given.
	Run(ctx context.Context, session string, action Action, arg, client string) error
}

// Supports reports whether m can carry out a.
func Supports(m Multiplexer, a Action) bool {
	for _, x := range m.Actions() {
		if x == a {
			return true
		}
	}
	return false
}

// Session names reach a command line, so they are matched against an
// allowlist rather than escaped. tmux additionally forbids `.` and `:`.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func ValidName(name string) bool { return nameRE.MatchString(name) }

// Browser identity, generated client-side and kept in localStorage.
var clientRE = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

func ValidClient(id string) bool { return clientRE.MatchString(id) }

// ValidWindowName is looser than a session name, because a window name is a
// label rather than a target -- but control characters would corrupt the
// status line, and a leading dash would be read by tmux as a flag.
func ValidWindowName(name string) bool {
	n := len([]rune(name))
	// JS counts UTF-16 units; runes are close enough for a 64 cap.
	if n < 1 || n > 64 || strings.HasPrefix(name, "-") {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
