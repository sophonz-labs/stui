package hookclient

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) []byte {
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestReduceFixtures(t *testing.T) {
	cases := map[string]string{
		"claude-SessionStart.json":                      "SessionStart",
		"claude-UserPromptSubmit.json":                  "UserPromptSubmit",
		"claude-PermissionRequest-Bash.json":            "PermissionRequest",
		"claude-PermissionRequest-AskUserQuestion.json": "PermissionRequest",
		"claude-PermissionRequest-ExitPlanMode.json":    "PermissionRequest",
		"claude-Notification-permission_prompt.json":    "Notification",
		"claude-Stop.json":                              "Stop",
		"claude-StopFailure.json":                       "StopFailure",
		"claude-SubagentStart.json":                     "SubagentStart",
		"claude-SubagentStop.json":                      "SubagentStop",
		"claude-SessionEnd.json":                        "SessionEnd",
	}
	for file, want := range cases {
		ev, data := Reduce("claude", fixture(t, file))
		if ev != want || data == nil {
			t.Errorf("%s: %q", file, ev)
			continue
		}
		for _, secret := range []string{"prompt", "scratchpad_dir"} {
			if _, ok := data[secret]; ok {
				t.Errorf("%s kept %s", file, secret)
			}
		}
	}
	_, data := Reduce("claude", fixture(t, "claude-PermissionRequest-Bash.json"))
	if data["tool_name"] != "Bash" || !strings.Contains(data["tool_input"].(string), "echo hello > greeting.txt") {
		t.Fatalf("%v", data)
	}
	_, data = Reduce("claude", fixture(t, "claude-Notification-permission_prompt.json"))
	if data["notification_type"] != "permission_prompt" {
		t.Fatalf("%v", data)
	}
	ev, data := Reduce("codex", fixture(t, "codex-notify.json"))
	if ev != "agent-turn-complete" || data["last-assistant-message"] == nil {
		t.Fatalf("%s %v", ev, data)
	}
	if _, data := Reduce("claude", []byte("not json")); data != nil {
		t.Fatal("garbage reduced")
	}
}

// The transcript's path and the working directory reach stui (the chat view
// reads the one, finds a Codex rollout by the other), whole or not at all.
func TestReducePaths(t *testing.T) {
	_, data := Reduce("claude", []byte(`{"hook_event_name":"Stop","session_id":"s","transcript_path":"/home/u/.claude/projects/-p/s.jsonl","cwd":"/home/u/p","prompt":"x"}`))
	if data["transcript_path"] != "/home/u/.claude/projects/-p/s.jsonl" || data["cwd"] != "/home/u/p" || data["prompt"] != nil {
		t.Fatalf("%v", data)
	}
	long := "/" + strings.Repeat("a", 5000) + ".jsonl"
	for _, in := range []string{
		`{"hook_event_name":"Stop","transcript_path":"relative/s.jsonl"}`,
		`{"hook_event_name":"Stop","transcript_path":"/etc/passwd"}`,
		`{"hook_event_name":"Stop","transcript_path":"` + long + `"}`,
	} {
		if _, data := Reduce("claude", []byte(in)); data["transcript_path"] != nil {
			t.Fatalf("kept %v", data["transcript_path"])
		}
	}
}

func TestClip(t *testing.T) {
	s := strings.Repeat("가", 10)
	c := clip(s, 7)
	if !strings.HasSuffix(c, "…") || !json.Valid([]byte(strconv.Quote(c))) || strings.ContainsRune(c, '�') {
		t.Fatalf("%q", c)
	}
}

func agentAt(t *testing.T, port int, token string) {
	home := t.TempDir()
	t.Setenv("STUI_HOME", home)
	b, _ := json.Marshal(map[string]any{"pid": 1, "port": port, "token": token})
	os.WriteFile(filepath.Join(home, "agent.json"), b, 0o600)
}

func TestRunForwards(t *testing.T) {
	got := make(chan Event, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/hook" || r.Header.Get("x-stui-token") != "tok" {
			w.WriteHeader(403)
			return
		}
		var e Event
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &e)
		got <- e
	}))
	defer srv.Close()
	port, _ := strconv.Atoi(srv.URL[strings.LastIndex(srv.URL, ":")+1:])
	agentAt(t, port, "tok")
	t.Setenv("TMUX_PANE", "%7")
	t.Setenv("TMUX", "/tmp/aw/tmux-501/default,123,0")
	f, _ := os.Open(filepath.Join("testdata", "claude-PermissionRequest-Bash.json"))
	defer f.Close()
	Run([]string{"claude"}, f)
	select {
	case e := <-got:
		if e.Kind != "claude" || e.Pane != "%7" || e.Socket != "/tmp/aw/tmux-501/default" || e.Event != "PermissionRequest" {
			t.Fatalf("%+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nothing forwarded")
	}

	Run([]string{"codex", string(fixture(t, "codex-notify.json"))}, strings.NewReader(""))
	if e := <-got; e.Event != "agent-turn-complete" || e.Kind != "codex" {
		t.Fatalf("%+v", e)
	}

	// stui's opencode plugin gives its event as the last argument too.
	Run([]string{"opencode", `{"hook_event_name":"Stop","session_id":"ses_1","cwd":"/work"}`}, strings.NewReader(""))
	if e := <-got; e.Event != "Stop" || e.Kind != "opencode" || e.Data["session_id"] != "ses_1" || e.Data["cwd"] != "/work" {
		t.Fatalf("%+v", e)
	}
}

// With no agent listening, stui hook is done at once.
func TestRunAgentDown(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	agentAt(t, port, "tok")
	t.Setenv("TMUX_PANE", "%7")
	start := time.Now()
	Run([]string{"claude"}, strings.NewReader(string(fixture(t, "claude-Stop.json"))))
	if d := time.Since(start); d > 150*time.Millisecond {
		t.Fatalf("took %v", d)
	}
}

// An agent that accepts and never answers costs at most the timeout.
func TestRunAgentHung(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	agentAt(t, ln.Addr().(*net.TCPAddr).Port, "tok")
	t.Setenv("TMUX_PANE", "%7")
	start := time.Now()
	Run([]string{"claude"}, strings.NewReader(string(fixture(t, "claude-Stop.json"))))
	if d := time.Since(start); d > 450*time.Millisecond {
		t.Fatalf("took %v", d)
	}
}

// Claude submits a background agent's result as a prompt: the agent is told
// it was one, and never sees the prompt.
func TestTaskNotification(t *testing.T) {
	raw := []byte(`{"hook_event_name":"UserPromptSubmit","session_id":"s","prompt":"<task-notification>\n<task-id>a42877f6e31444b7a</task-id>\n<status>completed</status>"}`)
	ev, data := Reduce("claude", raw)
	if ev != "UserPromptSubmit" || data["task_notification"] != true || data["prompt"] != nil {
		t.Fatalf("%s %v", ev, data)
	}
	if _, data := Reduce("claude", fixture(t, "claude-UserPromptSubmit.json")); data["task_notification"] != nil {
		t.Fatalf("%v", data)
	}
}

// The Agent tool's input gives a subagent's type and short description; its
// prompt never leaves the hook.
func TestReduceAgentTool(t *testing.T) {
	_, data := Reduce("claude", fixture(t, "claude-PreToolUse-Agent.json"))
	if data["agent_desc"] != "Find API endpoints" || data["agent_subtype"] != "Explore" || data["tool_use_id"] != "toolu_01ABC123" {
		t.Fatalf("%v", data)
	}
	if strings.Contains(data["tool_input"].(string), "REDACTED") {
		t.Fatalf("prompt kept: %v", data["tool_input"])
	}
	_, data = Reduce("claude", fixture(t, "claude-PostToolUse-Agent-async.json"))
	if data["agent_ref"] != "a4d2c8f1e0b3a297" || data["agent_desc"] != "Find API endpoints" {
		t.Fatalf("%v", data)
	}
	b, _ := json.Marshal(data)
	if strings.Contains(string(b), "REDACTED") {
		t.Fatalf("prompt kept: %s", b)
	}
	long := `{"hook_event_name":"PreToolUse","tool_name":"Task","tool_input":{"description":"` + strings.Repeat("가", 100) + `","prompt":"x"}}`
	_, data = Reduce("claude", []byte(long))
	if d := data["agent_desc"].(string); len([]rune(d)) != 80 || !strings.HasSuffix(d, "…") {
		t.Fatalf("%q", d)
	}
	for _, f := range []string{"claude-TeammateIdle.json", "claude-TaskCreated.json", "claude-TaskCompleted.json", "claude-SubagentStart-teammate.json"} {
		_, data := Reduce("claude", fixture(t, f))
		if _, ok := data["task_description"]; ok {
			t.Fatalf("%s kept task_description", f)
		}
		if f != "claude-SubagentStart-teammate.json" && data["teammate_name"] != "researcher" {
			t.Fatalf("%s: %v", f, data)
		}
	}
}

// Codex's hooks name their event as Claude's do; its notify program does
// not, and a command comes as its argv.
func TestReduceCodex(t *testing.T) {
	ev, data := Reduce("codex", []byte(`{"hook_event_name":"PermissionRequest","session_id":"019a","tool_name":"shell","tool_input":{"command":["bash","-lc","rm -rf build"]}}`))
	if ev != "PermissionRequest" || data["tool_summary"] != "bash -lc rm -rf build" || data["session_id"] != "019a" {
		t.Fatalf("%s %+v", ev, data)
	}
	if ev, _ := Reduce("codex", []byte(`{"type":"agent-turn-complete","thread-id":"019a"}`)); ev != "agent-turn-complete" {
		t.Fatalf("notify: %q", ev)
	}
}
