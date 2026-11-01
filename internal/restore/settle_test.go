//go:build !windows

package restore

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A restored window keeps saying so until it has been dealt with, however
// long that takes -- and looking at it (a client resizing it) is not dealing
// with it.
func TestSettleClearsTheMarksOnceDealtWith(t *testing.T) {
	x := sandbox(t)
	ctx := context.Background()
	must(t, x, "new-session", "-d", "-s", "s", "-n", "agent", "-x", "120", "-y", "40")
	agentPane := must(t, x, "display-message", "-p", "-t", "=s:agent", "#{pane_id}")
	must(t, x, "new-window", "-d", "-t", "=s:", "-n", "plain")
	plainPane := must(t, x, "display-message", "-p", "-t", "=s:plain", "#{pane_id}")
	waitFor(t, "the shells", func() bool {
		return IsShell(must(t, x, "display-message", "-p", "-t", agentPane, "#{pane_current_command}")) &&
			IsShell(must(t, x, "display-message", "-p", "-t", plainPane, "#{pane_current_command}"))
	})
	time.Sleep(500 * time.Millisecond)

	// Restored hours ago, as a restore marks it.
	now := time.Now()
	stamp := now.Add(-8 * time.Hour).Format(time.RFC3339)
	for _, w := range []string{"=s:agent", "=s:plain"} {
		must(t, x, "set-option", "-w", "-t", w, "@stui_restored", stamp)
	}
	must(t, x, "set-option", "-w", "-t", "=s:agent", "@stui_restored_agent", "claude")
	must(t, x, "set-option", "-p", "-t", agentPane, paneAgent, "claude")
	mark := func(w, opt string) string {
		v, _ := x.Run(ctx, "show-options", "-wv", "-t", w, opt)
		return strings.TrimSpace(v)
	}

	// Nothing has happened: the marks stay, however old.
	Settle(ctx, x, now)
	Settle(ctx, x, now)
	if mark("=s:plain", "@stui_restored") == "" || mark("=s:agent", "@stui_restored") == "" {
		t.Fatal("a mark went with nothing having run")
	}

	// Looking is not dealing with it: a phone opening the window resizes it.
	must(t, x, "resize-window", "-t", "=s:plain", "-x", "60", "-y", "20")
	time.Sleep(500 * time.Millisecond)
	Settle(ctx, x, now)
	Settle(ctx, x, now)
	if mark("=s:plain", "@stui_restored") == "" {
		t.Fatal("resizing the window cleared its mark")
	}

	// A command that ended before anybody looked still counts.
	must(t, x, "send-keys", "-t", plainPane, "true", "Enter")
	waitFor(t, "the plain window to settle", func() bool {
		Settle(ctx, x, now)
		return mark("=s:plain", "@stui_restored") == ""
	})

	// Something ran next to the agent, but its resume line waits: both stay.
	must(t, x, "split-window", "-d", "-t", "=s:agent")
	other := ""
	for _, id := range strings.Split(must(t, x, "list-panes", "-t", "=s:agent", "-F", "#{pane_id}"), "\n") {
		if id != agentPane {
			other = id
		}
	}
	must(t, x, "send-keys", "-t", other, "sleep 100", "Enter")
	waitFor(t, "sleep in the other pane", func() bool {
		return must(t, x, "display-message", "-p", "-t", other, "#{pane_current_command}") == "sleep"
	})
	Settle(ctx, x, now)
	if mark("=s:agent", "@stui_restored_agent") != "claude" || mark("=s:agent", "@stui_restored") == "" {
		t.Fatal("the agent window lost its marks while its agent still waited")
	}

	// The agent runs again: the window has been dealt with.
	must(t, x, "send-keys", "-t", agentPane, "sleep 100", "Enter")
	waitFor(t, "the agent window to settle", func() bool {
		Settle(ctx, x, now)
		return mark("=s:agent", "@stui_restored_agent") == "" && mark("=s:agent", "@stui_restored") == ""
	})
	if v, _ := x.Run(ctx, "show-options", "-pv", "-t", agentPane, paneAgent); strings.TrimSpace(v) != "" {
		t.Errorf("the agent's pane still says %q", v)
	}
}

// What a restore does itself in its first seconds -- the note, the shell,
// the typed resume line -- is not somebody dealing with the window.
func TestSettleWaitsOutTheRestoreItself(t *testing.T) {
	x := sandbox(t)
	ctx := context.Background()
	must(t, x, "new-session", "-d", "-s", "s", "sleep 100")
	now := time.Now()
	must(t, x, "set-option", "-w", "-t", "=s:", "@stui_restored", now.Add(-2*time.Second).Format(time.RFC3339))
	if Settle(ctx, x, now) != 0 {
		t.Fatal("cleared a mark the restore had just set")
	}
	if Settle(ctx, x, now.Add(settleAfter)) != 1 {
		t.Fatal("kept a mark on a window running something long after the restore")
	}
}
