package mux

import "testing"

func TestParsePaneRows(t *testing.T) {
	// Window 1 zoomed on pane 0: its pane_* say the whole window, the
	// layout says where it sits under the zoom.
	out := "1|@1|0|%1|1|zsh|/dev/ttys001|0|0|0|161|24|161|24|1|c1a0,161x24,0,0{80x24,0,0,1,80x24,81,0,2}|edit\n" +
		"1|@1|1|%2|0|vim|/dev/ttys002|0|81|0|80|24|161|24|1|c1a0,161x24,0,0{80x24,0,0,1,80x24,81,0,2}|edit\n" +
		"2|@4|0|%7|1|zsh|/dev/ttys003|1|0|0|80|24|80|24|0|x,80x24,0,0,7|gone\n" +
		// psmux, or an older tmux, leaving the geometry empty.
		"3|@5|0|%9|1|node|/dev/ttys004|0|||||||0||a|b\n" +
		// No layout: pane_* it is, unzoomed.
		"4|@6|0|%10|1|zsh|/dev/ttys005|0|0|0|80|24|80|24|0||solo\n"
	rows := parsePaneRows(out)
	if len(rows) != 4 {
		t.Fatalf("%+v", rows)
	}
	if r := rows[0]; r.Window != 1 || r.WindowID != "@1" || r.Index != 0 || r.ID != "%1" || !r.Active || r.Command != "" || r.WindowName != "edit" {
		t.Fatalf("%+v", r)
	}
	if r := rows[1]; r.Index != 1 || r.Active || r.proc != "vim" || !r.Zoomed ||
		r.Geometry == nil || *r.Geometry != (PaneGeometry{Left: 81, Top: 0, Width: 80, Height: 24, WindowWidth: 161, WindowHeight: 24}) {
		t.Fatalf("%+v %+v", r, r.Geometry)
	}
	if g := rows[0].Geometry; g == nil || *g != (PaneGeometry{Left: 0, Top: 0, Width: 80, Height: 24, WindowWidth: 161, WindowHeight: 24}) {
		t.Fatalf("the zoomed pane's box under the zoom: %+v", g)
	}
	if r := rows[2]; r.WindowName != "a|b" || r.Geometry != nil || r.Zoomed {
		t.Fatalf("window name with the separator, no geometry: %+v", r)
	}
	if g := rows[3].Geometry; g == nil || g.Width != 80 || g.WindowWidth != 80 {
		t.Fatalf("no layout, from pane_*: %+v", g)
	}
	if w := PanesOfWindow(rows, 1); len(w) != 2 || w[0].Index != 0 || w[1].Index != 1 {
		t.Fatalf("one window: %+v", w)
	}
	rows = rows[:3]
	PaneAgents(rows, func(id string) string {
		if id == "%9" {
			return "claude"
		}
		return ""
	}, func(id string) string {
		if id == "%9" {
			return "working"
		}
		return ""
	})
	if rows[2].Agent != "claude" || rows[2].State != "working" || rows[0].Agent != "" {
		t.Fatalf("%+v", rows)
	}
}

func TestPickLocation(t *testing.T) {
	out := "%3|2|1|work__abc|code\n%3|2|1|work|code\n%4|0|0|other|x\n"
	loc, ok := pickLocation(out, "%3", (&Tmux{id: "tmux"}).paneOut)
	if !ok || loc.Session != "work" || loc.Window != 2 || loc.Pane != 1 || loc.WindowName != "code" {
		t.Fatalf("%+v %v", loc, ok)
	}
	// Only a view has it: its base session holds the same window.
	loc, ok = pickLocation("%5|1|0|notes__xyz|n\n", "%5", (&Tmux{id: "tmux"}).paneOut)
	if !ok || loc.Session != "notes" {
		t.Fatalf("%+v", loc)
	}
	if _, ok := pickLocation(out, "%8", (&Tmux{id: "tmux"}).paneOut); ok {
		t.Fatal("found a pane that is not there")
	}
	if IsPaneID("%3;rm") || !IsPaneID("%12") {
		t.Fatal("pane id check")
	}
}

func TestParseGeometry(t *testing.T) {
	for _, f := range [][]string{
		{"0", "0", "80", "24", "80", "24"},
		{"81", "12", "79", "12", "160", "24"},
	} {
		if parseGeometry(f) == nil {
			t.Errorf("%v: no geometry", f)
		}
	}
	for _, f := range [][]string{
		{"", "0", "80", "24", "80", "24"},
		{"0", "0", "0", "24", "80", "24"},
		{"10", "0", "80", "24", "80", "24"}, // past the window's edge
		{"0", "-1", "80", "24", "80", "24"},
		{"0", "0", "80", "x", "80", "24"},
	} {
		if g := parseGeometry(f); g != nil {
			t.Errorf("%v: %+v", f, g)
		}
	}
}

func TestLayoutGeometry(t *testing.T) {
	layout := "b25d,161x40,0,0{80x40,0,0,1,80x40,81,0[80x19,81,0,2,80x20,81,20,3]}"
	for id, want := range map[string]PaneGeometry{
		"%1": {0, 0, 80, 40, 161, 40},
		"%2": {81, 0, 80, 19, 161, 40},
		"%3": {81, 20, 80, 20, 161, 40},
	} {
		if g := layoutGeometry(layout, id); g == nil || *g != want {
			t.Errorf("%s: %+v, want %+v", id, g, want)
		}
	}
	// One pane, nested splits, and what is not a layout.
	if g := layoutGeometry("ab12,80x24,0,0,5", "%5"); g == nil || g.Width != 80 || g.WindowHeight != 24 {
		t.Errorf("one pane: %+v", g)
	}
	nested := "0000,100x30,0,0[100x15,0,0{50x15,0,0,4,49x15,51,0,6},100x14,0,16,9]"
	if g := layoutGeometry(nested, "%6"); g == nil || g.Left != 51 || g.Width != 49 || g.Height != 15 {
		t.Errorf("nested: %+v", g)
	}
	for _, bad := range []string{"", "x", "b25d,161x40,0,0{80x40,0,0,1", "b25d,161x40,0,0{80x40,0,0,1}junk", "b25d,161x40,0,0,1"} {
		if g := layoutGeometry(bad, "%3"); g != nil {
			t.Errorf("%q: %+v", bad, g)
		}
	}
	if g := layoutGeometry(layout, "3"); g != nil {
		t.Errorf("an id without %%: %+v", g)
	}
	// tmux 3.8 gives JSON (measured, 80x24, three panes, zoomed or not).
	v38 := `{"V":2,"L":{"t":"h","w":80,"h":24,"x":0,"y":0,"c":[{"t":"p","w":40,"h":24,"x":0,"y":0,"l":1,"i":0,"I":"%0"},{"t":"v","w":39,"h":24,"x":41,"y":0,"c":[{"t":"p","w":39,"h":12,"x":41,"y":0,"l":0,"i":1,"I":"%1"},{"t":"p","w":39,"h":11,"x":41,"y":13,"a":true,"i":2,"I":"%2"}]}]}}`
	for id, want := range map[string]PaneGeometry{
		"%0": {0, 0, 40, 24, 80, 24},
		"%2": {41, 13, 39, 11, 80, 24},
	} {
		if g := layoutGeometry(v38, id); g == nil || *g != want {
			t.Errorf("3.8 %s: %+v, want %+v", id, g, want)
		}
	}
}
