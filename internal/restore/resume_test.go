package restore

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Which line a pane gets, and whether stui enters it: an agent stui can name
// the conversation of, kept here, yes; anything else no.
func TestDecide(t *testing.T) {
	on := Options{ResumeAgents: true, HasRecord: func(a *Agent) bool { return a.ID == "kept" }}
	for _, c := range []struct {
		name string
		p    Pane
		o    Options
		want Decision
	}{
		{"agent, conversation on record", Pane{Agent: &Agent{Kind: "claude", ID: "kept"}}, on,
			Decision{Action: Run, Line: "claude --resume kept"}},
		{"codex, on record", Pane{Agent: &Agent{Kind: "codex", ID: "kept"}}, on,
			Decision{Action: Run, Line: "codex resume kept"}},
		{"agent, no record", Pane{Agent: &Agent{Kind: "claude", ID: "gone"}}, on,
			Decision{Action: TypeOnly, Line: "claude --resume gone", Why: "no record of conversation gone on this machine", NoRecord: true}},
		{"agent, no id: not one conversation", Pane{Agent: &Agent{Kind: "claude"}}, on,
			Decision{Action: TypeOnly, Line: "claude --continue", Why: "no conversation id in the snapshot"}},
		{"resuming switched off", Pane{Agent: &Agent{Kind: "claude", ID: "kept"}}, Options{HasRecord: on.HasRecord},
			Decision{Action: TypeOnly, Line: "claude --resume kept", Why: "resuming agents is off"}},
		{"an agent stui has no line for", Pane{Agent: &Agent{Kind: "gemini", ID: "x"}}, on, Decision{}},
		{"a command: never run", Pane{Command: "sleep", Args: "sleep 1000"}, on, Decision{Action: Command}},
		{"a deploy, even with an agent's look", Pane{Command: "node", Args: "node deploy.js --prod"}, on, Decision{Action: Command}},
		{"a shell", Pane{Command: "zsh"}, on, Decision{}},
		{"its first options kept, its old resume and prompt not",
			Pane{Agent: &Agent{Kind: "claude", ID: "kept"}, Args: "/Users/me/.local/bin/claude --model opus --dangerously-skip-permissions --resume old-id --teleport fix it"}, on,
			Decision{Action: Run, Line: "claude --resume kept --model 'opus' --dangerously-skip-permissions", Left: []string{"--teleport"}}},
	} {
		if got := Decide(c.p, c.o); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

// A conversation is on record where its CLI keeps it, and only a file with
// something in it counts.
func TestHasRecord(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cc := filepath.Join(t.TempDir(), "claude")
	t.Setenv("CLAUDE_CONFIG_DIR", cc)
	cx := filepath.Join(t.TempDir(), "codex")
	t.Setenv("CODEX_HOME", cx)
	put := func(p, body string) {
		os.MkdirAll(filepath.Dir(p), 0o700)
		os.WriteFile(p, []byte(body), 0o600)
	}
	put(filepath.Join(cc, "projects", "-Users-me-work", "be3612de-d629.jsonl"), "{}\n")
	put(filepath.Join(home, ".claude", "projects", "-x", "default-dir.jsonl"), "{}\n")
	put(filepath.Join(cc, "projects", "-y", "empty.jsonl"), "")
	put(filepath.Join(cx, "sessions", "2026", "10", "20", "rollout-2026-10-20T09-00-00-0199aa.jsonl"), "{}\n")
	for a, want := range map[Agent]bool{
		{Kind: "claude", ID: "be3612de-d629"}: true,
		{Kind: "claude", ID: "default-dir"}:   true,
		{Kind: "claude", ID: "empty"}:         false,
		{Kind: "claude", ID: "nowhere"}:       false,
		{Kind: "claude", ID: "../../etc"}:     false,
		{Kind: "claude", ID: ""}:              false,
		{Kind: "codex", ID: "0199aa"}:         true,
		{Kind: "codex", ID: "0199bb"}:         false,
		{Kind: "gemini", ID: "be3612de-d629"}: false,
	} {
		if got := HasRecord(&a); got != want {
			t.Errorf("%+v: %v, want %v", a, got, want)
		}
	}
}

// Off by restore.json: every line typed, none entered.
func TestConfigResume(t *testing.T) {
	home := t.TempDir()
	if !LoadConfig(home).ResumeOn() {
		t.Fatal("resuming is off with no restore.json")
	}
	os.WriteFile(filepath.Join(home, "restore.json"), []byte(`{"resumeAgents": false}`), 0o600)
	if LoadConfig(home).ResumeOn() {
		t.Fatal(`"resumeAgents": false did not turn it off`)
	}
	os.WriteFile(filepath.Join(home, "restore.json"), []byte(`{"resumeAgents": true}`), 0o600)
	if !LoadConfig(home).ResumeOn() {
		t.Fatal("on")
	}
}
