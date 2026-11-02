package ui

import (
	"strings"
	"testing"
)

// The tests never touch the real clipboard.
func init() { deliver = func(*app, string) {} }

func TestOSC52(t *testing.T) {
	if got := osc52("hi\n"); got != "\x1b]52;c;aGkK\x07" {
		t.Fatalf("osc52: %q", got)
	}
}

func TestCopyOutput(t *testing.T) {
	a := testApp(80, 24, sessionsView{})
	pg := &pager{lines: []string{"first   ", "two  ", "three", "four\t"}}
	a.pushPager(pg)
	a.normalKey("y") // no selection: every line, not only the ones on screen
	if want := "first\ntwo\nthree\nfour"; a.copied != want {
		t.Fatalf("y copied %q, want %q", a.copied, want)
	}
	a.normalKey("v") // the bottom line on screen: four
	a.normalKey("k")
	a.normalKey("k")
	if !pg.selecting || pg.anchor != 3 || pg.cur != 1 {
		t.Fatalf("selection %v %d..%d", pg.selecting, pg.anchor, pg.cur)
	}
	if r := a.render(); !strings.Contains(strings.Join(r, "\n"), selected+fit("two", 78)) {
		t.Errorf("the selection is not drawn")
	}
	a.normalKey("y")
	if a.copied != "two\nthree\nfour" || pg.selecting {
		t.Fatalf("v k k y copied %q (still selecting %v)", a.copied, pg.selecting)
	}
	if a.flash != "copied 3 lines" {
		t.Errorf("said %q", a.flash)
	}
	a.normalKey("v")
	a.normalKey("esc") // ends the selection, stays in the output
	if pg.selecting || a.top().pg != pg {
		t.Fatalf("esc: selecting %v, top %+v", pg.selecting, a.top())
	}
}

func TestCopyRow(t *testing.T) {
	a := testApp(80, 24, windowsView{session: "default"})
	a.normalKey("y")
	if a.copied != "default:0" {
		t.Fatalf("y on a window: %q", a.copied)
	}
	a.normalKey("Y")
	if !strings.HasPrefix(a.copied, "0\twork *\t1\t") {
		t.Fatalf("Y on a window: %q", a.copied)
	}
	a = testApp(80, 24, agentsView{})
	a.normalKey("y")
	if a.copied != "%0" {
		t.Fatalf("y on an agent: %q", a.copied)
	}
	a = testApp(80, 24, orchView{})
	a.normalKey("y")
	if a.copied != "r-1" {
		t.Fatalf("y on a run: %q", a.copied)
	}
	a = testApp(80, 24, ciView{})
	a.normalKey("y")
	if a.copied != "https://github.com/acme/web/actions/runs/1" {
		t.Fatalf("y on a CI run: %q", a.copied)
	}
	if got := a.copyValue(a.top(), &row{id: "7", vars: map[string]string{"url": "https://example.com/pull/7"}}); got != "https://example.com/pull/7" {
		t.Fatalf("a plugin row with a url: %q", got)
	}
}

func testApp(w, h int, v view) *app {
	s := &snap{
		sessions: []session{{Name: "default", Windows: 1}},
		windows:  map[string][]window{"default": {{Index: 0, ID: "@0", Name: "work", Panes: 1, Active: true}}},
		panes:    map[string][]pane{"default": {{ID: "%0", WindowID: "@0", WindowName: "work"}}},
		agents:   agents{Panes: []agentPane{{ID: "%0", Session: "default", State: "working", Orch: &orchInfo{Run: "r-1", Role: "coordinator"}}}},
		ci: map[string]*ciInfo{"%0": {State: "ok", Repo: "acme/web", Root: "/src/web", Branch: "main", Runs: []ciRun{
			{Workflow: "ci", Branch: "main", Status: "completed", Conclusion: "success", URL: "https://github.com/acme/web/actions/runs/1"}}}},
	}
	return &app{snap: s, folded: map[string]bool{}, w: w, h: h, stack: []*page{{v: v}}, later: make(chan func(), 16), refresh: make(chan struct{}, 1)}
}

// z is the output alone: no frame, each line ended by erase-to-end, not
// padding, so a mouse selection is the text.
func TestZoom(t *testing.T) {
	a := testApp(40, 6, sessionsView{})
	pg := &pager{lines: []string{"a", "b  "}}
	a.pushPager(pg)
	a.normalKey("z")
	r := a.render()
	if len(r) != 6 || r[0] != "a\x1b[K" || r[1] != "b\x1b[K" || r[2] != "\x1b[K" {
		t.Fatalf("zoomed: %q", r)
	}
	a.normalKey("esc")
	if a.zoom || a.top().pg != pg {
		t.Fatalf("esc leaves the zoom first")
	}
}
