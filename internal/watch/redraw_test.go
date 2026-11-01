package watch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sophonz-labs/stui/internal/hookclient"
)

// One question on screen is one prompt, however often the agent redraws it:
// a resize that clears the screen and prints the transcript again, a
// re-render that erases the prompt and writes it back, a narrower window that
// wraps it differently. Each extra prompt-opened was a notification of its
// own on the phone.
//
// STUI_AW_REDRAW_FOR=2m runs it for that long and logs every event.
// STUI_AW_REDRAW_SECS makes each redraw take that long.
func TestPromptSurvivesRedraws(t *testing.T) {
	// shortcut: also fails in st on tmux 3.8 (prompt never seen); opt in until that is understood.
	if testing.Short() || os.Getenv("STUI_SLOW_TESTS") == "" {
		t.Skip("real tmux, real time: STUI_SLOW_TESTS=1 runs it")
	}
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3")
	}
	tm := sandboxTmux(t)
	settle, settleMax = 400*time.Millisecond, time.Second
	sink := &fakeSink{}
	w := New(Options{Tmux: tm, Sink: sink, ClaudeDir: t.TempDir()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx, tm.Bin)

	script, _ := filepath.Abs("testdata/fakeclaude.py")
	// How long a resize takes to print the conversation again: Claude's is
	// well under a second; STUI_AW_REDRAW_SECS=1.4 drags a window non-stop.
	redraw := "0.6"
	if s := os.Getenv("STUI_AW_REDRAW_SECS"); s != "" {
		redraw = s
	}
	id := tmuxOut(t, tm, "new-window", "-d", "-P", "-F", "#{pane_id}", "-t", "work", "-n", "agent", "FAKE_REDRAW="+redraw+" "+py+" "+script)
	eventually(t, "pane listed", func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.panes[id] != nil
	})
	// What Claude's hooks say as the prompt comes up.
	w.Hook(hookclient.Event{Kind: "claude", Pane: id, Event: "PreToolUse", Data: map[string]any{"tool_name": "Bash"}})
	w.Hook(hookclient.Event{Kind: "claude", Pane: id, Event: "PermissionRequest", Data: map[string]any{"tool_name": "Bash", "tool_summary": "rm -rf build/ && make"}})
	eventually(t, "prompt opened", func() bool { return len(sink.of("prompt-opened")) > 0 })

	run := 14 * time.Second
	if d, err := time.ParseDuration(os.Getenv("STUI_AW_REDRAW_FOR")); err == nil {
		run = d
	}
	sizes := [][2]int{{80, 30}, {120, 40}, {64, 24}, {160, 50}, {100, 36}}
	start := time.Now()
	for i := 0; time.Since(start) < run; i++ {
		s := sizes[i%len(sizes)]
		tm.Run(context.Background(), "resize-window", "-t", "work:agent", "-x", strconv.Itoa(s[0]), "-y", strconv.Itoa(s[1]))
		// Now and then while it is still printing, as a window being dragged does.
		if i%3 == 1 {
			time.Sleep(300 * time.Millisecond)
			tm.Run(context.Background(), "resize-window", "-t", "work:agent", "-x", strconv.Itoa(s[0]-7), "-y", strconv.Itoa(s[1]))
		}
		time.Sleep(2300 * time.Millisecond)
		// A hook now and then, as Claude's Notification comes some seconds in.
		if i == 2 {
			w.Hook(hookclient.Event{Kind: "claude", Pane: id, Event: "Notification", Data: map[string]any{"notification_type": "permission_prompt"}})
		}
	}
	time.Sleep(2 * time.Second)

	opened, cleared := sink.of("prompt-opened"), sink.of("prompt-cleared")
	ids := map[string]bool{}
	for _, e := range opened {
		ids[e.id] = true
	}
	var log []string
	for _, e := range sink.events {
		if e.typ == "prompt-opened" || e.typ == "prompt-cleared" {
			log = append(log, e.typ+" "+e.id+" "+e.reason)
		}
	}
	t.Logf("%s: %d prompt-opened, %d distinct ids, %d prompt-cleared\n%s", run, len(opened), len(ids), len(cleared), strings.Join(log, "\n"))
	if len(ids) != 1 || len(cleared) != 0 {
		t.Fatalf("one question became %d prompts (%d opened, %d cleared)", len(ids), len(opened), len(cleared))
	}
	if p := pane(t, w, id); p.State != Waiting || p.Prompt == nil {
		t.Fatalf("pane is %s with prompt %v, want waiting with its prompt", p.State, p.Prompt)
	}
}
