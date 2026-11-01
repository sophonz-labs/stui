package orch

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// What was typed at one pane.
func (f *fakeMail) at(pane string) []string {
	var out []string
	for _, s := range f.sent {
		if rest, ok := strings.CutPrefix(s, pane+" "); ok {
			out = append(out, rest)
		}
	}
	return out
}

func owedSend(t *testing.T, to string) Event {
	t.Helper()
	no := false
	ev, err := AppendEvent(Event{Run: run3, Kind: EvSend, From: "%1", To: to, ToName: "mobile", Title: "the brief", File: "/tmp/brief.md", OK: &no, Why: "busy-input", Owed: true})
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func kinds(run, kind string) []Event {
	var out []Event
	for _, ev := range ReadEvents(run) {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

// A send stui worker send could not type (someone was typing there): no
// pointer while the box is busy or the worker is in a turn; one once it is
// between turns with an empty box.
func TestOwedSendIsPointedToBetweenTurns(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	f := &fakeMail{t: t, panes: threeLevels(), states: map[string]string{"%1": "working", "%2": "working", "%3": "done"},
		since: map[string]int64{}, typed: map[string]bool{"%2": true}, submitted: map[string]time.Time{}}
	a := f.adopter()
	send := owedSend(t, "%2")
	a.MailTick(context.Background())
	if got := f.at("%2"); len(got) != 0 {
		t.Fatalf("typed at a worker in a turn: %q", got)
	}
	f.states["%2"], f.since["%2"] = "idle", time.Now().UnixMilli()
	a.MailTick(context.Background())
	if got := f.at("%2"); len(got) != 0 {
		t.Fatalf("typed over someone's typing: %q", got)
	}
	f.typed["%2"] = false
	a.mail.tried["point:%2"] = time.Now().Add(-mailRetry - time.Second)
	a.MailTick(context.Background())
	if got := f.at("%2"); len(got) != 1 || got[0] != "[stui] 1 message for you -- stui worker inbox" {
		t.Fatalf("pointer: %q", got)
	}
	if p := kinds(run3, EvPointed); len(p) != 1 || p[0].Ref != send.ID || p[0].To != "%2" {
		t.Fatalf("pointed: %+v", p)
	}
	// Not again with no turn there.
	for i := 0; i < 3; i++ {
		a.mail.tried["point:%2"] = time.Now().Add(-mailRetry - time.Second)
		f.since["%2"] = time.Now().UnixMilli() + int64(i) // idle_prompt moves it
		a.MailTick(context.Background())
	}
	if got := f.at("%2"); len(got) != 1 {
		t.Fatalf("pointed again with no prompt there: %q", got)
	}
}

// Waiter first: a pane waiting in stui worker inbox --wait (or stui worker
// wait) is not typed at; its wait wakes on the mail.
func TestWaiterIsNotTypedAt(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	f := &fakeMail{t: t, panes: threeLevels(), states: map[string]string{"%1": "idle", "%2": "idle", "%3": "done"},
		since: map[string]int64{}, typed: map[string]bool{}, submitted: map[string]time.Time{}}
	a := f.adopter()
	owedSend(t, "%2")
	stop := Waiting("%2")
	a.MailTick(context.Background())
	if got := f.at("%2"); len(got) != 0 {
		t.Fatalf("typed at a pane that waits: %q", got)
	}
	// Its wait finds it.
	owner := a.owner(parseMailPanes(f.panes))
	if m := WaitMail("%2", owner, time.Now().Add(time.Second), 10*time.Millisecond); len(m) != 1 {
		t.Fatalf("the wait found %d", len(m))
	}
	// A done for %1 waiting the same way is not typed either.
	stop1 := Waiting("%1")
	AppendEvent(Event{Run: run3, Kind: EvDone, From: "%2", FromName: "mobile", To: "%1", Up: []string{"%1"}})
	a.MailTick(context.Background())
	if got := f.at("%1"); len(got) != 0 {
		t.Fatalf("a done typed at a waiting parent: %q", got)
	}
	stop()
	stop1()
	a.MailTick(context.Background())
	if len(f.at("%2")) != 1 || len(f.at("%1")) != 1 {
		t.Fatalf("once nobody waits: %q", f.sent)
	}
}

// Owed until taken: pointed again after a turn there that did not take
// it, at most mailPoints times; stui worker inbox takes it and it stops.
func TestMailIsOwedUntilAcked(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	f := &fakeMail{t: t, panes: threeLevels(), states: map[string]string{"%1": "idle", "%2": "idle", "%3": "done"},
		since: map[string]int64{}, typed: map[string]bool{}, submitted: map[string]time.Time{}}
	a := f.adopter()
	owedSend(t, "%2")
	tick := func() {
		if a.mail.tried != nil {
			a.mail.tried["point:%2"] = time.Now().Add(-mailRetry - time.Second)
		}
		a.MailTick(context.Background())
	}
	tick()
	for i := 0; i < 5; i++ {
		// The pointer went in, a turn ran, and nobody ran stui worker inbox.
		f.submitted["%2"] = time.Now().Add(time.Second)
		time.Sleep(2 * time.Millisecond)
		tick()
	}
	if got := f.at("%2"); len(got) != mailPoints {
		t.Fatalf("pointed %d times, want %d: %q", len(got), mailPoints, got)
	}
	owner := a.owner(parseMailPanes(f.panes))
	m := Inbox("%2", owner)
	if len(m) != 1 || m[0].Pointed != mailPoints {
		t.Fatalf("inbox: %+v", m)
	}
	if got := Ack("%2", m); len(got) != 1 {
		t.Fatalf("acked %v", got)
	}
	if m := Inbox("%2", owner); len(m) != 0 {
		t.Fatalf("still owed after the ack: %+v", m)
	}
	// A new piece is pointed to, and says how many.
	owedSend(t, "%2")
	owedSend(t, "%2")
	tick()
	if got := f.at("%2"); got[len(got)-1] != "[stui] 2 messages for you -- stui worker inbox" {
		t.Fatalf("%q", got)
	}
}

// What is mail: a send not typed (owed), a done whose line was not seen to
// go in. Not: a send that went in, a send an older stui could not type (its
// sender was told), a done typed and taken, one already acked.
func TestWhatIsOwed(t *testing.T) {
	yes, no := true, false
	evs := []Event{
		{ID: "s1", Kind: EvSend, To: "%2", OK: &no, Owed: true},
		{ID: "s2", Kind: EvSend, To: "%2", OK: &yes},
		{ID: "s3", Kind: EvSend, To: "%2", OK: &no},
		{ID: "s4", Kind: EvSend, To: "%2", OK: &yes, Owed: true},
		{ID: "d4", Kind: EvDelivered, Ref: "s4"},
		{ID: "s5", Kind: EvSend, To: "box/%2", Machine: "box", OK: &no, Owed: true},
		{ID: "o1", Kind: EvDone, From: "%3", To: "%2"},
		{ID: "o2", Kind: EvDone, From: "%3", To: "%2"},
		{ID: "d2", Kind: EvDelivered, Ref: "o2"},
		{ID: "o3", Kind: EvDone, From: "%3", To: "%2"},
		{ID: "d3", Kind: EvDelivered, Ref: "o3", Why: Unconfirmed},
		{ID: "o4", Kind: EvIdle, From: "%3", To: "%2"},
		{ID: "a4", Kind: EvAcked, Ref: "o4"},
	}
	var got []string
	for _, m := range owed(evs) {
		got = append(got, m.ID)
		if m.ID == "o3" && !m.Typed {
			t.Error("o3 was typed")
		}
	}
	if strings.Join(got, " ") != "s1 o1 o3" {
		t.Fatalf("owed %v", got)
	}
	// o1 is still the carrier's to type in full; o4 was taken from the inbox.
	var full []string
	for _, ev := range Undelivered(evs) {
		full = append(full, ev.ID)
	}
	if strings.Join(full, " ") != "o1" {
		t.Fatalf("undelivered %v", full)
	}
}

// GET /api/orch/inbox shows what is owed without taking it; POST
// /api/orch/inbox/ack takes it.
func TestInboxRoutes(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	f := &fakeMail{t: t, panes: threeLevels(), states: map[string]string{"%1": "idle", "%2": "idle", "%3": "done"},
		since: map[string]int64{}, typed: map[string]bool{}, submitted: map[string]time.Time{}}
	a := f.adopter()
	send := owedSend(t, "%2")
	get := func() []map[string]any {
		w := httptest.NewRecorder()
		if !a.ServeHTTP(w, httptest.NewRequest("GET", "/api/orch/inbox?pane=%252", nil)) || w.Code != 200 {
			t.Fatalf("GET: %d %s", w.Code, w.Body)
		}
		var out struct {
			Mail []map[string]any `json:"mail"`
		}
		json.Unmarshal(w.Body.Bytes(), &out)
		return out.Mail
	}
	if m := get(); len(m) != 1 || m[0]["id"] != send.ID || m[0]["kind"] != "send" || m[0]["owed"] != true {
		t.Fatalf("%+v", m)
	}
	if m := get(); len(m) != 1 {
		t.Fatal("reading took it")
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, httptest.NewRequest("POST", "/api/orch/inbox/ack", strings.NewReader(`{"pane":"%2","ids":["`+send.ID+`","e-nothing"]}`)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"acked":["`+send.ID+`"]`) {
		t.Fatalf("ack: %d %s", w.Code, w.Body)
	}
	if m := get(); len(m) != 0 {
		t.Fatalf("still owed: %+v", m)
	}
	w = httptest.NewRecorder()
	a.ServeHTTP(w, httptest.NewRequest("POST", "/api/orch/inbox/ack", strings.NewReader(`{"pane":"%2"}`)))
	if w.Code != 400 {
		t.Fatalf("an ack of nothing: %d", w.Code)
	}
}
