package hookclient

import (
	"strings"
	"testing"
)

func TestNotifyArg(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"--hv", "3"}, ""}, // hooks.json: input on stdin
		{nil, ""},                   // hooks.json before versions
		{[]string{`{"type":"x"}`}, `{"type":"x"}`}, // notify
		{[]string{"--hv", "3", `{"type":"x"}`}, `{"type":"x"}`},
	}
	for _, c := range cases {
		if got := notifyArg(c.in); got != c.want {
			t.Errorf("notifyArg(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A hooks.json command (`stui hook codex --hv 3`) reads its event from stdin;
// it used to take the "3" for Codex's notify JSON and drop every event.
func TestCodexHookWithVersionReadsStdin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("STUI_HOME", home)
	t.Setenv("TMUX_PANE", "%7")
	t.Setenv("STUI_HOOK_PROBE", "")
	Run([]string{"codex", "--hv", "3"}, strings.NewReader(`{"hook_event_name":"Stop","session_id":"s"}`))
	r, ok := ReadLast(home, "codex")
	if !ok || r.Event != "Stop" || r.Outcome != Spooled || r.Pane != "%7" {
		t.Fatalf("%+v %v", r, ok)
	}
}

func TestOutsideTmuxIsRecorded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("STUI_HOME", home)
	t.Setenv("TMUX_PANE", "")
	t.Setenv("STUI_HOOK_PROBE", "")
	Run([]string{"claude"}, strings.NewReader(`{"hook_event_name":"PreToolUse"}`))
	r, ok := ReadLast(home, "claude")
	if !ok || r.Outcome != OutsideTmux || r.Event != "PreToolUse" {
		t.Fatalf("%+v %v", r, ok)
	}
}

func TestNoteRunWritesAChangeAtOnce(t *testing.T) {
	home := t.TempDir()
	noteRun(home, "codex", LastRun{At: 1000, Outcome: Delivered, Pane: "%1", Event: "A"})
	noteRun(home, "codex", LastRun{At: 2000, Outcome: Delivered, Pane: "%1", Event: "B"})
	if r, _ := ReadLast(home, "codex"); r.Event != "A" {
		t.Fatalf("same outcome within 30s rewritten: %+v", r)
	}
	noteRun(home, "codex", LastRun{At: 3000, Outcome: OutsideTmux})
	if r, _ := ReadLast(home, "codex"); r.Outcome != OutsideTmux {
		t.Fatalf("a new outcome was not written: %+v", r)
	}
	noteRun(home, "../x", LastRun{At: 1, Outcome: Delivered})
	if _, ok := ReadLast(home, "../x"); ok {
		t.Fatal("a kind with a path in it was written")
	}
}
