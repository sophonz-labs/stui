package mux

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// A scroll of n moves n lines, on a server of its own. (psmux dropped a
// lone -N, so there it moved one: the count goes as -XN.)
func TestScrollMovesTheCount(t *testing.T) {
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
	if err := tm.CreateSession(ctx, "sc"); err != nil {
		t.Fatal(err)
	}
	exec.Command(tm.bin, "send-keys", "-t", "sc:", "-l", "seq 1 300\n").Run()
	time.Sleep(500 * time.Millisecond)
	tm.scroll(ctx, "sc:", "up", 7)
	if pos, _ := tm.settledPosition(ctx, "sc:"); pos != 7 {
		t.Fatalf("scrolled to %d, want 7", pos)
	}
}
