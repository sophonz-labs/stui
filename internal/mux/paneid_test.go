package mux

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakePsmuxBin is a psmux that answers from canned output and writes down
// each command line it got. It finds a pane by "<session>:.%N" only, as
// psmux 3.4.0 does ("<session>:%N" is "can't find window").
func fakePsmuxBin(t *testing.T) (*Tmux, func() string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for psmux")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	script := `#!/bin/sh
echo "$*" >> "` + log + `"
case "$*" in
"list-panes -s -t =work: -F #{pane_id}") printf '%%1\n%%2\n' ;;
"list-panes -s -t =work: -F "*window_layout*) printf '0|@1|0|%%1|1|claude|/dev/pty1|0|0|0|80|24|80|24|0||main\n' ;;
"list-panes -s -t =work: -F "*) printf '@1|0|%%1|claude|/dev/pty1|0|main\n' ;;
"list-panes -a -F #{session_name}|#{pane_id}") printf 'work|%%1\nplay|%%1\n' ;;
"list-panes -a -F "*) printf '%%1|0|0|work|main\n%%1|2|1|play|logs\n' ;;
"list-windows -t work: -F "*) printf '0|80|1|claude|%%1|/dev/pty1|||@1|1|0||main\n' ;;
"display-message -p -t play:.%1 #{pane_current_path}") echo 'C:\src' ;;
"display-message -p -t work:.%2 "*) echo '80 24 0 0 0 10 0 3' ;;
"capture-pane -p -e -J -S -1 -t work:.%2") echo 'hello' ;;
esac
`
	bin := filepath.Join(dir, "psmux")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Tmux{id: "psmux", bin: bin}, func() string { b, _ := os.ReadFile(log); return string(b) }
}

func TestPsmuxReportsQualifiedPaneIDs(t *testing.T) {
	m, _ := fakePsmuxBin(t)
	ctx := context.Background()
	rows, ok := m.ListPanes(ctx, "work")
	if !ok || len(rows) != 1 || rows[0].ID != "work:%1" {
		t.Fatalf("ListPanes %+v", rows)
	}
	panes, _, _ := m.Panes(ctx, "work")
	if len(panes) != 1 || panes[0].ID != "work:%1" {
		t.Fatalf("Panes %+v", panes)
	}
	by := m.PanesBySession(ctx)
	if by["work"][0] != "work:%1" || by["play"][0] != "play:%1" {
		t.Fatalf("PanesBySession %v", by)
	}
	wins := m.ListWindows(ctx, "work", "")
	if len(wins) != 1 || wins[0].Pane != "work:%1" {
		t.Fatalf("ListWindows %+v", wins)
	}
	// Both sessions have a %1: the qualified id finds the right one.
	loc, ok := m.LocatePane(ctx, "play:%1")
	if !ok || loc.Session != "play" || loc.Window != 2 || loc.ID != "play:%1" {
		t.Fatalf("LocatePane %+v %v", loc, ok)
	}
}

func TestPsmuxTakesQualifiedPaneIDs(t *testing.T) {
	m, log := fakePsmuxBin(t)
	ctx := context.Background()
	// A shell script, on a busy test machine: not tmux's answer time.
	defer func(d time.Duration) { queryTimeout = d }(queryTimeout)
	queryTimeout = 20 * time.Second
	if cwd, ok := m.PaneCwd(ctx, "play:%1"); !ok || cwd != `C:\src` {
		t.Fatalf("PaneCwd %q %v", cwd, ok)
	}
	for _, pane := range []string{"work:%2", "%2"} {
		h, ok := m.PaneHistory(ctx, "work", pane, 1)
		if !ok || strings.TrimSpace(h.Text) != "hello" || h.Pane != pane {
			t.Fatalf("PaneHistory(%s) %+v %v\n%s", pane, h, ok, log())
		}
	}
	// Another session's pane is not this session's.
	if _, ok := m.PaneHistory(ctx, "work", "play:%2", 1); ok {
		t.Fatal("history of play's pane through work")
	}
}

func TestPaneIDForms(t *testing.T) {
	for s, want := range map[string]bool{
		"%3": true, "work:%3": true, "my-s_1:%12": true,
		"3": false, "work:3": false, "a b:%3": false, ":%3": false, "work:%": false, "%3;ls": false,
	} {
		if IsPaneID(s) != want {
			t.Errorf("IsPaneID(%q) = %v", s, !want)
		}
	}
	tm, ps := &Tmux{id: "tmux"}, &Tmux{id: "psmux"}
	if tm.paneOut("work", "%3") != "%3" || ps.paneOut("work", "%3") != "work:%3" {
		t.Error("paneOut")
	}
	if tm.paneTarget("", "work:%3") != "%3" || ps.paneTarget("", "work:%3") != "work:.%3" || ps.paneTarget("work", "%3") != "work:.%3" {
		t.Error("paneTarget")
	}
}
