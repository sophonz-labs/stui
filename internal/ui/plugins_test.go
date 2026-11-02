package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pluginApp is an app on one session with one agent pane, and the plugins
// in manifests loaded from a directory of its own. tmux is not asked for
// the pane's directory: it is that directory.
func pluginApp(t *testing.T, manifests ...string) *app {
	t.Helper()
	dir := t.TempDir()
	for _, m := range manifests {
		var named struct{ Name string }
		json.Unmarshal([]byte(m), &named)
		d := filepath.Join(dir, named.Name)
		os.MkdirAll(d, 0o755)
		if err := os.WriteFile(filepath.Join(d, "plugin.json"), []byte(m), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := paneCwd
	paneCwd = func(string) string { return dir }
	t.Cleanup(func() { paneCwd = old })
	s := &snap{
		sessions: []session{{Name: "work", Windows: 1}},
		windows:  map[string][]window{"work": {{Index: 0, ID: "@0", Name: "edit", Panes: 1, Active: true}}},
		panes:    map[string][]pane{"work": {{ID: "%0", WindowID: "@0", WindowName: "edit", Active: true, Command: "claude"}}},
		agents:   agents{Panes: []agentPane{{ID: "%0", Session: "work", State: "working"}}},
	}
	a := &app{snap: s, folded: map[string]bool{}, ver: "v0.1.0", w: 120, h: 30, stack: []*page{{v: sessionsView{}}},
		later: make(chan func(), 16), refresh: make(chan struct{}, 1)}
	a.loadPlugins([]string{dir})
	return a
}

// settle runs what the background work hands back, until it is quiet.
func settle(t *testing.T, a *app) {
	t.Helper()
	wait := 5 * time.Second // the first answer may take a while; then quiet means done
	for {
		select {
		case f := <-a.later:
			f()
			wait = 300 * time.Millisecond
		case <-time.After(wait):
			return
		}
	}
}

func TestPluginViewAndKeys(t *testing.T) {
	a := pluginApp(t, `{"v":1,"name":"pa","views":[{"name":"things","title":"Things","run":["printf","name\tsize\nalpha\t1\nbeta\t2\n"],"format":"tsv","cwd":"pane"}],
	  "actions":[{"key":"x","title":"echo","scopes":["things"],"run":["echo","got {name} in {session}"]},
	             {"key":"t","title":"open things","scopes":["panes"],"open":"things"},
	             {"key":"c","title":"steals CI","scopes":["panes"],"run":["false"]}]}`,
		`{"v":1,"name":"pb","views":[{"name":"broken","run":["sh","-c","echo nope >&2; exit 3"],"format":"lines"}],
		  "commands":[{"name":"usage","run":["true"]}]}`)
	a.runCommand("things")
	settle(t, a)
	v, ok := a.top().v.(*pluginView)
	if !ok || v.err != "" || len(v.data) != 2 || v.data[1].cells[0] != "beta" || v.heads[1] != "SIZE" {
		t.Fatalf("view: %+v", v)
	}
	a.normalKey("down")
	a.normalKey("x")
	settle(t, a)
	if a.flash != "echo: got beta in work" || a.bad {
		t.Errorf("action: %q", a.flash)
	}
	out := plain(strings.Join(a.render(), "\n"))
	for _, want := range []string{"Things(", "<x> echo", "alpha"} {
		if !strings.Contains(out, want) {
			t.Errorf("render has no %q:\n%s", want, out)
		}
	}
	// stui's own keys and commands win.
	if a.ps().Actions["panes"]["c"] != nil || a.ps().Commands["usage"] != nil {
		t.Error("a plugin took c on panes or :usage")
	}
	// The agents view's rows are panes: a panes key works there, with the pane.
	a.setRoot(agentsView{})
	a.normalKey("t")
	settle(t, a)
	if v, ok := a.top().v.(*pluginView); !ok || v.ctx["pane"] != "%0" || v.ctx["session"] != "work" || v.ctx["pane_command"] != "claude" {
		t.Errorf("open from agents: %T %+v", a.top().v, a.top().v)
	}
	a.runCommand("broken")
	settle(t, a)
	v = a.top().v.(*pluginView)
	if !strings.Contains(v.err, "nope") || v.header()[0] != "ERROR" {
		t.Errorf("error view: %q", v.err)
	}
	a.runCommand("ext")
	rows := a.top().v.rows(a)
	if len(rows) != 2 || !strings.Contains(rows[0].cells[5], "key c on panes is built in") || !strings.Contains(rows[1].cells[5], ":usage is built in") {
		t.Errorf(":ext rows: %+v", rows)
	}
}

func TestPluginCommandArgs(t *testing.T) {
	a := pluginApp(t, `{"v":1,"name":"pa","commands":[{"name":"say","run":["printf","%s|","{args}"]}]}`)
	a.runCommand(`say "two words" three`)
	settle(t, a)
	if a.flash != ":say: two words|three|" {
		t.Errorf("flash %q", a.flash)
	}
	if got := complete("sa", a.pluginNames()...); got != "say " {
		t.Errorf("complete: %q", got)
	}
}

// The six plugins of tui-plugins, as stui sees them: no key of theirs meets
// one of stui's. Copies of their keys and names, so this needs no network.
func TestTuiPluginsFit(t *testing.T) {
	a := pluginApp(t,
		`{"v":1,"name":"gh-prs","views":[{"name":"prs","run":["true"],"format":"lines"}],"actions":[{"key":"P","title":"t","scopes":["panes","windows"],"open":"prs"},{"key":"o","title":"t","scopes":["prs"],"run":["true"]},{"key":"d","title":"t","scopes":["prs"],"run":["true"]},{"key":"c","title":"t","scopes":["prs"],"run":["true"]},{"key":"C","title":"t","scopes":["prs"],"run":["true"]}]}`,
		`{"v":1,"name":"gh-runs","views":[{"name":"gh-runs","run":["true"],"format":"lines"}],"actions":[{"key":"A","title":"t","scopes":["panes","windows"],"open":"gh-runs"},{"key":"w","title":"t","scopes":["gh-runs"],"run":["true"]},{"key":"o","title":"t","scopes":["gh-runs"],"run":["true"]},{"key":"L","title":"t","scopes":["gh-runs"],"run":["true"]},{"key":"r","title":"t","scopes":["gh-runs"],"run":["true"]}]}`,
		`{"v":1,"name":"docker-ps","views":[{"name":"containers","run":["true"],"format":"lines"}],"actions":[{"key":"L","title":"t","scopes":["containers"],"run":["true"]},{"key":"e","title":"t","scopes":["containers"],"run":["true"]},{"key":"i","title":"t","scopes":["containers"],"run":["true"]},{"key":"S","title":"t","scopes":["containers"],"run":["true"]}]}`,
		`{"v":1,"name":"kubectl-pods","views":[{"name":"pods","run":["true"],"format":"lines"}],"actions":[{"key":"L","title":"t","scopes":["pods"],"run":["true"]},{"key":"d","title":"t","scopes":["pods"],"run":["true"]},{"key":"e","title":"t","scopes":["pods"],"run":["true"]},{"key":"D","title":"t","scopes":["pods"],"run":["true"]}]}`,
		`{"v":1,"name":"git-status","views":[{"name":"git-status","run":["true"],"format":"lines"}],"actions":[{"key":"S","title":"t","scopes":["panes","windows"],"open":"git-status"},{"key":"d","title":"t","scopes":["git-status"],"run":["true"]},{"key":"a","title":"t","scopes":["git-status"],"run":["true"]},{"key":"u","title":"t","scopes":["git-status"],"run":["true"]}]}`,
		`{"v":1,"name":"lazygit","actions":[{"key":"L","title":"t","scopes":["panes","windows","sessions"],"run":["true"]}],"commands":[{"name":"lazygit","run":["true"]}]}`)
	for _, p := range a.plugins {
		if p.Err != nil || len(p.Problems) > 0 {
			t.Errorf("%s: %v %q", p.Name, p.Err, p.Problems)
		}
	}
}
