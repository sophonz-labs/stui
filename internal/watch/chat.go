package watch

import (
	"time"

	"github.com/sophonz-labs/stui/internal/hookclient"
	"github.com/sophonz-labs/stui/internal/restore"
)

// Which conversation each pane's agent is in, and where it is written down,
// for the chat view. Kept in memory only: the path and the directory
// never reach the state file (persist.go) or an event.

// Transcript is what the chat view needs to find a pane's conversation.
type Transcript struct {
	Agent     string
	SessionID string
	// From the agent's hooks; "" when they did not say.
	Path string
	Cwd  string
	// When the conversation began in this pane (its SessionStart, or the
	// first hook stui heard from it).
	Since time.Time
	// Another pane took this conversation over (claude --resume of the same
	// session elsewhere): its id.
	MovedTo string
	// Claude config directories hooks have named.
	ClaudeDirs []string
	// When the agent last took a prompt and last ended a turn, from its
	// hooks: a whole turn after the transcript's last write means it is not
	// writing one (chat "not-recording").
	Submitted, Stopped time.Time
}

// noteTranscriptLocked keeps the transcript path and working directory a
// main agent's hook gave, following the conversation as it changes: a
// SessionStart (startup, /clear, --resume, compact) or a new session id
// starts over with what that hook says.
func (w *Watcher) noteTranscriptLocked(p *paneState, ev hookclient.Event, prevResume string, now time.Time) {
	str := func(k string) string {
		s, _ := ev.Data[k].(string)
		return s
	}
	path, cwd := str("transcript_path"), str("cwd")
	switched := ev.Event == "SessionStart" || (p.resumeID != prevResume && prevResume != "")
	if switched {
		p.transcript, p.chatSince = "", now
	}
	if p.chatSince.IsZero() {
		p.chatSince = now
	}
	if path != "" {
		p.transcript = path
	}
	if cwd != "" {
		p.cwd = cwd
	}
	// One conversation, one pane: the last to send its hooks has it.
	if p.resumeID != "" {
		if w.chatOwner == nil {
			w.chatOwner = map[string]string{}
		}
		if len(w.chatOwner) > 4096 {
			clear(w.chatOwner)
		}
		w.chatOwner[p.Agent+":"+p.resumeID] = p.ID
	}
}

// Transcript is where pane's agent writes its conversation down, as far as
// its hooks said; false for a pane with no agent stui has heard from. A
// Claude pane whose hooks have not named its conversation (one known from
// its screen only) gets it from Claude's own session file for its process
// (claudesession.go), for this answer only.
func (w *Watcher) Transcript(pane string) (Transcript, bool) {
	w.mu.Lock()
	p := w.panes[pane]
	if p == nil || p.Agent == "" || p.Agent == "other" {
		w.mu.Unlock()
		return Transcript{}, false
	}
	t := Transcript{
		Agent: p.Agent, SessionID: p.resumeID, Path: p.transcript, Cwd: p.cwd, Since: p.chatSince,
		Submitted: p.submitAt, Stopped: p.stoppedAt,
	}
	for d := range w.claudeDirs {
		t.ClaudeDirs = append(t.ClaudeDirs, d)
	}
	tty := p.tty
	w.mu.Unlock()

	// Read without the lock: ps and a file.
	if t.Agent == "claude" && t.SessionID == "" && t.Path == "" {
		if sid, cwd := w.claudeSession(tty, append([]string(nil), t.ClaudeDirs...)); sid != "" {
			t.SessionID = sid
			if t.Cwd == "" {
				t.Cwd = cwd
			}
		}
	}

	if t.SessionID != "" {
		w.mu.Lock()
		if o := w.chatOwner[t.Agent+":"+t.SessionID]; o != "" && o != pane {
			if q := w.panes[o]; q != nil && q.Agent == t.Agent && q.resumeID == t.SessionID {
				t.MovedTo = o
			}
		}
		w.mu.Unlock()
	}
	return t, true
}

// ChatPending says whether a key typed into pane now would answer
// something: agent watch has a question or permission prompt open there, or
// the pane waits on one it has not read yet. The chat's send refuses then.
func (w *Watcher) ChatPending(pane string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	p := w.panes[pane]
	if p == nil {
		return false
	}
	if p.prompt != nil && p.prompt.status != "ended" {
		return true
	}
	return p.State == Waiting
}

// Submitted is when pane's main agent last took a prompt (its
// UserPromptSubmit hook, heard live); zero for never.
func (w *Watcher) Submitted(pane string) time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	if p := w.panes[pane]; p != nil {
		return p.submitAt
	}
	return time.Time{}
}

// Working says whether pane's agent is in a turn.
func (w *Watcher) Working(pane string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	p := w.panes[pane]
	return p != nil && p.State == Working
}

// NotRecordingCause is what in a pane's agent process explains it not
// writing its transcript: "CLAUDE_CODE_CHILD_SESSION" when a process on the
// pane's terminal carries it (Claude Code then keeps no transcript), else
// "". Only an explanation: whether a transcript stopped is told by the
// file (chat NotRecording).
func (w *Watcher) NotRecordingCause(pane string) string {
	w.mu.Lock()
	p := w.panes[pane]
	tty := ""
	if p != nil {
		tty = p.tty
	}
	w.mu.Unlock()
	if tty == "" {
		return ""
	}
	for _, pr := range w.ttyProcs(tty) {
		if restore.EnvOf(pr.PID, "CLAUDE_CODE_CHILD_SESSION") != "" {
			return "CLAUDE_CODE_CHILD_SESSION"
		}
	}
	return ""
}
