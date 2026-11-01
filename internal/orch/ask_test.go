package orch

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// A question is typed into the pane it is for; the answer wakes the one
// waiting and is not typed at it; a long question is kept whole.
func TestQuestionAndAnswer(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	f := &fakeMail{t: t, panes: threeLevels(), states: map[string]string{"%1": "idle", "%2": "idle", "%3": "working"},
		since: map[string]int64{}, typed: map[string]bool{}, submitted: map[string]time.Time{}}
	a := f.adopter()
	ask, err := Ask(run3, "%2", "mobile", "%1", "coordinator", "Which branch do I base it on?")
	if err != nil {
		t.Fatal(err)
	}
	stop := Waiting("%2")
	a.MailTick(context.Background())
	want := `[stui] mobile (%2) asks: Which branch do I base it on? -- answer: stui worker reply ` + ask.ID + ` "<answer>"`
	if got := f.at("%1"); len(got) != 1 || got[0] != want {
		t.Fatalf("typed %q, want %q", got, want)
	}
	// Answered while it waits: it gets it from its wait, not typed in.
	if _, ok := WaitReply(ask, time.Now().Add(50*time.Millisecond), 10*time.Millisecond); ok {
		t.Fatal("an answer before one was given")
	}
	if _, err := Reply(ask, "%1", "coordinator", "", "main, after the merge"); err != nil {
		t.Fatal(err)
	}
	r, ok := WaitReply(ask, time.Now().Add(time.Second), 10*time.Millisecond)
	if !ok || FullText(r) != "main, after the merge" || r.To != "%2" {
		t.Fatalf("%+v %v", r, ok)
	}
	// stui worker question takes what it woke on.
	Ack("%2", []Mail{{Event: r}})
	a.MailTick(context.Background())
	if got := f.at("%2"); len(got) != 0 {
		t.Fatalf("typed the answer at the one waiting for it: %q", got)
	}
	// The question is taken with its answer.
	owner := a.owner(parseMailPanes(f.panes))
	if m := Inbox("%1", owner); len(m) != 0 {
		t.Fatalf("the answered question is still owed: %+v", m)
	}
	// Stopped waiting (it timed out) before the answer came: the answer is
	// mail for it, typed between its turns.
	stop()
	ask2, _ := Ask(run3, "%2", "mobile", "%1", "coordinator", "And the tag?")
	a.MailTick(context.Background())
	Reply(ask2, "%1", "coordinator", "", "v0.3.113")
	a.MailTick(context.Background())
	if got := f.at("%2"); len(got) != 1 || !strings.Contains(got[0], "answered your question: v0.3.113") || !strings.Contains(got[0], "--resume "+ask2.ID) {
		t.Fatalf("%q", got)
	}
	// A long question is kept whole, and the line says so.
	long := strings.Repeat("a detail. ", 60)
	ask3, _ := Ask(run3, "%2", "mobile", "%1", "coordinator", "First line\n"+long)
	if ask3.File == "" || FullText(ask3) != "First line\n"+long {
		t.Fatalf("not kept whole: %q", ask3.File)
	}
	if b, _ := os.ReadFile(ask3.File); len(b) == 0 {
		t.Fatal("no copy")
	}
	if l := askLine(ask3); !strings.Contains(l, "asks: First line (stui worker inbox shows all of it)") {
		t.Fatalf("%q", l)
	}
}

// POST /api/orch/reply answers from an app, once.
func TestReplyRoute(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	f := &fakeMail{t: t, panes: threeLevels(), states: map[string]string{}, since: map[string]int64{}, typed: map[string]bool{}}
	a := f.adopter()
	ask, _ := Ask(run3, "%2", "mobile", "%1", "coordinator", "Ship it?")
	post := func(body string) int {
		w := httptest.NewRecorder()
		a.ServeHTTP(w, httptest.NewRequest("POST", "/api/orch/reply", strings.NewReader(body)))
		return w.Code
	}
	if c := post(`{"id":"e-none","text":"yes"}`); c != 404 {
		t.Fatalf("no such question: %d", c)
	}
	if c := post(`{"id":"` + ask.ID + `","text":" "}`); c != 400 {
		t.Fatalf("an empty answer: %d", c)
	}
	if c := post(`{"id":"` + ask.ID + `","text":"yes"}`); c != 200 {
		t.Fatalf("answer: %d", c)
	}
	r, ok := ReplyTo(ask)
	if !ok || r.By != "app" || FullText(r) != "yes" {
		t.Fatalf("%+v", r)
	}
	if c := post(`{"id":"` + ask.ID + `","text":"no"}`); c != 409 {
		t.Fatalf("answered twice: %d", c)
	}
}
