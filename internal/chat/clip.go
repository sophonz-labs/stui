package chat

// What goes to a phone is cut down: a tool's output or input to 4000
// characters, a message's text to 64,000, no image bytes at all. The limits
// and the way a tool's input is cut (items, nodes, depth) are Orca's
// (src/main/runtime/rpc/methods/native-chat-rpc-block-sanitize.ts, MIT,
// Copyright (c) 2026 Lovecast Inc.).

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Limits says how much of each block is sent.
type Limits struct {
	// tool_result output, a tool call's input, command output.
	Tool int
	// text and reasoning.
	Text int
}

var (
	// Snapshots and the stream.
	Wire = Limits{Tool: 4000, Text: 64000}
	// GET /api/chat/block: a block whole, up to 1 MB of characters.
	Full = Limits{Tool: 1 << 20, Text: 1 << 20}
)

const (
	inputItems = 20
	inputNodes = 100
	inputDepth = 5
	truncated  = "… (truncated)"
	// A diff: at most this many hunks, this many lines each, this long.
	maxHunks     = 40
	maxHunkLines = 400
	maxDiffLine  = 1000
	summaryChars = 200
)

// clipText cuts s to n characters; the whole length goes in size when cut.
func clipText(s string, n int) (out string, cut bool, size int) {
	if len(s) <= n {
		return s, false, 0
	}
	size = utf8.RuneCountInString(s)
	if size <= n {
		return s, false, 0
	}
	i := 0
	for k := 0; k < n; k++ {
		_, w := utf8.DecodeRuneInString(s[i:])
		i += w
	}
	return s[:i], true, size
}

// Clip is m as it goes to a client under l. m itself is not changed.
func Clip(m Message, l Limits) Message {
	out := m
	out.Blocks = make([]Block, len(m.Blocks))
	for i, b := range m.Blocks {
		switch b.Type {
		case BlockText, BlockReasoning:
			b.Text, b.Clipped, b.Size = clipText(b.Text, l.Text)
		case BlockCommandOutput, BlockCommand:
			b.Text, b.Clipped, b.Size = clipText(b.Text, l.Tool)
		case BlockToolResult:
			b.Output, b.Clipped, b.Size = clipText(b.Output, l.Tool)
		case BlockToolCall:
			if b.Input != nil {
				budget := &inputBudget{remaining: l.Tool, nodes: inputNodes}
				b.Input = sanitizeInput(b.Input, budget, 0)
				if budget.cut {
					b.Clipped = true
				}
			}
		}
		out.Blocks[i] = b
	}
	return out
}

type inputBudget struct {
	remaining, nodes int
	cut              bool
}

func sanitizeInput(v any, b *inputBudget, depth int) any {
	b.nodes--
	if b.nodes < 0 || b.remaining <= 0 {
		b.cut = true
		return truncated
	}
	switch t := v.(type) {
	case string:
		s, cut, _ := clipText(t, b.remaining)
		b.remaining -= utf8.RuneCountInString(s)
		if cut {
			b.cut = true
			return s + truncated
		}
		return s
	case []any:
		if depth >= inputDepth {
			b.cut = true
			return truncated
		}
		n := min(len(t), inputItems)
		out := make([]any, 0, n+1)
		for _, it := range t[:n] {
			out = append(out, sanitizeInput(it, b, depth+1))
		}
		if len(t) > inputItems {
			b.cut = true
			out = append(out, truncated)
		}
		return out
	case map[string]any:
		if depth >= inputDepth {
			b.cut = true
			return truncated
		}
		out := map[string]any{}
		count := 0
		for _, k := range sortedKeys(t) {
			if count >= inputItems || b.remaining <= 0 {
				b.cut = true
				out["…"] = "truncated"
				break
			}
			key, _, _ := clipText(k, min(b.remaining, 128))
			if _, dup := out[key]; dup {
				key += "~" + strconv.Itoa(count)
			}
			b.remaining -= utf8.RuneCountInString(key)
			out[key] = sanitizeInput(t[k], b, depth+1)
			count++
		}
		return out
	}
	return v
}

// summaryKeys are the fields that say in one line what a tool call does,
// in the order they are tried.
var summaryKeys = []string{"command", "cmd", "file_path", "path", "notebook_path", "pattern", "url", "query", "description", "plan", "prompt", "skill"}

// summarize is the one line a phone shows for a tool call's input.
func summarize(input any) string {
	m, ok := input.(map[string]any)
	if !ok {
		if s, ok := input.(string); ok {
			// Codex gives a call's arguments as JSON text.
			var inner map[string]any
			if json.Unmarshal([]byte(s), &inner) == nil {
				return summarize(inner)
			}
			return oneLine(s)
		}
		return ""
	}
	for _, k := range summaryKeys {
		switch v := m[k].(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return oneLine(v)
			}
		case []any:
			parts := make([]string, 0, len(v))
			for _, a := range v {
				if s, ok := a.(string); ok {
					parts = append(parts, s)
				}
			}
			if len(parts) > 0 {
				return oneLine(strings.Join(parts, " "))
			}
		}
	}
	return ""
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	first, rest, more := strings.Cut(s, "\n")
	first = strings.TrimSpace(first)
	out, cut, _ := clipText(first, summaryChars)
	if cut || (more && strings.TrimSpace(rest) != "") {
		out += "…"
	}
	return out
}
