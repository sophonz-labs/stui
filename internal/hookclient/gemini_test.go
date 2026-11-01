package hookclient

import "testing"

// Gemini CLI's payload carries the same core fields as Claude Code's
// (session_id, transcript_path, cwd, hook_event_name), which is why stui needs
// no reducer of its own for it. If Gemini ever renames them, this fails
// before anyone wonders why a Gemini pane never changes state.
func TestReduceGemini(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		event string
	}{
		{"turn starts", `{"hook_event_name":"BeforeAgent","session_id":"g1",
			"transcript_path":"/home/u/.gemini/tmp/g1.jsonl","cwd":"/home/u/p",
			"timestamp":"2026-10-11T00:00:00Z","prompt":"fix the test"}`, "BeforeAgent"},
		{"turn ends", `{"hook_event_name":"AfterAgent","session_id":"g1",
			"transcript_path":"/home/u/.gemini/tmp/g1.jsonl","cwd":"/home/u/p",
			"prompt":"fix the test","prompt_response":"done","stop_hook_active":false}`, "AfterAgent"},
		{"asks permission", `{"hook_event_name":"Notification","session_id":"g1",
			"transcript_path":"/home/u/.gemini/tmp/g1.jsonl","cwd":"/home/u/p",
			"notification_type":"ToolPermission","message":"Run rm -rf build?"}`, "Notification"},
		{"session ends", `{"hook_event_name":"SessionEnd","session_id":"g1",
			"transcript_path":"/home/u/.gemini/tmp/g1.jsonl","cwd":"/home/u/p","reason":"exit"}`, "SessionEnd"},
	}
	for _, c := range cases {
		event, data := Reduce("gemini", []byte(c.in))
		if event != c.event {
			t.Errorf("%s: event %q, want %q", c.name, event, c.event)
		}
		if data == nil {
			t.Fatalf("%s: nothing came back", c.name)
		}
		if data["transcript_path"] != "/home/u/.gemini/tmp/g1.jsonl" {
			t.Errorf("%s: transcript path %v", c.name, data["transcript_path"])
		}
		if data["cwd"] != "/home/u/p" {
			t.Errorf("%s: cwd %v", c.name, data["cwd"])
		}
		// The prompt itself stays on the machine, as for every agent.
		if data["prompt"] != nil {
			t.Errorf("%s: the prompt was kept: %v", c.name, data["prompt"])
		}
	}
}
