package orch

// A run's events: who handed what to whom, and when, one
// JSON line each in <run>/events.jsonl. It is only ever appended to, in
// one write per line, so the coordinators and workers of a run (separate
// processes) can all write to it. It is also the parents' mailbox: a
// "done" or "idle" event is mail for the worker's parent, and "delivered"
// says which one was typed to whom (mail.go).

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The kinds of event.
const (
	EvStart     = "start"
	EvSend      = "send"
	EvAdopt     = "adopt"
	EvRelease   = "release"
	EvStop      = "stop"
	EvDone      = "done"
	EvIdle      = "idle"
	EvDelivered = "delivered"
	// A worker's done for work it was given before the last (stui worker
	// done in a turn that took its prompt before the newer work was sent):
	// written down, not mail, and not the worker done.
	EvLateDone = "late-done"
	// The tree changing (tree.go): a worker's name, and its role.
	EvRename = "rename"
	EvAbout  = "about"
)

// What a worker's done says of its work (stui worker done --outcome).
const (
	OutcomeSucceeded = "succeeded"
	OutcomeFailed    = "failed"
)

// Event is one line of events.jsonl. Fields that do not apply are left
// out; readers ignore fields they do not know.
type Event struct {
	V    int    `json:"v"`
	ID   string `json:"id"`
	TS   int64  `json:"ts"`
	Run  string `json:"run"`
	Kind string `json:"kind"`
	// Who did it and to whom: for done and idle, the worker and its
	// parent; for the rest, who asked and the worker.
	From     string `json:"from,omitempty"`
	FromName string `json:"fromName,omitempty"`
	To       string `json:"to,omitempty"`
	ToName   string `json:"toName,omitempty"`
	// The slug of the machine the event's remote side is on, when one side
	// is on another machine. Which side depends on the kind: To for a send
	// to a worker there, From for a done or idle from a worker there (its
	// To is a pane here). Other kinds leave it out. A reader that wants a
	// given side's machine reads the "<slug>/" of that side's id.
	Machine string `json:"machine,omitempty"`
	// done and idle: the parents above From when it happened, nearest
	// first: where the mail goes when a parent is done or gone.
	Up []string `json:"up,omitempty"`
	// What was handed over: its document, its unchanging copy, its title.
	Doc    string `json:"doc,omitempty"`
	File   string `json:"file,omitempty"`
	Title  string `json:"title,omitempty"`
	Bytes  int    `json:"bytes,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	// send: whether it went in, and why not. idle: the agent's line for
	// why it stopped (its limit, its error).
	OK  *bool  `json:"ok,omitempty"`
	Why string `json:"why,omitempty"`
	// idle: the turn end it is about (ms), so it is told once; why the
	// turn ended (stopreason.go: end_turn, _limit, error, cancelled; empty
	// in what an older stui wrote, and read as end_turn, as is a value a
	// reader does not know), and for a limit, when it resets (ms). Why
	// holds the agent's own line saying so.
	Since      int64  `json:"since,omitempty"`
	StopReason string `json:"stopReason,omitempty"`
	ResetsAt   int64  `json:"resetsAt,omitempty"`
	// send: stui worker send could not get it in now (the box had someone's
	// typing, a question was up, or no turn was seen to take it): it is
	// owed to the worker's inbox (inbox.go).
	Owed bool `json:"owed,omitempty"`

	// send, done, late-done: which work it is, counted per worker from 1
	// (@stui-gen): each send is the next, typed now or owed to the inbox. done and late-done:
	// succeeded or failed (stui worker done --outcome; empty in what an older
	// stui wrote, and read as succeeded).
	Gen     int    `json:"gen,omitempty"`
	Outcome string `json:"outcome,omitempty"`

	// delivered: the event it carried, and the line typed.
	Ref  string `json:"ref,omitempty"`
	Text string `json:"text,omitempty"`

	// adopt, release, rename, about: the tree changing (tree.go). From is
	// who asked, a pane; By says who when no pane did ("app": an app's
	// request). To is the worker and ToKey its lasting key. Parent is the
	// parent it has now, WasParent the one before, each with its key: a
	// pane id means nothing after a restart. Was and Now are its name (or
	// role, for about) before and after; About the role given with adopt.
	By           string `json:"by,omitempty"`
	ToKey        string `json:"toKey,omitempty"`
	Parent       string `json:"parent,omitempty"`
	ParentName   string `json:"parentName,omitempty"`
	ParentKey    string `json:"parentKey,omitempty"`
	WasParent    string `json:"wasParent,omitempty"`
	WasParentKey string `json:"wasParentKey,omitempty"`
	Was          string `json:"was,omitempty"`
	Now          string `json:"now,omitempty"`
	About        string `json:"about,omitempty"`
}

// EventsFile is a run's events.
func EventsFile(run string) string { return filepath.Join(RunDir(run), "events.jsonl") }

func newEventID(now time.Time) string {
	b := make([]byte, 3)
	rand.Read(b)
	return "e-" + strconv.FormatInt(now.UnixMilli(), 36) + hex.EncodeToString(b)
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// AppendEvent adds ev to its run's events, filling in its version, id and
// time. One line stays well under 4 KB, so appends from several processes
// do not mix.
func AppendEvent(ev Event) (Event, error) {
	if !runID.MatchString(ev.Run) {
		return ev, os.ErrInvalid
	}
	now := time.Now()
	ev.V = 1
	if ev.ID == "" {
		ev.ID = newEventID(now)
	}
	if ev.TS == 0 {
		ev.TS = now.UnixMilli()
	}
	ev.Title, ev.Why, ev.Text = clip(ev.Title, 120), clip(ev.Why, 200), clip(ev.Text, 400)
	if len(ev.Up) > MaxChain+2 {
		ev.Up = ev.Up[:MaxChain+2]
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return ev, err
	}
	if err := os.MkdirAll(RunDir(ev.Run), 0o700); err != nil {
		return ev, err
	}
	f, err := os.OpenFile(EventsFile(ev.Run), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return ev, err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return ev, err
}

// ReadEvents is a run's events, oldest first; a line that does not read
// (cut short by a crash) is skipped.
func ReadEvents(run string) []Event {
	if !runID.MatchString(run) {
		return nil
	}
	f, err := os.Open(EventsFile(run))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	for sc.Scan() {
		var ev Event
		if json.Unmarshal(sc.Bytes(), &ev) == nil && ev.Kind != "" {
			out = append(out, ev)
		}
	}
	return out
}

// Undelivered is the mail (done and idle) among evs that no "delivered"
// event carried yet, oldest first.
func Undelivered(evs []Event) []Event {
	carried := map[string]bool{}
	for _, ev := range evs {
		// Typed (taken or not), or taken from the inbox: the line is not
		// typed again (inbox.go points to what is still owed).
		if ev.Kind == EvDelivered || ev.Kind == EvAcked {
			carried[ev.Ref] = true
		}
	}
	var out []Event
	for _, ev := range evs {
		if (ev.Kind == EvDone || ev.Kind == EvIdle || ev.Kind == EvAsk || ev.Kind == EvReply) && !carried[ev.ID] {
			out = append(out, ev)
		}
	}
	return out
}

// RecentRuns is the runs with a record or events written to in the last
// `within`, for the mail carrier, which need not read every run there was.
func RecentRuns(within time.Duration) []string {
	ents, err := os.ReadDir(Dir())
	if err != nil {
		return nil
	}
	var out []string
	cut := time.Now().Add(-within)
	for _, e := range ents {
		if !e.IsDir() || !runID.MatchString(e.Name()) {
			continue
		}
		for _, f := range []string{"events.jsonl", "run.json"} {
			if fi, err := os.Stat(filepath.Join(Dir(), e.Name(), f)); err == nil && fi.ModTime().After(cut) {
				out = append(out, e.Name())
				break
			}
		}
	}
	return out
}
