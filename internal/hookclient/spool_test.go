package hookclient

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func spoolLines(t *testing.T, path string) []Event {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatal(err)
		}
		out = append(out, ev)
	}
	return out
}

// With no agent to take it, a hook's event goes to its pane's spool; a tool
// hook's does not.
func TestRunSpoolsWithoutAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("STUI_HOME", home)
	t.Setenv("TMUX_PANE", "%7")
	t.Setenv("TMUX", "/tmp/tmux-501/default,123,0")
	Run([]string{"claude"}, strings.NewReader(`{"hook_event_name":"Stop","session_id":"s1"}`))
	Run([]string{"claude"}, strings.NewReader(`{"hook_event_name":"PreToolUse","session_id":"s1","tool_name":"Bash"}`))
	evs := spoolLines(t, SpoolFile(home, "%7"))
	if len(evs) != 1 || evs[0].Event != "Stop" || evs[0].Pane != "%7" || evs[0].Socket != "/tmp/tmux-501/default" || evs[0].At == 0 {
		t.Fatalf("%+v", evs)
	}
	if fi, err := os.Stat(SpoolFile(home, "%7")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", fi.Mode(), err)
	}
}

// A pane's spool stops growing at SpoolMax.
func TestSpoolStopsAtMax(t *testing.T) {
	home := t.TempDir()
	path := SpoolFile(home, "%1")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, make([]byte, SpoolMax), 0o600)
	Spool(home, Event{Kind: "claude", Pane: "%1", Event: "Stop", At: 1})
	if fi, _ := os.Stat(path); fi.Size() != SpoolMax {
		t.Fatalf("grew to %d", fi.Size())
	}
}

func TestSpoolFileName(t *testing.T) {
	if got := filepath.Base(SpoolFile("/h", "%12")); got != "pane-_12.jsonl" {
		t.Fatal(got)
	}
	if got := filepath.Base(SpoolFile("/h", "../x")); got != "pane-___x.jsonl" {
		t.Fatal(got)
	}
}

// The transcript's path and the working directory reach a live agent only:
// a spooled event leaves them out.
func TestSpoolKeepsNoPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("STUI_HOME", home)
	t.Setenv("TMUX_PANE", "%7")
	Run([]string{"claude"}, strings.NewReader(`{"hook_event_name":"SessionStart","session_id":"s1","transcript_path":"/home/u/.claude/projects/-p/s1.jsonl","cwd":"/home/u/p"}`))
	b, _ := os.ReadFile(SpoolFile(home, "%7"))
	if !strings.Contains(string(b), `"s1"`) || strings.Contains(string(b), ".jsonl") || strings.Contains(string(b), "/home/u/p") {
		t.Fatalf("%s", b)
	}
}

// A tool hook no agent took is not spooled but counted: its event name in
// the dropped file, and the run says "dropped", not "spooled".
func TestRunCountsDroppedToolHooks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("STUI_HOME", home)
	t.Setenv("TMUX_PANE", "%7")
	t.Setenv("TMUX", "/tmp/tmux-501/default,123,0")
	t.Setenv("STUI_HOOK_PROBE", "")
	Run([]string{"claude"}, strings.NewReader(`{"hook_event_name":"PreToolUse","session_id":"s1","tool_name":"Agent","tool_input":{"description":"Android agent tree","subagent_type":"general-purpose"}}`))
	Run([]string{"claude"}, strings.NewReader(`{"hook_event_name":"PostToolUse","session_id":"s1","tool_name":"Agent","tool_response":{"agentId":"a1"}}`))
	b, err := os.ReadFile(DroppedFile(home))
	if err != nil || string(b) != "PreToolUse\nPostToolUse\n" {
		t.Fatalf("%q %v", b, err)
	}
	if r, ok := ReadLast(home, "claude"); !ok || r.Outcome != Dropped {
		t.Fatalf("%+v", r)
	}
	if evs := spoolLines(t, SpoolFile(home, "%7")); len(evs) != 0 {
		t.Fatalf("spooled %+v", evs)
	}
	// It stops growing at DroppedMax.
	os.WriteFile(DroppedFile(home), make([]byte, DroppedMax), 0o600)
	noteDropped(home, "PreToolUse")
	if fi, _ := os.Stat(DroppedFile(home)); fi.Size() != DroppedMax {
		t.Fatalf("grew to %d", fi.Size())
	}
}
