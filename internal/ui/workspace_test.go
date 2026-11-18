package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sophonz-labs/stui/internal/vault"
)

// The client goes to stui's server by its socket, at a window when the
// frame asks for one.
func TestWorkspaceAttach(t *testing.T) {
	a := &app{snap: &snap{}}
	a.snap.version.MuxSocket = "/tmp/t/default"
	h := &wsHost{a: a}
	if got := strings.Join(h.Attach("main", nil), " "); got != "tmux -S /tmp/t/default attach-session -t =main" {
		t.Errorf("session: %s", got)
	}
	if got := strings.Join(h.Attach("main", []string{"--window", "2"}), " "); !strings.HasSuffix(got, "-t =main:2") {
		t.Errorf("window: %s", got)
	}
	// Without st: this machine only, no rows of the host's own.
	if h.Rows(nil) != nil {
		t.Error("rows")
	}
	// Another machine's target goes through st.
	got := h.Attach("m2/work", []string{"--window", "1"})
	if !strings.HasSuffix(got[0], "st") || strings.Join(got[1:], " ") != "attach m2/work --window 1" {
		t.Errorf("remote: %q", got)
	}
}

func TestOtherMachines(t *testing.T) {
	ms := []machine{
		{Slug: "m1", Name: "here", Online: true, Sessions: []remoteSession{{Name: "a"}}},
		{Slug: "m2", Name: "box", Online: true, Sessions: []remoteSession{{Name: "work", WindowList: []remoteWindow{{Name: "main", Index: 1}}}}},
		{Slug: "m3", Name: "old", Seen: 1},
	}
	var texts, targets []string
	for _, r := range otherMachines(&stAccount{slug: "m1", machines: ms}, nil) {
		texts = append(texts, r.Text)
		targets = append(targets, r.Target+" "+strings.Join(r.More, " "))
	}
	want := []string{"OTHER MACHINES", "▾ box", "  work", "    1 main", "  old  offline"}
	if len(texts) != len(want) {
		t.Fatalf("rows: %q", texts)
	}
	for i, w := range want {
		if !strings.HasPrefix(texts[i], w) {
			t.Errorf("row %d: %q, want %q", i, texts[i], w)
		}
	}
	if targets[2] != "m2/work " || targets[3] != "m2/work --window 1" || targets[4] != " " {
		t.Errorf("targets: %q", targets)
	}
	// st signed out (no machines), or no st: nothing.
	if rows := otherMachines(&stAccount{slug: "m1"}, nil); rows != nil {
		t.Errorf("signed out: %v", rows)
	}
	if rows := otherMachines(nil, nil); rows != nil {
		t.Errorf("no st: %v", rows)
	}
}

// st's agents are on st's server; the frame shows stui's own.
func TestWorkspaceSnapOwnAgents(t *testing.T) {
	s := &snap{}
	s.agents.Panes = []agentPane{{ID: "%1", Agent: "claude"}, {ID: "%2", Agent: "codex", st: true}}
	ps := wsSnap(s).Agents.Panes
	if len(ps) != 1 || ps[0].ID != "%1" {
		t.Errorf("panes %+v", ps)
	}
}

// ":" with a view goes back to the list; stui ui or nothing stays.
func TestWorkspaceCommand(t *testing.T) {
	a := &app{later: make(chan func(), 4)}
	h := &wsHost{a: a}
	if !h.Command("agents") || len(a.later) != 1 {
		t.Error("a view did not leave")
	}
	if h.Command("stui ui") || h.Command("  ") {
		t.Error("stui ui or nothing left")
	}
}

// The Documents tab: the vault by session ▸ window.
func TestWorkspaceDocs(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	src := filepath.Join(t.TempDir(), "plan.md")
	os.WriteFile(src, []byte("# Plan\n"), 0o644)
	vault.Keep(src, "main", "build")
	vault.Keep(src, "", "")
	h := &wsHost{a: &app{}}
	root, err := h.Docs()
	if err != nil || root.Count != 2 || len(root.Docs) != 1 || len(root.Children) != 1 {
		t.Fatalf("root %+v %v", root, err)
	}
	win := root.Children[0].Children[0]
	if root.Children[0].Name != "main" || win.Name != "build" || win.Docs[0].ID != "main/build/plan.md" || win.Docs[0].Title != "Plan" {
		t.Errorf("tree %+v", win)
	}
	d, err := h.Doc("main/build/plan.md")
	if err != nil || d.Content != "# Plan\n" || d.Folder != "main/build" {
		t.Errorf("doc %+v %v", d, err)
	}
	if _, err := h.Doc("../x.md"); err == nil {
		t.Error("left the vault")
	}
}

// The panel's tab icons are st's UI slugs: they are drawn, not lettered.
func TestTabIcons(t *testing.T) {
	for _, slug := range []string{"generic-shell", "generic-monitor", "generic-test", "generic-vcs", "generic-agent", "generic-decision", "generic-docs"} {
		if key, svg, _ := stuiIcon(slug); key != slug || len(svg) == 0 {
			t.Errorf("%s: %q, %d bytes", slug, key, len(svg))
		}
	}
	if key, _, _ := stuiIcon("claude"); key != "claude" {
		t.Errorf("claude: %q", key)
	}
}
