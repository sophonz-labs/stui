package mux

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A pane's folder, on a tmux server of the test's own.
func TestPaneCwd(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tm := NewTmux()
	defer exec.Command(tm.bin, "kill-server").Run()
	work := filepath.Join(dir, "work")
	os.MkdirAll(work, 0o755)
	if _, ok := tm.sh(ctx, "new-session", "-d", "-s", "cwd", "-c", work); !ok {
		t.Fatal("new-session")
	}
	id, _ := tm.ask(ctx, "cwd:", "#{pane_id}")
	got, ok := tm.PaneCwd(ctx, id)
	want, _ := filepath.EvalSymlinks(work)
	if real, _ := filepath.EvalSymlinks(got); !ok || real != want {
		t.Fatalf("cwd of %s: %q %v, want %q", id, got, ok, want)
	}
	if _, ok := tm.PaneCwd(ctx, "%99999"); ok {
		t.Fatal("no such pane")
	}
	if _, ok := tm.PaneCwd(ctx, "cwd:"); ok {
		t.Fatal("not a pane id")
	}
}
