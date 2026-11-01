package watch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sophonz-labs/stui/internal/hookclient"
)

const testSession = "5f1c2a9e-0000-4000-8000-000000000001"

// persistTmux is a server with one Claude pane on a tty, and a shell.
func persistTmux(t *testing.T) *fakeTmux {
	t.Helper()
	return &fakeTmux{
		panes: "%1|~|work|~||~|0|~|0|~|2.1.288|~|api|~|/dev/ttys001|~|Refactor auth\n" +
			"%2|~|work|~||~|1|~|0|~|zsh|~|sh|~|/dev/ttys002|~|host\n",
		screens: map[string]string{"%1": "", "%2": "$ "},
	}
}

type persistRig struct {
	path    string
	clock   *clock
	started time.Time // the Claude in %1
	sink    *fakeSink
	f       *fakeTmux
	// The foreground processes on a tty; nil: none (never ps).
	procs func(tty string) []Proc
}

func newPersistRig(t *testing.T) *persistRig {
	shortTiming(t)
	settle, settleMax = 20*time.Millisecond, 100*time.Millisecond
	delegateSettle = 100 * time.Millisecond
	c := &clock{t: time.Now()}
	return &persistRig{
		path:    StatePath(t.TempDir(), "/tmp/tmux-501/default"),
		clock:   c,
		started: c.now().Add(-time.Hour),
		f:       persistTmux(t),
	}
}

// stui, started now.
func (r *persistRig) start(t *testing.T, claudeDir string) *Watcher {
	t.Helper()
	if claudeDir == "" {
		claudeDir = t.TempDir()
	}
	r.sink = &fakeSink{}
	return New(Options{
		Tmux: r.f, Sink: r.sink, Socket: "/tmp/tmux-501/default", ClaudeDir: claudeDir, Now: r.clock.now,
		StatePath: r.path,
		Args:      func(string) []string { return []string{"claude"} },
		ProcStart: func(tty string) (time.Time, bool) { return r.started, tty == "/dev/ttys001" },
		Procs: func(tty string) []Proc {
			if r.procs == nil {
				return nil
			}
			return r.procs(tty)
		},
	})
}

func shook(w *Watcher, event string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	if _, ok := data["session_id"]; !ok {
		data["session_id"] = testSession
	}
	w.Hook(hookclient.Event{Kind: "claude", Pane: "%1", Event: event, Data: data})
}

func readState(t *testing.T, path string) stateFile {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f stateFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func itemByID(its []SubagentItem, id string) *SubagentItem {
	for i := range its {
		if its[i].ID == id {
			return &its[i]
		}
	}
	return nil
}

// The reported bug: stui restarts (an auto-upgrade) while a pane's main agent
// has subagents running, and they were gone from every list. They stay,
// with their start times, and finishing them ends the turn as before.
func TestRestartKeepsSubagents(t *testing.T) {
	r := newPersistRig(t)
	w := r.start(t, "")
	w.poll(context.Background())
	shook(w, "UserPromptSubmit", nil)
	for i, typ := range []string{"oh-my-claudecode:executor", "oh-my-claudecode:explore", "general-purpose"} {
		id := []string{"a1", "b2", "c3"}[i]
		tid := "toolu_" + id
		shook(w, "PreToolUse", map[string]any{"tool_name": "Agent", "tool_use_id": tid, "agent_subtype": typ, "agent_desc": "task " + id})
		shook(w, "SubagentStart", map[string]any{"agent_id": id, "agent_type": typ})
		shook(w, "PostToolUse", map[string]any{"tool_name": "Agent", "tool_use_id": tid, "agent_subtype": typ, "agent_desc": "task " + id, "agent_ref": id})
		r.clock.add(10 * time.Second)
	}
	r.clock.add(20 * time.Second)
	shook(w, "Stop", nil)
	shook(w, "SubagentStop", map[string]any{"agent_id": "a1", "agent_type": "oh-my-claudecode:executor"})
	before := pane(t, w, "%1")
	if before.Subagents.Running != 2 || before.Phase != PhaseSubagents {
		t.Fatalf("%+v", before)
	}
	w.Flush()

	fi, err := os.Stat(r.path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if f := readState(t, r.path); f.V != stateVersion || len(f.Panes) != 1 || f.Panes[0].ID != "%1" {
		t.Fatalf("%+v", f)
	}

	// The upgrade: stui is down for 20 s, then a new one starts.
	r.clock.add(20 * time.Second)
	w2 := r.start(t, "")
	w2.poll(context.Background())
	p := pane(t, w2, "%1")
	if p.State != Working || p.Phase != PhaseSubagents || p.Subagents.Running != 2 || p.Subagents.Done != 1 || p.Turn == nil {
		t.Fatalf("after restart: %+v", p)
	}
	if p.Turn.StartedAt != before.Turn.StartedAt {
		t.Fatalf("turn start %d, was %d", p.Turn.StartedAt, before.Turn.StartedAt)
	}
	for _, want := range before.Subagents.Items {
		got := itemByID(p.Subagents.Items, want.ID)
		if got == nil || got.State != want.State || got.StartedAt != want.StartedAt || got.Description != want.Description || got.Type != want.Type {
			t.Fatalf("item %s: %+v, was %+v", want.ID, got, want)
		}
	}
	if PaneResumeID(w2, "%1") != testSession {
		t.Fatal("conversation id not kept")
	}

	// The rest finish after the restart: the turn ends, all three counted.
	shook(w2, "SubagentStop", map[string]any{"agent_id": "b2"})
	shook(w2, "SubagentStop", map[string]any{"agent_id": "c3"})
	if s := state(t, w2); s != Done {
		t.Fatalf("state %s", s)
	}
	d := waitDone(t, r.sink, 1)
	if d[0].sub.Done != 3 {
		t.Fatalf("%+v", d)
	}
}

func PaneResumeID(w *Watcher, id string) string { return w.PaneResume()[id].ID }

// Restored runs still go lost when they died without SubagentStop; time stui
// was down does not count as their silence.
func TestRestoredSubagentStillAges(t *testing.T) {
	r := newPersistRig(t)
	w := r.start(t, "")
	w.poll(context.Background())
	shook(w, "UserPromptSubmit", nil)
	shook(w, "SubagentStart", map[string]any{"agent_id": "a1", "agent_type": "general-purpose"})
	// Heard from (kept across the restart): it can go lost.
	shook(w, "PostToolUse", map[string]any{"agent_id": "a1", "agent_type": "general-purpose", "tool_name": "Read"})
	shook(w, "Stop", nil)
	w.Flush()

	r.clock.add(9 * time.Minute) // down, nearly a quiet period
	w2 := r.start(t, "")
	w2.poll(context.Background())
	w2.tick()
	if p := pane(t, w2, "%1"); p.Subagents.Running != 1 {
		t.Fatalf("down time counted as silence: %+v", p)
	}
	for left := subagentQuiet + 2*time.Second; left > 0; left -= 2 * time.Second {
		r.clock.add(2 * time.Second)
		w2.tick()
	}
	p := pane(t, w2, "%1")
	if p.State != Done || p.Subagents.Running != 0 || p.Subagents.Lost != 1 {
		t.Fatalf("%+v", p)
	}
	if it := itemByID(p.Subagents.Items, "a1"); it == nil || it.State != SubLost {
		t.Fatalf("%+v", p.Subagents.Items)
	}
}

// A record whose pane is gone, back at a shell, or running an agent that
// started after the file was written is dropped.
func TestRestoreDropsStalePanes(t *testing.T) {
	r := newPersistRig(t)
	w := r.start(t, "")
	w.poll(context.Background())
	shook(w, "UserPromptSubmit", nil)
	shook(w, "SubagentStart", map[string]any{"agent_id": "a1", "agent_type": "general-purpose"})
	w.Flush()

	// A pane that has gone since (%9), alongside.
	f := readState(t, r.path)
	gone := f.Panes[0]
	gone.ID, gone.TTY = "%9", "/dev/ttys009"
	f.Panes = append(f.Panes, gone)
	b, _ := json.Marshal(f)
	os.WriteFile(r.path, b, 0o600)

	// A new Claude in %1: started after the file was written.
	r.clock.add(5 * time.Second)
	r.started = r.clock.now().Add(-time.Second)
	w2 := r.start(t, "")
	w2.poll(context.Background())
	if p := pane(t, w2, "%1"); len(p.Subagents.Items) != 0 || p.Subagents.Running != 0 {
		t.Fatalf("a new agent took the old one's subagents: %+v", p)
	}
	for _, p := range w2.Snapshot().Panes {
		if p.ID == "%9" {
			t.Fatalf("gone pane listed: %+v", p)
		}
	}
	w2.Flush()
	for _, p := range readState(t, r.path).Panes {
		if p.ID == "%9" || len(p.Items) > 0 {
			t.Fatalf("stale record kept: %+v", p)
		}
	}

	// Back at a shell.
	r.started = r.clock.now().Add(-time.Hour)
	shook(w2, "SubagentStart", map[string]any{"agent_id": "b2", "agent_type": "general-purpose"})
	w2.Flush()
	r.f.mu.Lock()
	r.f.panes = "%1|~|work|~||~|0|~|0|~|zsh|~|api|~|/dev/ttys001|~|host\n"
	r.f.mu.Unlock()
	w3 := r.start(t, "")
	w3.poll(context.Background())
	if ps := w3.Snapshot().Panes; len(ps) != 0 {
		t.Fatalf("%+v", ps)
	}
}

// A hook naming the same conversation settles a record before the first
// poll; one naming another drops it.
func TestRestoreByConversation(t *testing.T) {
	for _, same := range []bool{true, false} {
		r := newPersistRig(t)
		w := r.start(t, "")
		w.poll(context.Background())
		shook(w, "UserPromptSubmit", nil)
		shook(w, "SubagentStart", map[string]any{"agent_id": "a1", "agent_type": "general-purpose"})
		w.Flush()

		w2 := r.start(t, "")
		sid := testSession
		if !same {
			sid = "another-conversation"
		}
		shook(w2, "PreToolUse", map[string]any{"tool_name": "Read", "session_id": sid})
		w2.poll(context.Background())
		p := pane(t, w2, "%1")
		if same && (p.Subagents.Running != 1 || itemByID(p.Subagents.Items, "a1") == nil) {
			t.Fatalf("same conversation: %+v", p)
		}
		if !same && len(p.Subagents.Items) != 0 {
			t.Fatalf("another conversation: %+v", p)
		}
	}
}

// Another schema, another server, or an old file: nothing is restored.
func TestRestoreIgnoresOtherFiles(t *testing.T) {
	for _, name := range []string{"version", "socket", "old", "garbage"} {
		r := newPersistRig(t)
		w := r.start(t, "")
		w.poll(context.Background())
		shook(w, "UserPromptSubmit", nil)
		shook(w, "SubagentStart", map[string]any{"agent_id": "a1", "agent_type": "general-purpose"})
		w.Flush()
		f := readState(t, r.path)
		switch name {
		case "version":
			f.V = stateVersion + 1
		case "socket":
			f.Socket = "/tmp/tmux-501/other"
		case "old":
			f.SavedAt -= (restoreMaxAge + time.Minute).Milliseconds()
		}
		b, _ := json.Marshal(f)
		if name == "garbage" {
			b = []byte("{not json")
		}
		os.WriteFile(r.path, b, 0o600)
		w2 := r.start(t, "")
		w2.poll(context.Background())
		if p := pane(t, w2, "%1"); len(p.Subagents.Items) != 0 {
			t.Fatalf("%s: restored %+v", name, p)
		}
	}
}

// Saving while hooks, polls and readers run: no race, and the file on disk
// is always whole.
func TestPersistConcurrency(t *testing.T) {
	r := newPersistRig(t)
	saveEvery = time.Millisecond
	t.Cleanup(func() { saveEvery = 2 * time.Second })
	w := r.start(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.saveLoop(ctx.Done()); close(done) }()
	w.poll(context.Background())
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				id := string(rune('a'+g)) + itoa(i)
				shook(w, "SubagentStart", map[string]any{"agent_id": id, "agent_type": "general-purpose"})
				shook(w, "SubagentStop", map[string]any{"agent_id": id})
				if i%10 == 0 {
					w.Flush()
					w.poll(context.Background())
					w.Snapshot()
				}
			}
		}(g)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			if b, err := os.ReadFile(r.path); err == nil {
				var f stateFile
				if json.Unmarshal(b, &f) != nil {
					t.Error("torn file")
					return
				}
			}
			time.Sleep(time.Millisecond)
		}
	}()
	wg.Wait()
	cancel()
	<-done
	entries, _ := os.ReadDir(filepath.Dir(r.path))
	if len(entries) != 1 {
		t.Fatalf("left behind: %v", entries)
	}
	if p := readState(t, r.path).Panes; len(p) != 1 || p[0].Done != 200 {
		t.Fatalf("%+v", p)
	}
}

// A subagent that started before stui did, heard from by its own hooks, is
// listed from Claude's record of it; a hook from one with no record is not.
func TestRecoverFromClaudeRecords(t *testing.T) {
	r := newPersistRig(t)
	dir := t.TempDir()
	subs := filepath.Join(dir, "projects", "-Users-me-work", testSession, "subagents")
	os.MkdirAll(subs, 0o700)
	meta := filepath.Join(subs, "agent-a7935120af38b5e2d.meta.json")
	os.WriteFile(meta, []byte(`{"agentType":"oh-my-claudecode:executor-high","description":"Persist stui   subagent state","toolUseId":"toolu_01","spawnDepth":1}`), 0o644)
	began := r.clock.now().Add(-40 * time.Minute).Truncate(time.Second)
	os.Chtimes(meta, began, began)
	os.WriteFile(filepath.Join(subs, "agent-a7935120af38b5e2d.jsonl"), []byte(`{"secret":"never read"}`), 0o600)

	w := r.start(t, dir)
	w.poll(context.Background())
	shook(w, "PreToolUse", map[string]any{"tool_name": "Bash", "agent_id": "a7935120af38b5e2d", "agent_type": "oh-my-claudecode:executor-high"})
	shook(w, "PreToolUse", map[string]any{"tool_name": "Bash", "agent_id": "helper"})
	shook(w, "PreToolUse", map[string]any{"tool_name": "Bash", "agent_id": "../../etc", "session_id": "../.."})
	p := pane(t, w, "%1")
	if p.Subagents.Running != 1 || len(p.Subagents.Items) != 1 {
		t.Fatalf("%+v", p)
	}
	it := p.Subagents.Items[0]
	if it.ID != "a7935120af38b5e2d" || it.Type != "oh-my-claudecode:executor-high" || it.Description != "Persist stui subagent state" ||
		it.State != SubRunning || it.StartedAt != began.UnixMilli() {
		t.Fatalf("%+v", it)
	}
	// It ends as any other.
	shook(w, "SubagentStop", map[string]any{"agent_id": "a7935120af38b5e2d"})
	if p := pane(t, w, "%1"); p.Subagents.Running != 0 || p.Subagents.Done != 1 {
		t.Fatalf("%+v", p)
	}
}

func TestParseEtime(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"00:05":      5 * time.Second,
		"01:02:03":   time.Hour + 2*time.Minute + 3*time.Second,
		"2-00:00:01": 48*time.Hour + time.Second,
		"12:00":      12 * time.Minute,
	} {
		if got, ok := parseEtime(in); !ok || got != want {
			t.Errorf("%s: %v %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "5", "a:b", "1-2-3:00"} {
		if _, ok := parseEtime(in); ok {
			t.Errorf("%q parsed", in)
		}
	}
	out := " 100 100 200 01:00:00\n 200 200 200 00:30\n 201 200 200 00:29\n"
	if d, ok := foregroundElapsed(out); !ok || d != 30*time.Second {
		t.Fatalf("%v %v", d, ok)
	}
}

// A subagent listed without a description -- the main agent's Agent call
// never reached stui, so there was nothing to take it from -- gets it from
// Claude's record of the run on its next hook. One with no record stays
// empty, and is looked up a few times only; one with a description keeps it.
func TestDescriptionFromClaudeRecord(t *testing.T) {
	r := newPersistRig(t)
	dir := t.TempDir()
	subs := filepath.Join(dir, "projects", "-Users-me-work", testSession, "subagents")
	os.MkdirAll(subs, 0o700)
	write := func(id, desc string) {
		os.WriteFile(filepath.Join(subs, "agent-"+id+".meta.json"), []byte(`{"agentType":"general-purpose","description":"`+desc+`","requestShape":"background"}`), 0o644)
	}
	write("a913e0289ee3f9726", "Android   agent tree")
	write("c3", "Ends at once")
	write("d4", "Not this one")

	w := r.start(t, dir)
	w.poll(context.Background())
	desc := func(id string) string {
		for _, it := range pane(t, w, "%1").Subagents.Items {
			if it.ID == id {
				return it.Description
			}
		}
		t.Fatalf("no item %s", id)
		return ""
	}
	// No Agent PreToolUse came: SubagentStart lists it with no description.
	shook(w, "SubagentStart", map[string]any{"agent_id": "a913e0289ee3f9726", "agent_type": "general-purpose"})
	if d := desc("a913e0289ee3f9726"); d != "" {
		t.Fatalf("before: %q", d)
	}
	// Its own first tool call: Claude's record fills it.
	shook(w, "PreToolUse", map[string]any{"tool_name": "Bash", "agent_id": "a913e0289ee3f9726", "agent_type": "general-purpose"})
	if d := desc("a913e0289ee3f9726"); d != "Android agent tree" {
		t.Fatalf("after: %q", d)
	}

	// One that makes no tool call: its SubagentStop fills it.
	shook(w, "SubagentStart", map[string]any{"agent_id": "c3", "agent_type": "general-purpose"})
	shook(w, "SubagentStop", map[string]any{"agent_id": "c3"})
	if d := desc("c3"); d != "Ends at once" {
		t.Fatalf("at its stop: %q", d)
	}

	// No record: stays empty, and is looked up descTries times, no more.
	shook(w, "SubagentStart", map[string]any{"agent_id": "b2", "agent_type": "Explore"})
	for range 5 {
		shook(w, "PreToolUse", map[string]any{"tool_name": "Read", "agent_id": "b2", "agent_type": "Explore"})
	}
	if d := desc("b2"); d != "" {
		t.Fatalf("no record: %q", d)
	}
	w.mu.Lock()
	tried := w.descTried["b2"]
	w.mu.Unlock()
	if tried != descTries {
		t.Fatalf("looked up %d times, want %d", tried, descTries)
	}

	// The Agent call's own description wins over the record.
	shook(w, "PreToolUse", map[string]any{"tool_name": "Agent", "agent_desc": "What the main agent said", "agent_subtype": "general-purpose", "tool_use_id": "toolu_d4"})
	shook(w, "SubagentStart", map[string]any{"agent_id": "d4", "agent_type": "general-purpose"})
	shook(w, "PreToolUse", map[string]any{"tool_name": "Bash", "agent_id": "d4", "agent_type": "general-purpose"})
	if d := desc("d4"); d != "What the main agent said" {
		t.Fatalf("kept: %q", d)
	}
}
