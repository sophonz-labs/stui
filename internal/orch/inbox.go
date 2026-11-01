package orch

// The inbox: mail that did not get in when it
// was typed waits here until the pane it is for takes it.
//
// Mail is three kinds of event: a send (work for a worker), and a worker's
// done and idle (news for its parent). Each is typed once as a line of its
// own -- stui worker send types the send, the carrier (mail.go) the done and
// idle. When that line is taken (the chat send saw the prompt go in, or a
// turn began there after) a "delivered" event says so, and that is the end
// of it. When it is not -- the box had someone's typing in it, a question
// was up, the line went in but no turn was seen to start -- the mail is
// still owed. The carrier then types a pointer between the pane's turns,
// "[stui] 2 messages for you -- stui worker inbox", and stui worker inbox shows
// what is owed and marks it taken ("acked").
//
// The state is folded from events.jsonl, as the rest of the mail is: an
// "acked" or a plain "delivered" ends a piece of mail, a "pointed" counts a
// pointer typed for it. Nothing is rewritten, so an older stui that does not
// know these kinds reads past them: it still types done and idle once, and
// the worst a mix of the two on one machine does is show a piece of mail
// twice, never lose one.

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sophonz-labs/stui/internal/proc"
)

const (
	// A piece of mail was taken by the pane it was for (stui worker inbox,
	// or POST /api/orch/inbox/ack). Ref is the mail's event id, From who
	// took it.
	EvAcked = "acked"
	// A pointer was typed for a piece of mail: Ref is the mail, To the pane.
	EvPointed = "pointed"
)

const (
	// mailPoints is how many pointers one piece of mail gets. A pointer is
	// typed again only after a turn there ended without taking it; after
	// this many it stays in the inbox and is not typed about again (a new
	// piece of mail is). An agent that never runs stui worker inbox would
	// otherwise be prompted into a turn by every one of its turn ends.
	mailPoints = 3
	// Unconfirmed is the delivered event's why for a line typed but not
	// seen to go in: still owed (what an older stui wrote and then dropped).
	Unconfirmed = "unconfirmed"
)

// Mail is one piece of mail owed to a pane.
type Mail struct {
	Event
	// Pointers typed for it so far, and the last one's time (ms).
	Pointed   int   `json:"pointed"`
	PointedAt int64 `json:"pointedAt,omitempty"`
	// Its line was typed and not seen to go in: the carrier points to it
	// rather than typing the line again.
	Typed bool `json:"typed,omitempty"`
}

// IsMail is whether ev is mail: a send, a done, an idle, an ask, a reply.
func IsMail(ev Event) bool {
	switch ev.Kind {
	case EvSend, EvDone, EvIdle, EvAsk, EvReply:
		return true
	}
	return false
}

// toPane is mail that goes to its To and nowhere else: work, a question, an
// answer. A done or idle goes up its chain (Recipient).
func toPane(ev Event) bool {
	return ev.Kind == EvSend || ev.Kind == EvAsk || ev.Kind == EvReply
}

// owed is a run's mail not yet taken, with its pointers, oldest first.
// Undelivered (mail.go) is the done and idle whose line has not been typed
// at all; this is everything still owed, typed or not.
func owed(evs []Event) []Mail {
	type mark struct {
		taken, typed bool
		points       int
		last         int64
	}
	m := map[string]*mark{}
	at := func(id string) *mark {
		if m[id] == nil {
			m[id] = &mark{}
		}
		return m[id]
	}
	for _, ev := range evs {
		switch ev.Kind {
		case EvAcked:
			at(ev.Ref).taken = true
		case EvDelivered:
			if ev.Why == Unconfirmed {
				at(ev.Ref).typed = true
			} else {
				at(ev.Ref).taken = true
			}
		case EvPointed:
			k := at(ev.Ref)
			k.points++
			k.last = max(k.last, ev.TS)
		}
	}
	var out []Mail
	for _, ev := range evs {
		if !IsMail(ev) || ev.ID == "" {
			continue
		}
		// A send is mail only when stui worker send could not get it in and
		// said so (Owed): one that went in has its delivered event, and one
		// an older stui could not type was reported to its sender as not sent.
		// A send to another machine is that machine's to type.
		if ev.Kind == EvSend && (!ev.Owed || ev.Machine != "" || strings.Contains(ev.To, "/") || ev.To == "") {
			continue
		}
		k := m[ev.ID]
		if k != nil && k.taken {
			continue
		}
		ml := Mail{Event: ev}
		if k != nil {
			ml.Typed, ml.Pointed, ml.PointedAt = k.typed, k.points, k.last
		}
		out = append(out, ml)
	}
	return out
}

// MailOwner is who a piece of mail is for now: a send's worker, a done's or
// idle's nearest parent still at work (Recipient).
type MailOwner func(ev Event) string

// Inbox is the mail owed to pane, in every recent run, oldest first.
func Inbox(pane string, owner MailOwner) []Mail {
	var out []Mail
	for _, run := range RecentRuns(mailRuns) {
		for _, ml := range owed(ReadEvents(run)) {
			if owner(ml.Event) == pane {
				out = append(out, ml)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out
}

// Ack marks mail taken by pane. Ids it does not find are left out of what
// it returns.
func Ack(pane string, mail []Mail) []string {
	var done []string
	for _, ml := range mail {
		if _, err := AppendEvent(Event{Run: ml.Run, Kind: EvAcked, Ref: ml.ID, From: pane}); err == nil {
			done = append(done, ml.ID)
		}
	}
	return done
}

// PointerLine is the line typed for mail owed: how many, and the command
// that shows them.
func PointerLine(n int) string {
	if n == 1 {
		return "[stui] 1 message for you -- stui worker inbox"
	}
	return "[stui] " + strconv.Itoa(n) + " messages for you -- stui worker inbox"
}

// ---- waiting: a pane blocked in stui worker inbox --wait is woken by the
// mail itself, not typed at.

func waitFile(pane string) string {
	return filepath.Join(Dir(), "waiting", strings.NewReplacer("%", "p", "/", "_", ":", "_").Replace(pane))
}

// Waiting marks pane as waiting for its mail for as long as this process
// runs; the returned func unmarks it.
func Waiting(pane string) func() {
	f := waitFile(pane)
	os.MkdirAll(filepath.Dir(f), 0o700)
	os.WriteFile(f, []byte(strconv.Itoa(os.Getpid())), 0o600)
	return func() { os.Remove(f) }
}

// IsWaiting is whether a live process waits for pane's mail.
func IsWaiting(pane string) bool {
	b, err := os.ReadFile(waitFile(pane))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	return err == nil && proc.Alive(pid)
}

// WaitMail blocks until pane has mail owed or until is past, polling.
func WaitMail(pane string, owner MailOwner, until time.Time, every time.Duration) []Mail {
	for {
		if m := Inbox(pane, owner); len(m) > 0 {
			return m
		}
		if !time.Now().Before(until) {
			return nil
		}
		time.Sleep(every)
	}
}
