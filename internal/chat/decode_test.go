package chat

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures in testdata are made up, in the record shapes of each Claude
// Code version seen on a real machine (claude-<version>.jsonl) and of two
// Codex rollouts; orca-openclaude-*.jsonl is Orca's (MIT, Copyright (c) 2026
// Lovecast Inc.). No real conversation is in them.

func fixture(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// shape is a message as "role:block,block".
func shape(m Message) string {
	var b []string
	for _, x := range m.Blocks {
		b = append(b, x.Type)
	}
	return m.Role + ":" + strings.Join(b, ",")
}

func decodeAll(t *testing.T, path, agent string) ([]Message, []Turn) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	d := newDecoder(agent)
	var msgs []Message
	var turns []Turn
	off := int64(0)
	for _, l := range bytes.SplitAfter(raw, []byte("\n")) {
		if len(bytes.TrimSpace(l)) > 0 {
			m, tr := d.decode(bytes.TrimRight(l, "\n"), off)
			msgs = append(msgs, m...)
			if tr != nil {
				turns = append(turns, *tr)
			}
		}
		off += int64(len(l))
	}
	return msgs, turns
}

func TestClaudeVersions(t *testing.T) {
	for _, v := range []string{"2.1.231", "2.1.270", "2.1.284", "2.1.293"} {
		t.Run(v, func(t *testing.T) {
			msgs, turns := decodeAll(t, fixture(t, "claude-"+v+".jsonl"), "claude")
			want := []string{
				"user:text",
				"assistant:reasoning", // 2.1.293 keeps a signature only: no message
				"assistant:text",
				"assistant:tool_call",
				"tool:tool_result,diff",
				"assistant:tool_call",
				"tool:tool_result",
				"assistant:text",
				"system:command",
				"system:command_output",
				"user:text",
				"assistant:tool_call",
				"tool:tool_result",
				"assistant:tool_call",
				"tool:tool_result",
				"system:interrupted",
				"user:text",
				"assistant:text",
				"system:interrupted",
				"system:compacted", // 2.1.270 and 2.1.284 only
				"user:text",
				"assistant:text",
			}
			if v == "2.1.293" {
				want = append(want[:1], want[2:]...)
			}
			if v == "2.1.231" || v == "2.1.293" {
				for i, s := range want {
					if s == "system:compacted" {
						want = append(want[:i], want[i+1:]...)
						break
					}
				}
			}
			var got []string
			for _, m := range msgs {
				got = append(got, shape(m))
				if m.Agent != "claude" || m.ID == "" || m.TS == 0 {
					t.Errorf("%+v", m)
				}
				for _, b := range m.Blocks {
					for _, hidden := range []string{"system-reminder", "task-notification", "Caveat:", "being continued", "\x1b"} {
						if strings.Contains(b.Text+b.Output, hidden) {
							t.Errorf("%q shown: %+v", hidden, m)
						}
					}
				}
			}
			if strings.Join(got, " ") != strings.Join(want, " ") {
				t.Fatalf("\n got %v\nwant %v", got, want)
			}

			byShape := func(s string, n int) Message {
				for _, m := range msgs {
					if shape(m) == s {
						if n == 0 {
							return m
						}
						n--
					}
				}
				t.Fatalf("no %s", s)
				return Message{}
			}
			edit := byShape("tool:tool_result,diff", 0)
			if r := edit.Blocks[0]; r.Name != "Edit" || r.CallID != "toolu_fixture_edit" {
				t.Fatalf("%+v", r)
			}
			if d := edit.Blocks[1]; d.Path != "/home/dev/project/hello.txt" || len(d.Hunks) != 1 || d.Hunks[0].OldStart != 1 ||
				strings.Join(d.Hunks[0].Lines, "|") != "-hello|+hi| world" {
				t.Fatalf("%+v", d)
			}
			call := byShape("assistant:tool_call", 0).Blocks[0]
			if call.Name != "Edit" || call.Summary != "/home/dev/project/hello.txt" {
				t.Fatalf("%+v", call)
			}
			cmd := byShape("system:command", 0).Blocks[0]
			out := byShape("system:command_output", 0).Blocks[0]
			if cmd.Name != "/context" || !strings.HasPrefix(out.Text, "Context Usage\n") {
				t.Fatalf("%+v %+v", cmd, out)
			}
			agent := byShape("tool:tool_result", 1)
			if agent.Subagent == nil || agent.Subagent.ID != "a0fixture00000001" || agent.Subagent.Type != "Explore" || agent.Subagent.Description != "Find TODOs" {
				t.Fatalf("%+v", agent.Subagent)
			}
			if c := byShape("assistant:tool_call", 2); c.Subagent == nil || c.Subagent.Type != "Explore" {
				t.Fatalf("%+v", c)
			}
			if r := byShape("tool:tool_result", 2).Blocks[0]; !r.IsError {
				t.Fatalf("%+v", r)
			}
			if last := msgs[len(msgs)-1]; !last.Error {
				t.Fatalf("api error: %+v", last)
			}

			// Every message of a turn carries the prompt that started it.
			p1 := msgs[0].ID
			for _, m := range msgs[:7] {
				if m.Turn != p1 {
					t.Fatalf("%s in turn %q, not %q", shape(m), m.Turn, p1)
				}
			}
			// The turns: started, finished, stopped.
			var seq []string
			for _, tr := range turns {
				seq = append(seq, tr.State)
			}
			if turns[0].ID != p1 || turns[0].State != TurnWorking || turns[1].State != TurnCompleted {
				t.Fatalf("%+v", turns)
			}
			if strings.Count(strings.Join(seq, " "), TurnInterrupted) != 2 || seq[len(seq)-1] != TurnCompleted {
				t.Fatalf("%v", seq)
			}
			if v != "2.1.231" && v != "2.1.293" {
				// turn_duration adds how long it took.
				found := false
				for _, tr := range turns {
					found = found || tr.DurationMs == 12345
				}
				if !found {
					t.Fatalf("no duration: %+v", turns)
				}
			}
		})
	}
}

// Orca's OpenClaude fixture: three local commands, no model call.
func TestClaudeLocalCommands(t *testing.T) {
	msgs, turns := decodeAll(t, fixture(t, "orca-openclaude-0.31.0-local-command-rows.jsonl"), "claude")
	var got []string
	for _, m := range msgs {
		s := shape(m)
		if m.Blocks[0].Type == BlockCommand {
			s += m.Blocks[0].Name
		}
		got = append(got, s)
	}
	if strings.Join(got, " ") != "system:command/context system:command_output system:command/cost system:command_output system:command/context system:command_output" {
		t.Fatal(got)
	}
	if !strings.Contains(msgs[1].Blocks[0].Text, "gpt-4o · 18.3k/128k tokens (14%)") || msgs[1].ParentID != msgs[0].ID {
		t.Fatalf("%+v", msgs[1])
	}
	if len(turns) != 0 {
		t.Fatalf("a command alone is no turn: %+v", turns)
	}
}

func TestClaudeRecords(t *testing.T) {
	d := newDecoder("claude")
	dec := func(s string) []Message {
		m, _ := d.decode([]byte(s), 7)
		return m
	}
	// Records of other kinds, a subagent's own, garbage: nothing.
	for _, s := range []string{
		`{"type":"summary","summary":"x"}`,
		`{"type":"user","isSidechain":true,"message":{"role":"user","content":"x"}}`,
		`not json`, `{"type":"assistant","message":{"content":[{"type":"mystery"}]}}`,
		`{"type":"user","message":{"role":"user","content":"<system-reminder>x</system-reminder>"}}`,
		`{"type":"user","isMeta":true,"message":{"role":"user","content":"expanded skill text"}}`,
	} {
		if m := dec(s); len(m) != 0 {
			t.Fatalf("%s: %+v", s, m)
		}
	}
	// No uuid: named by its offset.
	if m := dec(`{"type":"user","message":{"role":"user","content":"hi"}}`); m[0].ID != "o7" {
		t.Fatalf("%+v", m)
	}
	// A paste, unwrapped; a user's own tag kept.
	m := dec(`{"type":"user","uuid":"u","message":{"role":"user","content":"<pasted_content>\nline one\nline two\n</pasted_content>"}}`)
	if m[0].Blocks[0].Text != "line one\nline two" {
		t.Fatalf("%q", m[0].Blocks[0].Text)
	}
	if m := dec(`{"type":"user","uuid":"u2","message":{"role":"user","content":"<my-element> is broken"}}`); len(m) != 1 {
		t.Fatal("a user's own tag hidden")
	}
	// A shell command typed into Claude, and its output.
	m = dec(`{"type":"user","uuid":"b","message":{"role":"user","content":"<bash-input>ls</bash-input>"}}`)
	if m[0].Blocks[0].Type != BlockCommand || m[0].Blocks[0].Name != "!" || m[0].Blocks[0].Text != "ls" {
		t.Fatalf("%+v", m)
	}
	m = dec(`{"type":"user","uuid":"c","message":{"role":"user","content":"<bash-stdout>a\nb</bash-stdout><bash-stderr></bash-stderr>"}}`)
	if m[0].Blocks[0].Type != BlockCommandOutput || m[0].Blocks[0].Text != "a\nb" || m[0].Blocks[0].IsError {
		t.Fatalf("%+v", m)
	}
	// Image bytes never.
	m = dec(`{"type":"user","uuid":"i","message":{"role":"user","content":[{"type":"text","text":"see"},{"type":"image","source":{"type":"base64","data":"QUJD"}}]}}`)
	b, _ := json.Marshal(m)
	if strings.Contains(string(b), "QUJD") || shape(m[0]) != "user:text,image" {
		t.Fatalf("%s", b)
	}
}

func TestCodexModern(t *testing.T) {
	msgs, turns := decodeAll(t, fixture(t, "codex-0.159.jsonl"), "codex")
	var got []string
	for _, m := range msgs {
		got = append(got, shape(m))
	}
	want := "user:text assistant:reasoning assistant:tool_call tool:tool_result assistant:tool_call tool:tool_result assistant:text user:text assistant:tool_call tool:tool_result system:interrupted"
	if strings.Join(got, " ") != want {
		t.Fatalf("\n got %v\nwant %s", got, want)
	}
	if msgs[0].Turn != "turn-1" || msgs[7].Turn != "turn-2" || msgs[0].Blocks[0].Text != "Fix the typo in README.md." {
		t.Fatalf("%+v %+v", msgs[0], msgs[7])
	}
	if c := msgs[2].Blocks[0]; c.Name != "exec_command" || c.Summary != "grep -n teh README.md" || c.CallID != "call-1" {
		t.Fatalf("%+v", c)
	}
	if r := msgs[3].Blocks[0]; r.Output != "3:teh quick fox\n" || r.Name != "exec_command" || r.IsError {
		t.Fatalf("%+v", r)
	}
	if c := msgs[4].Blocks[0]; c.Name != "apply_patch" || c.Summary != "README.md" {
		t.Fatalf("%+v", c)
	}
	if r := msgs[9].Blocks[0]; !r.IsError {
		t.Fatalf("exit 2: %+v", r)
	}
	if len(turns) != 4 || turns[1].State != TurnCompleted || turns[1].DurationMs != 4200 || turns[3].ID != "turn-2" || turns[3].State != TurnInterrupted {
		t.Fatalf("%+v", turns)
	}
}

// A Codex that writes no turn markers: a prompt starts a turn.
func TestCodexLegacy(t *testing.T) {
	msgs, turns := decodeAll(t, fixture(t, "codex-0.46.jsonl"), "codex")
	var got []string
	for _, m := range msgs {
		got = append(got, shape(m))
	}
	if strings.Join(got, " ") != "user:text assistant:tool_call tool:tool_result assistant:text" {
		t.Fatal(got)
	}
	if msgs[1].Blocks[0].Summary != "bash -lc ls src" || msgs[2].Blocks[0].Output != "main.go\nutil.go\n" {
		t.Fatalf("%+v %+v", msgs[1], msgs[2])
	}
	if len(turns) != 1 || turns[0].ID != msgs[0].ID || msgs[3].Turn != msgs[0].ID {
		t.Fatalf("%+v", turns)
	}
}
