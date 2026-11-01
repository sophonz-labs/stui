package restore

import (
	"context"
	"strings"
	"time"
)

// Typing an agent's resume line into a restored pane.
//
// A restored pane starts with a short note and then execs the user's login
// shell. Typed before that shell's line editor is up, the line was on screen
// twice: the terminal echoed it as it arrived, while the pane was still in
// cooked mode, and then zsh's editor drew it again on its prompt. So each
// line waits until its pane's terminal is in the mode a line editor puts it
// in -- no canonical input, no echo -- and is typed then, once. A pane whose
// terminal cannot be looked at (another platform, a fake tmux in a test)
// gets it straight away, as before; so does one still not ready after
// typeWait, which costs a doubled line and nothing worse.

type typed struct {
	pane, line string
	// Enter after it: an agent's resume line stui runs (Decide).
	enter bool
}

// typeWait bounds how long a restore waits for the shells it started: a slow
// shell profile is seconds, not minutes.
var typeWait = 15 * time.Second

// editing reports whether the terminal at path is being read by a line
// editor right now (-icanon -echo). ok is false when it cannot be told.
var editing = ttyEditing

func typeWhenReady(ctx context.Context, t Tmux, lines []typed) {
	if len(lines) == 0 {
		return
	}
	send := func(l typed) {
		t.Run(ctx, "send-keys", "-t", l.pane, "-l", l.line)
		if l.enter {
			t.Run(ctx, "send-keys", "-t", l.pane, "Enter")
		}
	}
	ttys := map[string]string{}
	for _, l := range lines {
		out, _ := t.Run(ctx, "display-message", "-p", "-t", l.pane, "#{pane_tty}")
		ttys[l.pane] = strings.TrimSpace(out)
	}
	deadline := time.Now().Add(typeWait)
	left := lines
	for len(left) > 0 {
		var waiting []typed
		for _, l := range left {
			ready, ok := editing(ttys[l.pane])
			if ready || !ok {
				send(l)
				continue
			}
			waiting = append(waiting, l)
		}
		left = waiting
		if len(left) == 0 || time.Now().After(deadline) || ctx.Err() != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, l := range left {
		send(l)
	}
}

// TypeWhenReady types line into pane once its shell's line editor is up, and
// Enter after it when enter is set: the same wait a restore uses, for a
// window stui opens on a phone's behalf (internal/launch).
func TypeWhenReady(ctx context.Context, t Tmux, pane, line string, enter bool) {
	typeWhenReady(ctx, t, []typed{{pane: pane, line: line, enter: enter}})
}

// Editing reports whether the terminal at path is in raw mode, as a line
// editor or a full-screen program puts it; ok is false when it cannot be told.
func Editing(path string) (ready, ok bool) { return editing(path) }

// TypeWait is the longest TypeWhenReady waits for a shell.
func TypeWait() time.Duration { return typeWait }
