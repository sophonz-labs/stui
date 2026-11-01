package chat

// Codex's rollout (~/.codex/sessions/YYYY/MM/DD/rollout-<time>-<id>.jsonl)
// to messages.
//
// Ported from Orca's transcript-line-decoders-codex.ts and
// transcript-turn-lifecycle.ts (MIT, Copyright (c) 2026 Lovecast Inc.; see
// NOTICE): response_item records are the conversation,
// event_msg task_started / task_complete / turn_aborted bound a turn. Unlike
// Orca, the event_msg copies of a message (user_message, agent_message,
// item_completed) are not shown a second time: every Codex that writes
// them writes the response_item as well.

import (
	"encoding/json"
	"regexp"
	"strings"
)

type codexDecoder struct {
	turn  string
	names map[string]string
	last  Turn
}

func (d *codexDecoder) startsTurn(line []byte) bool {
	_, t := (&codexDecoder{names: map[string]string{}}).decode(line, 0)
	return t != nil && t.State == TurnWorking
}

func (d *codexDecoder) setTurn(t Turn) *Turn {
	if t.ID == d.last.ID && t.State == d.last.State && (t.DurationMs == 0 || d.last.DurationMs != 0) {
		return nil
	}
	d.last = t
	return &t
}

// Context Codex sends the model as a user message: the environment, the
// project's instructions, a skill. A user message that is one tag from start
// to end, or the AGENTS.md preamble, is that, not something typed.
var codexWrapped = regexp.MustCompile(`(?s)^<([A-Za-z][A-Za-z0-9_ -]*)>.*</([A-Za-z][A-Za-z0-9_ -]*)>$`)

func codexContext(text string) bool {
	t := strings.TrimSpace(text)
	if strings.HasPrefix(t, "# AGENTS.md instructions") {
		return true
	}
	m := codexWrapped.FindStringSubmatch(t)
	return m != nil && m[1] == m[2]
}

func (d *codexDecoder) decode(line []byte, off int64) ([]Message, *Turn) {
	var r map[string]any
	if json.Unmarshal(line, &r) != nil {
		return nil, nil
	}
	ts := timestamp(r["timestamp"])
	payload, wrapped := r["payload"].(map[string]any)
	if !wrapped {
		// The oldest rollouts: the item itself on the line.
		payload = r
	}
	id := str(payload["id"])
	if id == "" {
		id = offsetID(off)
	}
	msg := func(role string, blocks ...Block) Message {
		return Message{ID: id, Role: role, Blocks: blocks, Turn: d.turn, TS: ts, Agent: "codex", Offset: off}
	}
	switch {
	case wrapped && str(r["type"]) == "turn_context":
		if t := str(payload["turn_id"]); t != "" && d.turn == "" {
			d.turn = t
		}
		return nil, nil
	case wrapped && str(r["type"]) == "event_msg":
		turnID := str(payload["turn_id"])
		if turnID == "" {
			turnID = d.turn
		}
		switch str(payload["type"]) {
		case "task_started":
			if turnID == "" {
				turnID = id
			}
			d.turn = turnID
			return nil, d.setTurn(Turn{ID: turnID, State: TurnWorking, At: ts})
		case "task_complete":
			if turnID == "" {
				return nil, nil
			}
			return nil, d.setTurn(Turn{ID: turnID, State: TurnCompleted, At: ts, DurationMs: num(payload["duration_ms"])})
		case "turn_aborted":
			m := msg(RoleSystem, Block{Type: BlockInterrupted})
			if turnID == "" {
				return []Message{m}, nil
			}
			return []Message{m}, d.setTurn(Turn{ID: turnID, State: TurnInterrupted, At: ts})
		}
		return nil, nil
	case wrapped && str(r["type"]) != "response_item":
		return nil, nil
	}

	switch str(payload["type"]) {
	case "message":
		role := str(payload["role"])
		if role != RoleUser && role != RoleAssistant {
			return nil, nil
		}
		blocks := codexContent(payload["content"])
		if role == RoleUser {
			kept := blocks[:0]
			for _, b := range blocks {
				if b.Type == BlockText && (codexContext(b.Text) || strings.HasPrefix(strings.ToLower(strings.TrimSpace(b.Text)), "<skill>")) {
					continue
				}
				kept = append(kept, b)
			}
			blocks = kept
		}
		if len(blocks) == 0 {
			return nil, nil
		}
		var t *Turn
		if role == RoleUser && (d.last.State != TurnWorking || d.turn == "") {
			// A Codex that does not mark its turns: the prompt starts one.
			d.turn = id
			t = d.setTurn(Turn{ID: id, State: TurnWorking, At: ts})
		}
		return []Message{msg(role, blocks...)}, t
	case "reasoning":
		text := str(payload["text"])
		if text == "" {
			var parts []string
			sum, _ := payload["summary"].([]any)
			for _, s := range sum {
				if m, ok := s.(map[string]any); ok && str(m["text"]) != "" {
					parts = append(parts, str(m["text"]))
				} else if s, ok := s.(string); ok && s != "" {
					parts = append(parts, s)
				}
			}
			text = strings.Join(parts, "\n")
		}
		if strings.TrimSpace(text) == "" {
			return nil, nil
		}
		return []Message{msg(RoleAssistant, Block{Type: BlockReasoning, Text: text})}, nil
	case "function_call", "local_shell_call", "custom_tool_call":
		name := str(payload["name"])
		if name == "" {
			name = "shell"
			if str(payload["type"]) != "local_shell_call" {
				name = "tool"
			}
		}
		var input any = payload["arguments"]
		if input == nil {
			input = payload["input"]
		}
		if input == nil {
			input = payload["action"]
		}
		// Arguments come as JSON text; an object reads better.
		if s, ok := input.(string); ok {
			var obj any
			if json.Unmarshal([]byte(s), &obj) == nil {
				if _, isMap := obj.(map[string]any); isMap {
					input = obj
				}
			}
		}
		callID := str(payload["call_id"])
		if callID != "" {
			if len(d.names) > 1024 {
				clear(d.names)
			}
			d.names[callID] = name
		}
		return []Message{msg(RoleAssistant, Block{Type: BlockToolCall, Name: name, CallID: callID, Summary: codexSummary(name, input), Input: input})}, nil
	case "function_call_output", "custom_tool_call_output":
		callID := str(payload["call_id"])
		out, isErr := codexOutput(payload["output"])
		return []Message{msg(RoleTool, Block{Type: BlockToolResult, CallID: callID, Name: d.names[callID], Output: out, IsError: isErr})}, nil
	}
	return nil, nil
}

func codexContent(v any) []Block {
	var out []Block
	list, _ := v.([]any)
	for _, c := range list {
		b, _ := c.(map[string]any)
		switch str(b["type"]) {
		case "input_text", "output_text", "text", "Text":
			if t := str(b["text"]); strings.TrimSpace(t) != "" {
				out = append(out, Block{Type: BlockText, Text: t})
			}
		case "input_image", "image", "Image", "local_image", "LocalImage":
			out = append(out, Block{Type: BlockImage})
		}
	}
	return out
}

var patchFile = regexp.MustCompile(`(?m)^\*\*\* (?:Update|Add|Delete) File: (.+)$`)

func codexSummary(name string, input any) string {
	if s, ok := input.(string); ok && strings.Contains(s, "*** Begin Patch") {
		files := patchFile.FindAllStringSubmatch(s, -1)
		if len(files) > 0 {
			parts := make([]string, 0, len(files))
			for _, f := range files {
				parts = append(parts, strings.TrimSpace(f[1]))
			}
			return oneLine(strings.Join(parts, ", "))
		}
	}
	return summarize(input)
}

// codexOutput is a call's output: often JSON text with the output inside.
func codexOutput(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		var inner map[string]any
		if json.Unmarshal([]byte(t), &inner) == nil {
			if o, ok := inner["output"].(string); ok {
				meta, _ := inner["metadata"].(map[string]any)
				code, hasCode := meta["exit_code"].(float64)
				return o, hasCode && code != 0
			}
		}
		return t, false
	case map[string]any:
		isErr := t["success"] == false || t["is_error"] == true
		for _, k := range []string{"content", "output"} {
			if t[k] != nil {
				return resultOutput(t[k]), isErr
			}
		}
		return resultOutput(t), isErr
	}
	return resultOutput(v), false
}
