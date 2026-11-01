package mux

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The window a CI job's log runs in: opened once, found again by name, and
// started again in place for another attempt of the same job.
func TestNamedWindow(t *testing.T) {
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
	if _, ok := tm.sh(ctx, "new-session", "-d", "-s", "work", "-c", dir); !ok {
		t.Fatal("new-session")
	}
	// A per-client view in the session's group: the window goes to the base.
	if _, ok := tm.sh(ctx, "new-session", "-d", "-t", "work", "-s", ViewNameFor("work", "abc123")); !ok {
		t.Fatal("view")
	}
	pane, _ := tm.ask(ctx, "="+ViewNameFor("work", "abc123")+":", "#{pane_id}")
	argv := []string{"sh", "-c", "echo attempt-1; sleep 30"}
	s, i, err := tm.NamedWindow(ctx, pane, "ci:ci/e2e", "9", dir, argv)
	if err != nil || s != "work" || i != 1 {
		t.Fatalf("opened %q %d %v", s, i, err)
	}
	active, _ := tm.ask(ctx, "=work:", "#{window_index}")
	if active != "0" {
		t.Errorf("opening moved the session to window %s", active)
	}
	// Again: the same window, nothing new.
	s, i2, err := tm.NamedWindow(ctx, pane, "ci:ci/e2e", "9", dir, []string{"sh", "-c", "echo never; sleep 30"})
	if err != nil || i2 != i {
		t.Fatalf("again %q %d %v", s, i2, err)
	}
	out, _ := tm.sh(ctx, "list-windows", "-t", "=work:", "-F", "#{window_name}")
	if n := strings.Count(out, "ci:ci/e2e"); n != 1 {
		t.Errorf("%d windows called ci:ci/e2e", n)
	}
	// The same job name for another job id (another branch, another
	// attempt): a window of its own, the first one untouched.
	_, i3, err := tm.NamedWindow(ctx, pane, "ci:ci/e2e", "10", dir, []string{"sh", "-c", "echo attempt-2; sleep 30"})
	if err != nil || i3 == i {
		t.Fatalf("another job: %d %v", i3, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		first, _ := tm.sh(ctx, "capture-pane", "-p", "-t", "=work:"+strconv.Itoa(i))
		second, _ := tm.sh(ctx, "capture-pane", "-p", "-t", "=work:"+strconv.Itoa(i3))
		if strings.Contains(first, "attempt-1") && strings.Contains(second, "attempt-2") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("windows:\n%s\n--\n%s", first, second)
		}
		time.Sleep(100 * time.Millisecond)
	}
	// And each is found again by its own job.
	if _, again, _ := tm.NamedWindow(ctx, pane, "ci:ci/e2e", "9", dir, argv); again != i {
		t.Errorf("job 9 is in window %d, not %d", again, i)
	}
}
