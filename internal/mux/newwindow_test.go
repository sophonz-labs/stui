package mux

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Against a real tmux, on a server of its own so nothing of the user's is touched.
func TestNewWindowPaneAndPaste(t *testing.T) {
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

	if err := tm.CreateSession(ctx, "gap"); err != nil {
		t.Fatal(err)
	}
	first, ok := tm.FirstPane(ctx, "gap")
	if !ok || !IsPaneID(first) {
		t.Fatalf("first pane %q", first)
	}
	// A phone with a view: the window opens in it.
	if out, err := exec.Command(tm.bin, append([]string{"new-session", "-d"}, tm.AttachArgs("gap", "abc")[1:]...)...).CombinedOutput(); err != nil {
		t.Fatalf("view: %v %s", err, out)
	}
	pane, index, err := tm.NewWindowPane(ctx, "gap", "abc")
	if err != nil || !IsPaneID(pane) || pane == first || index != 1 {
		t.Fatalf("%q %d %v", pane, index, err)
	}
	if cur, _ := tm.ask(ctx, ViewNameFor("gap", "abc")+":", "#{window_index}"); cur != "1" {
		t.Fatalf("the view is on window %q", cur)
	}
	// A phone with none yet: the base session.
	if _, index, err := tm.NewWindowPane(ctx, "gap", "nobody"); err != nil || index != 2 {
		t.Fatalf("%d %v", index, err)
	}
	if _, _, err := tm.NewWindowPane(ctx, "nope", ""); err == nil {
		t.Fatal("a window in a session that is not there")
	}

	// cat echoes what is pasted; no brackets, since cat did not ask for them.
	exec.Command(tm.bin, "send-keys", "-t", pane, "-l", "cat\n").Run()
	time.Sleep(300 * time.Millisecond)
	if err := tm.PasteText(ctx, pane, "/tmp/a b.png -rf "); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		out, _ := exec.Command(tm.bin, "capture-pane", "-p", "-t", pane).Output()
		if strings.Contains(string(out), "/tmp/a b.png -rf") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not pasted: %q", out)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if out, _ := exec.Command(tm.bin, "list-buffers").Output(); strings.Contains(string(out), "stui-paste-") {
		t.Fatalf("buffer left behind: %s", out)
	}
	if err := tm.PasteText(ctx, "%999", "x"); err == nil {
		t.Fatal("pasted into a pane that is not there")
	}
	if err := tm.PasteText(ctx, "gap:0", "x"); err == nil {
		t.Fatal("a target that is not a pane id")
	}
}

// On psmux each session's server numbers its panes from %1, so the panes
// a new window and a new session give are named by their session: a bare
// %1 would be another session's. Real tmux, standing in for psmux (the same
// backend: NewPsmux is a *Tmux with id "psmux").
func TestPsmuxNamesItsNewPanes(t *testing.T) {
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
	if err := tm.CreateSession(ctx, "gap"); err != nil {
		t.Fatal(err)
	}
	ps := &Tmux{id: "psmux", bin: tm.bin}
	first, ok := ps.FirstPane(ctx, "gap")
	if !ok || !strings.HasPrefix(first, "gap:%") || !IsPaneID(first) {
		t.Fatalf("first pane %q", first)
	}
	pane, index, err := ps.NewWindowPane(ctx, "gap", "")
	if err != nil || !strings.HasPrefix(pane, "gap:%") || !IsPaneID(pane) || pane == first || index != 1 {
		t.Fatalf("%q %d %v", pane, index, err)
	}
	// tmux itself is unchanged.
	if p, _, err := tm.NewWindowPane(ctx, "gap", ""); err != nil || strings.Contains(p, ":") {
		t.Fatalf("tmux: %q %v", p, err)
	}
}

// How a pane goes to psmux as a -t, from the forms measured on psmux 3.4.0:
// "<session>:%N" is not found, "<session>:.%N" is.
func TestPsmuxTarget(t *testing.T) {
	for in, want := range map[string]string{
		"work:%2":   "work:.%2",
		"work:%1":   "work:.%1",
		"work:.%2":  "work:.%2",
		"work:1.%2": "work:1.%2",
		"%2":        "%2",
		"work:":     "work:",
		"=work:":    "=work:",
		"work":      "work",
		"work:1":    "work:1",
	} {
		if got := PsmuxTarget(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	ps := &Tmux{id: "psmux"}
	if got := ps.paneTarget("", "work:%3"); got != "work:.%3" {
		t.Errorf("psmux paneTarget %q", got)
	}
	if got := ps.paneTarget("work", "%3"); got != "work:.%3" {
		t.Errorf("psmux paneTarget of a bare id %q", got)
	}
	if got := (&Tmux{id: "tmux"}).paneTarget("", "work:%3"); got != "%3" {
		t.Errorf("tmux paneTarget %q", got)
	}
}
