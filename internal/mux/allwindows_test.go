package mux

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Against a real tmux, on a server of its own: one call lists every
// session's windows, and a client's view is not a session of its own.
func TestTmuxAllWindows(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux")
	}
	dir, err := os.MkdirTemp("/tmp", "stgo")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("TMUX_TMPDIR", dir)
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("SHELL", "/bin/sh")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tm := NewTmux()
	defer exec.Command(tm.bin, "kill-server").Run()

	for _, s := range []string{"alpha", "beta"} {
		if err := tm.CreateSession(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command(tm.bin, "new-window", "-d", "-t", "=alpha:", "-n", "logs|x", "sleep 30").CombinedOutput(); err != nil {
		t.Fatalf("new-window: %v %s", err, out)
	}
	if out, err := exec.Command(tm.bin, append([]string{"new-session", "-d"}, tm.AttachArgs("alpha", "abc")[1:]...)...).CombinedOutput(); err != nil {
		t.Fatalf("view: %v %s", err, out)
	}
	list := tm.ListSessions(ctx)
	got := AllWindows(ctx, tm, list, 16)
	if len(got) != 2 || len(got["alpha"]) != 2 || len(got["beta"]) != 1 {
		t.Fatalf("got %+v", got)
	}
	if w := got["alpha"][1]; w.Name != "logs|x" || w.ID == "" {
		t.Errorf("window %+v", w)
	}
}
