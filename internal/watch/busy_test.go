package watch

import (
	"strings"
	"testing"
	"time"

	"github.com/sophonz-labs/stui/internal/hookclient"
)

// Screens captured from Claude Code 2.1.283 in tmux (testdata/screens),
// and Codex's and Gemini's indicators as their sources render them.
func TestReadScreen(t *testing.T) {
	for _, c := range []struct {
		name, agent, title string
		text               string // instead of a file
		busy, stopping     bool
		inputBox           bool
	}{
		{name: "claude-thinking.txt", busy: true, inputBox: true},
		{name: "claude-tool-running.txt", busy: true, inputBox: true},
		// A custom statusLine: no "esc to interrupt", the spinner says it.
		{name: "claude-continuation.txt", busy: true, inputBox: true},
		{name: "claude-working.txt", busy: true, inputBox: true},
		// The first frames, before the counter: "· Crafting…".
		{name: "claude-first-frame.txt", busy: true, inputBox: true},
		{name: "claude-running-stop-hooks.txt", busy: true, stopping: true, inputBox: true},
		{name: "claude-done.txt", inputBox: true},
		{name: "claude-done-hint.txt", inputBox: true},
		{name: "claude-idle.txt", inputBox: true},
		{name: "claude-idle-statusline.txt", inputBox: true},
		// Main idle, a background agent running: not the main agent's turn.
		{name: "claude-bg-wait.txt", inputBox: true},
		{name: "claude-interrupted.txt", inputBox: true},
		// A permission prompt says "Esc to cancel", not busy (it is a prompt).
		{name: "claude-permission-hint.txt"},
		// The title animates only when Claude is not under a multiplexer (or
		// its static-title flag is off); when it does, it says busy.
		{name: "claude-done-hint.txt", title: "◐ Sleep command with joke", busy: true, inputBox: true},
		{name: "claude-done-hint.txt", title: "✳ Sleep command with joke", inputBox: true},
		// The words in the conversation, above the input box, are not the
		// footer.
		{text: "❯ it shows esc to interrupt while busy\n  esc to interrupt\n" + strings.Repeat("─", 40) + "\n❯ \n" + strings.Repeat("─", 40) + "\n  ? for shortcuts\n", inputBox: true},
		{text: strings.Repeat("─", 40) + "\n❯ \n" + strings.Repeat("─", 40) + "\n  ⏸ manual mode on · 1 shell · esc to interrupt · ← for agents\n", busy: true, inputBox: true},
		{agent: "codex", text: "• Working (0s • esc to interrupt)\n\n› Ask Codex to do anything\n", busy: true},
		{agent: "codex", text: "• Analyzing (1m 05s • esc to interrupt)\n", busy: true},
		// Codex's question UI lists the key among its hints.
		{agent: "codex", text: "  enter to submit answer | esc to interrupt\n"},
		{agent: "codex", text: "› Ask Codex to do anything\n\n  ? for shortcuts\n"},
		{agent: "gemini", text: "⠋ Thinking… (esc to cancel, 5s)\n", busy: true},
		{agent: "gemini", text: "⠋ Reticulating splines (esc to cancel, 1m 5s)\n", busy: true},
		{agent: "gemini", text: "> Type your message or @path/to/file\n"},
		// Qwen Code's indicator (not Gemini's), and its own confirm hint.
		{agent: "qwen", text: "⠋ Reading files (5s · esc to cancel)\n", busy: true},
		{agent: "qwen", text: "⠋ Thinking (1m 5s · ↓ 1.2k tokens · esc to cancel)\n", busy: true},
		{agent: "qwen", text: "  Y/Enter to confirm · N/Esc to cancel\n"},
		{agent: "qwen", text: ">   Type your message or @path/to/file\n"},
		// opencode 1.19.4's footer, working, after one Esc, and idle.
		{agent: "opencode", text: "   ⬝⬝⬝⬝⬝⬝⬝⬝  esc interrupt          8.6K (1%) · $0.01  ctrl+p commands    • OpenCode 1.19.4\n", busy: true},
		{agent: "opencode", text: "   ⬝⬝⬝⬝⬝⬝■■  esc again to interrupt                     ctrl+p commands\n", busy: true},
		{agent: "opencode", text: "  ┃  Build · Claude Sonnet 4.6 Anthropic\n                      tab agents  ctrl+p commands\n"},
		// In a narrow pane its middle cell folds: "interrupt" goes a line
		// down, "esc" stays by the blocks (a 53 column pane).
		{agent: "opencode", text: "   ■■⬝⬝⬝⬝⬝⬝  esc         44.8K (68%) ctrl+p commands\n             interrupt\n", busy: true},
		// Pi 1.4.0: the rule over its box carries a spinner while it
		// works, and is bare when idle or while a dialog is up.
		{agent: "pi", text: "── ⠇ Working ───────────────────────────────────────\n hello\n────────────────────────────────────────────────────\n/tmp/w (main)\n0.0%/128k (auto)        qwen3.5:9b\n", busy: true},
		{agent: "pi", text: "── ⠋ Compacting context ─────────────────────────────\n\n────────────────────────────────────────────────────\n", busy: true},
		{agent: "pi", text: "────────────────────────────────────────────────────\n Allow bash?\n date +%Y\n → Yes\n   No\n ↑↓ navigate  enter select  escape/ctrl+c cancel\n────────────────────────────────────────────────────\n"},
		{agent: "pi", text: "────────────────────────────────────────────────────\n\n────────────────────────────────────────────────────\n/tmp/w (main)\n"},
		{agent: "pi", text: " ⠇ Working on it, the model said\n"},
		// Its own answer saying the words is not the footer.
		{agent: "opencode", text: "     Press esc interrupt to stop it.\n"},
	} {
		text, agent := c.text, c.agent
		if text == "" {
			text = screen(t, c.name)
		}
		if agent == "" {
			agent = "claude"
		}
		got := readScreen(text, c.title, agent)
		want := screenState{busy: c.busy, stopping: c.stopping, inputBox: c.inputBox}
		if got != want {
			t.Errorf("%s %q %q: %+v, want %+v", agent, c.name, c.text, got, want)
		}
	}
	if ParseScreen(screen(t, "claude-permission-hint.txt"), "claude") == nil {
		t.Error("the permission prompt is not read as one")
	}
}

func shortTiming(t *testing.T) {
	g, q := stopGrace, screenQuiet
	stopGrace, screenQuiet = 150*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { stopGrace, screenQuiet = g, q })
}

func show(w *Watcher, f *fakeTmux, t *testing.T, name string) {
	f.setScreen("%1", screen(t, name))
	w.Output("%1", []byte("x"))
}

func state(t *testing.T, w *Watcher) string { return pane(t, w, "%1").State }

// The reported bug: Claude working on a turn no prompt started (a Stop hook
// that blocked, a background agent's result) showed as done/ready.
func TestTurnWithoutPrompt(t *testing.T) {
	shortTiming(t)
	w, f, sink := setup(t, "2.1.283")
	hook(w, "UserPromptSubmit", nil)
	show(w, f, t, "claude-thinking.txt")
	hook(w, "Stop", map[string]any{"stop_hook_active": false})
	// Done, with turn-done held while the screen still shows the turn.
	if state(t, w) != Done || len(sink.of("turn-done")) != 0 {
		t.Fatalf("%s %+v", state(t, w), sink.of("turn-done"))
	}

	// Claude running its other Stop hooks under the spinner: still done.
	show(w, f, t, "claude-running-stop-hooks.txt")
	time.Sleep(400 * time.Millisecond)
	if state(t, w) != Done {
		t.Fatal("the ending turn's spinner started a turn")
	}
	// One blocked: Claude goes on, with no UserPromptSubmit. The screen
	// says so, and it is the same turn: the held turn-done never went.
	show(w, f, t, "claude-continuation.txt")
	eventually(t, "working", func() bool { return state(t, w) == Working })
	p := pane(t, w, "%1")
	if p.Source != "screen" || p.Turn == nil || p.Turn.DurationMs < 400 || len(sink.of("turn-done")) != 0 {
		t.Fatalf("%+v %+v %+v", p, p.Turn, sink.events)
	}
	time.Sleep(50 * time.Millisecond)
	show(w, f, t, "claude-done-hint.txt")
	hook(w, "Stop", map[string]any{"stop_hook_active": true})
	d := waitDone(t, sink, 1)
	if state(t, w) != Done || len(d) != 1 || d[0].dur < 450 || d[0].dur > 3000 {
		t.Fatalf("%s %+v", state(t, w), d)
	}

	// A background agent finishes while Claude sits at its prompt: the
	// "Waiting for 1 background agent" line is not a turn...
	show(w, f, t, "claude-bg-wait.txt")
	hook(w, "SubagentStop", map[string]any{"agent_id": "a1", "agent_type": "general-purpose"})
	time.Sleep(400 * time.Millisecond)
	if state(t, w) != Done {
		t.Fatal("waiting on a background agent is not the main agent working")
	}
	// ...its result is, with or without UserPromptSubmit.
	show(w, f, t, "claude-thinking.txt")
	eventually(t, "working", func() bool { return state(t, w) == Working })
	// Interrupted with Esc: Claude sends no Stop. The idle screen ends
	// the turn once it has stayed idle.
	show(w, f, t, "claude-interrupted.txt")
	time.Sleep(100 * time.Millisecond)
	if state(t, w) != Working {
		t.Fatal("ended at the first quiet screen")
	}
	eventually(t, "done from the screen", func() bool { return state(t, w) == Done })
	if len(waitDone(t, sink, 2)) != 2 || pane(t, w, "%1").Source != "screen" {
		t.Fatalf("%+v", sink.of("turn-done"))
	}
	// A Stop after the screen ended it is not another turn.
	hook(w, "Stop", nil)
	time.Sleep(200 * time.Millisecond)
	if len(sink.of("turn-done")) != 2 {
		t.Fatalf("%+v", sink.of("turn-done"))
	}
}

// A quick screen flicker after Stop that is gone by the time the grace
// ends is not a turn.
func TestSpinnerTailAfterStop(t *testing.T) {
	shortTiming(t)
	w, f, sink := setup(t, "2.1.283")
	hook(w, "UserPromptSubmit", nil)
	hook(w, "Stop", nil)
	show(w, f, t, "claude-thinking.txt")
	time.Sleep(60 * time.Millisecond)
	f.setScreen("%1", screen(t, "claude-done-hint.txt"))
	time.Sleep(400 * time.Millisecond)
	if state(t, w) != Done || len(waitDone(t, sink, 1)) != 1 {
		t.Fatalf("%s %+v", state(t, w), sink.of("turn-done"))
	}
}

// With hooks the screen does not end a turn it cannot read: no input box.
func TestScreenQuietNeedsInputBox(t *testing.T) {
	shortTiming(t)
	w, f, _ := setup(t, "2.1.283")
	hook(w, "UserPromptSubmit", nil)
	f.setScreen("%1", "  Settings\n  > Theme  dark\n")
	w.Output("%1", []byte("x"))
	time.Sleep(600 * time.Millisecond)
	if state(t, w) != Working {
		t.Fatal("an unreadable screen ended the turn")
	}
}

// The main agent's tool hooks start a turn; a subagent's, or one from
// before the last Stop, do not.
func TestToolHooks(t *testing.T) {
	w, _, sink := setup(t, "2.1.283")
	at := func(event string, ms int64, data map[string]any) {
		if data == nil {
			data = map[string]any{}
		}
		w.Hook(hookclient.Event{Kind: "claude", Pane: "%1", Event: event, At: ms, Data: data})
	}
	at("UserPromptSubmit", 1000, nil)
	at("PreToolUse", 1100, map[string]any{"tool_name": "Agent"})
	at("SubagentStart", 1150, map[string]any{"agent_id": "a1", "agent_type": "general-purpose"})
	at("Stop", 2000, nil)
	// Handed to the subagent: still working, in its phase.
	if p := pane(t, w, "%1"); p.State != Working || p.Phase != PhaseSubagents {
		t.Fatalf("%+v", p)
	}
	// Posted before Stop, arriving after it.
	at("PostToolUse", 1900, map[string]any{"tool_name": "Agent"})
	// The background subagent's own tools.
	at("PreToolUse", 2100, map[string]any{"tool_name": "Bash", "agent_id": "a1", "agent_type": "general-purpose"})
	at("PostToolUse", 2200, map[string]any{"tool_name": "Bash", "agent_id": "a1", "agent_type": "general-purpose"})
	if p := pane(t, w, "%1"); p.Phase != PhaseSubagents {
		t.Fatal("a subagent's tool or a late hook resumed the main agent")
	}
	at("SubagentStop", 2300, map[string]any{"agent_id": "a1", "agent_type": "general-purpose"})
	eventually(t, "done after the settle", func() bool { return state(t, w) == Done })
	at("SubagentStart", 2400, map[string]any{"agent_id": "a2"})
	at("TaskCompleted", 2450, nil)
	if p := pane(t, w, "%1"); p.State != Done || p.Subagents.Done != 1 || p.Subagents.Running != 1 {
		t.Fatalf("%+v", p)
	}
	// The main agent reaching for a tool with no prompt, past the hold on
	// the last turn: a new turn.
	waitDone(t, sink, 1)
	at("PreToolUse", 3000, map[string]any{"tool_name": "Bash"})
	p := pane(t, w, "%1")
	if p.State != Working || p.Source != "hook" || p.Turn == nil || p.Subagents.Done != 0 {
		t.Fatalf("%+v", p)
	}
	at("PostToolUse", 3100, map[string]any{"tool_name": "Bash"})
	at("SubagentStop", 3150, map[string]any{"agent_id": "a2"})
	at("Stop", 3200, nil)
	if state(t, w) != Done || len(waitDone(t, sink, 2)) != 2 {
		t.Fatalf("%+v", sink.of("turn-done"))
	}
	// Waiting stays waiting: a parallel tool's hook does not answer the
	// prompt on screen.
	at("UserPromptSubmit", 4000, nil)
	at("PermissionRequest", 4100, map[string]any{"tool_name": "Bash"})
	at("PostToolUse", 4200, map[string]any{"tool_name": "Read"})
	if state(t, w) != Waiting {
		t.Fatal("a tool hook ended waiting")
	}
	// idle_prompt: Claude sat at its prompt; nothing was running.
	at("Stop", 5000, nil)
	at("PreToolUse", 6000, map[string]any{"tool_name": "Bash"})
	at("Notification", 70000, map[string]any{"notification_type": "idle_prompt"})
	if state(t, w) != Idle {
		t.Fatalf("%s", state(t, w))
	}
}

// Codex after its notify hook: the screen still starts and ends turns.
func TestCodexHookedScreen(t *testing.T) {
	shortTiming(t)
	w, f, sink := setup(t, "codex")
	w.Hook(hookclient.Event{Kind: "codex", Pane: "%1", Event: "agent-turn-complete", Data: map[string]any{}})
	time.Sleep(200 * time.Millisecond)
	f.setScreen("%1", "• Working (3s • esc to interrupt)\n")
	w.Output("%1", []byte("x"))
	eventually(t, "working", func() bool { return state(t, w) == Working })
	f.setScreen("%1", "› \n")
	w.Output("%1", []byte("x"))
	eventually(t, "done", func() bool { return state(t, w) == Done })
	w.Hook(hookclient.Event{Kind: "codex", Pane: "%1", Event: "agent-turn-complete", Data: map[string]any{}})
	time.Sleep(300 * time.Millisecond)
	if n := len(sink.of("turn-done")); n != 2 {
		t.Fatalf("%d turn-done", n)
	}
}

// A main agent that has handed work to subagents prints nothing while they
// run. Its screen is quiet and shows its input box, which is what an idle
// agent looks like -- and it was being read as one, so a pane with two
// subagents going reported itself idle.
func TestQuietScreenIsNotIdleWhileSubagentsRun(t *testing.T) {
	shortTiming(t)
	w, f, _ := setup(t, "2.1.283")
	hook(w, "UserPromptSubmit", nil)
	hook(w, "SubagentStart", map[string]any{"agent_id": "a1"})
	hook(w, "SubagentStart", map[string]any{"agent_id": "a2"})

	// The idle-looking screen, held long enough to end an ordinary turn.
	show(w, f, t, "claude-idle.txt")
	time.Sleep(600 * time.Millisecond)
	if s := state(t, w); s != Working {
		t.Fatalf("state = %s while two subagents run, want working", s)
	}

	// They finish; the same quiet screen now means what it says.
	hook(w, "SubagentStop", map[string]any{"agent_id": "a1"})
	hook(w, "SubagentStop", map[string]any{"agent_id": "a2"})
	show(w, f, t, "claude-idle.txt")
	eventually(t, "the turn ends once nothing is running", func() bool {
		return state(t, w) == Done
	})
}
