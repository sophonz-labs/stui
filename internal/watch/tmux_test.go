package watch

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/sophonz-labs/stui/internal/mux"
)

// A tmux server of the test's own, never the user's: its own TMUX_TMPDIR,
// short enough for a socket path, and no inherited $TMUX.
func sandboxTmux(t *testing.T) ExecTmux {
	t.Helper()
	bin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("no tmux")
	}
	dir, err := os.MkdirTemp("/tmp", "aw")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_TMPDIR", dir)
	t.Setenv("TMUX", "")
	os.Unsetenv("TMUX")
	os.Unsetenv("TMUX_PANE")
	tm := ExecTmux{Bin: bin}
	t.Cleanup(func() {
		tm.Run(context.Background(), "kill-server")
		os.RemoveAll(dir)
	})
	if _, err := tm.Run(context.Background(), "-f", "/dev/null", "new-session", "-d", "-s", "work", "-x", "120", "-y", "40", "sh"); err != nil {
		t.Fatal(err)
	}
	tm.Run(context.Background(), "set-option", "-g", "window-size", "latest")
	return tm
}

func tmuxOut(t *testing.T, tm ExecTmux, args ...string) string {
	t.Helper()
	out, _ := tm.Run(context.Background(), args...)
	return strings.TrimSpace(out)
}

func TestWatcherOnRealTmux(t *testing.T) {
	tm := sandboxTmux(t)
	answerWait = 2 * time.Second
	settle, settleMax = 200*time.Millisecond, time.Second
	sink := &fakeSink{}
	w := New(Options{Tmux: tm, Sink: sink, ClaudeDir: t.TempDir()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx, tm.Bin)

	// The control client is attached, ignores size and is not counted.
	eventually(t, "control client", func() bool {
		return strings.Contains(tmuxOut(t, tm, "list-clients", "-F", "#{client_flags}"), "control-mode")
	})
	flags := tmuxOut(t, tm, "list-clients", "-F", "#{client_flags}|#{session_name}")
	if !strings.Contains(flags, "ignore-size") || strings.Contains(flags, "read-only") {
		t.Fatalf("flags %q", flags)
	}
	if n := mux.WatcherClients(flags)["work"]; n != 1 {
		t.Fatalf("watcher clients %d", n)
	}

	// A program that notifies (OSC 9) and asks on its own line.
	tm.Run(ctx, "send-keys", "-t", "work", `printf '\033]9;needs you\007Proceed? [y/n] '; read x; echo "got $x"`, "Enter")
	eventually(t, "prompt", func() bool {
		for _, p := range w.Snapshot().Panes {
			if p.Prompt != nil && p.Prompt.Kind == "confirm" {
				return true
			}
		}
		return false
	})
	if size := tmuxOut(t, tm, "display-message", "-p", "-t", "work", "#{window_width}x#{window_height}"); size != "120x40" {
		t.Fatalf("window resized to %s", size)
	}
	p := w.Snapshot().Panes[0]
	zero := 0
	status, body := w.Answer(p.Prompt.ID, &zero, nil)
	if status != 200 || body["confirmed"] != true {
		t.Fatalf("%d %v", status, body)
	}
	eventually(t, "answer typed", func() bool {
		return strings.Contains(tmuxOut(t, tm, "capture-pane", "-p", "-t", "work"), "got y")
	})
	if size := tmuxOut(t, tm, "display-message", "-p", "-t", "work", "#{window_width}x#{window_height}"); size != "120x40" {
		t.Fatalf("window resized to %s", size)
	}

	// A new session gets its own control client; a killed one loses it.
	tm.Run(ctx, "new-session", "-d", "-s", "second", "sh")
	eventually(t, "second client", func() bool {
		return mux.WatcherClients(tmuxOut(t, tm, "list-clients", "-F", "#{client_flags}|#{session_name}"))["second"] == 1
	})
	tm.Run(ctx, "kill-session", "-t", "second")
	cancel()
	eventually(t, "clients gone", func() bool {
		return !strings.Contains(tmuxOut(t, tm, "list-clients", "-F", "#{client_flags}"), "control-mode")
	})
	if tmuxOut(t, tm, "has-session", "-t", "work") != "" {
		t.Fatal("session disturbed")
	}
}
