package mux

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A split or a new window opens in the folder of the pane being looked at,
// and still opens when that folder has gone.
func TestNewPanesOpenHere(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux")
	}
	sock, err := os.MkdirTemp("/tmp", "stgo")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sock)
	t.Setenv("TMUX_TMPDIR", sock)
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tm := NewTmux()
	defer exec.Command(tm.bin, "kill-server").Run()
	if err := tm.CreateSession(ctx, "here"); err != nil {
		t.Fatal(err)
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	work := filepath.Join(dir, "proj")
	os.Mkdir(work, 0o755)
	// The pane the client sees runs in work.
	if _, ok := tm.sh(ctx, "respawn-pane", "-k", "-t", "here:", "-c", work); !ok {
		t.Fatal("respawn")
	}
	paths := func() []string {
		out, _ := tm.sh(ctx, "list-panes", "-s", "-t", "here", "-F", "#{pane_current_path}")
		return strings.Fields(out)
	}
	// A command reply alone does not establish the pane's observed cwd.
	// Wait for the expected path before using it to create another pane.
	waitPaths := func(count int, want string) []string {
		t.Helper()
		var got []string
		for end := time.Now().Add(3 * time.Second); time.Now().Before(end); {
			got = paths()
			ready := len(got) == count
			if want != "" {
				for _, p := range got {
					ready = ready && p == want
				}
			}
			if ready {
				return got
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("panes did not settle: %v, want %d panes in %q", got, count, want)
		return nil
	}
	waitPaths(1, work)
	for _, a := range []Action{SplitRight, SplitDown, NewWindow} {
		tm.sh(ctx, "select-window", "-t", "here:0")
		tm.sh(ctx, "select-pane", "-t", "here:0.0")
		before := len(paths())
		if err := tm.Run(ctx, "here", a, "", ""); err != nil {
			t.Fatalf("%v: %v", a, err)
		}
		got := waitPaths(before+1, work)
		if len(got) != before+1 {
			t.Fatalf("%v: %d panes, want %d", a, len(got), before+1)
		}
		for _, p := range got {
			if p != work {
				t.Errorf("%v: a pane in %s, want %s (%v)", a, p, work, got)
			}
		}
	}
	// The folder goes: the window opens all the same (where it did before).
	tm.sh(ctx, "select-window", "-t", "here:0")
	tm.sh(ctx, "select-pane", "-t", "here:0.0")
	os.RemoveAll(work)
	before := len(paths())
	if err := tm.Run(ctx, "here", NewWindow, "", ""); err != nil {
		t.Fatalf("with the folder gone: %v", err)
	}
	waitPaths(before+1, "")
	if _, err := os.Stat(filepath.Join(dir, "#{pane_current_path}")); err == nil {
		t.Error("tmux made a directory of the format")
	}
}

func TestWithoutDir(t *testing.T) {
	got := withoutDir([]string{"split-window", "-t", "w:", "-h", "-c", herePath})
	if strings.Join(got, " ") != "split-window -t w: -h" {
		t.Errorf("%v", got)
	}
	if got := withoutDir([]string{"kill-pane", "-t", "w:"}); len(got) != 3 {
		t.Errorf("%v", got)
	}
}
