package watch

import (
	"testing"
	"time"
)

// The reported bug: with only some of stui's hooks installed (no PreToolUse
// or PostToolUse), a subagent says nothing between SubagentStart and
// SubagentStop. Ten minutes in, stui called it lost and, as the last one of a
// handed-off turn, sent "finished" while the agent was still at work.
func TestLostNeedsOwnHooks(t *testing.T) {
	w, _, sink := setup(t, "2.1.283")
	c := withClock(w)
	w.tick()
	hook(w, "UserPromptSubmit", nil)
	hook(w, "SubagentStart", sub("a"))
	hook(w, "Stop", nil)
	for left := 11 * time.Minute; left > 0; left -= 2 * time.Second {
		c.add(2 * time.Second)
		w.tick()
	}
	time.Sleep(200 * time.Millisecond)
	p := pane(t, w, "%1")
	if p.State != Working || p.Phase != PhaseSubagents || p.Subagents.Running != 1 || p.Subagents.Lost != 0 {
		t.Fatalf("%+v", p)
	}
	if it := itemByID(p.Subagents.Items, "a"); it == nil || it.State != SubRunning {
		t.Fatalf("%+v", p.Subagents.Items)
	}
	if len(sink.of("turn-done")) != 0 {
		t.Fatalf("%+v", sink.events)
	}
	// It ends when it says so.
	hook(w, "SubagentStop", sub("a"))
	if d := waitDone(t, sink, 1); len(d) != 1 || d[0].sub != (TurnSubagents{Done: 1}) {
		t.Fatalf("%+v", d)
	}
}

// A subagent heard from and then silent still goes lost, and when that ends
// the turn stui says nothing: it only stopped hearing it.
func TestTimeoutNoPush(t *testing.T) {
	w, _, sink := setup(t, "2.1.283")
	c := withClock(w)
	w.tick()
	hook(w, "UserPromptSubmit", nil)
	hook(w, "SubagentStart", sub("a"))
	hook(w, "PostToolUse", map[string]any{"tool_name": "Read", "agent_id": "a"})
	hook(w, "Stop", nil)
	for left := subagentQuiet + 4*time.Second; left > 0; left -= 2 * time.Second {
		c.add(2 * time.Second)
		w.tick()
	}
	p := pane(t, w, "%1")
	if p.State != Done || p.Source != "timeout" || p.Subagents.Lost != 1 {
		t.Fatalf("%+v", p)
	}
	time.Sleep(300 * time.Millisecond)
	if n := len(sink.of("turn-done")); n != 0 {
		t.Fatalf("%+v", sink.events)
	}
}

// Claude submits a queued prompt right after its Stop hooks. The turn did
// not end for anyone: the held turn-done is withdrawn.
func TestQueuedPromptDropsDone(t *testing.T) {
	w, _, sink := setup(t, "2.1.283")
	hook(w, "UserPromptSubmit", nil)
	hook(w, "Stop", nil)
	time.Sleep(20 * time.Millisecond)
	hook(w, "UserPromptSubmit", nil)
	if state(t, w) != Working {
		t.Fatal(state(t, w))
	}
	time.Sleep(doneHoldMax + 200*time.Millisecond)
	if n := len(sink.of("turn-done")); n != 0 {
		t.Fatalf("%+v", sink.events)
	}
	// The queued turn's own end is reported.
	hook(w, "Stop", nil)
	if d := waitDone(t, sink, 1); len(d) != 1 {
		t.Fatalf("%+v", d)
	}

	// The same for a turn handed to subagents: no turn-delegated.
	hook(w, "UserPromptSubmit", nil)
	hook(w, "SubagentStart", sub("a"))
	hook(w, "Stop", nil)
	hook(w, "UserPromptSubmit", nil)
	time.Sleep(300 * time.Millisecond)
	if n := len(sink.of("turn-delegated")); n != 0 {
		t.Fatalf("%+v", sink.events)
	}
}

// A lone Stop: one turn-done, once the screen has shown no turn for
// doneSettle; a screen still running Stop hooks holds it, up to
// doneHoldMax.
func TestDoneAfterSettle(t *testing.T) {
	w, f, sink := setup(t, "2.1.283")
	doneSettle = 150 * time.Millisecond
	hook(w, "UserPromptSubmit", nil)
	start := time.Now()
	hook(w, "Stop", nil)
	if state(t, w) != Done || len(sink.of("turn-done")) != 0 {
		t.Fatalf("%s %+v", state(t, w), sink.events)
	}
	time.Sleep(50 * time.Millisecond)
	if len(sink.of("turn-done")) != 0 {
		t.Fatal("sent before the screen settled")
	}
	waitDone(t, sink, 1)
	if el := time.Since(start); el < doneSettle {
		t.Fatalf("sent after %v", el)
	}
	time.Sleep(200 * time.Millisecond)
	if n := len(sink.of("turn-done")); n != 1 {
		t.Fatalf("%d turn-done", n)
	}

	// Stop hooks still running on screen: held until they are done, or the
	// most it is held.
	doneHoldMax = 600 * time.Millisecond
	hook(w, "UserPromptSubmit", nil)
	f.setScreen("%1", screen(t, "claude-running-stop-hooks.txt"))
	start = time.Now()
	hook(w, "Stop", nil)
	time.Sleep(400 * time.Millisecond)
	if n := len(sink.of("turn-done")); n != 1 {
		t.Fatalf("sent with Stop hooks on screen: %d", n)
	}
	waitDone(t, sink, 2)
	if el := time.Since(start); el < doneHoldMax {
		t.Fatalf("sent after %v", el)
	}
}

// A task notification after a held turn takes it up again: one turn-done,
// for the whole of it. A tool call from the main agent does the same.
func TestTaskNotificationReopensHeld(t *testing.T) {
	w, _, sink := setup(t, "2.1.283")
	c := withClock(w)
	hook(w, "UserPromptSubmit", nil)
	c.add(10 * time.Second)
	hook(w, "Stop", nil)
	hook(w, "UserPromptSubmit", taskNote())
	if p := pane(t, w, "%1"); p.State != Working || p.Turn == nil || p.Turn.DurationMs != 10000 {
		t.Fatalf("%+v", p)
	}
	c.add(5 * time.Second)
	hook(w, "Stop", nil)
	hook(w, "PreToolUse", map[string]any{"tool_name": "Read"})
	if state(t, w) != Working {
		t.Fatal(state(t, w))
	}
	c.add(time.Second)
	hook(w, "Stop", nil)
	d := waitDone(t, sink, 1)
	time.Sleep(200 * time.Millisecond)
	if d = sink.of("turn-done"); len(d) != 1 || d[0].dur != 16000 {
		t.Fatalf("%+v", d)
	}
}
