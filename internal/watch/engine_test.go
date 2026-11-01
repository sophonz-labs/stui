package watch

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sophonz-labs/stui/internal/hookclient"
	agentstate "github.com/sophonz-labs/stui/internal/state"
	"github.com/sophonz-labs/stui/internal/stream"
)

// fakeTmux is a tmux server with panes whose screens the test sets, and
// which records what stui sends. onKeys lets a test change the screen when
// keys arrive, as the agent would.
type fakeTmux struct {
	mu      sync.Mutex
	panes   string // list-panes output
	screens map[string]string
	sent    [][]string
	onKeys  func(pane string, keys []string)
}

func (f *fakeTmux) Run(ctx context.Context, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch args[0] {
	case "list-panes":
		return f.panes, nil
	case "list-sessions":
		return "work|~|\n", nil
	case "capture-pane":
		s, ok := f.screens[args[len(args)-1]]
		if !ok {
			return "", errors.New("no pane")
		}
		return s, nil
	case "send-keys":
		f.sent = append(f.sent, args)
		if f.onKeys != nil {
			pane, keys := args[2], args[3:]
			f.mu.Unlock()
			f.onKeys(pane, keys)
			f.mu.Lock()
		}
	}
	return "", nil
}

func (f *fakeTmux) setScreen(pane, s string) {
	f.mu.Lock()
	f.screens[pane] = s
	f.mu.Unlock()
}

func (f *fakeTmux) keys() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string{}, f.sent...)
}

type sinkEvent struct {
	typ    string
	id     string
	reason string
	dur    int64
	result string
	prompt Prompt
	sub    TurnSubagents
	// turn-delegated
	running int
}

type fakeSink struct {
	mu     sync.Mutex
	events []sinkEvent
}

func (s *fakeSink) add(e sinkEvent)                     { s.mu.Lock(); s.events = append(s.events, e); s.mu.Unlock() }
func (s *fakeSink) StateChanged([]SessionSummary, Pane) {}
func (s *fakeSink) PromptOpened(p Pane, pr Prompt) {
	s.add(sinkEvent{typ: "prompt-opened", id: pr.ID, prompt: pr})
}
func (s *fakeSink) PromptCleared(p Pane, id, reason string) {
	s.add(sinkEvent{typ: "prompt-cleared", id: id, reason: reason})
}
func (s *fakeSink) TurnDone(p Pane, d int64, result string, sub TurnSubagents) {
	s.add(sinkEvent{typ: "turn-done", dur: d, result: result, sub: sub})
}
func (s *fakeSink) TurnDelegated(p Pane, running int, turnMs int64) {
	s.add(sinkEvent{typ: "turn-delegated", dur: turnMs, running: running})
}
func (s *fakeSink) of(typ string) []sinkEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []sinkEvent
	for _, e := range s.events {
		if e.typ == typ {
			out = append(out, e)
		}
	}
	return out
}

func setup(t *testing.T, command string) (*Watcher, *fakeTmux, *fakeSink) {
	t.Helper()
	answerWait = 500 * time.Millisecond
	delegateSettle = 100 * time.Millisecond
	doneSettle, doneHoldMax, holdPoll = 60*time.Millisecond, time.Second, 15*time.Millisecond
	settle, settleMax = 20*time.Millisecond, 100*time.Millisecond
	promptGone, promptGoneMax, promptRevive, unsettledMax = 100*time.Millisecond, time.Second, 30*time.Second, 200*time.Millisecond
	f := &fakeTmux{
		panes:   "%1|~|work|~||~|0|~|0|~|" + command + "|~|api|~|Refactor auth\n%2|~|work|~||~|1|~|0|~|zsh|~|sh|~|host\n",
		screens: map[string]string{"%1": screen(t, "claude-idle.txt"), "%2": "$ "},
	}
	sink := &fakeSink{}
	w := New(Options{Tmux: f, Sink: sink, ClaudeDir: t.TempDir()})
	w.poll(context.Background())
	return w, f, sink
}

func pane(t *testing.T, w *Watcher, id string) Pane {
	t.Helper()
	for _, p := range w.Snapshot().Panes {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("no pane %s in %+v", id, w.Snapshot().Panes)
	return Pane{}
}

func hook(w *Watcher, event string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	w.Hook(hookclient.Event{Kind: "claude", Pane: "%1", Event: event, Data: data})
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPollFindsAgents(t *testing.T) {
	w, _, _ := setup(t, "2.1.283")
	snap := w.Snapshot()
	if len(snap.Panes) != 1 {
		t.Fatalf("shell pane listed: %+v", snap.Panes)
	}
	p := snap.Panes[0]
	if p.Agent != "claude" || p.State != Idle || p.Session != "work" || p.Title != "Refactor auth" || p.WindowName != "api" {
		t.Fatalf("%+v", p)
	}
	if len(snap.Sessions) != 1 || snap.Sessions[0].Idle != 1 {
		t.Fatalf("%+v", snap.Sessions)
	}
}

// A permission prompt from hooks and the screen, answered from the phone.
func TestPermissionAnsweredFromPhone(t *testing.T) {
	w, f, sink := setup(t, "2.1.283")
	f.onKeys = func(pane string, keys []string) {
		if keys[0] == "1" {
			f.setScreen(pane, screen(t, "claude-working.txt"))
		}
	}
	hook(w, "UserPromptSubmit", nil)
	if pane(t, w, "%1").State != Working {
		t.Fatal("not working")
	}
	f.setScreen("%1", screen(t, "claude-permission-bash.txt"))
	hook(w, "PermissionRequest", map[string]any{"tool_name": "Bash", "tool_summary": "echo hello > greeting.txt && ls -la"})
	eventually(t, "prompt", func() bool { return pane(t, w, "%1").Prompt != nil })
	p := pane(t, w, "%1")
	if p.State != Waiting || p.Prompt.Kind != "permission" || p.Prompt.Tool != "Bash" || p.Turn == nil {
		t.Fatalf("%+v %+v", p, p.Prompt)
	}
	id := p.Prompt.ID
	if len(id) != 24 {
		t.Fatalf("id %q", id)
	}
	if opened := sink.of("prompt-opened"); len(opened) != 1 || opened[0].id != id {
		t.Fatalf("%+v", opened)
	}

	zero := 0
	status, body := w.Answer(id, &zero, nil)
	if status != 200 || body["confirmed"] != true {
		t.Fatalf("%d %v", status, body)
	}
	if k := f.keys(); len(k) != 1 || strings.Join(k[0], " ") != "send-keys -t %1 1" {
		t.Fatalf("%q", k)
	}
	cleared := sink.of("prompt-cleared")
	if len(cleared) != 1 || cleared[0].reason != AnsweredPhone {
		t.Fatalf("%+v", cleared)
	}
	if p := pane(t, w, "%1"); p.Prompt != nil || p.State != Working {
		t.Fatalf("%+v", p)
	}
	// The same answer again is fine; another one is not.
	if status, body := w.Answer(id, &zero, nil); status != 200 || body["already"] != true {
		t.Fatalf("%d %v", status, body)
	}
	two := 2
	if status, body := w.Answer(id, &two, nil); status != 409 || body["reason"] != AnsweredPhone {
		t.Fatalf("%d %v", status, body)
	}
	if len(f.keys()) != 1 {
		t.Fatal("keys sent for a repeated answer")
	}
	if status, _ := w.Answer("0123456789abcdef01234567", &zero, nil); status != 404 {
		t.Fatal(status)
	}
}

// Answered at the desk first: the phone's answer is refused and nothing is
// typed.
func TestAnsweredAtDesk(t *testing.T) {
	w, f, sink := setup(t, "2.1.283")
	hook(w, "UserPromptSubmit", nil)
	f.setScreen("%1", screen(t, "claude-question.txt"))
	hook(w, "PermissionRequest", map[string]any{"tool_name": "AskUserQuestion"})
	eventually(t, "prompt", func() bool { return pane(t, w, "%1").Prompt != nil })
	id := pane(t, w, "%1").Prompt.ID

	// Changed on screen, not yet noticed: stale.
	f.setScreen("%1", screen(t, "claude-working.txt"))
	one := 1
	if status, body := w.Answer(id, &one, nil); status != 409 || body["error"] != "stale" {
		t.Fatalf("%d %v", status, body)
	}
	if len(f.keys()) != 0 {
		t.Fatal("keys sent to a stale prompt")
	}
	if c := sink.of("prompt-cleared"); len(c) != 1 || c[0].reason != AnsweredDesk {
		t.Fatalf("%+v", c)
	}
	if status, body := w.Answer(id, &one, nil); status != 409 || body["reason"] != AnsweredDesk {
		t.Fatalf("%d %v", status, body)
	}
	if p := pane(t, w, "%1"); p.State != Working || p.Prompt != nil {
		t.Fatalf("%+v", p)
	}
}

// The screen moving on without an answer clears the prompt too.
func TestScreenClearsPrompt(t *testing.T) {
	w, f, sink := setup(t, "2.1.283")
	hook(w, "UserPromptSubmit", nil)
	f.setScreen("%1", screen(t, "claude-permission-write.txt"))
	hook(w, "Notification", map[string]any{"notification_type": "permission_prompt"})
	eventually(t, "prompt", func() bool { return pane(t, w, "%1").Prompt != nil })
	f.setScreen("%1", screen(t, "claude-working.txt"))
	w.Output("%1", []byte("x"))
	eventually(t, "cleared", func() bool { return len(sink.of("prompt-cleared")) == 1 })
	if p := pane(t, w, "%1"); p.Prompt != nil || p.State != Working {
		t.Fatalf("%+v", p)
	}
}

func TestTextAnswer(t *testing.T) {
	w, f, _ := setup(t, "2.1.283")
	f.onKeys = func(pane string, keys []string) {
		if keys[0] == "Enter" {
			f.setScreen(pane, screen(t, "claude-working.txt"))
		}
	}
	f.setScreen("%1", screen(t, "claude-question.txt"))
	hook(w, "PermissionRequest", map[string]any{"tool_name": "AskUserQuestion"})
	eventually(t, "prompt", func() bool { return pane(t, w, "%1").Prompt != nil })
	id := pane(t, w, "%1").Prompt.ID
	text := "purple,\nwith a gradient\x1b[31m"
	if status, body := w.Answer(id, nil, &text); status != 200 {
		t.Fatalf("%d %v", status, body)
	}
	var got []string
	for _, k := range f.keys() {
		got = append(got, strings.Join(k[3:], " "))
	}
	want := []string{"4", "-l purple,", "M-Enter", "-l with a gradient[31m", "Enter"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("%q", got)
	}
}

func TestBadAnswers(t *testing.T) {
	pr := Prompt{Kind: "permission", Agent: "claude", Options: []Option{{Label: "Yes", Keys: []string{"1"}}, {Label: "No", Keys: []string{"2"}}}}
	nine, s := 9, "hi"
	if _, ok := answerKeys(pr, &nine, nil); ok {
		t.Fatal("option out of range")
	}
	if _, ok := answerKeys(pr, nil, &s); ok {
		t.Fatal("text where none is allowed")
	}
	zero := 0
	if _, ok := answerKeys(pr, &zero, &s); ok {
		t.Fatal("text on an option without input")
	}
	yn := Prompt{Kind: "text", AllowText: true, Agent: "codex"}
	steps, ok := answerKeys(yn, nil, &s)
	if !ok || len(steps) != 2 || steps[0][1] != "hi" {
		t.Fatalf("%q", steps)
	}
}

func TestTurnsAndSubagents(t *testing.T) {
	w, f, sink := setup(t, "2.1.283")
	hook(w, "UserPromptSubmit", nil)
	hook(w, "SubagentStart", map[string]any{"agent_id": "a"})
	hook(w, "SubagentStart", map[string]any{"agent_id": "b"})
	hook(w, "SubagentStop", map[string]any{"agent_id": "a"})
	hook(w, "SubagentStop", map[string]any{"agent_id": "helper-never-started"})
	if s := pane(t, w, "%1").Subagents; s.Running != 1 || s.Done != 1 {
		t.Fatalf("%+v", s)
	}
	time.Sleep(20 * time.Millisecond)
	hook(w, "Stop", nil)
	// b still runs: the turn goes on in it.
	if p := pane(t, w, "%1"); p.State != Working || p.Phase != PhaseSubagents || p.Turn == nil {
		t.Fatalf("%+v", p)
	}
	if d := waitDelegated(t, sink, 1); len(d) != 1 || d[0].running != 1 || len(sink.of("turn-done")) != 0 {
		t.Fatalf("%+v", sink.events)
	}
	hook(w, "SubagentStop", map[string]any{"agent_id": "b"})
	eventually(t, "done after the settle", func() bool { return state(t, w) == Done })
	p := pane(t, w, "%1")
	if p.State != Done || p.Phase != PhaseMain || p.LastTurnMs == nil || *p.LastTurnMs < 20 || p.Turn != nil {
		t.Fatalf("%+v", p)
	}
	if d := waitDone(t, sink, 1); len(d) != 1 || d[0].result != Done || d[0].dur < 20 || d[0].sub.Done != 2 {
		t.Fatalf("%+v", d)
	}
	hook(w, "SubagentStart", map[string]any{"agent_id": "c"})
	// The spinner Claude still shows just after Stop is not a new turn.
	f.setScreen("%1", screen(t, "claude-working.txt"))
	w.captureNow("%1")
	if pane(t, w, "%1").State != Done {
		t.Fatal("spinner after Stop restarted the turn")
	}
	hook(w, "Notification", map[string]any{"notification_type": "idle_prompt"})
	if pane(t, w, "%1").State != Idle {
		t.Fatal("idle_prompt did not make it idle")
	}
	hook(w, "UserPromptSubmit", nil)
	hook(w, "StopFailure", map[string]any{"error": "429"})
	if pane(t, w, "%1").State != Error {
		t.Fatal("not error")
	}
	hook(w, "SessionEnd", nil)
	if s := pane(t, w, "%1").Subagents; s.Lost != 1 || s.Running != 0 {
		t.Fatalf("%+v", s)
	}
	hook(w, "TeammateIdle", map[string]any{"teammate_name": "tester"})
	if tm := pane(t, w, "%1").Teammates; len(tm) != 1 || tm[0].State != Idle {
		t.Fatalf("%+v", tm)
	}
}

// Codex without hooks: working, asking and done, all from the screen.
func TestCodexFromScreen(t *testing.T) {
	w, f, sink := setup(t, "codex")
	f.setScreen("%1", "• Working (3s • esc to interrupt)\n")
	w.Output("%1", []byte("x"))
	eventually(t, "working", func() bool { return pane(t, w, "%1").State == Working })
	f.setScreen("%1", screen(t, "codex-approval.txt"))
	w.Output("%1", []byte("x"))
	eventually(t, "waiting", func() bool { return pane(t, w, "%1").State == Waiting })
	if pr := pane(t, w, "%1").Prompt; pr == nil || pr.Options[0].Keys[0] != "y" || pr.Agent != "codex" {
		t.Fatalf("%+v", pr)
	}
	f.setScreen("%1", "• Working (5s • esc to interrupt)\n")
	w.Output("%1", []byte("x"))
	eventually(t, "working again", func() bool { return pane(t, w, "%1").State == Working })
	f.setScreen("%1", "› \n")
	w.Output("%1", []byte("x"))
	eventually(t, "done", func() bool { return pane(t, w, "%1").State == Done })
	if len(waitDone(t, sink, 1)) != 1 {
		t.Fatal("no turn-done")
	}
	w.Hook(hookclient.Event{Kind: "codex", Pane: "%1", Event: "agent-turn-complete", Data: map[string]any{}})
	if pane(t, w, "%1").State != Done {
		t.Fatal("codex notify")
	}
}

// A plain program that rings the bell with a y/n on screen gets a prompt.
func TestBellAsks(t *testing.T) {
	w, f, _ := setup(t, "apt")
	f.setScreen("%1", screen(t, "shell-yn.txt"))
	w.Output("%1", []byte("Do you want to continue? [Y/n] \x07"))
	eventually(t, "prompt", func() bool {
		for _, p := range w.Snapshot().Panes {
			if p.ID == "%1" && p.Prompt != nil {
				return p.Prompt.Kind == "confirm" && p.Agent == ""
			}
		}
		return false
	})
}

func TestPaneGoes(t *testing.T) {
	w, f, sink := setup(t, "2.1.283")
	f.setScreen("%1", screen(t, "claude-permission-bash.txt"))
	hook(w, "PermissionRequest", map[string]any{"tool_name": "Bash"})
	eventually(t, "prompt", func() bool { return pane(t, w, "%1").Prompt != nil })
	f.mu.Lock()
	f.panes = ""
	f.mu.Unlock()
	w.poll(context.Background())
	if len(w.Snapshot().Panes) != 0 {
		t.Fatal("pane still listed")
	}
	if c := sink.of("prompt-cleared"); len(c) != 1 || c[0].reason != Expired {
		t.Fatalf("%+v", c)
	}
}

func TestHookFromOtherServer(t *testing.T) {
	f := &fakeTmux{screens: map[string]string{}}
	w := New(Options{Tmux: f, Socket: "/tmp/tmux-501/default", ClaudeDir: t.TempDir()})
	w.Hook(hookclient.Event{Kind: "claude", Pane: "%3", Socket: "/tmp/other/default", Event: "UserPromptSubmit"})
	if len(w.Snapshot().Panes) != 0 {
		t.Fatal("hook from another server taken")
	}
}

func TestOSCScanner(t *testing.T) {
	var s oscScanner
	bell, notes, _ := s.scan([]byte("a\x1b]9;hello\x07b\x1b]777;notify;T;B\x1b\\c\x1b]9;4;1;50\x07"))
	if bell || len(notes) != 2 || notes[0] != "hello" || notes[1] != "T;B" {
		t.Fatalf("%v %q", bell, notes)
	}
	// Split across chunks, and a bare bell.
	s.scan([]byte("\x1b]9;spl"))
	bell, notes, _ = s.scan([]byte("it\x07 \x07"))
	if !bell || len(notes) != 1 || notes[0] != "split" {
		t.Fatalf("%v %q", bell, notes)
	}
	// A title (OSC 2) rings nothing.
	if bell, notes, _ := s.scan([]byte("\x1b]2;title\x07")); bell || len(notes) != 0 {
		t.Fatal("title counted")
	}
}

func TestControlLine(t *testing.T) {
	// As tmux 3.7 wrote it in the sandbox.
	got := unescape(`a\033]9;hello osc9\007b\033]777;notify;T;B\007c\007d\015\012 \\ \134`)
	if string(got) != "a\x1b]9;hello osc9\x07b\x1b]777;notify;T;B\x07c\x07d\r\n \\ \\" {
		t.Fatalf("%q", got)
	}
}

func TestParsePanesDedupesViews(t *testing.T) {
	out := "%1|~|work|~|work|~|0|~|0|~|zsh|~|w|~|t\n%1|~|work__abc123|~|work|~|0|~|0|~|zsh|~|w|~|t\n%2|~|other|~||~|0|~|0|~|zsh|~|w|~|a|b\n"
	rows := parsePanes(out)
	if len(rows) != 2 || rows[0].session != "work" || rows[1].title != "a|b" {
		t.Fatalf("%+v", rows)
	}
	targets := watchTargets("work|~|work\nwork__abc123|~|work\nmine|~|g\nmine2|~|g\nsolo|~|\n")
	if strings.Join(targets, ",") != "mine,solo,work" {
		t.Fatalf("%v", targets)
	}
}

func TestSummarize(t *testing.T) {
	s := Summarize([]Pane{
		{Session: "a", State: Working, Since: 1, Subagents: Subagents{Running: 2}},
		{Session: "a", State: Waiting, Since: 5},
		{Session: "b", State: Done, Since: 3},
	})
	if len(s) != 2 || s[0].State != Waiting || s[0].Since != 5 || s[0].Working != 1 || s[0].Subagents.Running != 2 || s[1].State != Done {
		t.Fatalf("%+v", s)
	}
}

func TestAPI(t *testing.T) {
	stream.Ping = 50 * time.Millisecond
	w, _, _ := setup(t, "2.1.283")
	// As the agent's gate does: the machine's token marks the request.
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Header.Get(agentstate.TokenHeader) == "t" {
			r = agentstate.WithToken(r)
		}
		if !w.ServeHTTP(rw, r) {
			rw.WriteHeader(404)
		}
	}))
	defer srv.Close()

	res, _ := http.Get(srv.URL + "/api/agents")
	var snap Snapshot
	json.NewDecoder(res.Body).Decode(&snap)
	res.Body.Close()
	if snap.V != 1 || len(snap.Panes) != 1 || snap.Panes[0].Teammates == nil {
		t.Fatalf("%+v", snap)
	}

	res, _ = http.Post(srv.URL+"/api/prompts/0123456789abcdef01234567/answer", "application/json", strings.NewReader(`{"option":0}`))
	if res.StatusCode != 404 {
		t.Fatal(res.StatusCode)
	}
	for _, body := range []string{`{}`, `{"option":"x"}`, `nope`} {
		res, _ = http.Post(srv.URL+"/api/prompts/0123456789abcdef01234567/answer", "application/json", strings.NewReader(body))
		if res.StatusCode != 400 {
			t.Fatalf("%s: %d", body, res.StatusCode)
		}
	}

	// The hook route takes stui's own programs only: by the machine's token,
	// not by the address (httptest is loopback, and so is a page in a
	// browser here).
	b, _ := json.Marshal(hookclient.Event{Kind: "claude", Pane: "%1", Event: "UserPromptSubmit", Data: map[string]any{}})
	hook := func(token, proxied bool) int {
		req, _ := http.NewRequest("POST", srv.URL+"/api/hook", strings.NewReader(string(b)))
		if token {
			req.Header.Set(agentstate.TokenHeader, "t")
		}
		if proxied {
			req.Header.Set("cf-connecting-ip", "1.2.3.4")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode
	}
	if code := hook(false, false); code != 403 {
		t.Fatalf("a loopback hook without the token: %d", code)
	}
	if code := hook(true, true); code != 403 {
		t.Fatalf("hook through a proxy: %d", code)
	}

	// The stream: a snapshot first, then the change a hook makes.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/agents/stream", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.Header.Get("content-type") != "text/event-stream; charset=utf-8" {
		t.Fatalf("%v %v", err, res.Header)
	}
	r := bufio.NewReader(res.Body)
	readEvent := func() (string, string) {
		var name, data string
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			line = strings.TrimRight(line, "\n")
			switch {
			case strings.HasPrefix(line, "event: "):
				name = line[7:]
			case strings.HasPrefix(line, "data: "):
				data = line[6:]
			case line == "" && name != "":
				return name, data
			}
		}
	}
	if name, _ := readEvent(); name != "snapshot" {
		t.Fatal(name)
	}
	if code := hook(true, false); code != 200 {
		t.Fatalf("hook with the token: %d", code)
	}
	name, data := readEvent()
	var p Pane
	json.Unmarshal([]byte(data), &p)
	if name != "pane" || p.State != Working {
		t.Fatalf("%s %s", name, data)
	}
	if name, _ := readEvent(); name != "sessions" {
		t.Fatal(name)
	}
}

func TestPaneResumeFromHooks(t *testing.T) {
	f := &fakeTmux{screens: map[string]string{}}
	w := New(Options{Tmux: f, ClaudeDir: t.TempDir()})
	w.Hook(hookclient.Event{Kind: "claude", Pane: "%3", Event: "SessionStart", Data: map[string]any{"session_id": "be3612de"}})
	// A subagent's event does not name the conversation to resume.
	w.Hook(hookclient.Event{Kind: "claude", Pane: "%3", Event: "PreToolUse", Data: map[string]any{"session_id": "other", "agent_id": "sub1"}})
	w.Hook(hookclient.Event{Kind: "codex", Pane: "%4", Event: "agent-turn-complete", Data: map[string]any{"thread-id": "t-9"}})
	got := w.PaneResume()
	if got["%3"] != (AgentResume{Kind: "claude", ID: "be3612de"}) || got["%4"] != (AgentResume{Kind: "codex", ID: "t-9"}) {
		t.Fatalf("%+v", got)
	}
}

// The agent redrawing its question (the screen blank for a moment, the
// conversation printed again) is not an answer: no prompt-cleared, no second
// prompt-opened, the same id.
func TestRedrawKeepsPrompt(t *testing.T) {
	w, f, sink := setup(t, "2.1.283")
	promptGone = 400 * time.Millisecond
	hook(w, "UserPromptSubmit", nil)
	f.setScreen("%1", screen(t, "claude-permission-bash.txt"))
	hook(w, "PermissionRequest", map[string]any{"tool_name": "Bash"})
	eventually(t, "prompt", func() bool { return pane(t, w, "%1").Prompt != nil })
	id := pane(t, w, "%1").Prompt.ID
	for i := 0; i < 3; i++ {
		f.setScreen("%1", "\n\n  ⎿  what the agent printed\n")
		w.captureNow("%1")
		time.Sleep(150 * time.Millisecond)
		f.setScreen("%1", screen(t, "claude-permission-bash.txt"))
		w.captureNow("%1")
	}
	time.Sleep(600 * time.Millisecond)
	if n, c := len(sink.of("prompt-opened")), len(sink.of("prompt-cleared")); n != 1 || c != 0 {
		t.Fatalf("%d opened, %d cleared", n, c)
	}
	if p := pane(t, w, "%1"); p.Prompt == nil || p.Prompt.ID != id || p.State != Waiting {
		t.Fatalf("%+v", p)
	}
	// Gone for good: cleared once the grace is over.
	f.setScreen("%1", screen(t, "claude-working.txt"))
	w.captureNow("%1")
	eventually(t, "cleared", func() bool { return len(sink.of("prompt-cleared")) == 1 })
}

// Away longer than the grace and back with nothing from the hooks in
// between: the same prompt, its id back. After a hook that says the agent
// moved on, the same text is a new question.
func TestRevivedPromptKeepsID(t *testing.T) {
	w, f, sink := setup(t, "2.1.283")
	hook(w, "UserPromptSubmit", nil)
	f.setScreen("%1", screen(t, "claude-permission-bash.txt"))
	hook(w, "PermissionRequest", map[string]any{"tool_name": "Bash"})
	eventually(t, "prompt", func() bool { return pane(t, w, "%1").Prompt != nil })
	id := pane(t, w, "%1").Prompt.ID

	f.setScreen("%1", "\n")
	w.captureNow("%1")
	eventually(t, "cleared", func() bool { return len(sink.of("prompt-cleared")) == 1 })
	f.setScreen("%1", screen(t, "claude-permission-bash.txt"))
	w.captureNow("%1")
	opened := sink.of("prompt-opened")
	if len(opened) != 2 || opened[1].id != id {
		t.Fatalf("revived as %+v, want %s again", opened, id)
	}
	if p := pane(t, w, "%1"); p.Prompt == nil || p.Prompt.ID != id || p.State != Waiting {
		t.Fatalf("%+v", p)
	}
	// It answers as the same prompt.
	f.onKeys = func(pane string, keys []string) { f.setScreen(pane, screen(t, "claude-working.txt")) }
	one := 0
	if status, body := w.Answer(id, &one, nil); status != 200 {
		t.Fatalf("%d %v", status, body)
	}

	// Asked again after the tool ran: a new prompt.
	f.onKeys = nil
	hook(w, "PostToolUse", map[string]any{"tool_name": "Bash"})
	hook(w, "PreToolUse", map[string]any{"tool_name": "Bash"})
	f.setScreen("%1", screen(t, "claude-permission-bash.txt"))
	hook(w, "PermissionRequest", map[string]any{"tool_name": "Bash"})
	eventually(t, "new prompt", func() bool { return len(sink.of("prompt-opened")) == 3 })
	if again := sink.of("prompt-opened")[2].id; again == id {
		t.Fatal("a new question got the old id")
	}
}

// A different question in its place is a new prompt at once.
func TestOtherQuestionIsNewPrompt(t *testing.T) {
	w, f, sink := setup(t, "2.1.283")
	hook(w, "UserPromptSubmit", nil)
	f.setScreen("%1", screen(t, "claude-permission-bash.txt"))
	hook(w, "PermissionRequest", map[string]any{"tool_name": "Bash"})
	eventually(t, "prompt", func() bool { return pane(t, w, "%1").Prompt != nil })
	f.setScreen("%1", screen(t, "claude-permission-write.txt"))
	w.captureNow("%1")
	if o, c := sink.of("prompt-opened"), sink.of("prompt-cleared"); len(o) != 2 || len(c) != 1 || o[0].id == o[1].id {
		t.Fatalf("%+v %+v", o, c)
	}
}

// Codex with stui's hooks: its events drive the turn as Claude's do, and an
// Esc (Interrupt) ends it without telling anybody it finished.
func TestCodexFromHooks(t *testing.T) {
	w, f, sink := setup(t, "codex")
	f.setScreen("%1", "› \n")
	codex := func(event string, data map[string]any) {
		data["session_id"] = "019a2b"
		w.Hook(hookclient.Event{Kind: "codex", Pane: "%1", Event: event, Data: data})
	}
	codex("SessionStart", map[string]any{"source": "startup"})
	codex("UserPromptSubmit", map[string]any{"turn_id": "t1"})
	if p := pane(t, w, "%1"); p.State != Working || p.Agent != "codex" {
		t.Fatalf("%+v", p)
	}
	codex("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_summary": "rm -rf build"})
	if p := pane(t, w, "%1"); p.State != Waiting {
		t.Fatalf("%+v", p)
	}
	codex("PostToolUse", map[string]any{"tool_name": "Bash"})
	codex("Stop", map[string]any{"turn_id": "t1"})
	if len(waitDone(t, sink, 1)) != 1 {
		t.Fatal("no turn-done")
	}
	// Its notify, still installed beside the hooks, ends the same turn again.
	w.Hook(hookclient.Event{Kind: "codex", Pane: "%1", Event: "agent-turn-complete", Data: map[string]any{}})

	codex("UserPromptSubmit", map[string]any{"turn_id": "t2"})
	if p := pane(t, w, "%1"); p.State != Working {
		t.Fatalf("%+v", p)
	}
	codex("Interrupt", map[string]any{"turn_id": "t2"})
	if p := pane(t, w, "%1"); p.State != Idle {
		t.Fatalf("%+v", p)
	}
	time.Sleep(200 * time.Millisecond)
	if n := len(sink.of("turn-done")); n != 1 {
		t.Fatalf("%d turn-done, want 1: the interrupted turn was announced", n)
	}
}

// opencode through stui's plugin (hooks/opencode.js): its events come in
// Claude's names, drive the turn as Claude's do, and Esc twice (Interrupt)
// ends it without telling anybody it finished.
func TestOpencodeFromHooks(t *testing.T) {
	w, f, sink := setup(t, "opencode")
	f.setScreen("%1", "  ┃  Build · Claude Sonnet 4.6 Anthropic\n")
	oc := func(event string, data map[string]any) {
		if data == nil {
			data = map[string]any{}
		}
		data["session_id"] = "ses_e920"
		w.Hook(hookclient.Event{Kind: "opencode", Pane: "%1", Event: event, Data: data})
	}
	oc("SessionStart", nil)
	oc("UserPromptSubmit", nil)
	if p := pane(t, w, "%1"); p.State != Working || p.Agent != "opencode" {
		t.Fatalf("%+v", p)
	}
	oc("PreToolUse", map[string]any{"tool_name": "bash"})
	oc("PermissionRequest", map[string]any{"tool_name": "bash", "tool_summary": "date"})
	if p := pane(t, w, "%1"); p.State != Waiting {
		t.Fatalf("%+v", p)
	}
	oc("PostToolUse", map[string]any{"tool_name": "bash"})
	oc("Stop", nil)
	if len(waitDone(t, sink, 1)) != 1 {
		t.Fatal("no turn-done")
	}
	// A turn begun without a prompt: the plugin's tool hook with no tool.
	oc("PreToolUse", nil)
	if p := pane(t, w, "%1"); p.State != Working {
		t.Fatalf("%+v", p)
	}
	oc("Interrupt", nil)
	if p := pane(t, w, "%1"); p.State != Idle {
		t.Fatalf("%+v", p)
	}
	time.Sleep(200 * time.Millisecond)
	if n := len(sink.of("turn-done")); n != 1 {
		t.Fatalf("%d turn-done, want 1: the interrupted turn was announced", n)
	}
	oc("UserPromptSubmit", nil)
	oc("StopFailure", map[string]any{"error": "overloaded"})
	if p := pane(t, w, "%1"); p.State != Error {
		t.Fatalf("%+v", p)
	}
	oc("SessionEnd", nil)
	if p := pane(t, w, "%1"); p.State != Unknown {
		t.Fatalf("%+v", p)
	}
}

// Kimi Code: Claude's names, plus TurnStarted (a turn without a prompt,
// and after UserPromptSubmit nothing new), PermissionResult (answered) and
// Interrupt (Esc, instead of Stop).
func TestKimiFromHooks(t *testing.T) {
	w, f, sink := setup(t, "kimi-code")
	f.setScreen("%1", "")
	kimi := func(event string, data map[string]any) {
		if data == nil {
			data = map[string]any{}
		}
		data["session_id"] = "session_abc"
		w.Hook(hookclient.Event{Kind: "kimi", Pane: "%1", Event: event, Data: data})
	}
	kimi("SessionStart", map[string]any{"source": "startup"})
	if p := pane(t, w, "%1"); p.State != Idle || p.Agent != "kimi" {
		t.Fatalf("%+v", p)
	}
	kimi("UserPromptSubmit", nil)
	start := pane(t, w, "%1").Since
	time.Sleep(5 * time.Millisecond)
	kimi("TurnStarted", map[string]any{"origin_kind": "user"})
	if p := pane(t, w, "%1"); p.State != Working || p.Since != start {
		t.Fatalf("TurnStarted after the prompt began another turn: %+v", p)
	}
	kimi("PermissionRequest", map[string]any{"tool_name": "Bash"})
	if p := pane(t, w, "%1"); p.State != Waiting {
		t.Fatalf("%+v", p)
	}
	kimi("PermissionResult", map[string]any{"tool_name": "Bash"})
	if p := pane(t, w, "%1"); p.State != Working {
		t.Fatalf("%+v", p)
	}
	// A background task's status is not someone wanted (stui does not
	// install it; were it sent, nothing changes).
	kimi("Notification", map[string]any{"notification_type": "task.completed"})
	if p := pane(t, w, "%1"); p.State != Working {
		t.Fatalf("%+v", p)
	}
	kimi("Stop", nil)
	if len(waitDone(t, sink, 1)) != 1 {
		t.Fatal("no turn-done")
	}
	// A task's turn: TurnStarted alone.
	kimi("TurnStarted", map[string]any{"origin_kind": "task"})
	if p := pane(t, w, "%1"); p.State != Working {
		t.Fatalf("%+v", p)
	}
	kimi("Interrupt", map[string]any{"reason": "cancelled"})
	if p := pane(t, w, "%1"); p.State != Idle {
		t.Fatalf("%+v", p)
	}
	time.Sleep(200 * time.Millisecond)
	if n := len(sink.of("turn-done")); n != 1 {
		t.Fatalf("%d turn-done, want 1: the interrupted turn was announced", n)
	}
}

// Kimi's screen is one stui cannot read: a quiet one does not end its turn.
func TestKimiQuietScreenKeepsTheTurn(t *testing.T) {
	w, f, _ := setup(t, "kimi-code")
	f.setScreen("%1", "> \n")
	w.Hook(hookclient.Event{Kind: "kimi", Pane: "%1", Event: "UserPromptSubmit", Data: map[string]any{"session_id": "s"}})
	g, q := stopGrace, screenQuiet
	stopGrace, screenQuiet = 50*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() { stopGrace, screenQuiet = g, q })
	for range 3 {
		time.Sleep(120 * time.Millisecond)
		w.captureNow("%1")
	}
	if p := pane(t, w, "%1"); p.State != Working {
		t.Fatalf("%+v", p)
	}
}

// Qwen Code: Claude's hooks, subagents by agent_id, and its Notification
// is Claude's.
func TestQwenFromHooks(t *testing.T) {
	w, f, sink := setup(t, "qwen")
	f.setScreen("%1", "")
	qwen := func(event string, data map[string]any) {
		if data == nil {
			data = map[string]any{}
		}
		data["session_id"] = "q1"
		w.Hook(hookclient.Event{Kind: "qwen", Pane: "%1", Event: event, Data: data})
	}
	qwen("SessionStart", map[string]any{"source": "startup"})
	qwen("UserPromptSubmit", nil)
	if p := pane(t, w, "%1"); p.State != Working || p.Agent != "qwen" {
		t.Fatalf("%+v", p)
	}
	qwen("SubagentStart", map[string]any{"agent_id": "a1", "agent_type": "general"})
	qwen("PreToolUse", map[string]any{"agent_id": "a1", "tool_name": "read_file"})
	qwen("Notification", map[string]any{"notification_type": "permission_prompt"})
	if p := pane(t, w, "%1"); p.State != Waiting || p.Subagents.Running != 1 {
		t.Fatalf("%+v", p)
	}
	qwen("SubagentStop", map[string]any{"agent_id": "a1"})
	qwen("Stop", nil)
	if len(waitDone(t, sink, 1)) != 1 {
		t.Fatal("no turn-done")
	}
	qwen("UserPromptSubmit", nil)
	qwen("StopFailure", map[string]any{"error": "rate limited"})
	if p := pane(t, w, "%1"); p.State != Error {
		t.Fatalf("%+v", p)
	}
	qwen("SessionEnd", nil)
	if p := pane(t, w, "%1"); p.State != Unknown {
		t.Fatalf("%+v", p)
	}
}

// Pi through stui's extension (hooks/pi.js), in a pane tmux calls node: its
// SessionStart names it, a dialog an extension opens waits and its closing
// goes back to work, Esc (Interrupt) ends the turn unannounced.
func TestPiFromHooks(t *testing.T) {
	w, f, sink := setup(t, "node")
	f.setScreen("%1", "")
	piev := func(event string, data map[string]any) {
		if data == nil {
			data = map[string]any{}
		}
		data["session_id"] = "019e3f"
		w.Hook(hookclient.Event{Kind: "pi", Pane: "%1", Event: event, Data: data})
	}
	piev("SessionStart", nil)
	if p := pane(t, w, "%1"); p.Agent != "pi" || p.State != Idle {
		t.Fatalf("%+v", p)
	}
	piev("UserPromptSubmit", nil)
	piev("PreToolUse", map[string]any{"tool_name": "bash", "tool_input": map[string]any{"command": "date"}})
	if p := pane(t, w, "%1"); p.State != Working {
		t.Fatalf("%+v", p)
	}
	piev("PermissionRequest", map[string]any{"tool_name": "Allow bash?"})
	if p := pane(t, w, "%1"); p.State != Waiting {
		t.Fatalf("%+v", p)
	}
	piev("PermissionResult", nil)
	if p := pane(t, w, "%1"); p.State != Working {
		t.Fatalf("answered: %+v", p)
	}
	piev("PostToolUse", map[string]any{"tool_name": "bash"})
	piev("Stop", nil)
	if len(waitDone(t, sink, 1)) != 1 {
		t.Fatal("no turn-done")
	}
	piev("UserPromptSubmit", nil)
	piev("Interrupt", nil)
	if p := pane(t, w, "%1"); p.State != Idle {
		t.Fatalf("%+v", p)
	}
	time.Sleep(200 * time.Millisecond)
	if n := len(sink.of("turn-done")); n != 1 {
		t.Fatalf("%d turn-done, want 1: the interrupted turn was announced", n)
	}
	piev("PreToolUse", nil)
	piev("StopFailure", map[string]any{"error": "connection refused"})
	if p := pane(t, w, "%1"); p.State != Error {
		t.Fatalf("%+v", p)
	}
	// Gone: a pane tmux calls node is no agent once Pi has said so.
	piev("SessionEnd", nil)
	for _, p := range w.Snapshot().Panes {
		if p.ID == "%1" {
			t.Fatalf("still listed after SessionEnd: %+v", p)
		}
	}
}

func TestAgentForKimiQwen(t *testing.T) {
	for cmd, want := range map[string]string{"kimi": "kimi", "kimi-code": "kimi", "qwen": "qwen", "node": "", "qwen.exe": "qwen", "pi": "pi"} {
		if got := AgentFor(cmd); got != want {
			t.Errorf("%s: %q, want %q", cmd, got, want)
		}
	}
}

func writeSpool(t *testing.T, dir string, evs ...hookclient.Event) {
	t.Helper()
	path := hookclient.SpoolFile(filepath.Dir(dir), "%1")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, ev := range evs {
		b, _ := json.Marshal(ev)
		f.Write(append(b, '\n'))
	}
}

// A turn that ended while no agent was listening ends when the spool is
// played back, quietly; what the pane has moved past is dropped; the spool
// is emptied.
// The tool hooks stui hook could not deliver are counted in one log line and
// the file emptied; with none, nothing is said.
func TestTakeDropped(t *testing.T) {
	w, _, _ := setup(t, "2.1.283")
	path := filepath.Join(t.TempDir(), "dropped.log")
	w.opts.DroppedFile = path
	var logged []string
	w.opts.Logf = func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	if n := w.TakeDropped(); n != 0 || len(logged) != 0 {
		t.Fatalf("%d %q", n, logged)
	}
	os.WriteFile(path, []byte("PreToolUse\nPostToolUse\nPostToolUse\n"), 0o600)
	if n := w.TakeDropped(); n != 3 {
		t.Fatalf("counted %d", n)
	}
	if want := "hook: 3 tool hooks did not reach stui in time and were not kept (PostToolUse 2, PreToolUse 1)"; len(logged) != 1 || logged[0] != want {
		t.Fatalf("%q", logged)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("not emptied: %v", err)
	}
}

func TestReplaySpool(t *testing.T) {
	w, f, sink := setup(t, "2.1.283")
	dir := filepath.Join(t.TempDir(), hookclient.SpoolDir)
	w.opts.SpoolDir = dir
	var logged []string
	w.opts.Logf = func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	f.setScreen("%1", screen(t, "claude-working.txt"))
	hook(w, "UserPromptSubmit", map[string]any{"session_id": "s1"})
	if pane(t, w, "%1").State != Working {
		t.Fatal("not working")
	}
	now := time.Now().UnixMilli()
	writeSpool(t, dir,
		// From before the live hook: history.
		hookclient.Event{Kind: "claude", Pane: "%1", Event: "SessionEnd", At: now - 60_000, Data: map[string]any{}},
		hookclient.Event{Kind: "claude", Pane: "%1", Event: "SubagentStart", At: now + 100, Data: map[string]any{"agent_id": "a1", "agent_type": "Explore"}},
		hookclient.Event{Kind: "claude", Pane: "%1", Event: "SubagentStop", At: now + 200, Data: map[string]any{"agent_id": "a1"}},
		hookclient.Event{Kind: "claude", Pane: "%1", Event: "PermissionRequest", At: now + 250, Data: map[string]any{"tool_name": "Bash"}},
		hookclient.Event{Kind: "claude", Pane: "%1", Event: "PreToolUse", At: now + 260, Data: map[string]any{"tool_name": "Agent"}},
		hookclient.Event{Kind: "claude", Pane: "%1", Event: "PostToolUse", At: now + 270, Data: map[string]any{"tool_name": "Agent"}},
		hookclient.Event{Kind: "claude", Pane: "%1", Event: "PostToolUse", At: now + 280, Data: map[string]any{"tool_name": "Bash"}},
		hookclient.Event{Kind: "claude", Pane: "%1", Event: "Stop", At: now - 30*24*3600*1000, Data: map[string]any{}},
		hookclient.Event{Kind: "claude", Pane: "%1", Event: "Stop", At: now + 300, Data: map[string]any{"session_id": "s1"}},
	)
	if n := w.ReplaySpool(); n != 4 {
		t.Fatalf("played %d, want 4 (PermissionRequest is not played back)", n)
	}
	// What was not played back is counted, in one line.
	want := "hook spool: played back 4, dropped 5 that are not played back (PermissionRequest 1, PostToolUse 2, PreToolUse 1, too old 1)"
	if len(logged) != 1 || logged[0] != want {
		t.Fatalf("logged %q", logged)
	}
	p := pane(t, w, "%1")
	if p.State != Done || p.Subagents.Done != 1 || p.Agent != "claude" {
		t.Fatalf("%+v", p)
	}
	time.Sleep(200 * time.Millisecond)
	if n := len(sink.of("turn-done")) + len(sink.of("turn-delegated")) + len(sink.of("prompt-opened")); n != 0 {
		t.Fatalf("%d notices for history", n)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("spool left: %v", left)
	}
}

// GET /api/agents says when the agent last took a prompt: stui worker done
// tells a report of the last task from one of the work sent since.
func TestSubmittedIsListed(t *testing.T) {
	w, _, _ := setup(t, "2.1.283")
	if p := pane(t, w, "%1"); p.Submitted != 0 {
		t.Fatalf("a prompt before any was taken: %d", p.Submitted)
	}
	before := time.Now().UnixMilli()
	hook(w, "UserPromptSubmit", nil)
	p := pane(t, w, "%1")
	if p.Submitted < before || p.Submitted > time.Now().UnixMilli() {
		t.Fatalf("submitted %d, asked at %d", p.Submitted, before)
	}
	for _, q := range w.Snapshot().Panes {
		if q.ID == "%1" && q.Submitted != p.Submitted {
			t.Fatalf("the snapshot has %d", q.Submitted)
		}
	}
}
