package orch

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// One line of events.jsonl, as the apps read it.
func TestEventLine(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	ok := true
	ev, err := AppendEvent(Event{Run: "r-dc57e9fc", Kind: EvSend, From: "%12", FromName: "coordinator", To: "%31", ToName: "ux",
		Title: "위임을 기록으로 남긴다\nsecond line", Bytes: 4210, OK: &ok})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(EventsFile("r-dc57e9fc"))
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 1 {
		t.Fatalf("lines: %q", b)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &m); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{"v": 1.0, "run": "r-dc57e9fc", "kind": "send", "from": "%12", "fromName": "coordinator",
		"to": "%31", "toName": "ux", "title": "위임을 기록으로 남긴다 second line", "bytes": 4210.0, "ok": true} {
		if m[k] != want {
			t.Errorf("%s = %v, want %v", k, m[k], want)
		}
	}
	if !strings.HasPrefix(ev.ID, "e-") || m["id"] != ev.ID || m["ts"].(float64) <= 0 {
		t.Errorf("id %v ts %v", m["id"], m["ts"])
	}
	for _, k := range []string{"doc", "ref", "up", "since", "machine"} {
		if _, there := m[k]; there {
			t.Errorf("%s written though empty", k)
		}
	}
	if _, err := AppendEvent(Event{Run: "../x", Kind: EvSend}); err == nil {
		t.Error("a bad run id was written")
	}
}

func TestUndelivered(t *testing.T) {
	evs := []Event{
		{ID: "a", Kind: EvSend}, {ID: "b", Kind: EvDone}, {ID: "c", Kind: EvIdle},
		{ID: "d", Kind: EvDelivered, Ref: "b"},
	}
	got := Undelivered(evs)
	if len(got) != 1 || got[0].ID != "c" {
		t.Fatalf("%+v", got)
	}
}

// Mail goes to the nearest parent that is still there and not done.
func TestRecipient(t *testing.T) {
	parents := map[string]string{"%3": "%2", "%2": "%1", "%1": ""}
	alive := map[string]bool{"%1": true, "%2": true, "%3": true}
	done := map[string]bool{}
	ask := func(ev Event) string {
		return Recipient(ev,
			func(x string) (string, bool) { p, ok := parents[x]; return p, ok && alive[x] },
			func(x string) bool { return alive[x] },
			func(x string) bool { return done[x] })
	}
	ev := Event{From: "%3", To: "%2", Up: []string{"%2", "%1"}}
	if got := ask(ev); got != "%2" {
		t.Fatalf("parent at work: %q", got)
	}
	done["%2"] = true
	if got := ask(ev); got != "%1" {
		t.Fatalf("parent done: %q", got)
	}
	done["%2"], alive["%2"] = false, false
	if got := ask(ev); got != "%1" {
		t.Fatalf("parent gone (from what was written): %q", got)
	}
	alive["%1"] = false
	if got := ask(ev); got != "" {
		t.Fatalf("all gone: %q", got)
	}
	if got := ask(Event{From: "%3", To: "ci-mac/%7", Up: []string{"ci-mac/%7"}}); got != "" {
		t.Fatalf("a parent on another machine is told from there: %q", got)
	}
	if got := ChainUp(func(x string) (string, bool) { p, ok := parents[x]; return p, ok }, "%3"); strings.Join(got, ",") != "%2,%1" {
		t.Fatalf("chain: %v", got)
	}
}

// fakeMail is a tmux with panes, their agents' states, and a chat send
// that turns down a box with someone's typing in it.
type fakeMail struct {
	t      *testing.T
	panes  string
	states map[string]string
	since  map[string]int64
	typed  map[string]bool
	sent   []string
	// unsent: the agent takes the keys but not the Enter (just started):
	// the line stays in the box, and the send cannot say it went in.
	unsent map[string]bool
	box    map[string]string
	// queued: it went in behind a turn, and the send could not see it.
	queued map[string]bool
	// submitted: when a prompt last went in, by the submit hook.
	submitted map[string]time.Time
}

func (f *fakeMail) adopter() *Adopter {
	return NewAdopter(AdoptOptions{
		Tmux: func(ctx context.Context, args ...string) (string, error) {
			if args[0] == "list-panes" {
				return f.panes, nil
			}
			return "", nil
		},
		State:     func(p string) (string, int64) { return f.states[p], f.since[p] },
		Submitted: func(p string) time.Time { return f.submitted[p] },
		Deliver: func(ctx context.Context, pane, text string) (int, map[string]any) {
			if f.typed[pane] {
				return 409, map[string]any{"reason": "busy-input"}
			}
			if b := f.box[pane]; b != "" {
				return 409, map[string]any{"reason": "busy-input", "typed": b}
			}
			if f.unsent[pane] {
				if f.box == nil {
					f.box = map[string]string{}
				}
				f.box[pane] = text
				return 200, map[string]any{"delivered": "unknown"}
			}
			f.sent = append(f.sent, pane+" "+text)
			if f.queued[pane] {
				return 200, map[string]any{"delivered": "unknown", "working": true}
			}
			return 200, map[string]any{"delivered": true}
		},
		DeliverOver: func(ctx context.Context, pane, text, own string) (int, map[string]any) {
			if f.box[pane] != own {
				return 409, map[string]any{"reason": "busy-input", "typed": f.box[pane]}
			}
			delete(f.box, pane)
			f.sent = append(f.sent, pane+" "+text+" (over its own)")
			return 200, map[string]any{"delivered": true}
		},
	})
}

const run3 = "r-0000aaaa"

// A coordinator %1, its worker %2 (mobile), and %2's worker %3 (ux).
func threeLevels() string {
	row := func(id, role, parent, name, done string) string {
		return strings.Join([]string{id, run3, "", role, parent, name, done, ""}, "\t")
	}
	return strings.Join([]string{
		row("%1", RoleCoordinatorName, "", "", ""),
		row("%2", RoleWorkerName, "%1", "mobile", ""),
		row("%3", RoleWorkerName, "%2", "ux", "1"),
	}, "\n")
}

func TestMailWaitsForTheTurnEndAndTheEmptyBox(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	now := time.Now().UnixMilli()
	f := &fakeMail{t: t, panes: threeLevels(), states: map[string]string{"%1": "idle", "%2": "working", "%3": "done"},
		since: map[string]int64{"%3": now}, typed: map[string]bool{}}
	a := f.adopter()
	done, _ := AppendEvent(Event{Run: run3, Kind: EvDone, From: "%3", FromName: "ux", To: "%2", ToName: "mobile", Up: []string{"%2", "%1"}, Title: "Made the design doc"})

	// Its parent is in a turn: nothing yet.
	a.MailTick(context.Background())
	if len(f.sent) != 0 {
		t.Fatalf("typed into a parent at work: %v", f.sent)
	}
	// The turn ends, but someone is typing there.
	f.states["%2"], f.since["%2"], f.typed["%2"] = "done", now, true
	a.MailTick(context.Background())
	if len(f.sent) != 0 {
		t.Fatalf("typed over someone's typing: %v", f.sent)
	}
	// The box is emptied: after the retry wait, or at once on a new turn end.
	f.typed["%2"], f.since["%2"] = false, time.Now().UnixMilli()+1
	a.MailTick(context.Background())
	want := "%2 [stui] worker ux (%3) is done: Made the design doc -- stui worker read %3"
	if len(f.sent) != 1 || f.sent[0] != want {
		t.Fatalf("sent %q, want %q", f.sent, want)
	}
	// Once.
	a.MailTick(context.Background())
	if len(f.sent) != 1 {
		t.Fatalf("told twice: %v", f.sent)
	}
	evs := ReadEvents(run3)
	last := evs[len(evs)-1]
	if last.Kind != EvDelivered || last.Ref != done.ID || last.To != "%2" {
		t.Fatalf("delivered: %+v", last)
	}
	// Another carrier (after a restart) reads it as told too.
	if b := f.adopter(); true {
		b.MailTick(context.Background())
		if len(f.sent) != 1 {
			t.Fatalf("told again after a restart: %v", f.sent)
		}
	}
}

func TestMailGoesUpPastADoneParent(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	f := &fakeMail{t: t, states: map[string]string{"%1": "done", "%2": "done", "%3": "done"}, since: map[string]int64{}, typed: map[string]bool{}}
	// mobile (%2) is done too.
	f.panes = strings.Replace(threeLevels(), "mobile\t", "mobile\t1", 1)
	a := f.adopter()
	AppendEvent(Event{Run: run3, Kind: EvDone, From: "%3", FromName: "ux", To: "%2", ToName: "mobile", Up: []string{"%2", "%1"}, Title: "Made the design doc"})
	a.MailTick(context.Background())
	want := "%1 [stui] worker ux (%3, under mobile) is done: Made the design doc -- stui worker read %3"
	if len(f.sent) != 1 || f.sent[0] != want {
		t.Fatalf("sent %q, want %q", f.sent, want)
	}
}

// A worker that ended its turn without stui worker done is told too, once
// a turn end, and not before it took up what it was sent.
func TestMailTellsATurnEndWithoutDone(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	ended := time.Now().Add(-5 * time.Second).UnixMilli()
	f := &fakeMail{t: t, states: map[string]string{"%1": "idle", "%2": "working", "%3": "done"}, since: map[string]int64{"%3": ended}, typed: map[string]bool{}}
	f.panes = strings.Replace(threeLevels(), "ux\t1", "ux\t", 1)
	a := f.adopter()
	a.MailTick(context.Background())
	a.MailTick(context.Background())
	evs := ReadEvents(run3)
	if len(evs) != 1 || evs[0].Kind != EvIdle || evs[0].From != "%3" || evs[0].To != "%2" || evs[0].Since != ended || strings.Join(evs[0].Up, ",") != "%2,%1" {
		t.Fatalf("events: %+v", evs)
	}
	if len(f.sent) != 0 {
		t.Fatalf("typed into a parent at work: %v", f.sent)
	}
	f.states["%2"] = "done"
	a.MailTick(context.Background())
	want := "%2 [stui] worker ux (%3) ended its turn without stui worker done -- stui worker read %3"
	if len(f.sent) != 1 || f.sent[0] != want {
		t.Fatalf("sent %q, want %q", f.sent, want)
	}

	// A worker waiting for its own workers is not stopping short.
	f3 := &fakeMail{t: t, states: map[string]string{"%1": "idle", "%2": "done", "%3": "working"}, since: map[string]int64{"%2": ended}, typed: map[string]bool{}}
	f3.panes = strings.Replace(threeLevels(), "ux\t1", "ux\t", 1)
	t.Setenv("STUI_HOME", t.TempDir())
	f3.adopter().MailTick(context.Background())
	if evs := ReadEvents(run3); len(evs) != 0 {
		t.Fatalf("told the turn end of a worker waiting for its own: %+v", evs)
	}

	// Sent something after that turn end: not idle about it.
	f2 := &fakeMail{t: t, states: map[string]string{"%2": "idle", "%4": "done"}, since: map[string]int64{"%4": ended}, typed: map[string]bool{}}
	f2.panes = strings.Join([]string{"%4", run3, "", RoleWorkerName, "%2", "qa", "", "9999999999999"}, "\t")
	b := f2.adopter()
	b.MailTick(context.Background())
	for _, ev := range ReadEvents(run3) {
		if ev.From == "%4" {
			t.Fatalf("told a turn end from before what it was sent: %+v", ev)
		}
	}
}

// Told only when the agent took it: a line left in the box unsent is not
// delivered, and is sent again, over itself only.
func TestMailToldOnlyWhenTaken(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	f := &fakeMail{t: t, panes: threeLevels(), states: map[string]string{"%1": "idle", "%2": "idle", "%3": "done"},
		since: map[string]int64{}, typed: map[string]bool{}, unsent: map[string]bool{"%2": true}}
	a := f.adopter()
	done, _ := AppendEvent(Event{Run: run3, Kind: EvDone, From: "%3", FromName: "ux", To: "%2", ToName: "mobile", Up: []string{"%2", "%1"}, Title: "Made the design doc"})
	delivered := func() []Event {
		var out []Event
		for _, ev := range ReadEvents(run3) {
			if ev.Kind == EvDelivered {
				out = append(out, ev)
			}
		}
		return out
	}
	a.MailTick(context.Background())
	if f.box["%2"] == "" || len(delivered()) != 0 {
		t.Fatalf("an unsent line was written down as told: box %q, %+v", f.box["%2"], delivered())
	}
	// The agent is ready now; the line still sits there.
	f.unsent["%2"] = false
	a.MailTick(context.Background())
	if len(delivered()) != 0 {
		t.Fatal("tried again before the retry wait")
	}
	a.mail.tried[done.ID] = time.Now().Add(-mailRetry - time.Second)
	a.MailTick(context.Background())
	got := delivered()
	if len(got) != 1 || got[0].Ref != done.ID || got[0].FromName != "ux" || len(f.sent) != 1 || !strings.HasSuffix(f.sent[0], "(over its own)") || f.box["%2"] != "" {
		t.Fatalf("not sent again over its own line: sent %q, delivered %+v", f.sent, got)
	}
	// Someone typed over stui's line: theirs is left alone.
	t.Setenv("STUI_HOME", t.TempDir())
	g := &fakeMail{t: t, panes: threeLevels(), states: map[string]string{"%1": "idle", "%2": "idle", "%3": "done"},
		since: map[string]int64{}, typed: map[string]bool{}, box: map[string]string{"%2": "[stui] worker ux (%3) is done: and my own words"}}
	AppendEvent(Event{Run: run3, Kind: EvDone, From: "%3", FromName: "ux", To: "%2", Up: []string{"%2", "%1"}, Title: "Made the design doc"})
	g.adopter().MailTick(context.Background())
	if len(g.sent) != 0 {
		t.Fatalf("typed over someone's words: %q", g.sent)
	}
}

// Queued behind a turn, the send cannot see it go in: it is told once a
// turn there begins after, and not typed twice.
func TestMailQueuedIsToldAtTheTurn(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	f := &fakeMail{t: t, panes: threeLevels(), states: map[string]string{"%1": "idle", "%2": "done", "%3": "done"},
		since: map[string]int64{"%2": 1}, typed: map[string]bool{}, queued: map[string]bool{"%2": true}}
	a := f.adopter()
	AppendEvent(Event{Run: run3, Kind: EvDone, From: "%3", FromName: "ux", To: "%2", Up: []string{"%2", "%1"}})
	a.MailTick(context.Background())
	count := func() int {
		n := 0
		for _, ev := range ReadEvents(run3) {
			if ev.Kind == EvDelivered {
				n++
			}
		}
		return n
	}
	if count() != 0 || len(f.sent) != 1 {
		t.Fatalf("first: %d told, sent %q", count(), f.sent)
	}
	f.states["%2"], f.since["%2"] = "working", time.Now().UnixMilli()+5
	a.MailTick(context.Background())
	if count() != 1 || len(f.sent) != 1 {
		t.Fatalf("at the turn: %d told, sent %q", count(), f.sent)
	}
}

// Typed and never seen taken (the send said "unknown", and the parent sat
// between turns): typed once more after mailRetry, then written down as
// typed but unconfirmed and not typed again. It is still owed: one pointer
// to the inbox follows, and no more with no turn there.
func TestMailUnconfirmedIsTypedTwiceAtMost(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	f := &fakeMail{t: t, panes: threeLevels(), states: map[string]string{"%1": "idle", "%2": "idle", "%3": "done"},
		since: map[string]int64{"%2": 1}, typed: map[string]bool{}, queued: map[string]bool{"%2": true}}
	a := f.adopter()
	done, _ := AppendEvent(Event{Run: run3, Kind: EvDone, From: "%3", FromName: "ux", To: "%2", Up: []string{"%2", "%1"}})
	a.MailTick(context.Background())
	for i := 0; i < 10; i++ {
		a.mail.tried[done.ID] = time.Now().Add(-mailRetry - time.Second)
		a.MailTick(context.Background())
	}
	full, pointers := 0, 0
	for _, l := range f.sent {
		if strings.Contains(l, "stui worker inbox") {
			pointers++
		} else {
			full++
		}
	}
	if full != mailTypings || pointers != 1 {
		t.Fatalf("typed the line %d times (want %d) and %d pointers (want 1): %q", full, mailTypings, pointers, f.sent)
	}
	var got []Event
	for _, ev := range ReadEvents(run3) {
		if ev.Kind == EvDelivered {
			got = append(got, ev)
		}
	}
	if len(got) != 1 || got[0].Ref != done.ID || got[0].Why != "unconfirmed" {
		t.Fatalf("delivered: %+v", got)
	}
}

// An adopted agent's turns before it is sent work -- the conversation it
// was having, the brief stui asks of it -- are not work it stopped short of
// (measured: both were told to the coordinator, after its workers' done).
func TestMailAdoptedTurnEndsBeforeWork(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	ended := time.Now().Add(-5 * time.Second).UnixMilli()
	row := func(sent, adopted string) string {
		return strings.Join([]string{"%1", run3, "", RoleCoordinatorName, "", "", "", "", ""}, "\t") + "\n" +
			strings.Join([]string{"%2", run3, "", RoleWorkerName, "%1", "tests", "", sent, adopted}, "\t")
	}
	f := &fakeMail{t: t, panes: row("", "1"), states: map[string]string{"%1": "idle", "%2": "done"}, since: map[string]int64{"%2": ended}, typed: map[string]bool{}}
	a := f.adopter()
	a.MailTick(context.Background())
	if evs := ReadEvents(run3); len(evs) != 0 {
		t.Fatalf("told a turn end of an adopted agent given nothing: %+v", evs)
	}
	// Sent work, and the turn after it ends without done: that is told.
	f.panes = row(strconv.FormatInt(ended+1, 10), "1")
	f.since["%2"] = ended + 2
	a.MailTick(context.Background())
	if evs := ReadEvents(run3); len(evs) == 0 || evs[0].Kind != EvIdle || evs[0].From != "%2" || evs[0].Since != ended+2 {
		t.Fatalf("events: %+v", evs)
	}
}

// A line the send could not confirm, taken in a turn that began and ended
// between two looks, the pane idle after it: the submit hook says it went
// in, so it is told, not typed again. An idle with no prompt submitted
// since (Claude's idle_prompt) is not taken: that line is typed again.
func TestMailTakenBetweenLooksIsNotTypedAgain(t *testing.T) {
	for _, c := range []struct {
		name      string
		submitted bool
		typed     int
	}{{"submitted", true, 1}, {"idle_prompt only", false, mailTypings + 1}} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("STUI_HOME", t.TempDir())
			f := &fakeMail{t: t, panes: threeLevels(), states: map[string]string{"%1": "idle", "%2": "done", "%3": "done"},
				since: map[string]int64{"%2": 1}, typed: map[string]bool{}, queued: map[string]bool{"%2": true}, submitted: map[string]time.Time{}}
			a := f.adopter()
			done, _ := AppendEvent(Event{Run: run3, Kind: EvDone, From: "%3", FromName: "ux", To: "%2", Up: []string{"%2", "%1"}})
			a.MailTick(context.Background())
			at := a.mail.pending[done.ID].at
			f.states["%2"], f.since["%2"] = "idle", at.UnixMilli()+60_000
			if c.submitted {
				f.submitted["%2"] = at.Add(200 * time.Millisecond)
			}
			for i := 0; i < 5; i++ {
				a.mail.tried[done.ID] = time.Now().Add(-mailRetry - time.Second)
				a.MailTick(context.Background())
			}
			if len(f.sent) != c.typed {
				t.Fatalf("typed %d times, want %d: %q", len(f.sent), c.typed, f.sent)
			}
			var why []string
			for _, ev := range ReadEvents(run3) {
				if ev.Kind == EvDelivered {
					why = append(why, ev.Why)
				}
			}
			want := ""
			if !c.submitted {
				want = "unconfirmed"
			}
			if len(why) != 1 || why[0] != want {
				t.Fatalf("delivered whys %q, want one %q", why, want)
			}
		})
	}
}

// A done that failed says so in its parent's line; a late done is not mail.
func TestFailedDoneAndLateDone(t *testing.T) {
	ev := Event{Kind: EvDone, From: "%3", FromName: "ux", To: "%2", Title: "no token for the registry", Outcome: OutcomeFailed}
	if got := mailLine(ev, "%2", time.Now()); got != "[stui] worker ux (%3) is done and failed: no token for the registry -- stui worker read %3" {
		t.Errorf("%q", got)
	}
	ev.Outcome = ""
	if got := mailLine(ev, "%2", time.Now()); got != "[stui] worker ux (%3) is done: no token for the registry -- stui worker read %3" {
		t.Errorf("no outcome (an older stui) reads as succeeded: %q", got)
	}
	late := Event{ID: "e-1", Kind: EvLateDone, From: "%3", To: "%2"}
	if n := len(Undelivered([]Event{late})); n != 0 {
		t.Errorf("a late done is mail: %d", n)
	}
	if reportedSinceLastWork([]Event{{Kind: EvSend, To: "%3", TS: 1}, {Kind: EvLateDone, From: "%3", TS: 2}}, "%3") {
		t.Error("a late done counted as the report of the work sent")
	}
}
