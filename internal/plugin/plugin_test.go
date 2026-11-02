package plugin

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const good = `{
  "v": 1, "name": "gh-prs", "requires": ["gh"],
  "views": [{"name": "prs", "run": ["gh","pr","list","--json","number,title,author"], "format": "json",
             "columns": [{"title": "#", "field": "number"}, {"field": "title"}, {"field": "author.login"}], "refresh": "30s", "cwd": "pane"}],
  "actions": [{"key": "o", "title": "open", "scopes": ["prs"], "run": ["gh","pr","view","{number}","--web"]},
              {"key": "S", "title": "git status", "scopes": ["panes"], "run": ["git","-C","{pane_cwd}","status"], "mode": "popup", "wait": true}],
  "commands": [{"name": "pr", "run": ["gh","pr","{args}"], "mode": "foreground"}]
}`

func TestParseGood(t *testing.T) {
	m, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if m.Views[0].RefreshEvery().String() != "30s" || m.Views[0].TimeoutAfter().String() != "10s" {
		t.Errorf("durations: %v %v", m.Views[0].RefreshEvery(), m.Views[0].TimeoutAfter())
	}
	if m.Views[0].Columns[1].Heading() != "TITLE" {
		t.Errorf("heading: %q", m.Views[0].Columns[1].Heading())
	}
}

func TestParseBad(t *testing.T) {
	for _, c := range []struct{ json, want string }{
		{`{"v": 2, "name": "x"}`, "needs a newer version"},
		{`{"name": "x", "commands": [{"name": "a", "run": ["a"]}]}`, `"v" must be 1`},
		{`{"v": 1, "name": "X y", "commands": [{"name": "a", "run": ["a"]}]}`, "name"},
		{`{"v": 1, "name": "x"}`, "no views, actions or commands"},
		{`{"v": 1, "name": "x", "commands": [{"name": "a", "run": ["a"], "confrim": true}]}`, "unknown field"},
		{`{"v": 1, "name": "x", "views": [{"name": "a", "run": ["a"], "format": "xml"}]}`, "format"},
		{`{"v": 1, "name": "x", "views": [{"name": "a", "run": ["a"], "format": "json"}]}`, "needs columns"},
		{`{"v": 1, "name": "x", "views": [{"name": "a", "run": [], "format": "lines"}]}`, "needs a program"},
		{`{"v": 1, "name": "x", "views": [{"name": "a", "run": ["a"], "format": "lines", "refresh": "10ms"}]}`, "refresh"},
		{`{"v": 1, "name": "x", "views": [{"name": "a", "run": ["a"], "format": "lines", "cwd": "/tmp"}]}`, "cwd"},
		{`{"v": 1, "name": "x", "actions": [{"key": "ab", "title": "t", "scopes": ["panes"], "run": ["a"]}]}`, "key"},
		{`{"v": 1, "name": "x", "actions": [{"key": "a", "title": "t", "scopes": [], "run": ["a"]}]}`, "no scopes"},
		{`{"v": 1, "name": "x", "actions": [{"key": "l", "title": "t", "scopes": ["panes"], "run": ["a"]}]}`, "reserved"},
		{`{"v": 1, "name": "x", "actions": [{"key": "y", "title": "t", "scopes": ["panes"], "run": ["a"]}]}`, "reserved"},
		{`{"v": 1, "name": "x", "actions": [{"key": "a", "title": "t", "scopes": ["panes"], "run": ["a"], "mode": "x"}]}`, "mode"},
		{`{"v": 1, "name": "x", "actions": [{"key": "a", "title": "t", "scopes": ["panes"], "run": ["a", "{number}"]}]}`, "tmux's views have only"},
		{`{"v": 1, "name": "x", "actions": [{"key": "a", "title": "t", "scopes": ["panes"], "run": ["a"], "open": "v"}]}`, "not both"},
		{`{"v": 1, "name": "x", "views": [{"name": "a", "run": ["a"], "format": "lines"}], "commands": [{"name": "a", "run": ["a"]}]}`, "taken"},
	} {
		_, err := Parse([]byte(c.json))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", c.json, err, c.want)
		}
	}
}

func TestValidKey(t *testing.T) {
	for k, want := range map[string]bool{"o": true, "가": true, "ctrl-x": true, "O": true, " ": false, "": false, "ab": false, "ctrl-1": false, "\x7f": false} {
		if ValidKey(k) != want {
			t.Errorf("ValidKey(%q) = %v", k, !want)
		}
	}
}

func TestExpand(t *testing.T) {
	vars := map[string]string{"number": "12", "title": "x; rm -rf ~ $(id)", "pane_cwd": "/a b"}
	got, err := Expand([]string{"gh", "pr", "view", "{number}", "--title={title}", "{pane_cwd}"}, vars, nil)
	want := []string{"gh", "pr", "view", "12", "--title=x; rm -rf ~ $(id)", "/a b"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q %v, want %q", got, err, want)
	}
	// {args} alone is one element per word; inside a string it is joined.
	got, _ = Expand([]string{"deploy", "{args}", "--note=[{args}]"}, nil, []string{"a b", "c"})
	if want := []string{"deploy", "a b", "c", "--note=[a b c]"}; !reflect.DeepEqual(got, want) {
		t.Errorf("args: %q", got)
	}
	got, _ = Expand([]string{"x", "{args}"}, nil, nil)
	if want := []string{"x"}; !reflect.DeepEqual(got, want) {
		t.Errorf("no args: %q", got)
	}
	// Braces that are not placeholders stay; {{ is a literal {.
	got, _ = Expand([]string{"jq", ".[] | {a: .b}", "{{number}"}, vars, nil)
	if want := []string{"jq", ".[] | {a: .b}", "{number}"}; !reflect.DeepEqual(got, want) {
		t.Errorf("literal braces: %q", got)
	}
	if _, err := Expand([]string{"x", "{nope}"}, vars, nil); err == nil {
		t.Error("an unknown placeholder must not run")
	}
	// A value that looks like a placeholder is not expanded again.
	got, _ = Expand([]string{"{a}"}, map[string]string{"a": "{b}", "b": "no"}, nil)
	if got[0] != "{b}" {
		t.Errorf("expanded twice: %q", got)
	}
}

func TestParseOutput(t *testing.T) {
	recs, _, err := ParseOutput("json", []byte(`[{"number": 1234, "title": "fix", "author": {"login": "me"}, "labels": ["a","b"], "draft": false, "x": null}]`))
	if err != nil || len(recs) != 1 {
		t.Fatal(recs, err)
	}
	r := recs[0]
	if r["number"] != "1234" || r["author.login"] != "me" || r["labels"] != "a, b" || r["draft"] != "false" || r["x"] != "" {
		t.Errorf("json: %v", r)
	}
	recs, _, err = ParseOutput("json", []byte("{\"ID\":\"a1\"}\n{\"ID\":\"b2\"}\n"))
	if err != nil || len(recs) != 2 || recs[1]["ID"] != "b2" {
		t.Errorf("json lines: %v %v", recs, err)
	}
	if _, _, err := ParseOutput("json", []byte(`[1,2]`)); err == nil {
		t.Error("json of numbers")
	}
	if _, _, err := ParseOutput("json", []byte(`not json`)); err == nil {
		t.Error("not json")
	}
	recs, fields, _ := ParseOutput("tsv", []byte("name\tstatus\nweb\tup\ndb\n\n"))
	if !reflect.DeepEqual(fields, []string{"name", "status"}) || len(recs) != 2 || recs[1]["status"] != "" || recs[0]["status"] != "up" {
		t.Errorf("tsv: %v %v", fields, recs)
	}
	recs, _, _ = ParseOutput("lines", []byte(" M a.go\r\n?? b\n"))
	if len(recs) != 2 || recs[0]["line"] != " M a.go" {
		t.Errorf("lines: %v", recs)
	}
}

func TestCapped(t *testing.T) {
	c := &Capped{N: 4}
	c.Write([]byte("abc"))
	c.Write([]byte("def"))
	if string(c.Bytes()) != "abcd" || !c.Cut {
		t.Errorf("%q %v", c.Bytes(), c.Cut)
	}
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, name), 0o755)
	if err := os.WriteFile(filepath.Join(dir, name, "plugin.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAndResolve(t *testing.T) {
	own, shared := t.TempDir(), t.TempDir()
	write(t, own, "a", `{"v":1,"name":"a","views":[{"name":"things","run":["echo"],"format":"lines"}],
	  "actions":[{"key":"o","title":"o","scopes":["things","panes"],"run":["x"]},
	             {"key":"s","title":"split?","scopes":["panes"],"run":["x"]},
	             {"key":"o","title":"again","scopes":["all"],"run":["x"]}],
	  "commands":[{"name":"tmux","run":["x"]},{"name":"go","run":["x"]}]}`)
	write(t, own, "b", `{"v":1,"name":"b","views":[{"name":"things","run":["echo"],"format":"lines"}],
	  "actions":[{"key":"o","title":"o","scopes":["panes"],"run":["x"]},{"key":"x","title":"x","scopes":["all"],"run":["x"]},
	             {"key":"w","title":"w","scopes":["nothere"],"run":["x"]}]}`)
	write(t, own, "broken", `{"v":1,`)
	write(t, shared, "a", `{"v":1,"name":"a","commands":[{"name":"other","run":["x"]}]}`)
	ps := Load([]string{own, shared})
	if len(ps) != 4 {
		t.Fatalf("%d plugins", len(ps))
	}
	b := Builtins{Commands: []string{"tmux", "q"}, Scopes: TmuxScopes, Keys: map[string][]string{"": {"j", "k"}, "panes": {"s", "v"}}}
	s := Resolve(ps, b)
	if s.Views["things"] == nil || s.Views["things"].P.Name != "a" {
		t.Error("first plugin keeps the view")
	}
	if s.Commands["tmux"] != nil || s.Commands["go"] == nil || s.Commands["other"] != nil {
		t.Errorf("commands: %v", s.Commands)
	}
	if s.Actions["panes"]["s"] != nil || s.Actions["panes"]["o"].P.Name != "a" || s.Actions["sessions"]["o"].P.Name != "a" {
		t.Error("built-in keys win, then the first plugin")
	}
	if s.Actions["things"]["x"] == nil || s.Actions["sessions"]["x"] == nil {
		t.Error("all reaches tmux's views and plugin views")
	}
	problems := func(name string, shared bool) string {
		for _, p := range ps {
			if p.Name == name && p.Shared == shared {
				return strings.Join(p.Problems, "\n")
			}
		}
		return ""
	}
	for _, want := range []string{"command :tmux is built in", "key s on panes is built in", "key o on panes is taken by plugin a"} {
		if !strings.Contains(problems("a", false), want) {
			t.Errorf("a: no %q in %q", want, problems("a", false))
		}
	}
	for _, want := range []string{"view :things is taken by plugin a", "key o on panes is taken by plugin a", `no view "nothere"`} {
		if !strings.Contains(problems("b", false), want) {
			t.Errorf("b: no %q in %q", want, problems("b", false))
		}
	}
	if !strings.Contains(problems("a", true), "shadowed by") {
		t.Error("the shared copy of a is shadowed")
	}
	for _, p := range ps {
		if p.Name == "broken" && p.Err == nil {
			t.Error("broken loaded")
		}
	}
}

func TestInstallRemove(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	src := filepath.Join(t.TempDir(), "gh-prs")
	write(t, filepath.Dir(src), "gh-prs", good)
	var out strings.Builder
	if code := Main("tmui", Builtins{}, []string{"install", src}, strings.NewReader("n\n"), &out); code != 1 {
		t.Fatalf("declined install: %d %s", code, out.String())
	}
	if !strings.Contains(out.String(), "gh pr view {number} --web") || !strings.Contains(out.String(), "git -C {pane_cwd} status") {
		t.Errorf("install must print every command:\n%s", out.String())
	}
	dest := filepath.Join(Dirs("tmui")[0], "gh-prs", "plugin.json")
	if exists(dest) {
		t.Fatal("written without a yes")
	}
	out.Reset()
	if code := Main("tmui", Builtins{}, []string{"install", src}, strings.NewReader("y\n"), &out); code != 0 || !exists(dest) {
		t.Fatalf("install: %d %s", code, out.String())
	}
	if code := Main("tmui", Builtins{}, []string{"remove", "../x"}, nil, &out); code != 1 {
		t.Error("remove took a path")
	}
	if code := Main("tmui", Builtins{}, []string{"remove", "gh-prs"}, nil, &out); code != 0 || exists(dest) {
		t.Errorf("remove: %d %s", code, out.String())
	}
}
