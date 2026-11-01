package mux

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// The layout minimap's half of stui, against a real tmux on
// a server of its own: a window split three ways lists each pane's box, the
// window says how many panes it has, zoom-pane works for a client that has
// not attached yet, and unzoom undoes it without toggling it back on.
func TestPaneMinimap(t *testing.T) {
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

	if err := tm.CreateSession(ctx, "mini"); err != nil {
		t.Fatal(err)
	}
	// A detached session is 80x24; split it left | right-top / right-bottom.
	tm.sh(ctx, "split-window", "-h", "-t", "mini:")
	tm.sh(ctx, "split-window", "-v", "-t", "mini:.1")

	wins := tm.ListWindows(ctx, "mini", "")
	if len(wins) != 1 || wins[0].Panes != 3 || wins[0].Zoomed {
		t.Fatalf("windows: %+v", wins)
	}
	rows, ok := tm.ListPanes(ctx, "mini")
	if !ok || len(rows) != 3 {
		t.Fatalf("panes: %+v", rows)
	}
	for _, r := range rows {
		g := r.Geometry
		if g == nil || g.WindowWidth == 0 || g.Left+g.Width > g.WindowWidth || g.Top+g.Height > g.WindowHeight {
			t.Fatalf("pane %d geometry: %+v", r.Index, g)
		}
	}
	l, rt, rb := rows[0].Geometry, rows[1].Geometry, rows[2].Geometry
	if l.Left != 0 || rt.Left <= l.Width-1 || rt.Left != rb.Left || rb.Top <= rt.Top || l.Height != l.WindowHeight {
		t.Fatalf("layout: %+v %+v %+v", l, rt, rb)
	}

	// Not attached: no view yet, so the base session's window.
	for _, n := range []int{2, 0} {
		tm.Run(ctx, "mini", ZoomPane, strconv.Itoa(n), "phone1")
		got, _ := tm.ask(ctx, "mini:", "#{pane_index} #{window_zoomed_flag}")
		if got != strconv.Itoa(n)+" 1" {
			t.Fatalf("zoom-pane %d: %q", n, got)
		}
	}
	// Zoomed on pane 0: every box is still where it sits under the zoom.
	zoomed, _ := tm.ListPanes(ctx, "mini")
	if !zoomed[0].Zoomed {
		t.Fatalf("not zoomed: %+v", zoomed)
	}
	for i, r := range zoomed {
		if r.Geometry == nil || *r.Geometry != *rows[i].Geometry {
			t.Fatalf("pane %d under the zoom: %+v, want %+v", i, r.Geometry, rows[i].Geometry)
		}
	}
	// Twice: the second must not zoom it again.
	for range 2 {
		tm.Run(ctx, "mini", Unzoom, "0", "phone1")
		if z, _ := tm.ask(ctx, "mini:", "#{window_zoomed_flag}"); z != "0" {
			t.Fatalf("unzoom left it zoomed: %q", z)
		}
	}
	if w := tm.ListWindows(ctx, "mini", ""); w[0].Zoomed {
		t.Fatalf("window still zoomed: %+v", w)
	}
}
