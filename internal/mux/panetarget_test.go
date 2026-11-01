package mux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestValidPaneTarget(t *testing.T) {
	for s, want := range map[string]bool{
		"%12": true, "0.2": true, "13.0": true, "work:%3": true,
		"": false, "%": false, "2": false, "1.": false, ".1": false, "%1;kill": false, "-1.0": false, "a.b": false,
	} {
		if got := ValidPaneTarget(s); got != want {
			t.Errorf("ValidPaneTarget(%q) = %v", s, got)
		}
	}
}

func TestFindPane(t *testing.T) {
	rows := []PaneRow{{Window: 0, Index: 0, ID: "%1"}, {Window: 0, Index: 1, ID: "%2"}, {Window: 1, Index: 0, ID: "%3"}}
	if r, ok := findPane(rows, "%3"); !ok || r.Window != 1 {
		t.Fatalf("%%3: %+v %v", r, ok)
	}
	if r, ok := findPane(rows, "0.1"); !ok || r.ID != "%2" {
		t.Fatalf("0.1: %+v %v", r, ok)
	}
	if _, ok := findPane(rows, "0.2"); ok {
		t.Fatal("0.2 found")
	}
	if _, ok := findPane(rows, "%9"); ok {
		t.Fatal("%9 found")
	}
	// psmux rows are <session>:%n; either form finds them, another
	// session's does not.
	ps := []PaneRow{{Window: 0, Index: 0, ID: "work:%1"}, {Window: 0, Index: 1, ID: "work:%2"}}
	if r, ok := findPane(ps, "work:%2"); !ok || r.Index != 1 {
		t.Fatalf("work:%%2: %+v %v", r, ok)
	}
	if r, ok := findPane(ps, "%2"); !ok || r.ID != "work:%2" {
		t.Fatalf("%%2 on psmux: %+v %v", r, ok)
	}
	if _, ok := findPane(ps, "play:%2"); ok {
		t.Fatal("play:%2 found")
	}
}

// A named pane is zoomed in its own window whatever window the base session
// (the desk) is on and whether or not the client has a view yet; a pane that
// went away is a 409, not a zoom somewhere else; unzoom gives back only the
// zoom the client made.
func TestPaneTargetTwoWindows(t *testing.T) {
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

	if err := tm.CreateSession(ctx, "two"); err != nil {
		t.Fatal(err)
	}
	// Window 0 split three ways; window 1 a single pane, where the desk is.
	tm.sh(ctx, "split-window", "-h", "-t", "two:0")
	tm.sh(ctx, "split-window", "-v", "-t", "two:0.1")
	tm.sh(ctx, "new-window", "-t", "two:")
	tm.sh(ctx, "select-window", "-t", "two:1")

	zoom := func(target string) PaneResult {
		t.Helper()
		res, err := tm.RunPane(ctx, "two", ZoomPane, target, "phone1")
		if err != nil {
			t.Fatalf("zoom %s: %v", target, err)
		}
		return res
	}
	flag := func(win string) string {
		z, _ := tm.ask(ctx, "two:"+win, "#{window_zoomed_flag} #{pane_index}")
		return z
	}

	res := zoom("0.2")
	if !res.Zoomed || res.Window != 0 || res.Pane != 2 {
		t.Fatalf("zoom 0.2: %+v", res)
	}
	if got := flag("0"); got != "1 2" {
		t.Fatalf("window 0: %q", got)
	}
	if got := flag("1"); got != "0 0" {
		t.Fatalf("the desk's window 1 changed: %q", got)
	}
	// Again: still zoomed, not toggled off.
	if res := zoom("0.2"); !res.Zoomed {
		t.Fatalf("zoom 0.2 twice: %+v", res)
	}
	// By id, onto another pane of the zoomed window.
	rows, _ := tm.ListPanes(ctx, "two")
	first, _ := findPane(rows, "0.0")
	if res := zoom(first.ID); !res.Zoomed || res.Pane != 0 {
		t.Fatalf("zoom %s: %+v", first.ID, res)
	}

	// Someone else's zoom is not given back by ours.
	mine, _ := findPane(rows, "0.1")
	zoom(mine.ID)
	zoom(first.ID)
	if res, err := tm.RunPane(ctx, "two", Unzoom, mine.ID, "phone1"); err != nil || res.Zoomed {
		t.Fatalf("unzoom %s: %+v %v", mine.ID, res, err)
	}
	if got := flag("0"); got != "1 0" {
		t.Fatalf("their zoom was undone: %q", got)
	}
	// Ours is.
	if res, err := tm.RunPane(ctx, "two", Unzoom, first.ID, "phone1"); err != nil || res.Zoomed {
		t.Fatalf("unzoom %s: %+v %v", first.ID, res, err)
	}
	if got := flag("0"); got != "0 0" {
		t.Fatalf("not unzoomed: %q", got)
	}

	// Indexes moved: pane 2 closed, so 0.2 names nothing.
	last, _ := findPane(rows, "0.2")
	tm.sh(ctx, "kill-pane", "-t", last.ID)
	if _, err := tm.RunPane(ctx, "two", ZoomPane, "0.2", "phone1"); !errors.Is(err, ErrNoPane) {
		t.Fatalf("zoom a closed pane: %v", err)
	}
	if _, err := tm.RunPane(ctx, "two", ZoomPane, last.ID, "phone1"); !errors.Is(err, ErrNoPane) {
		t.Fatalf("zoom a closed pane by id: %v", err)
	}
	if got := flag("1"); got != "0 0" {
		t.Fatalf("the desk's window 1 changed: %q", got)
	}
}
