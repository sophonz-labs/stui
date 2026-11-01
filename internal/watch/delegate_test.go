package watch

import (
	"sync"
	"testing"
	"time"
)

// The reported bug: Claude hands work to background subagents and ends its
// turn, and the phone said "finished" while they had only just started.
// The turn goes on in them; it is done when they are.

// clock is a settable wall clock for the watcher.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func withClock(w *Watcher) *clock {
	c := &clock{t: time.Now()}
	w.opts.Now = c.now
	return c
}

func sub(id string) map[string]any {
	return map[string]any{"agent_id": id, "agent_type": "general-purpose"}
}

func TestDelegatedTurn(t *testing.T) {
	shortTiming(t)
	w, f, sink := setup(t, "2.1.283")
	c := withClock(w)
	hook(w, "UserPromptSubmit", nil)
	for _, id := range []string{"a", "b", "c"} {
		hook(w, "SubagentStart", sub(id))
	}
	c.add(40 * time.Second)
	hook(w, "Stop", nil)

	p := pane(t, w, "%1")
	if p.State != Working || p.Phase != PhaseSubagents || p.Turn == nil || p.Subagents.Running != 3 {
		t.Fatalf("%+v", p)
	}
	if len(sink.of("turn-done")) != 0 || len(sink.of("turn-delegated")) != 0 {
		t.Fatalf("sent at Stop, not held: %+v", sink.events)
	}
	d := waitDelegated(t, sink, 1)
	if len(d) != 1 || d[0].running != 3 || d[0].dur != 40000 {
		t.Fatalf("%+v", d)
	}
	ss := w.Snapshot().Sessions[0]
	if ss.State != Working || ss.Phase != PhaseSubagents || ss.Delegated != 1 || ss.Subagents.Running != 3 {
		t.Fatalf("%+v", ss)
	}

	// The main agent's screen, quiet and idle-looking (with "Waiting for 3
	// background agents"), for longer than ends an ordinary turn.
	show(w, f, t, "claude-bg-wait.txt")
	time.Sleep(500 * time.Millisecond)
	// Claude's minute at its prompt says nothing about the subagents.
	hook(w, "Notification", map[string]any{"notification_type": "idle_prompt"})
	// A second Stop (a hook that ran twice) is not a second turn-delegated.
	hook(w, "Stop", nil)
	if p := pane(t, w, "%1"); p.State != Working || p.Phase != PhaseSubagents || len(sink.of("turn-delegated")) != 1 {
		t.Fatalf("%+v %+v", p, sink.events)
	}

	c.add(10 * time.Minute)
	hook(w, "SubagentStop", sub("a"))
	hook(w, "SubagentStop", sub("b"))
	if state(t, w) != Working || len(sink.of("turn-done")) != 0 {
		t.Fatal("done with a subagent still running")
	}
	c.add(2 * time.Minute)
	hook(w, "SubagentStop", sub("c"))
	// Done at once; turn-done is held a moment for a task notification
	// that could take the turn up again. None comes.
	if state(t, w) != Done || len(sink.of("turn-done")) != 0 {
		t.Fatalf("%+v", sink.events)
	}
	waitDone(t, sink, 1)
	p = pane(t, w, "%1")
	if p.State != Done || p.Phase != PhaseMain || p.Turn != nil || p.LastTurnMs == nil || *p.LastTurnMs != 12*60000+40000 {
		t.Fatalf("%+v", p)
	}
	done := sink.of("turn-done")
	if len(done) != 1 || done[0].dur != 12*60000+40000 || done[0].sub != (TurnSubagents{Done: 3}) {
		t.Fatalf("%+v", done)
	}
	// Nothing more for this turn: not a late Stop, not the quiet screen.
	hook(w, "Stop", nil)
	show(w, f, t, "claude-idle.txt")
	time.Sleep(400 * time.Millisecond)
	if len(sink.of("turn-done")) != 1 || state(t, w) != Done {
		t.Fatalf("%+v", sink.events)
	}
	hook(w, "Notification", map[string]any{"notification_type": "idle_prompt"})
	if state(t, w) != Idle {
		t.Fatal(state(t, w))
	}
}

// The main agent picks the subagents' results up in a turn of its own (a
// task notification: no UserPromptSubmit). turn-done comes when that turn
// ends with nothing running, for the whole of it.
func TestDelegatedTurnResumes(t *testing.T) {
	shortTiming(t)
	w, f, sink := setup(t, "2.1.283")
	c := withClock(w)
	hook(w, "UserPromptSubmit", nil)
	hook(w, "SubagentStart", sub("a"))
	hook(w, "SubagentStart", sub("b"))
	c.add(35 * time.Second)
	hook(w, "Stop", nil)
	waitDelegated(t, sink, 1)
	c.add(5 * time.Minute)
	hook(w, "SubagentStop", sub("a"))

	// a's result: the main agent at work again, on screen.
	show(w, f, t, "claude-thinking.txt")
	eventually(t, "main phase", func() bool { return pane(t, w, "%1").Phase == PhaseMain })
	p := pane(t, w, "%1")
	if p.State != Working || p.Turn == nil || p.Turn.DurationMs < 5*60000 {
		t.Fatalf("%+v %+v", p, p.Turn)
	}
	// b finishes while the main agent is busy: its turn, not over yet.
	hook(w, "SubagentStop", sub("b"))
	if state(t, w) != Working || len(sink.of("turn-done")) != 0 {
		t.Fatalf("%+v", sink.events)
	}
	c.add(time.Minute)
	show(w, f, t, "claude-done-hint.txt")
	hook(w, "Stop", nil)
	done := waitDone(t, sink, 1)
	if len(done) != 1 || done[0].dur != 35000+6*60000 || done[0].sub.Done != 2 || state(t, w) != Done {
		t.Fatalf("%+v", done)
	}
	if len(sink.of("turn-delegated")) != 1 {
		t.Fatalf("%+v", sink.events)
	}

	// The same through the main agent's tool hooks, and a resumed turn that
	// hands off again: still one turn-delegated, one turn-done.
	hook(w, "UserPromptSubmit", nil)
	hook(w, "SubagentStart", sub("x"))
	hook(w, "Stop", nil)
	hook(w, "PreToolUse", map[string]any{"tool_name": "Read"})
	if p := pane(t, w, "%1"); p.Phase != PhaseMain || p.State != Working {
		t.Fatalf("%+v", p)
	}
	hook(w, "SubagentStart", sub("y"))
	hook(w, "Stop", nil)
	// The first turn-delegated was withdrawn when the main agent went on
	// at once; this one goes once the screen settles.
	if p := pane(t, w, "%1"); p.Phase != PhaseSubagents || len(waitDelegated(t, sink, 2)) != 2 {
		t.Fatalf("%+v %+v", p, sink.events)
	}
	hook(w, "SubagentStop", sub("x"))
	hook(w, "SubagentStop", sub("y"))
	eventually(t, "done", func() bool { return state(t, w) == Done })
	if done := waitDone(t, sink, 2); len(done) != 2 || done[1].sub.Done != 2 || state(t, w) != Done {
		t.Fatalf("%+v", done)
	}
}

// A subagent that finishes before the main agent stops: an ordinary turn.
func TestQuickSubagentBeforeStop(t *testing.T) {
	w, _, sink := setup(t, "2.1.283")
	hook(w, "UserPromptSubmit", nil)
	hook(w, "SubagentStart", sub("a"))
	hook(w, "SubagentStop", sub("a"))
	hook(w, "Stop", nil)
	if state(t, w) != Done || len(sink.of("turn-delegated")) != 0 {
		t.Fatalf("%+v", sink.events)
	}
	if d := waitDone(t, sink, 1); len(d) != 1 || d[0].sub.Done != 1 {
		t.Fatalf("%+v", d)
	}
	// A background agent from that turn still running when the next Stop
	// finds the pane done is no turn to hand off.
	hook(w, "SubagentStart", sub("b"))
	hook(w, "Stop", nil)
	if state(t, w) != Done || len(sink.of("turn-delegated")) != 0 || len(sink.of("turn-done")) != 1 {
		t.Fatalf("%+v", sink.events)
	}
}

// Subagents that die without SubagentStop (the laptop slept, the process was
// killed) are given up on after a silence, and the turn ends then, quietly:
// stui only stopped hearing them.
func TestLostSubagents(t *testing.T) {
	w, _, sink := setup(t, "2.1.283")
	c := withClock(w)
	w.tick()
	hook(w, "UserPromptSubmit", nil)
	hook(w, "SubagentStart", sub("a"))
	hook(w, "SubagentStart", sub("b"))
	hook(w, "SubagentStart", sub("c"))
	c.add(20 * time.Second)
	hook(w, "Stop", nil)

	// b keeps working (its own tool hooks), c is inside a long tool call,
	// a was heard once and then says nothing.
	hook(w, "PostToolUse", map[string]any{"tool_name": "Read", "agent_id": "a"})
	step := func(d time.Duration) {
		for left := d; left > 0; left -= time.Second {
			c.add(time.Second)
			if left%time.Minute == 0 {
				hook(w, "PostToolUse", map[string]any{"tool_name": "Read", "agent_id": "b"})
			}
			w.tick()
		}
	}
	hook(w, "PreToolUse", map[string]any{"tool_name": "Bash", "agent_id": "c"})
	step(subagentQuiet + 5*time.Second)
	p := pane(t, w, "%1")
	if p.State != Working || p.Phase != PhaseSubagents || p.Subagents.Running != 2 || p.Subagents.Lost != 1 {
		t.Fatalf("%+v", p)
	}
	hook(w, "SubagentStop", sub("b"))
	// c inside its tool: longer, then lost too, and that ends the turn.
	step(subagentToolQuiet - subagentQuiet)
	p = pane(t, w, "%1")
	if p.State != Done || p.Subagents.Running != 0 || p.Subagents.Lost != 2 || p.Source != "timeout" {
		t.Fatalf("%+v", p)
	}
	time.Sleep(300 * time.Millisecond)
	if d := sink.of("turn-done"); len(d) != 0 {
		t.Fatalf("a timeout sent turn-done: %+v", d)
	}
	// a was alive after all: it is counted as done, not lost, and starts
	// nothing.
	hook(w, "SubagentStop", sub("a"))
	p = pane(t, w, "%1")
	if p.State != Done || p.Subagents.Lost != 1 || p.Subagents.Done != 2 || len(sink.of("turn-done")) != 0 {
		t.Fatalf("%+v", p)
	}
}

// Time the machine spent asleep is not a subagent's silence: after a long
// gap between ticks the clock starts again from the wake.
func TestSleepIsNotSilence(t *testing.T) {
	w, _, sink := setup(t, "2.1.283")
	c := withClock(w)
	w.tick()
	hook(w, "UserPromptSubmit", nil)
	hook(w, "SubagentStart", sub("a"))
	hook(w, "PreToolUse", map[string]any{"tool_name": "Read", "agent_id": "a"})
	hook(w, "PostToolUse", map[string]any{"tool_name": "Read", "agent_id": "a"})
	hook(w, "Stop", nil)
	c.add(3 * time.Hour) // asleep
	w.tick()
	if p := pane(t, w, "%1"); p.Subagents.Running != 1 || p.Phase != PhaseSubagents {
		t.Fatalf("%+v", p)
	}
	// Dead after all: awake and silent for the full quiet period.
	for left := subagentQuiet + 2*time.Second; left > 0; left -= 2 * time.Second {
		c.add(2 * time.Second)
		w.tick()
	}
	if p := pane(t, w, "%1"); p.State != Done || p.Subagents.Lost != 1 || len(sink.of("turn-done")) != 0 {
		t.Fatalf("%+v", p)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// A subagent waiting on the user's answer is not silent: time the pane
// spends waiting does not count.
func TestWaitingIsNotSilence(t *testing.T) {
	w, _, _ := setup(t, "2.1.283")
	c := withClock(w)
	w.tick()
	hook(w, "UserPromptSubmit", nil)
	hook(w, "SubagentStart", sub("a"))
	hook(w, "Stop", nil)
	hook(w, "PermissionRequest", map[string]any{"tool_name": "Bash", "agent_id": "a"})
	for i := 0; i < 1800; i++ { // an hour
		c.add(2 * time.Second)
		w.tick()
	}
	w.mu.Lock()
	p := w.panes["%1"]
	p.setState(Working, "screen", c.now()) // answered at the desk
	w.mu.Unlock()
	for i := 0; i < 60; i++ {
		c.add(2 * time.Second)
		w.tick()
	}
	if p := pane(t, w, "%1"); p.Subagents.Running != 1 || p.Subagents.Lost != 0 || p.Phase != PhaseSubagents {
		t.Fatalf("%+v", p)
	}
}

func taskNote() map[string]any { return map[string]any{"task_notification": true} }

// What Claude 2.1.284 really sends (a sandbox run, three background agents):
// each agent's result comes back as a UserPromptSubmit whose prompt is a
// <task-notification>, right after its SubagentStop, and the main agent
// answers it in a short turn of its own. All of it is one turn.
func TestTaskNotificationTurns(t *testing.T) {
	w, _, sink := setup(t, "2.1.283")
	c := withClock(w)
	step := func(d time.Duration, event string, data map[string]any) {
		c.add(d)
		hook(w, event, data)
	}
	step(0, "UserPromptSubmit", nil)
	step(time.Second, "SubagentStart", sub("a"))
	step(0, "SubagentStart", sub("b"))
	step(0, "SubagentStart", sub("c"))
	step(35*time.Second, "Stop", nil)
	waitDelegated(t, sink, 1) // the screen settles
	step(10*time.Second, "SubagentStop", sub("a"))
	step(0, "UserPromptSubmit", taskNote())
	if p := pane(t, w, "%1"); p.Phase != PhaseMain || p.Turn == nil || p.Turn.DurationMs != 46000 {
		t.Fatalf("%+v %+v", p, p.Turn)
	}
	step(4*time.Second, "Stop", nil)
	step(4*time.Second, "SubagentStop", sub("b"))
	step(time.Second, "UserPromptSubmit", taskNote())
	step(2*time.Second, "Stop", nil)
	if p := pane(t, w, "%1"); p.State != Working || p.Phase != PhaseSubagents || p.Subagents.Done != 2 {
		t.Fatalf("%+v", p)
	}
	step(3*time.Second, "SubagentStop", sub("c"))
	// Taken up at once: the same turn, and its end is this turn's Stop.
	step(0, "UserPromptSubmit", taskNote())
	step(3*time.Second, "Stop", nil)
	// Claude had two more queued: they take the ended turn up again.
	step(0, "UserPromptSubmit", taskNote())
	if p := pane(t, w, "%1"); p.State != Working || p.Subagents.Done != 3 || p.Turn.DurationMs != 63000 {
		t.Fatalf("%+v", p)
	}
	step(2*time.Second, "Stop", nil)
	d := waitDone(t, sink, 1)
	if len(d) != 1 || d[0].dur != 65000 || d[0].sub.Done != 3 || state(t, w) != Done {
		t.Fatalf("%+v", sink.events)
	}
	if n := len(sink.of("turn-delegated")); n != 1 {
		t.Fatalf("%d turn-delegated", n)
	}
	time.Sleep(200 * time.Millisecond) // past the settle: nothing more
	if len(sink.of("turn-done")) != 1 {
		t.Fatalf("%+v", sink.events)
	}

	// A task notification with nothing running is a turn of its own; one
	// that arrives while a turn runs is part of it.
	step(time.Minute, "UserPromptSubmit", taskNote())
	if p := pane(t, w, "%1"); p.State != Working || p.Turn == nil || p.Turn.DurationMs != 0 {
		t.Fatalf("%+v", p)
	}
	step(time.Second, "UserPromptSubmit", taskNote())
	if p := pane(t, w, "%1"); p.Turn.DurationMs != 1000 {
		t.Fatalf("%+v", p.Turn)
	}
}

// waitDone waits for the n-th turn-done (held ones go after delegateSettle).
func waitDone(t *testing.T, sink *fakeSink, n int) []sinkEvent {
	t.Helper()
	eventually(t, "turn-done", func() bool { return len(sink.of("turn-done")) >= n })
	return sink.of("turn-done")
}

func waitDelegated(t *testing.T, sink *fakeSink, n int) []sinkEvent {
	t.Helper()
	eventually(t, "turn-delegated", func() bool { return len(sink.of("turn-delegated")) >= n })
	return sink.of("turn-delegated")
}

// A prompt the user types or queued during the hold is a new turn, and the
// held turn-done is withdrawn: the user is there.
func TestHeldTurnDroppedByPrompt(t *testing.T) {
	w, _, sink := setup(t, "2.1.283")
	delegateSettle = time.Hour
	hook(w, "UserPromptSubmit", nil)
	hook(w, "SubagentStart", sub("a"))
	hook(w, "SubagentStop", sub("a"))
	hook(w, "Stop", nil)
	if len(sink.of("turn-done")) != 0 {
		t.Fatal("not held")
	}
	hook(w, "UserPromptSubmit", nil)
	if p := pane(t, w, "%1"); p.State != Working || p.Subagents.Done != 0 {
		t.Fatalf("%+v", p)
	}
	time.Sleep(200 * time.Millisecond)
	if d := sink.of("turn-done"); len(d) != 0 {
		t.Fatalf("%+v", d)
	}
}
