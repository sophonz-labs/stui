// Package chat reads the conversation a coding agent writes down as it
// works -- Claude Code's transcript, Codex's rollout -- and serves it as chat
// messages from stui's local server.
//
// The agent keeps running in its tmux pane; this is a second way to look at
// it. Nothing here leaves the machine, and nothing is kept: a stream
// remembers a byte offset, not the messages.
//
// The record-to-message rules, the limits and the turn markers follow Orca's
// terminal-lane native chat (src/main/native-chat, src/shared/native-chat-*,
// MIT, Copyright (c) 2026 Lovecast Inc.); see NOTICE.
package chat

import (
	"slices"
	"strings"
)

// Roles.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
	RoleSystem    = "system"
)

// Block types. A client skips a type it does not know.
const (
	BlockText          = "text"
	BlockReasoning     = "reasoning"
	BlockToolCall      = "tool_call"
	BlockToolResult    = "tool_result"
	BlockDiff          = "diff"
	BlockInterrupted   = "interrupted"
	BlockCommand       = "command"
	BlockCommandOutput = "command_output"
	BlockCompacted     = "compacted"
	BlockImage         = "image"
)

// Turn states.
const (
	TurnWorking     = "working"
	TurnCompleted   = "completed"
	TurnInterrupted = "interrupted"
)

// Hunk is one hunk of an edit, with the line numbers the agent resolved
// against the file. Lines start with ' ', '-' or '+'.
type Hunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
}

// Block is one part of a message. Which fields are set depends on Type.
type Block struct {
	Type string `json:"type"`
	// text, reasoning, command (its arguments), command_output.
	Text string `json:"text,omitempty"`
	// tool_call: the tool; command: the command ("/compact", "!" for a
	// shell command typed into the agent).
	Name string `json:"name,omitempty"`
	// tool_call and tool_result: the call they belong to.
	CallID string `json:"callId,omitempty"`
	// tool_call: one line saying what it does (the command, the file, the
	// pattern); its input, cut down (see Limits).
	Summary string `json:"summary,omitempty"`
	Input   any    `json:"input,omitempty"`
	// tool_result: what came back.
	Output  string `json:"output,omitempty"`
	IsError bool   `json:"isError,omitempty"`
	// diff: the file and its hunks.
	Path  string `json:"path,omitempty"`
	Hunks []Hunk `json:"hunks,omitempty"`
	// Text or Output was cut; Size is its whole length in characters, and
	// GET /api/chat/block has all of it.
	Clipped bool `json:"clipped,omitempty"`
	Size    int  `json:"size,omitempty"`
}

// Subagent is the subagent an Agent (Task) tool call started.
type Subagent struct {
	ID          string `json:"id,omitempty"`
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
}

// Message is one record of the conversation.
type Message struct {
	ID     string  `json:"id"`
	Role   string  `json:"role"`
	Blocks []Block `json:"blocks"`
	// The turn it belongs to: the id of the prompt that started it (Claude),
	// Codex's turn id. "" before the first prompt stui read.
	Turn string `json:"turn,omitempty"`
	// Unix milliseconds; 0 when the record had no time.
	TS    int64  `json:"ts,omitempty"`
	Agent string `json:"agent"`
	// The record this one answers (a command's output, its command).
	ParentID string    `json:"parentId,omitempty"`
	Subagent *Subagent `json:"subagent,omitempty"`
	// The agent's own error (an API error it wrote down as its reply).
	Error bool `json:"error,omitempty"`
	// Where its record starts in the file: what ?before= takes.
	Offset int64 `json:"offset"`
}

// Turn is where the conversation's latest turn is.
type Turn struct {
	ID    string `json:"id"`
	State string `json:"state"`
	At    int64  `json:"at,omitempty"`
	// completed: how long it took, when the agent said.
	DurationMs int64 `json:"durationMs,omitempty"`
}

// decoder turns one line of a transcript into messages and turn changes.
// It keeps what it needs across lines (the current turn, tool names by call
// id); a line it does not understand gives nothing.
type decoder interface {
	decode(line []byte, off int64) ([]Message, *Turn)
	// startsTurn says whether line, read on its own, opens a turn: how far
	// back a snapshot reads so its first messages have their turn.
	startsTurn(line []byte) bool
}

func newDecoder(agent string) decoder {
	switch agent {
	case "claude":
		return &claudeDecoder{names: map[string]string{}}
	case claudeSubagent:
		return &claudeDecoder{names: map[string]string{}, sidechain: true}
	case "codex":
		return &codexDecoder{names: map[string]string{}}
	}
	return nil
}

// Agents is the agents whose conversations stui can read.
var Agents = []string{"claude", "codex"}

// Readable says whether stui reads agent's conversations: whether it has a
// decoder for them.
func Readable(agent string) bool { return newDecoder(agent) != nil }

// Typed is the agents stui can type a message into and know it arrived: it
// reads their input box off the screen (ReadInput), and hears the prompt
// land from their hooks (UserPromptSubmit) when it cannot read their
// conversation. They are the agents stui can start as workers and send to.
//
// Not the same list as Agents: opencode and Pi are typed to, and their
// conversations are not ones stui reads yet (a chat view says no-transcript). Gemini,
// Qwen and Kimi are hooked but not here: stui has never seen their input box,
// so it could not tell whether it is empty or where the text went.
var Typed = []string{"claude", "codex", "opencode", "pi"}

// CanSend says whether agent is one of Typed.
func CanSend(agent string) bool { return slices.Contains(Typed, agent) }

// TypedWords is Typed as a sentence says it: "claude, codex, opencode or pi".
func TypedWords() string {
	return strings.Join(Typed[:len(Typed)-1], ", ") + " or " + Typed[len(Typed)-1]
}
