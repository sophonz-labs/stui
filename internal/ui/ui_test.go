package ui

import (
	"reflect"
	"strings"
	"testing"
)

func TestSplitKeys(t *testing.T) {
	got := splitKeys([]byte("j\x1b[A\x1b[1;5B\x1b[5~\x1b[6;2~\r\x7f\x04가\x1b"))
	want := []string{"j", "up", "down", "pgup", "pgdn", "enter", "backspace", "ctrl-d", "가", "esc"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFit(t *testing.T) {
	for _, c := range []struct {
		in   string
		w    int
		want string
	}{
		{"abc", 5, "abc  "},
		{"abcdef", 4, "abc…"},
		{"가나다", 5, "가나…"}, // a wide letter never splits
		{"가나", 4, "가나"},
		{"a\nb", 3, "a b"},
		{"⚡x", 3, "⚡x"},
	} {
		if got := fit(c.in, c.w); got != c.want || Cells(got) != c.w {
			t.Errorf("fit(%q, %d) = %q (%d columns), want %q", c.in, c.w, got, Cells(got), c.want)
		}
	}
}

func TestWidths(t *testing.T) {
	rows := []row{{cells: []string{"default", "18", strings.Repeat("x", 100)}}}
	w := widths([]string{"NAME", "WINDOWS", "TITLE"}, rows, 40)
	if sum := w[0] + w[1] + w[2] + 2; sum != 40 {
		t.Fatalf("widths %v use %d of 40", w, sum)
	}
	if w[0] != 7 || w[1] != 7 {
		t.Errorf("the narrow columns gave way first: %v", w)
	}
}

func TestSplitWords(t *testing.T) {
	got := splitWords(`worker send %3 "two words" 'and more'`)
	want := []string{"worker", "send", "%3", "two words", "and more"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
}

func TestComplete(t *testing.T) {
	if got := complete("sess"); got != "session" {
		t.Errorf("sess: %q", got) // session and sessions
	}
	if got := complete("wor"); got != "worker" { // worker and workers
		t.Errorf("wor: %q", got)
	}
	if got := complete("zz"); got != "zz" {
		t.Errorf("zz: %q", got)
	}
}

func TestOrchTree(t *testing.T) {
	o := func(run, role, parent, name string) *orchInfo {
		return &orchInfo{Run: run, Role: role, Parent: parent, Name: name}
	}
	a := &app{folded: map[string]bool{}, snap: &snap{agents: agents{Panes: []agentPane{
		{ID: "%9", Session: "s", State: "working", Orch: o("r-1", "coordinator", "", "")},
		{ID: "%8", Session: "s", State: "idle", Orch: o("r-1", "worker", "%9", "mobile")},
		{ID: "%13", Session: "s", State: "idle", Orch: o("r-1", "worker", "%9", "desktop")},
		{ID: "%12", Session: "s", State: "working", Orch: o("r-1", "worker", "%13", "go")},
		{ID: "%1", Session: "s", State: "idle"},                                         // no run: not in the tree
		{ID: "%5", Session: "s", State: "idle", Orch: o("r-1", "worker", "%77", "far")}, // its parent is elsewhere
	}}}}
	names := func() []string {
		var out []string
		for _, r := range (orchView{}).rows(a) {
			out = append(out, r.cells[0])
		}
		return out
	}
	want := []string{
		"▾ r-1",
		" ├─ ▾ coordinator",
		" │  ├─ ● mobile",
		" │  └─ ▾ desktop",
		" │     └─ ● go",
		" └─ ● far",
	}
	if got := names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("tree:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// Folding desktop hides go; ← on go goes up to desktop.
	rows := (orchView{}).rows(a)
	a.stack = []*page{{v: orchView{}}}
	(orchView{}).key(a, "left", &rows[4])
	if a.top().sel != "%13" {
		t.Errorf("left on a leaf went to %q, want its parent %%13", a.top().sel)
	}
	(orchView{}).key(a, "left", &rows[3])
	if got := names(); len(got) != 5 || got[3] != " │  └─ ▸ desktop" {
		t.Errorf("after folding desktop:\n%s", strings.Join(got, "\n"))
	}
	(orchView{}).key(a, "left", &rows[0])
	if got := names(); len(got) != 1 || got[0] != "▸ r-1" {
		t.Errorf("after folding the run: %q", got)
	}
}

// No line may be wider than the terminal: one that is wraps, and the
// screen scrolls under everything drawn after it.
func TestRenderFits(t *testing.T) {
	s := &snap{
		sessions: []session{{Name: "default", Windows: 3, CurrentWindow: "한글 창 이름이 아주 길다 ⚡"}},
		windows:  map[string][]window{"default": {{Index: 0, ID: "@0", Name: "work", Panes: 1}}},
		panes:    map[string][]pane{"default": {{ID: "%0", WindowID: "@0", WindowName: "work"}}},
		agents: agents{Sessions: []agentSession{{Name: "default", Working: 1}}, Panes: []agentPane{
			{ID: "%0", Session: "default", State: "working", Title: strings.Repeat("긴 제목 ", 30), Orch: &orchInfo{Run: "r-1", Role: "coordinator"}}}},
		ci: map[string]*ciInfo{"%0": {State: "ok", Repo: "acme/web", Root: "/src/web", Branch: "main", Runs: []ciRun{
			{Workflow: "ci", Branch: "main", Status: "in_progress", URL: "https://github.com/acme/web/actions/runs/1", Title: strings.Repeat("긴 제목 ", 20)}}}},
	}
	s.usage.Agents = []usageAgent{{Agent: "claude", Plan: "Max", Windows: []usageWindow{{Kind: "weekly", UsedPercent: 40}}}}
	views := []view{sessionsView{}, windowsView{session: "default"}, panesView{session: "default"}, agentsView{}, orchView{}, ciView{}, ciView{pane: "%0"}, usageView{}}
	for _, size := range [][2]int{{40, 12}, {60, 20}, {80, 24}, {120, 30}, {200, 30}, {200, 50}} {
		for _, v := range views {
			a := &app{snap: s, folded: map[string]bool{}, shared: "default", ver: "v0.1.0", w: size[0], h: size[1], stack: []*page{{v: v}}, logoOn: true}
			lines := a.render()
			if len(lines) != size[1] {
				t.Errorf("%T at %v: %d lines", v, size, len(lines))
			}
			for i, l := range lines {
				if n := Cells(plain(l)); n > size[0] {
					t.Errorf("%T at %v: line %d is %d columns: %q", v, size, i, n, plain(l))
				}
			}
			// The logo: whole at the top right of a wide header from logoTall
			// rows, the word at the end of the top row down to 120 columns,
			// nothing at 80.
			whole := size[0] >= 200 && size[1] >= logoTall
			if got := a.headerRows() == 9; got != whole {
				t.Errorf("%T at %v: header grown %v", v, size, got)
			}
			for i, l := range logoRows {
				if i < len(lines) && strings.HasSuffix(plain(lines[i]), l) != whole {
					t.Errorf("%T at %v: logo row %d shown %v: %q", v, size, i, !whole, plain(lines[i]))
				}
			}
			if word := size[0] >= 120 && !whole; word != strings.HasSuffix(plain(lines[0]), " "+logoWord) {
				t.Errorf("%T at %v: one-row logo %v: %q", v, size, !word, plain(lines[0]))
			}
		}
	}
}

func TestParseVersion(t *testing.T) {
	for in, want := range map[string]int{"tmux 3.0a": 30, "tmux 3.2": 32, "tmux 3.3a": 33, "tmux 3.8": 38, "tmux next-3.6": 36, "tmux openbsd-7.6": 76, "tmux master": 99} {
		if got := parseVersion(in); got != want {
			t.Errorf("%q: %d, want %d", in, got, want)
		}
	}
}

func TestPlanAttach(t *testing.T) {
	sock := "/tmp/tmux-501/default,123,0"
	cases := []struct {
		name         string
		w            where
		session, tgt string
		kind, argv   string
		back         string
	}{
		{"outside", where{version: 38}, "web", "=web", "attach", "tmux attach-session -t =web", "C-a d comes back to stui"},
		{"inside, same server", where{tmuxEnv: sock, sameServer: true, ownSession: "main", version: 38}, "web", "=web", "popup",
			"tmux display-popup -E -w 100% -h 100% -B TMUX= exec tmux -S /tmp/tmux-501/default attach-session -t '=web'", "C-a d comes back to stui"},
		{"inside, own session", where{tmuxEnv: sock, sameServer: true, ownSession: "web", version: 38}, "web", "%1", "switch", "tmux switch-client -t %1", "C-a l (last window) comes back to stui"},
		{"inside, old tmux", where{tmuxEnv: sock, sameServer: true, ownSession: "main", version: 31}, "web", "=web", "switch", "tmux switch-client -t =web", "C-a L (last session) or C-a s comes back to stui"},
		{"inside, old tmux, other server", where{tmuxEnv: sock, version: 30}, "web", "=web", "attach", "tmux attach-session -t =web", "C-a C-a d comes back to stui"},
	}
	for _, c := range cases {
		p := planAttach(c.w, c.session, c.tgt, "C-a")
		if p.kind != c.kind || strings.Join(p.argv, " ") != c.argv {
			t.Errorf("%s: %s %q\nwant %s %q", c.name, p.kind, strings.Join(p.argv, " "), c.kind, c.argv)
		}
		if p.back != c.back {
			t.Errorf("%s: back %q, want %q", c.name, p.back, c.back)
		}
	}
}

func TestLogo(t *testing.T) {
	if got, _ := logoLines("v0.1.0", 33, 9); len(got) != 2 || strings.TrimSpace(plain(got[0])) != logoWord || strings.TrimSpace(plain(got[1])) != "v0.1.0" {
		t.Errorf("room for the word only: %q", got)
	}
	if got, _ := logoLines("v0.1.0", 3, 6); got != nil {
		t.Errorf("no room: %q", got)
	}
	if got, _ := logoLines("v0.1.0", -1, 6); got != nil {
		t.Errorf("no key column: %q", got)
	}
	got, w := logoLines("v0.1.0", 34, 9)
	if len(got) != 9 || w != 34 || plain(got[0]) != logoRows[0] || plain(got[7]) != logoRows[7] || !strings.HasSuffix(plain(got[8]), "v0.1.0") {
		t.Fatalf("whole: %q", got)
	}
	for _, l := range got {
		if Cells(plain(l)) != w {
			t.Errorf("%q is %d columns", plain(l), Cells(plain(l)))
		}
	}
	if got, _ := logoLines("v0.1.0", 34, 6); len(got) != 2 || strings.TrimSpace(plain(got[0])) != logoWord {
		t.Errorf("a short header: %q", got)
	}
}

func TestCIState(t *testing.T) {
	run := func(wf, branch, status, conclusion string) ciRun {
		return ciRun{Workflow: wf, Branch: branch, Status: status, Conclusion: conclusion}
	}
	c := &ciInfo{Branch: "main", Runs: []ciRun{
		run("ci", "main", "completed", "success"),
		run("ci", "main", "completed", "failure"),    // older: the newest speaks
		run("lint", "other", "completed", "failure"), // another branch
		run("release", "main", "in_progress", ""),
	}}
	if got := ciState(c.latest()); got != "running" {
		t.Errorf("state %q, want running", got)
	}
	if got := ciMark(c); got != "● running" {
		t.Errorf("mark %q", got)
	}
	c.Runs = append([]ciRun{run("release", "main", "completed", "failure")}, c.Runs...)
	if got := ciState(c.latest()); got != "failed" {
		t.Errorf("state %q, want failed", got)
	}
	if got := ciState([]ciRun{run("ci", "main", "completed", "success")}); got != "passed" {
		t.Errorf("state %q, want passed", got)
	}
	if got := ciMark(nil); got != "" {
		t.Errorf("nil: %q", got)
	}
}

func TestEmptyOrchSaysHowToStart(t *testing.T) {
	a := &app{snap: &snap{}, folded: map[string]bool{}, w: 120, h: 30, stack: []*page{{v: orchView{}}}}
	if got := strings.Join(a.render(), "\n"); !strings.Contains(plain(got), `stui orch start --objective "<goal>"`) {
		t.Errorf("empty orchestration view:\n%s", plain(got))
	}
	a.stack[0].filter = "x"
	if got := plain(strings.Join(a.render(), "\n")); !strings.Contains(got, "nothing to show") {
		t.Errorf("filtered to nothing:\n%s", got)
	}
}

func TestLimitNameNamesTheModelWeekly(t *testing.T) {
	for _, c := range []struct {
		w    usageWindow
		want string
	}{
		{usageWindow{Kind: "weekly", Minutes: 10080}, "weekly"},
		{usageWindow{Kind: "weekly", Label: "Sonnet", Minutes: 10080}, "weekly sonnet"},
		{usageWindow{Kind: "session", Minutes: 300}, "5 hours"},
	} {
		if got := limitName(c.w); got != c.want {
			t.Errorf("limitName(%+v) = %q, want %q", c.w, got, c.want)
		}
	}
}

func TestHeaderNamesTheModelWeekly(t *testing.T) {
	s := &snap{}
	s.usage.Agents = []usageAgent{{Agent: "claude", Windows: []usageWindow{
		{Kind: "weekly", Minutes: 10080, UsedPercent: 58},
		{Kind: "weekly", Label: "Sonnet", Minutes: 10080, UsedPercent: 41},
	}}}
	a := &app{snap: s, folded: map[string]bool{}, shared: "default", ver: "v0.1.0", w: 160, h: 30, stack: []*page{{v: sessionsView{}}}}
	head := plain(strings.Join(a.render()[:6], "\n"))
	if !strings.Contains(head, "claude weekly") || !strings.Contains(head, "claude weekly sonnet") {
		t.Errorf("header does not name both weekly limits:\n%s", head)
	}
}
