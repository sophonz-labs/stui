package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type call struct {
	args []string
	at   time.Time
}

// fakeTmux plays the pane: it answers capture-pane with screen and
// remembers every command, with when it came.
type fakeTmux struct {
	mu     sync.Mutex
	calls  []call
	screen string
	inMode bool
	gone   bool
	// Runs on Enter (the agent writing the prompt down).
	onEnter func()
	// Holds every send-keys until closed.
	hold chan struct{}
}

func (f *fakeTmux) run(ctx context.Context, args ...string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, call{args: append([]string(nil), args...), at: time.Now()})
	screen, inMode, gone, onEnter, hold := f.screen, f.inMode, f.gone, f.onEnter, f.hold
	f.mu.Unlock()
	if gone {
		return "", errors.New("can't find pane")
	}
	switch args[0] {
	case "display-message":
		if inMode {
			return "1\n", nil
		}
		return "0\n", nil
	case "capture-pane":
		return screen, nil
	case "send-keys":
		if hold != nil {
			<-hold
		}
		if args[len(args)-1] == "Enter" && onEnter != nil {
			go onEnter()
		}
		if len(args) > 3 && args[3] == "-X" {
			f.mu.Lock()
			f.inMode = false
			f.mu.Unlock()
		}
	}
	return "", nil
}

// keys is the send-keys calls, without "send-keys -t <pane>".
func (f *fakeTmux) keys() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]string
	for _, c := range f.calls {
		if c.args[0] == "send-keys" {
			out = append(out, c.args[3:])
		}
	}
	return out
}

func (f *fakeTmux) keyCalls() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []call
	for _, c := range f.calls {
		if c.args[0] == "send-keys" {
			out = append(out, c)
		}
	}
	return out
}

type sendRig struct {
	mu        sync.Mutex
	src       Source
	ok        bool
	moved     string
	pending   bool
	working   bool
	owner     bool
	submitted time.Time
	tmux      *fakeTmux
	svc       *Service
	srv       *httptest.Server
}

const claudeBox = "\x1b[38;5;244m────────────────────────────────────────\n\x1b[39m❯ \x1b[2mTry \"edit <filepath> to...\"\x1b[0m\n\x1b[38;5;244m────────────────────────────────────────\n\x1b[39m  ⏵⏵ auto mode on\n"
const codexBox = "\x1b[1m›\x1b[0m \x1b[2mAsk Codex to do anything\x1b[0m\n\n  \x1b[1m?\x1b[0m for shortcuts\n"

func newSendRig(t *testing.T, agent string) *sendRig {
	t.Helper()
	keyEvery, clearSettle, enterAfter = 16*time.Millisecond, 5*time.Millisecond, 30*time.Millisecond
	confirmWithin, confirmPoll, idAfterHook = 400*time.Millisecond, 10*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() {
		keyEvery, clearSettle, enterAfter = 16*time.Millisecond, 40*time.Millisecond, 120*time.Millisecond
		confirmWithin, confirmPoll, idAfterHook = 5*time.Second, 100*time.Millisecond, 1500*time.Millisecond
	})
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	if agent == "claude" {
		os.WriteFile(path, []byte(userRec("u1", "earlier")+asstRec("a1", "ok", "end_turn")), 0o600)
	}
	r := &sendRig{ok: true, owner: true, tmux: &fakeTmux{}}
	r.src = Source{Agent: agent, SessionID: "s", Path: path}
	if agent == "claude" {
		r.tmux.screen = claudeBox
	} else {
		r.tmux.screen = codexBox
		r.src.Path = ""
	}
	r.svc = New(Options{
		Source: func(string) (Source, string, bool) {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.src, r.moved, r.ok
		},
		IsOwner: func(*http.Request) bool {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.owner
		},
		Tmux: r.tmux.run,
		Pending: func(string) bool {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.pending
		},
		Submitted: func(string) time.Time {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.submitted
		},
		Working: func(string) bool {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.working
		},
	})
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !r.svc.ServeHTTP(w, req) {
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *sendRig) post(t *testing.T, path string, body any) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if s, ok := body.(string); ok {
		buf.WriteString(s)
	} else if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	res, err := http.Post(r.srv.URL+path, "application/json", &buf)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func (r *sendRig) appendRecord(rec string) {
	fh, _ := os.OpenFile(r.src.Path, os.O_APPEND|os.O_WRONLY, 0o600)
	fh.WriteString(rec)
	fh.Close()
}

func joinKeys(ks [][]string) string {
	var parts []string
	for _, k := range ks {
		parts = append(parts, strings.Join(k, " "))
	}
	return strings.Join(parts, " | ")
}

func clearBurst(lines int) string { return strings.Join(clearKeys(lines), " ") }

func TestSendTypesClearsAndConfirms(t *testing.T) {
	r := newSendRig(t, "claude")
	r.tmux.onEnter = func() {
		time.Sleep(20 * time.Millisecond)
		r.appendRecord(userRec("u-new", "hello there"))
	}
	status, out := r.post(t, "/api/chat/send?pane=%253", map[string]any{"text": "hello there"})
	if status != 200 || out["delivered"] != true || out["messageId"] != "u-new" {
		t.Fatalf("%d %v", status, out)
	}
	want := clearBurst(1+clearSlack) + " | -l -- hello there | Enter"
	if got := joinKeys(r.tmux.keys()); got != want {
		t.Fatalf("keys\n got %s\nwant %s", got, want)
	}
	// The clear, the text and Enter are writes of their own, Enter after a
	// pause.
	kc := r.tmux.keyCalls()
	if gap := kc[2].at.Sub(kc[1].at); gap < enterAfter {
		t.Fatalf("enter %v after the text", gap)
	}
	if kc[0].args[2] != "%3" {
		t.Fatalf("pane %v", kc[0].args)
	}
}

func TestSendMultiLinePerAgent(t *testing.T) {
	for agent, nl := range map[string]string{"claude": "M-Enter", "codex": "C-j"} {
		r := newSendRig(t, agent)
		status, _ := r.post(t, "/api/chat/send?pane=%251", map[string]any{"text": "one\r\n\ttwo\n-three\n"})
		if status != 200 {
			t.Fatalf("%s %d", agent, status)
		}
		want := clearBurst(1+clearSlack) + " | -l -- one | " + nl + " | -l --     two | " + nl + " | -l -- -three | Enter"
		if got := joinKeys(r.tmux.keys()); got != want {
			t.Fatalf("%s\n got %s\nwant %s", agent, got, want)
		}
	}
}

func TestSendSlashCommandAKeyAtATime(t *testing.T) {
	r := newSendRig(t, "claude")
	r.tmux.onEnter = func() {
		r.appendRecord(`{"type":"user","uuid":"c1","timestamp":"2026-01-01T00:00:02Z","message":{"role":"user","content":"<command-name>/compact</command-name>\n<command-message>compact</command-message>\n<command-args>keep tests</command-args>"}}` + "\n")
	}
	status, out := r.post(t, "/api/chat/send?pane=%253", map[string]any{"text": "/compact keep tests"})
	if status != 200 || out["messageId"] != "c1" {
		t.Fatalf("%d %v", status, out)
	}
	ks := r.tmux.keys()
	want := clearBurst(1+clearSlack) + " | -l -- / | -l -- c | -l -- o | -l -- m | -l -- p | -l -- a | -l -- c | -l -- t | -l --  keep tests | Enter"
	if got := joinKeys(ks); got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
	kc := r.tmux.keyCalls()
	for i := 2; i <= 8; i++ {
		if gap := kc[i].at.Sub(kc[i-1].at); gap < keyEvery {
			t.Fatalf("key %d only %v after the one before", i, gap)
		}
	}
}

func TestSendRefusesOverAPrompt(t *testing.T) {
	r := newSendRig(t, "claude")
	r.pending = true
	status, out := r.post(t, "/api/chat/send?pane=%253", map[string]any{"text": "hi"})
	if status != 409 || out["reason"] != PromptPending {
		t.Fatalf("%d %v", status, out)
	}
	if len(r.tmux.keys()) != 0 {
		t.Fatal("typed over a prompt")
	}
}

func TestSendBusyInputThenForce(t *testing.T) {
	r := newSendRig(t, "claude")
	r.tmux.screen = "\x1b[38;5;244m" + strings.Repeat("─", 40) + "\n\x1b[39m❯ half a thought\n  and more\n\x1b[38;5;244m" + strings.Repeat("─", 40) + "\n"
	status, out := r.post(t, "/api/chat/send?pane=%253", map[string]any{"text": "hi"})
	if status != 409 || out["reason"] != BusyInput || out["typed"] != "half a thought\nand more" {
		t.Fatalf("%d %v", status, out)
	}
	if len(r.tmux.keys()) != 0 {
		t.Fatal("typed onto the desk's text")
	}
	status, out = r.post(t, "/api/chat/send?pane=%253", map[string]any{"text": "hi", "force": true})
	if status != 200 {
		t.Fatalf("%d %v", status, out)
	}
	ks := r.tmux.keys()
	if got := strings.Join(ks[0], " "); got != clearBurst(2+clearSlack) {
		t.Fatalf("clear %s", got)
	}
	if got := strings.Join(ks[1], " "); got != "-l -- hi" {
		t.Fatalf("text %s", got)
	}
}

func TestSendNoInputBox(t *testing.T) {
	r := newSendRig(t, "claude")
	r.tmux.screen = "Select a model\n❯ 1. Opus\n  2. Sonnet\n"
	status, out := r.post(t, "/api/chat/send?pane=%253", map[string]any{"text": "hi"})
	if status != 409 || out["reason"] != NoInput {
		t.Fatalf("%d %v", status, out)
	}
}

func TestSendOneWriteAtATime(t *testing.T) {
	r := newSendRig(t, "claude")
	hold := make(chan struct{})
	r.tmux.hold = hold
	done := make(chan int)
	go func() {
		s, _ := r.post(t, "/api/chat/send?pane=%253", map[string]any{"text": "first"})
		done <- s
	}()
	// Wait for the first to be typing.
	for len(r.tmux.keyCalls()) == 0 {
		time.Sleep(2 * time.Millisecond)
	}
	status, out := r.post(t, "/api/chat/send?pane=%253", map[string]any{"text": "second"})
	if status != 409 || out["reason"] != Sending {
		t.Fatalf("%d %v", status, out)
	}
	status, out = r.post(t, "/api/chat/stop?pane=%253", nil)
	if status != 409 || out["reason"] != Sending {
		t.Fatalf("stop %d %v", status, out)
	}
	// Another pane is not held up.
	r.tmux.mu.Lock()
	r.tmux.hold = nil
	r.tmux.mu.Unlock()
	status, _ = r.post(t, "/api/chat/send?pane=%254", map[string]any{"text": "elsewhere"})
	if status != 200 {
		t.Fatalf("other pane %d", status)
	}
	close(hold)
	if s := <-done; s != 200 {
		t.Fatalf("first %d", s)
	}
	// Free again.
	status, _ = r.post(t, "/api/chat/send?pane=%253", map[string]any{"text": "third"})
	if status != 200 {
		t.Fatalf("third %d", status)
	}
}

func TestSendUnknownIsNeverRetried(t *testing.T) {
	r := newSendRig(t, "claude")
	r.working = true
	start := time.Now()
	status, out := r.post(t, "/api/chat/send?pane=%253", map[string]any{"text": "hi"})
	if status != 200 || out["delivered"] != "unknown" || out["working"] != true {
		t.Fatalf("%d %v", status, out)
	}
	if time.Since(start) < confirmWithin {
		t.Fatal("gave up early")
	}
	enters := 0
	for _, k := range r.tmux.keys() {
		if k[len(k)-1] == "Enter" {
			enters++
		}
	}
	if enters != 1 {
		t.Fatalf("%d Enters", enters)
	}
}

func TestSendConfirmedByTheHook(t *testing.T) {
	r := newSendRig(t, "codex") // no transcript path
	r.tmux.onEnter = func() {
		r.mu.Lock()
		r.submitted = time.Now()
		r.mu.Unlock()
	}
	status, out := r.post(t, "/api/chat/send?pane=%253", map[string]any{"text": "hi"})
	if status != 200 || out["delivered"] != true || out["messageId"] != nil {
		t.Fatalf("%d %v", status, out)
	}
}

func TestSendAfterClearReadsTheNewTranscript(t *testing.T) {
	r := newSendRig(t, "claude")
	next := filepath.Join(filepath.Dir(r.src.Path), "t.jsonl")
	r.tmux.onEnter = func() {
		os.WriteFile(next, []byte(`{"type":"user","uuid":"c2","message":{"role":"user","content":"<command-name>/clear</command-name>\n<command-message>clear</command-message>\n<command-args></command-args>"}}`+"\n"), 0o600)
		r.mu.Lock()
		r.src.Path = next
		r.mu.Unlock()
	}
	status, out := r.post(t, "/api/chat/send?pane=%253", map[string]any{"text": "/clear"})
	if status != 200 || out["messageId"] != "c2" {
		t.Fatalf("%d %v", status, out)
	}
}

func TestSendTypeOnly(t *testing.T) {
	r := newSendRig(t, "claude")
	status, out := r.post(t, "/api/chat/send?pane=%253", map[string]any{"text": "draft", "mode": "type_only"})
	if status != 200 || out["typed"] != true {
		t.Fatalf("%d %v", status, out)
	}
	for _, k := range r.tmux.keys() {
		if k[len(k)-1] == "Enter" {
			t.Fatal("sent")
		}
	}
}

func TestSendLeavesCopyMode(t *testing.T) {
	r := newSendRig(t, "claude")
	r.tmux.inMode = true
	r.post(t, "/api/chat/send?pane=%253", map[string]any{"text": "hi"})
	if got := strings.Join(r.tmux.keys()[0], " "); got != "-X cancel" {
		t.Fatalf("first keys %s", got)
	}
}

func TestSendRefusals(t *testing.T) {
	r := newSendRig(t, "claude")
	cases := []struct {
		name   string
		setup  func()
		path   string
		body   any
		status int
		reason string
	}{
		{"bad pane", nil, "/api/chat/send?pane=3", map[string]any{"text": "x"}, 400, ""},
		{"no text", nil, "/api/chat/send?pane=%253", map[string]any{}, 400, ""},
		{"not json", nil, "/api/chat/send?pane=%253", "text=x", 400, ""},
		{"blank", nil, "/api/chat/send?pane=%253", map[string]any{"text": " \n\t\x01"}, 400, ""},
		{"bad mode", nil, "/api/chat/send?pane=%253", map[string]any{"text": "x", "mode": "shout"}, 400, ""},
		{"too long", nil, "/api/chat/send?pane=%253", map[string]any{"text": strings.Repeat("é", MaxSend+1)}, 413, ""},
		{"no agent", func() { r.ok = false }, "/api/chat/send?pane=%253", map[string]any{"text": "x"}, 409, NoAgent},
		{"psmux pane", func() { r.ok = false }, "/api/chat/send?pane=work:%253", map[string]any{"text": "x"}, 409, NoAgent},
		{"gemini", func() { r.src.Agent = "gemini" }, "/api/chat/send?pane=%253", map[string]any{"text": "x"}, 409, UnsupportedAgent},
		{"moved", func() { r.moved = "%5" }, "/api/chat/send?pane=%253", map[string]any{"text": "x"}, 409, Moved},
		{"gone", func() { r.tmux.gone = true }, "/api/chat/send?pane=%253", map[string]any{"text": "x"}, 409, PaneGone},
		{"read-only", func() { r.owner = false }, "/api/chat/send?pane=%253", map[string]any{"text": "x"}, 403, ""},
		{"read-only stop", func() { r.owner = false }, "/api/chat/stop?pane=%253", nil, 403, ""},
	}
	for _, c := range cases {
		r.mu.Lock()
		r.ok, r.moved, r.owner, r.src.Agent = true, "", true, "claude"
		r.tmux.gone = false
		if c.setup != nil {
			c.setup()
		}
		r.mu.Unlock()
		status, out := r.post(t, c.path, c.body)
		if status != c.status || (c.reason != "" && out["reason"] != c.reason) {
			t.Errorf("%s: %d %v", c.name, status, out)
		}
	}
	if len(r.tmux.keys()) != 0 {
		t.Fatalf("typed: %v", r.tmux.keys())
	}
	// GET is not a send.
	res, _ := http.Get(r.srv.URL + "/api/chat/send?pane=%253")
	if res.StatusCode != 405 {
		t.Fatalf("GET %d", res.StatusCode)
	}
	// POST is not a read.
	if s, _ := r.post(t, "/api/chat?pane=%253", nil); s != 405 {
		t.Fatalf("POST /api/chat %d", s)
	}
}

func TestStop(t *testing.T) {
	r := newSendRig(t, "claude")
	r.working = true
	status, out := r.post(t, "/api/chat/stop?pane=%253", nil)
	if status != 200 || out["ok"] != true || out["working"] != true {
		t.Fatalf("%d %v", status, out)
	}
	// One Escape, and only that: a second opens Claude's rewind menu.
	if got := joinKeys(r.tmux.keys()); got != "Escape" {
		t.Fatalf("keys %s", got)
	}
	r2 := newSendRig(t, "codex")
	r2.tmux.inMode = true
	r2.post(t, "/api/chat/stop?pane=%253", nil)
	if got := joinKeys(r2.tmux.keys()); got != "-X cancel | Escape" {
		t.Fatalf("codex keys %s", got)
	}
	r2.src.Agent = "aider"
	if s, out := r2.post(t, "/api/chat/stop?pane=%253", nil); s != 409 || out["reason"] != UnsupportedAgent {
		t.Fatalf("aider %d %v", s, out)
	}
	// opencode: two Escapes, apart. Sent together, opencode 1.19.4 stopped
	// nothing.
	r3 := newSendRig(t, "opencode")
	r3.post(t, "/api/chat/stop?pane=%253", nil)
	calls := r3.tmux.keyCalls()
	if got := joinKeys(r3.tmux.keys()); got != "Escape | Escape" || len(calls) != 2 {
		t.Fatalf("opencode keys %s", got)
	}
	if gap := calls[1].at.Sub(calls[0].at); gap < stopGap {
		t.Errorf("Escapes %v apart, want %v", gap, stopGap)
	}
	// Qwen and Kimi: what stops them has not been seen.
	for _, a := range []string{"qwen", "kimi"} {
		r3.src.Agent = a
		if s, out := r3.post(t, "/api/chat/stop?pane=%253", nil); s != 409 || out["reason"] != UnsupportedAgent {
			t.Errorf("%s %d %v", a, s, out)
		}
	}
}

// opencode's conversation is not one stui reads, so its hook says the prompt
// landed: a send is delivered without a transcript, and with one stui has no
// decoder for (nothing to panic on).
func TestSendOpencode(t *testing.T) {
	screen, err := os.ReadFile("testdata/input/opencode-1.19.4-conv-idle.ansi")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", filepath.Join(t.TempDir(), "x.jsonl")} {
		r := newSendRig(t, "opencode")
		r.tmux.screen = string(screen)
		r.src.Path = path
		if path != "" {
			os.WriteFile(path, []byte("{\"not\": \"a format stui reads\"}\n"), 0o600)
		}
		go func() {
			time.Sleep(60 * time.Millisecond)
			r.mu.Lock()
			r.submitted = time.Now()
			r.mu.Unlock()
		}()
		status, out := r.post(t, "/api/chat/send?pane=%253", map[string]any{"text": "first\nsecond"})
		if status != 200 || out["delivered"] != true {
			t.Fatalf("path %q: %d %v", path, status, out)
		}
		// Its new line is Alt-Enter.
		if got := joinKeys(r.tmux.keys()); !strings.Contains(got, "-l -- first | M-Enter | -l -- second | Enter") {
			t.Errorf("keys %s", got)
		}
	}
	// Text in its box is not typed over.
	typed, _ := os.ReadFile("testdata/input/opencode-1.19.4-conv-typed.ansi")
	r := newSendRig(t, "opencode")
	r.tmux.screen = string(typed)
	if status, out := r.post(t, "/api/chat/send?pane=%253", map[string]any{"text": "x"}); status != 409 || out["reason"] != BusyInput {
		t.Errorf("%d %v", status, out)
	}
}

func TestTypedAndReadable(t *testing.T) {
	for a, want := range map[string][2]bool{
		"claude": {true, true}, "codex": {true, true}, "opencode": {true, false}, "pi": {true, false},
		"gemini": {false, false}, "qwen": {false, false}, "kimi": {false, false}, "aider": {false, false},
	} {
		if got := [2]bool{CanSend(a), Readable(a)}; got != want {
			t.Errorf("%s: send, read = %v, want %v", a, got, want)
		}
	}
	if w := TypedWords(); w != "claude, codex, opencode or pi" {
		t.Errorf("%q", w)
	}
}

func TestNoTypingWithoutTmux(t *testing.T) {
	svc := New(Options{Source: func(string) (Source, string, bool) { return Source{Agent: "claude"}, "", true }})
	if svc.Level() != 1 {
		t.Fatal("level")
	}
	req := httptest.NewRequest("POST", "/api/chat/send?pane=%253", strings.NewReader(`{"text":"x"}`))
	if svc.ServeHTTP(httptest.NewRecorder(), req) {
		t.Fatal("served a send it cannot type")
	}
	if newSendRig(t, "claude").svc.Level() != 2 {
		t.Fatal("level 2")
	}
}
