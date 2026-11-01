package watch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sophonz-labs/stui/internal/hookclient"
)

func claudeDir(w *Watcher) string {
	for d := range w.claudeDirs {
		return d
	}
	return ""
}

func writeJob(t *testing.T, dir, sid, name string) {
	t.Helper()
	p := filepath.Join(dir, "jobs", sid[:8], "state.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"name":"`+name+`","sessionId":"`+sid+`","state":"working"}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestABackgroundSessionsHooksGoToThePaneShowingIt(t *testing.T) {
	w, f, _ := setup(t, "2.1.291")
	// The pane's title is the background session's name, after Claude's glyph.
	f.panes = "%1|~|work|~||~|0|~|0|~|2.1.291|~|api|~|✳ Refactor auth\n"
	w.poll(t.Context())
	const sid = "745815c5-8808-4fef-9069-815e7261040a"
	writeJob(t, claudeDir(w), sid, "Refactor auth")
	bg := func(event string, data map[string]any) {
		data["session_id"] = sid
		w.Hook(hookclient.Event{Kind: "claude", Event: event, Data: data, Background: true})
	}
	bg("UserPromptSubmit", map[string]any{})
	bg("SubagentStart", map[string]any{"agent_id": "a"})
	bg("SubagentStart", map[string]any{"agent_id": "b"})
	if s := pane(t, w, "%1").Subagents; s.Running != 2 {
		t.Fatalf("subagents %+v", s)
	}
	// Without the flag, or for a session no pane shows, nothing.
	w.Hook(hookclient.Event{Kind: "claude", Event: "SubagentStart", Data: map[string]any{"agent_id": "c", "session_id": sid}})
	other := "11111111-2222-3333-4444-555555555555"
	writeJob(t, claudeDir(w), other, "Something else")
	w.Hook(hookclient.Event{Kind: "claude", Event: "SubagentStart", Data: map[string]any{"agent_id": "d", "session_id": other}, Background: true})
	if s := pane(t, w, "%1").Subagents; s.Running != 2 {
		t.Fatalf("only its own: %+v", s)
	}
}

func TestABackgroundSessionOnTwoPanesGoesNowhere(t *testing.T) {
	w, f, _ := setup(t, "2.1.291")
	f.panes = "%1|~|work|~||~|0|~|0|~|2.1.291|~|a|~|✳ Same\n%3|~|work|~||~|1|~|0|~|2.1.291|~|b|~|⠂ Same\n"
	w.poll(t.Context())
	const sid = "745815c5-8808-4fef-9069-815e7261040a"
	writeJob(t, claudeDir(w), sid, "Same")
	if p := w.bgPane(sid, ""); p != "" {
		t.Fatalf("guessed %s", p)
	}
	if w.bgPane("../../etc", "") != "" {
		t.Fatal("a session id is not a path")
	}
}

func TestTitleText(t *testing.T) {
	for in, want := range map[string]string{"✳ Refactor auth": "Refactor auth", "⠂ 사용자 데이터 통합 검토": "사용자 데이터 통합 검토", "Plain": "Plain", "a b": "a b"} {
		if got := titleText(in); got != want {
			t.Errorf("%q -> %q", in, got)
		}
	}
}
