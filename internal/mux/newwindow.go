package mux

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
)

// WindowMaker is a backend that can open a window and say which pane it made,
// and paste text into a pane: what "New window with Claude Code" and an
// uploaded file need. tmux and
// psmux; on psmux the panes it gives are "<session>:%N" (paneid.go).
type WindowMaker interface {
	// NewWindowPane opens a window in session, shown to client's view when it
	// has one, and returns its first pane (%7) and the window's index.
	NewWindowPane(ctx context.Context, session, client string) (pane string, index int, err error)
	// PasteText pastes text into pane as one bracketed paste: a shell or an
	// agent takes it as text, a newline in it does not submit anything.
	PasteText(ctx context.Context, pane, text string) error
	// FirstPane is the active pane of session's current window: where a
	// session just made has its shell.
	FirstPane(ctx context.Context, session string) (string, bool)
}

func (t *Tmux) NewWindowPane(ctx context.Context, session, client string) (string, int, error) {
	if t.bin == "" {
		return "", 0, errors.New("no tmux")
	}
	targets := []string{session}
	// Into the view of the phone that asked, so it is the window it sees; the
	// base session when that phone has no view yet.
	if t.grouped && ValidClient(client) {
		targets = []string{ViewNameFor(session, client), session}
	}
	for _, s := range targets {
		out, ok := t.sh(ctx, "new-window", "-t", "="+s+":", "-P", "-F", "#{pane_id}"+sep+"#{window_index}")
		if !ok {
			continue
		}
		f := strings.SplitN(strings.TrimSpace(out), sep, 2)
		if len(f) == 2 && IsPaneID(f[0]) {
			n, _ := strconv.Atoi(f[1])
			return t.paneOut(s, f[0]), n, nil
		}
	}
	return "", 0, errors.New("could not open a window in " + session)
}

func (t *Tmux) PasteText(ctx context.Context, pane, text string) error {
	if !IsPaneID(pane) {
		return errors.New("bad pane")
	}
	if t.bin == "" {
		return errors.New("no tmux")
	}
	b := make([]byte, 6)
	rand.Read(b)
	buf := "stui-paste-" + hex.EncodeToString(b)
	if _, ok := t.sh(ctx, "set-buffer", "-b", buf, "--", text); !ok {
		return errors.New("could not set a buffer")
	}
	// -p: bracketed, when the program in the pane asked for it; -d: the
	// buffer goes with the paste, so nothing of it stays in tmux's list.
	if _, ok := t.sh(ctx, "paste-buffer", "-p", "-d", "-b", buf, "-t", t.paneTarget("", pane)); !ok {
		t.sh(ctx, "delete-buffer", "-b", buf)
		return errors.New("no such pane")
	}
	return nil
}

func (t *Tmux) FirstPane(ctx context.Context, session string) (string, bool) {
	out, ok := t.ask(ctx, "="+session+":", "#{pane_id}")
	if !ok || !IsPaneID(out) {
		return "", false
	}
	return t.paneOut(session, out), true
}
