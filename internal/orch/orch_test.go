package orch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestName(t *testing.T) {
	for in, want := range map[string]string{"API tests": "api-tests", "  Fix: the parser!! ": "fix-the-parser", "가나다": "", "a-very-long-name-that-goes-on-and-on": "a-very-long-name-that-go"} {
		if got := Name(in); got != want {
			t.Errorf("Name(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFirstLine(t *testing.T) {
	if got := FirstLine("\n\n# Add tests for /api/graph\nmore"); got != "Add tests for /api/graph" {
		t.Fatal(got)
	}
}

func TestRunRecordAndTask(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	r := &Run{ID: NewID(), Objective: "x", Coordinator: "%1", Session: "s", Created: Now()}
	r.Workers = append(r.Workers, Worker{ID: "%2", Name: "w"})
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	back, err := Load(r.ID)
	if err != nil || back.Worker("%2") == nil || back.Objective != "x" {
		t.Fatalf("%v %+v", err, back)
	}
	if _, err := Load("../../etc"); err == nil {
		t.Fatal("a bad id must not load")
	}
	p, err := WriteTask(r.ID, "w", "Do it\n")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(b), "Do it") || !strings.Contains(string(b), "stui worker done --summary") {
		t.Fatalf("%s", b)
	}
}

func TestGuidesAndPlugin(t *testing.T) {
	for _, g := range []string{"orchestrate", "worker"} {
		if s, err := Guide(g); err != nil || !strings.Contains(s, "stui worker") {
			t.Fatalf("%s: %v", g, err)
		}
	}
	if _, err := Guide("../x"); err == nil {
		t.Fatal("unknown guide")
	}
	t.Setenv("STUI_HOME", t.TempDir())
	root, err := WritePlugin("v0.3.66")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(root, "stui/.claude-plugin/plugin.json"))
	if !strings.Contains(string(b), `"version": "0.3.66"`) {
		t.Fatalf("%s", b)
	}
	t.Setenv("CODEX_HOME", t.TempDir())
	d, err := WriteCodexSkills()
	if err != nil {
		t.Fatal(err)
	}
	s, _ := os.ReadFile(filepath.Join(d, "stui-orchestrate/SKILL.md"))
	if !strings.Contains(string(s), "name: stui-orchestrate") {
		t.Fatalf("%s", s)
	}
}
