package orch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/sophonz-labs/stui/internal/chat"
)

func seoul(t *testing.T) *time.Location {
	l, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// The limit lines agents write, and when they say it resets.
func TestLimitIn(t *testing.T) {
	kst := seoul(t)
	noon := time.Date(2026, 10, 24, 12, 0, 0, 0, kst)
	at := func(h, m int) int64 { return time.Date(2026, 10, 24, h, m, 0, 0, kst).UnixMilli() }
	for _, c := range []struct {
		text   string
		now    time.Time
		resets int64
	}{
		// What Claude Code wrote (its transcript, as the
		// turn's reply).
		{"You've hit your weekly limit · resets 3pm (Asia/Seoul)", noon, at(15, 0)},
		{"  ⎿  You've hit your limit · resets 3pm (Asia/Seoul)\n     /upgrade to increase your usage limit.", noon, at(15, 0)},
		// After the time of day: the next one.
		{"You've hit your weekly limit · resets 3pm (Asia/Seoul)", noon.Add(4 * time.Hour), at(15, 0) + 24*3600*1000},
		{"You've hit your weekly limit · resets Oct 27, 9:30am (Asia/Seoul)", noon, time.Date(2026, 10, 27, 9, 30, 0, 0, kst).UnixMilli()},
		{"5-hour limit reached ∙ resets 3pm", noon, at(15, 0)},
		{"Claude AI usage limit reached|1761300000", noon, 1761300000 * 1000},
		// Codex.
		{"■ You've hit your usage limit. Upgrade to Pro (https://openai.com/chatgpt/pricing), or try again in 2 days 3 hours 5 minutes.", noon,
			noon.Add(51*time.Hour + 5*time.Minute).UnixMilli()},
		{"■ You've hit your usage limit. Try again at 3:05 PM.", noon, at(15, 5)},
		// Gemini: no time.
		{"Usage limit reached for all Pro models.", noon, 0},
		// A time zone stui cannot load: the limit, not when.
		{"You've hit your weekly limit · resets 3pm (Mars/Olympus)", noon, 0},
	} {
		line, resets, ok := LimitIn(c.text, c.now)
		if !ok || line == "" {
			t.Errorf("%q: not a limit", c.text)
			continue
		}
		if resets != c.resets {
			t.Errorf("%q: resets %v, want %v", c.text, time.UnixMilli(resets).In(kst), time.UnixMilli(c.resets).In(kst))
		}
	}
	for _, text := range []string{
		"",
		"Done. The test checks that you've hit your weekly limit when the API says so.",
		"Rate limits are handled by retrying.",
		"The usage limit reached in the test is fake.",
		"quota handling: see quota.go",
	} {
		if line, _, ok := LimitIn(text, noon); ok {
			t.Errorf("%q read as a limit (%q)", text, line)
		}
	}
}

// Why a turn ended: the limit from the conversation or the bottom of the
// pane, an error, an interrupt, and end_turn for the rest.
func TestJudgeStop(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, seoul(t))
	limit := "You've hit your weekly limit · resets 3pm (Asia/Seoul)"
	for _, c := range []struct {
		name, state, source string
		te                  chat.TurnEnd
		screen              string
		want                string
	}{
		{"claude's limit, its reply", "error", "", chat.TurnEnd{Known: true, Answer: limit, Error: limit}, "", StopLimit},
		{"a limit on screen, no reply (codex)", "done", "", chat.TurnEnd{}, "■ You've hit your usage limit. Try again at 3:05 PM.\n› ", StopLimit},
		// A turn that answered: an old limit still on the screen is not this
		// turn's.
		{"an old limit on screen", "done", "", chat.TurnEnd{Known: true, Answer: "pong"}, limit + "\n● pong", StopEndTurn},
		// A reply that talks about a limit is not one.
		{"a reply about limits", "done", "", chat.TurnEnd{Known: true, Answer: "Fixed: the mail says it when " + limit}, "", StopEndTurn},
		{"an API error", "error", "", chat.TurnEnd{Known: true, Answer: "API Error: 529 overloaded", Error: "API Error: 529 overloaded"}, "", StopError},
		{"error, nothing written", "error", "", chat.TurnEnd{}, "", StopError},
		{"claude's Esc", "done", "", chat.TurnEnd{Known: true, Interrupted: true}, "", StopCancelled},
		{"codex's Esc", "idle", "interrupt", chat.TurnEnd{}, "", StopCancelled},
		{"a turn end", "done", "hook", chat.TurnEnd{Known: true, Answer: "Made the doc."}, "", StopEndTurn},
		{"nothing known", "done", "", chat.TurnEnd{}, "", StopEndTurn},
	} {
		got := JudgeStop(c.state, c.source, c.te, c.screen, now)
		if got.Reason != c.want {
			t.Errorf("%s: %+v, want %s", c.name, got, c.want)
		}
		if c.want == StopLimit && got.Why == "" {
			t.Errorf("%s: no line", c.name)
		}
	}
	if got := JudgeStop("error", "", chat.TurnEnd{Known: true, Error: "API Error: 529 overloaded"}, "", now); got.Why != "API Error: 529 overloaded" {
		t.Errorf("error line: %q", got.Why)
	}
}

// The line typed into the parent says why, and does not offer a read
// that gives nothing.
func TestMailLineByStopReason(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.Local)
	resets := time.Date(2026, 10, 24, 15, 0, 0, 0, time.Local).UnixMilli()
	base := Event{Kind: EvIdle, From: "%12", FromName: "terminal-go", To: "%9"}
	with := func(reason, why string, at int64) Event {
		ev := base
		ev.StopReason, ev.Why, ev.ResetsAt = reason, why, at
		return ev
	}
	old := "[stui] worker terminal-go (%12) ended its turn without stui worker done -- stui worker read %12"
	for _, c := range []struct {
		ev   Event
		want string
	}{
		{with(StopLimit, "You've hit your weekly limit · resets 3pm (Asia/Seoul)", resets),
			`[stui] worker terminal-go (%12) stopped at its usage limit: "You've hit your weekly limit · resets 3pm (Asia/Seoul)" -- nothing to read; it takes work again after 15:00`},
		{with(StopLimit, "Usage limit reached for all Pro models.", 0),
			`[stui] worker terminal-go (%12) stopped at its usage limit: "Usage limit reached for all Pro models." -- nothing to read; it takes work again once the limit resets`},
		{with(StopError, "API Error: 529 overloaded", 0),
			`[stui] worker terminal-go (%12) stopped on an error: "API Error: 529 overloaded" -- stui worker read %12`},
		{with(StopError, "", 0), "[stui] worker terminal-go (%12) stopped on an error -- stui worker read %12"},
		{with(StopCancelled, "", 0), "[stui] worker terminal-go (%12) was interrupted at its pane, without stui worker done -- stui worker read %12"},
		{with(StopEndTurn, "", 0), old},
		// An older stui wrote none; a newer one may write one this stui does
		// not know. Both are the end of a turn.
		{with("", "", 0), old},
		{with("max_tokens", "", 0), old},
		{with("_handoff", "", 0), old},
	} {
		if got := mailLine(c.ev, "%9", now); got != c.want {
			t.Errorf("%q:\n got %s\nwant %s", c.ev.StopReason, got, c.want)
		}
		if c.ev.StopReason == StopLimit && strings.Contains(mailLine(c.ev, "%9", now), "stui worker read") {
			t.Errorf("a limit offers a read")
		}
	}
}

// Lines an older stui wrote (no stopReason), and a reason this stui does not
// know, read as they are.
func TestReadIdleWithoutStopReason(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	if err := os.MkdirAll(RunDir(run3), 0o755); err != nil {
		t.Fatal(err)
	}
	lines := `{"v":1,"id":"e-1","ts":1,"run":"r-0000aaaa","kind":"idle","from":"%11","fromName":"web","to":"%9","toName":"coordinator","up":["%9"],"since":1}
{"v":1,"id":"e-2","ts":2,"run":"r-0000aaaa","kind":"idle","from":"%12","to":"%9","since":2,"stopReason":"_something_new","resetsAt":5}
`
	if err := os.WriteFile(filepath.Join(RunDir(run3), "events.jsonl"), []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	evs := ReadEvents(run3)
	if len(evs) != 2 || evs[0].StopReason != "" || evs[1].StopReason != "_something_new" || evs[1].ResetsAt != 5 {
		t.Fatalf("%+v", evs)
	}
	if got := len(Undelivered(evs)); got != 2 {
		t.Fatalf("undelivered %d", got)
	}
	for _, ev := range evs {
		if l := MailLine(ev, "%9", "coordinator"); !strings.Contains(l, "ended its turn without stui worker done") {
			t.Errorf("%s: %s", ev.ID, l)
		}
	}
	if StopWord("") != "" || StopWord("_something_new") != "" || StopWord(StopLimit) != "limit" {
		t.Error("stop words")
	}
}

// The carrier writes why: a worker at its limit (Claude's reply), and one
// whose turn the person stopped (Codex's Esc: idle, from an interrupt).
func TestMailWritesTheStopReason(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	ended := time.Now().Add(-5 * time.Second).UnixMilli()
	limit := "You've hit your weekly limit · resets 3pm (Asia/Seoul)"
	f := &fakeMail{t: t, states: map[string]string{"%1": "idle", "%2": "done", "%3": "error"}, since: map[string]int64{"%3": ended}, typed: map[string]bool{}}
	f.panes = strings.Replace(threeLevels(), "ux\t1", "ux\t", 1)
	a := f.adopter()
	a.o.LastTurn = func(p string) chat.TurnEnd {
		if p == "%3" {
			return chat.TurnEnd{Known: true, Answer: limit, Error: limit}
		}
		return chat.TurnEnd{}
	}
	a.MailTick(context.Background())
	var ev *Event
	for _, e := range ReadEvents(run3) {
		if e.Kind == EvIdle && e.From == "%3" {
			e := e
			ev = &e
		}
	}
	if ev == nil || ev.StopReason != StopLimit || ev.Why != limit || ev.ResetsAt == 0 {
		t.Fatalf("limit: %+v", ev)
	}
	a.MailTick(context.Background())
	if len(f.sent) != 1 || !strings.HasPrefix(f.sent[0], "%2 [stui] worker ux (%3) stopped at its usage limit") || strings.Contains(f.sent[0], "stui worker read") {
		t.Fatalf("sent %q", f.sent)
	}
	if got := LastStop(ReadEvents(run3), "%3"); got == nil || got.StopReason != StopLimit {
		t.Fatalf("last stop: %+v", got)
	}

	// Codex's Esc: idle from an interrupt is a turn end; idle from
	// anything else follows a done, told already.
	t.Setenv("STUI_HOME", t.TempDir())
	g := &fakeMail{t: t, states: map[string]string{"%1": "idle", "%2": "working", "%3": "idle"}, since: map[string]int64{"%3": ended}, typed: map[string]bool{}}
	g.panes = f.panes
	source := "idle_prompt"
	b := g.adopter()
	b.o.Source = func(p string) string { return source }
	b.MailTick(context.Background())
	if evs := ReadEvents(run3); len(evs) != 0 {
		t.Fatalf("idle after a done told again: %+v", evs)
	}
	source, g.since["%3"] = "interrupt", ended+1
	b.MailTick(context.Background())
	if evs := ReadEvents(run3); len(evs) != 1 || evs[0].StopReason != StopCancelled {
		t.Fatalf("interrupt: %+v", evs)
	}
}

// The idle that says why a worker last stopped: none once it was given
// work, or reported, after it.
func TestLastStop(t *testing.T) {
	evs := []Event{
		{Kind: EvIdle, From: "%3", StopReason: StopLimit},
		{Kind: EvIdle, From: "%4", StopReason: StopError},
	}
	if got := LastStop(evs, "%3"); got == nil || got.StopReason != StopLimit {
		t.Fatalf("%+v", got)
	}
	if got := LastStop(append(evs, Event{Kind: EvSend, To: "%3"}), "%3"); got != nil {
		t.Fatalf("after a send: %+v", got)
	}
	if got := LastStop(append(evs, Event{Kind: EvDone, From: "%3"}), "%3"); got != nil {
		t.Fatalf("after done: %+v", got)
	}
	if got := LastStop(evs, "%5"); got != nil {
		t.Fatalf("none: %+v", got)
	}
}
