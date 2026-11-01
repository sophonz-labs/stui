package watch

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sophonz-labs/stui/internal/hookclient"
)

// The chat view finds a pane's conversation by what its hooks said, follows
// it to a new one, and the path stays in memory: not in the state file, not
// in what the API gives.
func TestTranscriptFollowsHooks(t *testing.T) {
	r := newPersistRig(t)
	w := r.start(t, "")
	w.poll(context.Background())
	if _, ok := w.Transcript("%9"); ok {
		t.Fatal("a pane stui does not know")
	}
	first := "/home/u/.claude/projects/-p/" + testSession + ".jsonl"
	shook(w, "SessionStart", map[string]any{"transcript_path": first, "cwd": "/home/u/p", "source": "startup"})
	shook(w, "UserPromptSubmit", map[string]any{"transcript_path": first})
	tr, ok := w.Transcript("%1")
	if !ok || tr.Agent != "claude" || tr.SessionID != testSession || tr.Path != first || tr.Cwd != "/home/u/p" || tr.Since.IsZero() || tr.MovedTo != "" {
		t.Fatalf("%+v %v", tr, ok)
	}
	// A subagent's hooks say nothing about the main conversation.
	shook(w, "PreToolUse", map[string]any{"agent_id": "a1", "transcript_path": "/elsewhere/agent-a1.jsonl", "tool_name": "Read"})
	if tr, _ := w.Transcript("%1"); tr.Path != first {
		t.Fatalf("subagent moved it: %+v", tr)
	}
	// /clear: a new session, a new file.
	r.clock.add(time.Minute)
	second := "/home/u/.claude/projects/-p/second.jsonl"
	shook(w, "SessionStart", map[string]any{"session_id": "second", "transcript_path": second, "source": "clear"})
	if tr, _ := w.Transcript("%1"); tr.Path != second || tr.SessionID != "second" {
		t.Fatalf("after /clear: %+v", tr)
	}
	// A new session id with no path: the old path is not this conversation.
	shook(w, "UserPromptSubmit", map[string]any{"session_id": "third"})
	if tr, _ := w.Transcript("%1"); tr.Path != "" || tr.SessionID != "third" {
		t.Fatalf("after a switch with no path: %+v", tr)
	}

	// Nowhere else.
	w.Flush()
	b, _ := os.ReadFile(r.path)
	snap, _ := json.Marshal(w.Snapshot())
	for _, s := range []string{string(b), string(snap)} {
		if strings.Contains(s, ".jsonl") || strings.Contains(s, "/home/u/p") {
			t.Fatalf("path leaked: %s", s)
		}
	}
	r.sink.mu.Lock()
	ev, _ := json.Marshal(r.sink.events)
	r.sink.mu.Unlock()
	if strings.Contains(string(ev), ".jsonl") {
		t.Fatalf("path in an event: %s", ev)
	}
}

// claude --resume of the same conversation in another pane: that pane has it.
func TestTranscriptMoves(t *testing.T) {
	r := newPersistRig(t)
	w := r.start(t, "")
	w.poll(context.Background())
	shook(w, "SessionStart", map[string]any{"transcript_path": "/p/a.jsonl"})
	w.Hook(hookclient.Event{Kind: "claude", Pane: "%2", Event: "SessionStart", Data: map[string]any{"session_id": testSession, "transcript_path": "/p/a.jsonl", "source": "resume"}})
	if tr, _ := w.Transcript("%1"); tr.MovedTo != "%2" {
		t.Fatalf("%+v", tr)
	}
	if tr, _ := w.Transcript("%2"); tr.MovedTo != "" || tr.Path != "/p/a.jsonl" {
		t.Fatalf("%+v", tr)
	}
}

// An event played back from the spool does not move the chat view.
func TestTranscriptIgnoresReplay(t *testing.T) {
	r := newPersistRig(t)
	w := r.start(t, "")
	w.poll(context.Background())
	shook(w, "SessionStart", map[string]any{"transcript_path": "/p/live.jsonl"})
	w.Hook(hookclient.Event{Kind: "claude", Pane: "%1", Event: "SessionStart", Replay: true, Data: map[string]any{"session_id": testSession, "source": "startup"}})
	if tr, _ := w.Transcript("%1"); tr.Path != "/p/live.jsonl" {
		t.Fatalf("%+v", tr)
	}
}

// What the chat's send asks agent watch: whether a key now would answer a
// prompt, when the agent last took a prompt, and whether it is working.
func TestChatSendQuestions(t *testing.T) {
	r := newPersistRig(t)
	w := r.start(t, "")
	w.poll(context.Background())
	if w.ChatPending("%1") || !w.Submitted("%1").IsZero() || w.Working("%1") {
		t.Fatal("a quiet pane")
	}
	before := time.Now()
	shook(w, "UserPromptSubmit", map[string]any{})
	if !w.Submitted("%1").After(before) || !w.Working("%1") {
		t.Fatalf("submitted %v working %v", w.Submitted("%1"), w.Working("%1"))
	}
	// A background agent's result is not a prompt anybody typed.
	at := w.Submitted("%1")
	shook(w, "UserPromptSubmit", map[string]any{"task_notification": true})
	if w.Submitted("%1") != at {
		t.Fatal("a task notification counted as a prompt")
	}
	shook(w, "PermissionRequest", map[string]any{"tool_name": "Bash"})
	if !w.ChatPending("%1") {
		t.Fatal("a permission request is pending")
	}
	if w.ChatPending("%7") || !w.Submitted("%7").IsZero() {
		t.Fatal("a pane stui does not know")
	}
}
