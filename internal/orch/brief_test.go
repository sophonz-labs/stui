package orch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// briefTmux is fakeTmux answering what the brief loop and the facts ask.
func briefTmux(f *fakeTmux) func(context.Context, ...string) (string, error) {
	return func(ctx context.Context, args ...string) (string, error) {
		switch {
		case args[0] == "list-panes" && args[len(args)-1] == briefFormat:
			f.mu.Lock()
			defer f.mu.Unlock()
			var b strings.Builder
			for _, id := range f.order {
				o := f.opts[id]
				fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\n", id, o[OptBriefState], o[OptBriefSent], o[OptInRun], o[OptTellLater])
			}
			return b.String(), nil
		case args[0] == "display-message":
			f.mu.Lock()
			defer f.mu.Unlock()
			name := strings.TrimSuffix(strings.TrimPrefix(args[len(args)-1], "#{"), "}")
			if name == "pane_current_path" {
				return "", nil
			}
			return f.opts[args[3]][name], nil
		}
		return f.run(ctx, args...)
	}
}

// An adopted agent that is working is not typed into: the message that it
// is a worker and the brief request wait for its turn to end, and go once,
// together. The role is kept, and the brief has keys of its own.
func TestBriefWaitsForTurnEnd(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	f := newFake([3]string{"%1", "work", "lead"}, [3]string{"%2", "work", "ui-desktop"})
	state := map[string]string{"%1": "idle", "%2": "working"}
	var mu sync.Mutex
	var told []string
	a := NewAdopter(AdoptOptions{
		Tmux:      briefTmux(f),
		Agents:    func() map[string]string { return map[string]string{"%1": "claude", "%2": "codex"} },
		State:     func(p string) (string, int64) { mu.Lock(); defer mu.Unlock(); return state[p], 1000 },
		LastAsked: func(p string) string { return "team as worker, gray tones" },
		Deliver: func(_ context.Context, pane, text string) (int, map[string]any) {
			mu.Lock()
			told = append(told, pane+" "+text)
			mu.Unlock()
			return 200, nil
		},
	})
	ctx := context.Background()
	res, err := a.Adopt(ctx, "%1", "%2", AdoptHow{Tell: true, About: "desktop sidebar\nand themes"})
	if err != nil {
		t.Fatal(err)
	}
	if res.About != "desktop sidebar and themes" || res.BriefState != BriefAsked || res.State != "working" || res.LastAsked != "team as worker, gray tones" {
		t.Fatalf("facts: %+v", res)
	}
	if res.Told["%2"] != TurnEnd || res.Told["%1"] != "ok" {
		t.Fatalf("told: %v", res.Told)
	}
	for _, m := range told {
		if strings.HasPrefix(m, "%2 ") {
			t.Fatalf("typed into a working agent: %q", m)
		}
	}
	if !strings.Contains(told[0], "stui worker wait %2 --brief") {
		t.Fatalf("the parent is not told a brief comes: %q", told[0])
	}
	if f.opts["%2"][OptAbout] != "desktop sidebar and themes" || f.opts["%2"]["@stui-summary"] != "" {
		t.Fatalf("options: %v", f.opts["%2"])
	}

	a.AskBriefs(ctx) // still working
	if len(told) != 1 {
		t.Fatalf("asked while working: %v", told)
	}
	mu.Lock()
	state["%2"] = "done"
	mu.Unlock()
	a.AskBriefs(ctx)
	a.AskBriefs(ctx) // once only
	var mine []string
	for _, m := range told {
		if strings.HasPrefix(m, "%2 ") {
			mine = append(mine, m)
		}
	}
	if len(mine) != 1 || !strings.Contains(mine[0], "You are now a worker") || !strings.Contains(mine[0], "stui worker brief --summary") {
		t.Fatalf("at turn end: %q", mine)
	}
	if f.opts["%2"][OptTellLater] != "" || f.opts["%2"][OptBriefSent] == "" {
		t.Fatalf("after: %v", f.opts["%2"])
	}
	r, _ := Load(res.Run)
	if w := r.Worker("%2"); w == nil || w.About != "desktop sidebar and themes" || w.BriefState != BriefAsked {
		t.Fatalf("record: %+v", w)
	}

	// Let go: role and brief go with the rest.
	if _, err := a.Release(ctx, "%2", false); err != nil {
		t.Fatal(err)
	}
	for _, k := range briefKeys {
		if f.opts["%2"][k] != "" {
			t.Fatalf("%s left after release", k)
		}
	}
}

func TestNoBriefAndIdleChild(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	f := newFake([3]string{"%1", "work", "lead"}, [3]string{"%2", "work", "docs"})
	var told []string
	a := NewAdopter(AdoptOptions{
		Tmux:   briefTmux(f),
		Agents: func() map[string]string { return map[string]string{"%1": "claude", "%2": "claude"} },
		State:  func(string) (string, int64) { return "idle", 0 },
		Deliver: func(_ context.Context, pane, text string) (int, map[string]any) {
			told = append(told, pane)
			return 200, nil
		},
	})
	res, err := a.Adopt(context.Background(), "%1", "%2", AdoptHow{Tell: true, NoBrief: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Told["%2"] != "ok" || res.BriefState != BriefSkipped || res.About != "" {
		t.Fatalf("%+v", res)
	}
	a.AskBriefs(context.Background())
	if len(told) != 2 {
		t.Fatalf("asked though --no-brief: %v", told)
	}
}

func TestCleanBriefAndAbout(t *testing.T) {
	if got := CleanAbout("  a\n b\tc  "); got != "a b c" {
		t.Fatalf("%q", got)
	}
	if got := CleanAbout(strings.Repeat("가", 200)); len([]rune(got)) != 120 || !strings.HasSuffix(got, "…") {
		t.Fatalf("%d", len([]rune(got)))
	}
	got := CleanBrief("one\n\n two \nthree\nfour\nfive\nsix |^| x")
	if got != `one\ntwo\nthree\nfour\nfive` || BriefText(got) != "one\ntwo\nthree\nfour\nfive" {
		t.Fatalf("%q", got)
	}
	if CleanBrief(`a\nb`) != `a\nb` {
		t.Fatal("an escaped newline typed by an agent is a newline")
	}
}

// POST /api/orch/about: an app sets the role after a card was dropped.
func TestServeAbout(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	f := newFake([3]string{"%1", "work", "lead"}, [3]string{"%2", "work", "docs"})
	a := NewAdopter(AdoptOptions{
		Tmux:   briefTmux(f),
		Agents: func() map[string]string { return map[string]string{"%1": "claude", "%2": "claude"} },
	})
	ctx := context.Background()
	res, err := a.Adopt(ctx, "%1", "%2", AdoptHow{})
	if err != nil {
		t.Fatal(err)
	}
	post := func(body string) (int, map[string]any) {
		w := httptest.NewRecorder()
		a.ServeHTTP(w, httptest.NewRequest("POST", "/api/orch/about", strings.NewReader(body)))
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	if code, out := post(`{"pane":"%2","about":"user docs"}`); code != 200 || out["about"] != "user docs" {
		t.Fatalf("%d %v", code, out)
	}
	r, _ := Load(res.Run)
	if f.opts["%2"][OptAbout] != "user docs" || r.Worker("%2").About != "user docs" {
		t.Fatalf("%v", f.opts["%2"])
	}
	if code, _ := post(`{"pane":"%1","about":"x"}`); code != 409 {
		t.Fatalf("a coordinator has no role: %d", code)
	}
	if code, _ := post(`{"pane":"%2","about":""}`); code != 200 || f.opts["%2"][OptAbout] != "" {
		t.Fatal("cleared")
	}
}
