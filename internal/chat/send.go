package chat

// Typing for the chat:
//
//	POST /api/chat/send?pane=%3  {"text": "…", "mode": "send"|"type_only", "force": false}
//	POST /api/chat/stop?pane=%3
//
// stui types into the agent's own input box the way a person at the machine
// would, so the agent takes the message exactly as if it had been typed
// there: the terminal stays the source of truth. The rules are Orca's
// (mobile-native-chat-send.ts, agent-tui-command-typing.ts,
// agent-tui-input-clear.ts; MIT, Copyright (c) 2026 Lovecast Inc.):
//
//   - Nothing is typed over a question or a permission prompt: a key there
//     answers it.
//   - Nothing is typed onto what the person at the machine is typing: the
//     box is read off the screen first, and a phone has to say force to
//     replace it.
//   - The box is cleared as a write of its own (Ctrl-U and Ctrl-K, enough
//     for every line), never in the same write as the text: bundled, the
//     clear reached the agent as literal characters.
//   - A slash command is typed a key at a time, 16 ms apart: typed at once
//     it looks pasted and the agent takes it as prose.
//   - Enter goes as a write of its own, after a pause: with the text in one
//     read, the agent sees a paste and wants Enter again.
//   - One write sequence per pane at a time.
//   - Whether it arrived is read from the transcript (or the agent's
//     UserPromptSubmit hook) for up to 5 seconds. A send that could not be
//     confirmed is "unknown", never sent again: a second copy is worse than
//     a question.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Timing, shortened by tests.
var (
	// Between the keys of a slash command (Orca's
	// AGENT_TUI_COMMAND_KEY_INTERVAL_MS).
	keyEvery = 16 * time.Millisecond
	// After clearing the box, and between a message's lines.
	clearSettle = 40 * time.Millisecond
	// Before Enter: the text and Enter read together look like a paste.
	enterAfter = 120 * time.Millisecond
	// How long stui watches for the message to arrive.
	confirmWithin = 5 * time.Second
	confirmPoll   = 100 * time.Millisecond
	// With the hook heard and a transcript to read, how much longer stui
	// waits for the record (for its id) before saying delivered without it.
	idAfterHook = 1500 * time.Millisecond
)

const (
	// MaxSend is the most characters one message may have.
	MaxSend = 16000
	// Lines of slack when clearing the box: what Orca measured is free to
	// overshoot (agent-tui-input-clear.ts).
	clearSlack    = 8
	clearMaxLines = 40
)

// Refusal reasons (409).
const (
	PromptPending = "prompt-pending"
	BusyInput     = "busy-input"
	Sending       = "sending"
	NoInput       = "no-input"
	PaneGone      = "gone"
)

// stopKeys is what stops each agent's turn. Claude Code and Codex stop on
// one Escape (their footers say "esc to interrupt"); a second one is not
// harmless there: with the box empty, two Escapes open Claude's rewind
// menu and Codex's edit-previous-message. Orca's two paced Escapes are for
// TUIs whose first Escape only closes a view (agent-interrupt-intent.ts
// lists opencode and copilot); its mobile Stop sends two to any agent. Gemini
// CLI says "esc to cancel". opencode 1.19.4 is one of those: its first
// Escape only shows "esc again to interrupt", and the second stops the turn
// -- sent stopGap apart (two in one send-keys stopped nothing, measured in a
// sandbox; ten seconds apart the first had lapsed). Qwen and Kimi are not
// here: what stops them has not been seen, and a wrong key can open a menu.
var stopKeys = map[string][]string{
	"claude":   {"Escape"},
	"codex":    {"Escape"},
	"gemini":   {"Escape"},
	"opencode": {"Escape", "Escape"},
	// Pi 1.4.0 stops on one Escape (its start screen says "escape
	// interrupt"; seen in a sandbox).
	"pi": {"Escape"},
}

// stopGap is the pause between the keys of a stop that takes more than one.
const stopGap = 150 * time.Millisecond

// newlineKey is what puts a new line in each agent's box without sending:
// Alt-Enter in Claude Code (as the prompt answers do), Ctrl-J in Codex (both
// seen adding a line in a sandbox). opencode 1.19.4 takes either; Alt-Enter,
// as Claude's (Shift-Enter reaches it as Enter through tmux, and sends).
var newlineKey = map[string]string{
	"claude":   "M-Enter",
	"codex":    "C-j",
	"opencode": "M-Enter",
	// Pi 1.4.0: Ctrl-J. Its Shift-Enter needs tmux's extended keys, and
	// Alt-Enter queues a follow-up message: it would send.
	"pi": "C-j",
}

func (s *Service) sends() bool { return s.o.Tmux != nil }

func (s *Service) tmux(ctx context.Context, args ...string) (string, error) {
	return s.o.Tmux(ctx, args...)
}

// lockPane takes pane's write lock, or says another write holds it.
func (s *Service) lockPane(pane string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writing == nil {
		s.writing = map[string]bool{}
	}
	if s.writing[pane] {
		return false
	}
	s.writing[pane] = true
	return true
}

func (s *Service) unlockPane(pane string) {
	s.mu.Lock()
	delete(s.writing, pane)
	s.mu.Unlock()
}

func refuse(w http.ResponseWriter, reason string, extra map[string]any) {
	body := map[string]any{"error": "refused", "reason": reason}
	for k, v := range extra {
		body[k] = v
	}
	writeJSON(w, 409, body)
}

// cleanMessage keeps text, newlines and spaces: \r\n and \r become \n, a tab
// four spaces (a tab in the box completes a path), other control
// characters go; blank lines at either end go.
func cleanMessage(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteRune(r)
		case r == '\t':
			b.WriteString("    ")
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
		default:
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "\n")
}

// clearKeys empties a box holding up to lines lines, from wherever the
// cursor is: Ctrl-U to the start and Ctrl-K to the end, 2n-1 of each
// (Orca's buildAgentTuiClearInput).
func clearKeys(lines int) []string {
	n := max(1, min(clearMaxLines, lines))
	reps := 2*n - 1
	keys := make([]string, 0, 2*reps)
	for range reps {
		keys = append(keys, "C-u")
	}
	for range reps {
		keys = append(keys, "C-k")
	}
	return keys
}

// typingSteps is the send-keys argument lists that type text into agent's
// box, and the pause after each.
type step struct {
	args  []string
	pause time.Duration
}

func typingSteps(agent, text string) []step {
	var steps []step
	line := !strings.Contains(text, "\n")
	if line && strings.HasPrefix(text, "/") {
		// The command a key at a time; its arguments, if any, at once.
		cmd, rest, spaced := strings.Cut(text, " ")
		for _, r := range cmd {
			steps = append(steps, step{[]string{"-l", "--", string(r)}, keyEvery})
		}
		if spaced {
			steps = append(steps, step{[]string{"-l", "--", " " + rest}, keyEvery})
		}
		return steps
	}
	nl := newlineKey[agent]
	if nl == "" {
		nl = "M-Enter"
	}
	for i, l := range strings.Split(text, "\n") {
		if i > 0 {
			steps = append(steps, step{[]string{nl}, 0})
		}
		if l != "" {
			steps = append(steps, step{[]string{"-l", "--", l}, 0})
		}
	}
	return steps
}

// mark is where the transcript ended when a send began.
type mark struct {
	path string
	off  int64
}

func markOf(path string) mark {
	if path == "" {
		return mark{}
	}
	fh, err := os.Open(path)
	if err != nil {
		return mark{}
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return mark{}
	}
	return mark{path: path, off: lineEnd(fh, st.Size())}
}

// sent reads what the agent added since m for the message a send typed: a
// prompt (a user record with text), or a command it ran (/compact, !ls).
type watcher struct {
	agent string
	m     mark
	fol   *Follower
	dec   decoder
}

func (wt *watcher) look(path string) string {
	if path == "" {
		return ""
	}
	if wt.fol == nil || wt.fol.path != path {
		off := int64(0)
		if path == wt.m.path {
			off = wt.m.off
		}
		f, err := NewFollower(path, off)
		if err != nil {
			return ""
		}
		wt.fol, wt.dec = f, newDecoder(wt.agent)
	}
	// An agent whose conversation stui does not read: its hooks say the
	// prompt landed (confirm), not its file.
	if wt.dec == nil {
		return ""
	}
	lines, err := wt.fol.Read()
	if err != nil {
		wt.fol = nil
		return ""
	}
	for _, l := range lines {
		msgs, _ := wt.dec.decode(l.Data, l.Off)
		for _, m := range msgs {
			for _, b := range m.Blocks {
				if (m.Role == RoleUser && b.Type == BlockText) || (m.Role == RoleSystem && b.Type == BlockCommand) {
					return m.ID
				}
			}
		}
	}
	return ""
}

type sendBody struct {
	Text  *string `json:"text"`
	Mode  string  `json:"mode"`
	Force bool    `json:"force"`
	// Replace is stui's own line, typed before and left in the box unsent
	// (an agent that was not ready for Enter): that, and only that, may be
	// typed over. Anything else in the box is still busy-input.
	Replace string `json:"-"`
}

// SameText is whether what the input box shows is text, whatever the box
// did to its spaces and line breaks when it wrapped it.
func SameText(box, text string) bool {
	squeeze := func(s string) string { return strings.Join(strings.Fields(s), "") }
	b := squeeze(box)
	return b != "" && b == squeeze(text)
}

func (s *Service) send(w http.ResponseWriter, r *http.Request, pane string) {
	var body sendBody
	raw, err := io.ReadAll(io.LimitReader(r.Body, 256<<10))
	if err != nil || json.Unmarshal(raw, &body) != nil || body.Text == nil {
		writeJSON(w, 400, map[string]any{"error": "bad body"})
		return
	}
	if own, _ := r.Context().Value(replaceKey{}).(string); own != "" {
		body.Replace = own
	}
	mode := body.Mode
	if mode == "" {
		mode = "send"
	}
	if mode != "send" && mode != "type_only" {
		writeJSON(w, 400, map[string]any{"error": "bad mode"})
		return
	}
	text := cleanMessage(*body.Text)
	if strings.TrimSpace(text) == "" {
		writeJSON(w, 400, map[string]any{"error": "empty"})
		return
	}
	if utf8.RuneCountInString(text) > MaxSend {
		writeJSON(w, 413, map[string]any{"error": "too long", "max": MaxSend})
		return
	}
	src, moved, ok := s.o.Source(pane)
	switch {
	case !ok || src.Agent == "":
		refuse(w, NoAgent, nil)
		return
	case !CanSend(src.Agent):
		refuse(w, UnsupportedAgent, map[string]any{"agent": src.Agent})
		return
	case moved != "":
		refuse(w, Moved, map[string]any{"movedTo": moved})
		return
	}
	agent := src.Agent

	if !s.lockPane(pane) {
		refuse(w, Sending, nil)
		return
	}
	locked := true
	unlock := func() {
		if locked {
			locked = false
			s.unlockPane(pane)
		}
	}
	defer unlock()

	if s.o.Pending != nil && s.o.Pending(pane) {
		refuse(w, PromptPending, nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if !s.leaveCopyMode(ctx, pane) {
		refuse(w, PaneGone, nil)
		return
	}
	screen, err := s.tmux(ctx, "capture-pane", "-p", "-e", "-t", pane)
	if err != nil {
		refuse(w, PaneGone, nil)
		return
	}
	box := ReadInput(screen, agent)
	if !box.Found {
		// A full-screen dialog, a menu, something stui cannot read: keys
		// there would land in it.
		refuse(w, NoInput, nil)
		return
	}
	if box.Text != "" && !body.Force && !(body.Replace != "" && SameText(box.Text, body.Replace)) {
		refuse(w, BusyInput, map[string]any{"typed": box.Text})
		return
	}
	// A question may have come up while the screen was read.
	if s.o.Pending != nil && s.o.Pending(pane) {
		refuse(w, PromptPending, nil)
		return
	}

	working := s.o.Working != nil && s.o.Working(pane)
	res := s.resolve(pane)
	m := markOf(res.path)
	start := time.Now()

	send := func(args ...string) bool {
		_, err := s.tmux(ctx, append([]string{"send-keys", "-t", pane}, args...)...)
		return err == nil
	}
	// The clear, on its own. With nothing typed it takes away what the
	// screen could not show (a half-typed key sequence); with force, all
	// the lines the person had.
	lines := 1
	if box.Text != "" {
		lines = strings.Count(box.Text, "\n") + 1
	}
	if !send(clearKeys(lines + clearSlack)...) {
		refuse(w, PaneGone, nil)
		return
	}
	time.Sleep(clearSettle)
	for _, st := range typingSteps(agent, text) {
		if !send(st.args...) {
			// Part of it may be in the box: the person sees it there.
			writeJSON(w, 200, map[string]any{"delivered": "unknown", "error": "typing failed"})
			return
		}
		if st.pause > 0 {
			time.Sleep(st.pause)
		}
	}
	if mode == "type_only" {
		writeJSON(w, 200, map[string]any{"typed": true})
		return
	}
	time.Sleep(enterAfter)
	if !send("Enter") {
		writeJSON(w, 200, map[string]any{"delivered": "unknown", "error": "enter failed"})
		return
	}
	// The keys are typed: the next write may go while this one is
	// confirmed.
	unlock()

	out := s.confirm(r.Context(), pane, agent, m, start)
	if out["delivered"] == "unknown" && working {
		// A message typed while the agent works waits in its queue, and is
		// written down when the agent takes it up.
		out["working"] = true
	}
	writeJSON(w, 200, out)
}

// confirm watches for what a send typed to arrive.
func (s *Service) confirm(ctx context.Context, pane, agent string, m mark, start time.Time) map[string]any {
	wt := &watcher{agent: agent, m: m}
	deadline := start.Add(confirmWithin)
	var hookAt time.Time
	for {
		res := s.resolve(pane)
		if id := wt.look(res.path); id != "" {
			return map[string]any{"delivered": true, "messageId": id}
		}
		if hookAt.IsZero() && s.o.Submitted != nil && s.o.Submitted(pane).After(start) {
			hookAt = time.Now()
		}
		if !hookAt.IsZero() && (res.path == "" || time.Since(hookAt) >= idAfterHook) {
			return map[string]any{"delivered": true}
		}
		if time.Now().After(deadline) {
			if !hookAt.IsZero() {
				return map[string]any{"delivered": true}
			}
			return map[string]any{"delivered": "unknown"}
		}
		select {
		case <-ctx.Done():
			return map[string]any{"delivered": "unknown"}
		case <-time.After(confirmPoll):
		}
	}
}

// leaveCopyMode takes the pane out of tmux's copy mode, where keys would
// scroll instead of reaching the agent. false: the pane is gone.
func (s *Service) leaveCopyMode(ctx context.Context, pane string) bool {
	out, err := s.tmux(ctx, "display-message", "-p", "-t", pane, "#{pane_in_mode}")
	if err != nil {
		return false
	}
	if strings.TrimSpace(out) == "1" {
		s.tmux(ctx, "send-keys", "-t", pane, "-X", "cancel")
	}
	return true
}

func (s *Service) stop(w http.ResponseWriter, r *http.Request, pane string) {
	src, _, ok := s.o.Source(pane)
	if !ok || src.Agent == "" {
		refuse(w, NoAgent, nil)
		return
	}
	keys := stopKeys[src.Agent]
	if keys == nil {
		refuse(w, UnsupportedAgent, map[string]any{"agent": src.Agent})
		return
	}
	if !s.lockPane(pane) {
		refuse(w, Sending, nil)
		return
	}
	defer s.unlockPane(pane)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	working := s.o.Working != nil && s.o.Working(pane)
	if !s.leaveCopyMode(ctx, pane) {
		refuse(w, PaneGone, nil)
		return
	}
	for i, k := range keys {
		if i > 0 {
			time.Sleep(stopGap)
		}
		if _, err := s.tmux(ctx, "send-keys", "-t", pane, k); err != nil {
			refuse(w, PaneGone, nil)
			return
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true, "keys": keys, "working": working})
}

// RefusalWords says why a message was not typed, from Deliver's answer: what
// a person reads when stui worker start or send says it could not, rather than
// "409 refused".
func RefusalWords(code int, res map[string]any) string {
	reason, _ := res["reason"].(string)
	switch reason {
	case BusyInput:
		typed, _ := res["typed"].(string)
		typed = strings.ReplaceAll(typed, "\n", " ")
		if r := []rune(typed); len(r) > 60 {
			typed = string(r[:60]) + "…"
		}
		return fmt.Sprintf("its input box has text in it (%q), which stui does not type over", typed)
	case NoInput:
		return "no input box on its screen: a trust, login or other full-screen prompt there?"
	case PromptPending:
		return "it is asking something first"
	case Sending:
		return "another message is being typed into it"
	case PaneGone:
		return "its pane is gone"
	case NoAgent:
		return "no agent runs in that pane"
	}
	if e, ok := res["error"].(string); ok && e != "" {
		if reason != "" {
			return fmt.Sprintf("%d %s (%s)", code, e, reason)
		}
		return fmt.Sprintf("%d %s", code, e)
	}
	return strconv.Itoa(code)
}
