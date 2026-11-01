package orch

// The parents' mail: a worker that is
// done (stui worker done), or that ended its turn without saying so, is told
// to its parent as one typed line. The mail is the run's own "done" and
// "idle" events (events.go); the carrier here types each one once, into
// the nearest parent above that is still there and not done itself, when
// that parent is between turns and its input box is empty -- the chat's
// send refuses a box with someone's typing in it, and the carrier never
// forces it. A line it could not type waits for the parent's next turn end.
//
// Workers on another machine are not told from there: this machine asks
// after its own workers there (GET /api/orch/workers/<pane>), and writes
// their done and idle here, so they come up the same way.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sophonz-labs/stui/internal/chat"
)

const (
	// mailEvery is how often the carrier looks; mailRetry how long a line
	// that was turned down (someone typing) waits before it is tried again
	// with the parent still between turns.
	mailEvery = 2 * time.Second
	mailRetry = 10 * time.Second
	// mailTypings is how many times one line is typed with nothing seen to
	// take it. After that it is written down as typed but unconfirmed, and
	// is not typed again: the same line put in front of someone every
	// mailRetry is worse than a pointer to the inbox, which is where it
	// still is (inbox.go).
	mailTypings = 2
	// A turn end is told once it has settled: stui worker done runs inside
	// the turn, so by then a worker that reported is marked done.
	turnSettle = 3 * time.Second
	// How far back the carrier reads runs, and turn ends it tells about
	// when it starts (older ones were seen by whoever was looking then).
	mailRuns     = 7 * 24 * time.Hour
	turnLookback = 15 * time.Minute
	// How often workers on other machines are asked after.
	remoteEvery = 15 * time.Second
)

type mailPane struct {
	ID, Run, InRun, Role, Parent, Name, Done string
	Sent                                     int64
	// Adopted: taken in by stui worker adopt, which gives it no work; what
	// it is given comes by stui worker send (Sent).
	Adopted bool
}

// call is what a pane is known by in a run: its worker name, or
// "coordinator".
func (p mailPane) call() string {
	if p.Name == "" && p.Role == RoleCoordinatorName {
		return "coordinator"
	}
	return p.Name
}

const mailFormat = "#{pane_id}\t#{@stui-run}\t#{@stui-in-run}\t#{@stui-role}\t#{@stui-parent}\t#{@stui-name}\t#{@stui-done}\t#{@stui-sent}\t#{@stui-adopted}"

func parseMailPanes(out string) map[string]mailPane {
	ps := map[string]mailPane{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.SplitN(l, "\t", 9)
		if len(f) < 8 || !paneID.MatchString(f[0]) {
			continue
		}
		if _, seen := ps[f[0]]; seen {
			continue
		}
		sent, _ := strconv.ParseInt(f[7], 10, 64)
		ps[f[0]] = mailPane{ID: f[0], Run: f[1], InRun: f[2], Role: f[3], Parent: f[4], Name: f[5], Done: f[6], Sent: sent, Adopted: len(f) > 8 && f[8] == "1"}
	}
	return ps
}

// ChainUp is the parents above id, nearest first, from a parent lookup:
// it stops at the top, at a pane it does not know, and after a parent on
// another machine ("<machine>/%N"), whose parents are not known here.
func ChainUp(parent func(string) (string, bool), id string) []string {
	var up []string
	seen := map[string]bool{id: true}
	cur := id
	for len(up) < MaxChain+2 {
		p, ok := parent(cur)
		if !ok || p == "" || seen[p] {
			break
		}
		up = append(up, p)
		seen[p] = true
		if strings.Contains(p, "/") {
			break
		}
		cur = p
	}
	return up
}

// ChainUpFrom is ChainUp over list-panes output in PaneFormat.
func ChainUpFrom(listOut, id string) []string {
	ps := parsePanes(listOut)
	return ChainUp(func(x string) (string, bool) {
		p, ok := ps[x]
		return p.Parent, ok
	}, id)
}

type mailState struct {
	started  time.Time
	tried    map[string]time.Time
	turnSeen map[string]int64
	pulled   time.Time
	// Typed but not seen to start a turn (the chat send said "unknown"):
	// by event, where and when. It is told once a turn there begins after.
	pending map[string]pendingMail
}

type pendingMail struct {
	to, line string
	// at is when it was first typed: a turn since then took it, whichever
	// typing that was.
	at    time.Time
	typed int
}

// RunMail carries the parents' mail until ctx ends. One carrier a machine:
// a second stui (on another port) leaves it to the first.
func (a *Adopter) RunMail(ctx context.Context) {
	if a.o.Deliver == nil || a.o.State == nil || !mailLock() {
		return
	}
	a.mail = mailState{started: time.Now(), tried: map[string]time.Time{}, turnSeen: map[string]int64{}, pending: map[string]pendingMail{}}
	t := time.NewTicker(mailEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.MailTick(ctx)
		}
	}
}

// MailTick looks once: turn ends to tell, workers elsewhere, then the
// mail waiting in every recent run.
func (a *Adopter) MailTick(ctx context.Context) {
	if a.mail.tried == nil {
		a.mail = mailState{started: time.Now(), tried: map[string]time.Time{}, turnSeen: map[string]int64{}, pending: map[string]pendingMail{}}
	}
	out, err := a.o.Tmux(ctx, "list-panes", "-a", "-F", mailFormat)
	if err != nil {
		return
	}
	ps := parseMailPanes(out)
	a.noticeTurnEnds(ctx, ps)
	if a.o.Remote != nil && time.Since(a.mail.pulled) >= remoteEvery {
		a.mail.pulled = time.Now()
		a.askElsewhere(ctx, ps)
	}
	runs := RecentRuns(mailRuns)
	a.events.Keep(runs)
	for _, run := range runs {
		for _, ev := range Undelivered(a.events.Read(run)) {
			a.carry(ctx, ps, ev)
		}
	}
	a.point(ctx, ps)
}

// owner is who a piece of mail is for now, here: a send's worker, a done's
// or idle's nearest parent still at work; "" for none here.
func (a *Adopter) owner(ps map[string]mailPane) MailOwner { return mailOwner(ps, a.o.State) }

func mailOwner(ps map[string]mailPane, state func(string) (string, int64)) MailOwner {
	parent := func(x string) (string, bool) {
		p, ok := ps[x]
		return p.Parent, ok
	}
	return func(ev Event) string {
		inRun := func(x string) bool {
			p, here := ps[x]
			s, _ := state(x)
			return here && s != "" && (ev.Run == "" || p.Run == ev.Run || p.InRun == ev.Run)
		}
		if toPane(ev) {
			if inRun(ev.To) {
				return ev.To
			}
			return ""
		}
		return Recipient(ev, parent, inRun, func(x string) bool { return ps[x].Done == "1" })
	}
}

// MailFormat is the list-panes format OwnerFrom reads.
const MailFormat = mailFormat

// OwnerFrom is who mail is for, from list-panes output in MailFormat and
// the agents' states: what stui worker inbox asks outside the carrier.
func OwnerFrom(listOut string, state func(string) (string, int64)) MailOwner {
	return mailOwner(parseMailPanes(listOut), state)
}

// point types, between a pane's turns, one line saying how much mail it is
// owed (inbox.go): for mail whose own line could not be typed or was not
// seen to go in. Again only after a turn there ended without taking it,
// and at most mailPoints times for one piece.
func (a *Adopter) point(ctx context.Context, ps map[string]mailPane) {
	owner := a.owner(ps)
	byPane := map[string][]Mail{}
	for _, run := range RecentRuns(mailRuns) {
		for _, ml := range owed(a.events.Read(run)) {
			to := owner(ml.Event)
			if to == "" {
				continue
			}
			// A send typed whose turn was not seen: a prompt there since is
			// it going in.
			if ml.Kind == EvSend && ml.OK != nil && *ml.OK && a.submittedSince(to, time.UnixMilli(ml.TS)) {
				AppendEvent(Event{Run: ml.Run, Kind: EvDelivered, Ref: ml.ID, From: ml.From, To: to, ToName: ps[to].call(), Text: ml.Text})
				continue
			}
			// A done or idle whose line is still to be typed is carry's.
			if ml.Kind != EvSend && !ml.Typed {
				continue
			}
			byPane[to] = append(byPane[to], ml)
		}
	}
	for to, mail := range byPane {
		st, since := a.o.State(to)
		if st == "working" || st == "waiting" || IsWaiting(to) {
			continue
		}
		var due []Mail
		var last int64
		for _, ml := range mail {
			last = max(last, ml.PointedAt)
		}
		for _, ml := range mail {
			if ml.Pointed >= mailPoints {
				continue
			}
			// New since the carrier started (older mail waits in the inbox
			// and is pointed to with the next new piece), or pointed before
			// and a prompt went in there since without taking it. Only a
			// prompt: the state's time moves with no turn (Claude's
			// idle_prompt), and an agent with no submit hook is pointed once.
			fresh := ml.Pointed == 0 && time.UnixMilli(ml.TS).After(a.mail.started.Add(-turnLookback))
			if fresh || (ml.Pointed > 0 && a.submittedSince(to, time.UnixMilli(last))) {
				due = append(due, ml)
			}
		}
		if len(due) == 0 {
			continue
		}
		key := "point:" + to
		if t, ok := a.mail.tried[key]; ok && time.Since(t) < mailRetry && since <= t.UnixMilli() {
			continue
		}
		a.mail.tried[key] = time.Now()
		line := PointerLine(len(mail))
		dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		code, res := a.o.Deliver(dctx, to, line)
		// The box holds a send's own line, typed and never entered: that
		// line goes in, which is the send itself.
		if code == 409 && res["reason"] == "busy-input" && a.o.DeliverOver != nil {
			typed, _ := res["typed"].(string)
			for _, ml := range mail {
				if ml.Kind == EvSend && ml.Text != "" && chat.SameText(typed, ml.Text) {
					if c, r := a.o.DeliverOver(dctx, to, ml.Text, typed); c == 200 && r["delivered"] == true {
						AppendEvent(Event{Run: ml.Run, Kind: EvDelivered, Ref: ml.ID, From: ml.From, To: to, ToName: ps[to].call(), Text: ml.Text})
					}
					code = 0
					break
				}
			}
		}
		cancel()
		if code != 200 {
			// Someone is typing there, or a question is up: it waits.
			continue
		}
		delete(a.mail.tried, key)
		for _, ml := range due {
			AppendEvent(Event{Run: ml.Run, Kind: EvPointed, Ref: ml.ID, To: to, ToName: ps[to].call(), Text: line})
		}
	}
}

func (a *Adopter) parentOf(ps map[string]mailPane) func(string) (string, bool) {
	return func(x string) (string, bool) {
		p, ok := ps[x]
		return p.Parent, ok
	}
}

// noticeTurnEnds writes an "idle" event for each worker whose turn ended
// without stui worker done, once a turn end, with why it ended.
func (a *Adopter) noticeTurnEnds(ctx context.Context, ps map[string]mailPane) {
	for id, p := range ps {
		if p.Role != RoleWorkerName || p.Done == "1" || strings.Contains(p.Parent, "/") {
			// Done told itself; one working for another machine is asked
			// after from there.
			continue
		}
		st, since := a.o.State(id)
		source := ""
		if a.o.Source != nil {
			source = a.o.Source(id)
		}
		if !turnEnded(st, source) || since <= a.mail.turnSeen[id] {
			continue
		}
		at := time.UnixMilli(since)
		if time.Since(at) < turnSettle {
			continue
		}
		a.mail.turnSeen[id] = since
		// An adopted agent given nothing yet: its turn ends are its own
		// (the conversation it had, the brief stui asked of it), not work
		// it stopped short of.
		if (p.Sent > 0 && since < p.Sent) || (p.Adopted && p.Sent == 0) || at.Before(a.mail.started.Add(-turnLookback)) || waitsForItsOwn(ps, id) {
			continue
		}
		run := p.InRun
		if run == "" {
			run = p.Run
		}
		if told(a.events.Read(run), id, since) {
			continue
		}
		up := ChainUp(a.parentOf(ps), id)
		stop := a.stopOf(ctx, id, st)
		ev := Event{Run: run, Kind: EvIdle, From: id, FromName: p.Name, Up: up, Since: since, StopReason: stop.Reason, Why: stop.Why, ResetsAt: stop.ResetsAt}
		if len(up) > 0 {
			ev.To, ev.ToName = up[0], ps[up[0]].call()
		}
		AppendEvent(ev)
	}
}

// waitsForItsOwn: id has workers of its own that are not done. Ending its
// turn then is waiting for them, not stopping short; their done wakes it.
func waitsForItsOwn(ps map[string]mailPane, id string) bool {
	for _, p := range ps {
		if p.Parent == id && p.Role == RoleWorkerName && p.Done != "1" {
			return true
		}
	}
	return false
}

// told is whether evs already have an idle event for that turn end.
func told(evs []Event, from string, since int64) bool {
	for _, ev := range evs {
		if ev.Kind == EvIdle && ev.From == from && ev.Since == since {
			return true
		}
	}
	return false
}

// askElsewhere asks after this machine's workers on other machines, and
// writes their done and their turn ends here.
func (a *Adopter) askElsewhere(ctx context.Context, ps map[string]mailPane) {
	var anchors map[string]PaneAnchor
	for _, run := range RecentRuns(2 * 24 * time.Hour) {
		r, err := Load(run)
		if err != nil {
			continue
		}
		evs := a.events.Read(run)
		for _, w := range r.Workers {
			if w.Machine == "" || reportedSinceLastWork(evs, w.ID) {
				continue
			}
			_, pane, ok := strings.Cut(w.ID, "/")
			if !ok {
				continue
			}
			cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			code, body, err := a.o.Remote(cctx, w.Machine, "GET", "/api/orch/workers/"+url.PathEscape(pane), nil)
			cancel()
			var rw RemoteWorker
			if err != nil || (code != 200 && code != 404) {
				continue
			}
			if code != 200 || json.Unmarshal(body, &rw) != nil || rw.Exited || (w.Key != "" && rw.Key != "" && rw.Key != w.Key) {
				// Gone from that pane, or that pane is another agent's now
				// (its machine restarted): where its key is now, if
				// anywhere. Asked again from there on the next pass.
				cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
				to := a.follow(cctx, w.Machine, w.Key)
				cancel()
				if to != "" && to != w.ID {
					a.moveRemote(run, w.ID, to)
				}
				continue
			}
			// Its machine says it works in another run now (taken into a new
			// one; this record is an old run's, put back from the archive):
			// not this run's to report.
			if rw.Run != "" && rw.Run != run {
				continue
			}
			// Its parent here by its key: none is not a pane that took
			// its old number (anchor.go). Asked again on the next pass.
			if anchors == nil {
				anchors = a.anchors(ctx)
			}
			parent, found := r.ParentHere(&w, anchors)
			if !found {
				continue
			}
			up := append([]string{parent}, ChainUp(a.parentOf(ps), parent)...)
			ev := Event{Run: run, From: w.ID, FromName: firstNonEmpty(rw.Name, w.Name), To: parent, ToName: ps[parent].call(), Machine: w.Machine, Up: up}
			switch {
			case rw.Done:
				ev.Kind, ev.Title, ev.Outcome = EvDone, FirstLine(rw.Summary), rw.Outcome
				AppendEvent(ev)
				evs = append(evs, ev)
			case (rw.State == "done" || rw.State == "error" || (rw.State == "idle" && rw.StopReason == StopCancelled)) && rw.Since > rw.Sent && !told(evs, w.ID, rw.Since) &&
				time.UnixMilli(rw.Since).After(a.mail.started.Add(-turnLookback)) && time.Since(time.UnixMilli(rw.Since)) >= turnSettle:
				// Why it stopped, as its machine read it; an older stui there
				// says nothing, read as end_turn.
				ev.Kind, ev.Since = EvIdle, rw.Since
				ev.StopReason, ev.Why, ev.ResetsAt = rw.StopReason, rw.StopWhy, rw.ResetsAt
				AppendEvent(ev)
				evs = append(evs, ev)
			}
		}
	}
}

// reportedSinceLastWork: a done event for id after the last time it was
// given work (start, send), so there is nothing more to ask it.
func reportedSinceLastWork(evs []Event, id string) bool {
	var work, done int64
	for _, ev := range evs {
		switch {
		case (ev.Kind == EvStart || ev.Kind == EvSend || ev.Kind == EvAdopt) && ev.To == id:
			work = max(work, ev.TS)
		case ev.Kind == EvDone && ev.From == id:
			done = max(done, ev.TS)
		}
	}
	return done > 0 && done >= work
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Recipient is who mail goes to now: the first of its parents (To, then
// Up) that is here, runs an agent and is not done itself. "" when there is
// none here: they are all gone, or the next one is on another machine.
func Recipient(ev Event, parent func(string) (string, bool), alive func(string) bool, done func(string) bool) string {
	chain := ev.Up
	if len(chain) == 0 && ev.To != "" {
		chain = []string{ev.To}
	}
	seen := map[string]bool{}
	for i := 0; i < len(chain); i++ {
		cur := chain[i]
		// From a pane that is here, its own parent now (it may have
		// moved), before the rest of what was written down.
		for cur != "" && !seen[cur] {
			seen[cur] = true
			if strings.Contains(cur, "/") {
				return ""
			}
			if alive(cur) && !done(cur) {
				return cur
			}
			p, here := parent(cur)
			if !here {
				break
			}
			cur = p
		}
	}
	return ""
}

func (a *Adopter) carry(ctx context.Context, ps map[string]mailPane, ev Event) {
	// A pane in another run is not one of ev's, whatever its number: one
	// written down before a restart may be another agent's now.
	to := a.owner(ps)(ev)
	if to == "" {
		return
	}
	// A pane waiting in stui worker inbox --wait gets it from there.
	if IsWaiting(to) {
		return
	}
	st, since := a.o.State(to)
	told := func(to, line, why string) {
		delete(a.mail.tried, ev.ID)
		delete(a.mail.pending, ev.ID)
		AppendEvent(Event{Run: ev.Run, Kind: EvDelivered, Ref: ev.ID, From: ev.From, FromName: ev.FromName, To: to, ToName: ps[to].call(), Text: line, Why: why})
	}
	// Typed before without a turn seen to start: a turn there since says
	// it was taken (queued behind a turn, or the hook came late).
	if p, ok := a.mail.pending[ev.ID]; ok && (since > p.at.UnixMilli() && st != "" && st != "idle" || a.submittedSince(p.to, p.at)) {
		told(p.to, p.line, "")
		return
	}
	// Between turns only: a parent at work is told when its turn ends.
	if st == "working" || st == "waiting" {
		return
	}
	if t, ok := a.mail.tried[ev.ID]; ok && time.Since(t) < mailRetry && since <= t.UnixMilli() {
		return
	}
	a.mail.tried[ev.ID] = time.Now()
	line := MailLine(ev, to, ps[to].call())
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	at := time.Now()
	code, res := a.o.Deliver(dctx, to, line)
	// The line is in the box from before, never sent (an agent that had
	// just started took the keys but not the Enter): stui types over its own
	// line, and nothing else.
	if code == 409 && res["reason"] == "busy-input" && a.o.DeliverOver != nil {
		if typed, _ := res["typed"].(string); chat.SameText(typed, line) {
			at = time.Now()
			code, res = a.o.DeliverOver(dctx, to, line, typed)
		}
	}
	if code != 200 {
		// Someone is typing there, or a question is up: it waits.
		return
	}
	// Told only once the agent took it: the chat send saw the prompt
	// submitted. "unknown" may be a line left in the box: it waits for a
	// turn to begin there, and is typed again if none does.
	if res["delivered"] == true {
		told(to, line, "")
		return
	}
	p, again := a.mail.pending[ev.ID]
	if !again || p.to != to {
		p = pendingMail{to: to, line: line, at: at}
	}
	p.line = line
	p.typed++
	if p.typed >= mailTypings {
		told(to, line, Unconfirmed)
		return
	}
	a.mail.pending[ev.ID] = p
}

// submittedSince: a prompt went in at pane after at, by its submit hook.
// That is the line taken whatever the state says now: a turn that began
// and ended between two looks leaves the pane done or idle, and an idle
// since then is not proof (Claude's idle_prompt moves it with no turn).
func (a *Adopter) submittedSince(pane string, at time.Time) bool {
	if a.o.Submitted == nil {
		return false
	}
	return a.o.Submitted(pane).After(at)
}

// MailLine is the one line typed into the parent: who, what, and how to
// read the rest. Not the summary in full: someone may be watching. A turn
// end says why it ended, and when reading the worker gives nothing (its
// usage limit), says so instead of offering the read.
func MailLine(ev Event, to, toName string) string {
	return mailLine(ev, to, time.Now())
}

func mailLine(ev Event, to string, now time.Time) string {
	switch ev.Kind {
	case EvAsk:
		return askLine(ev)
	case EvReply:
		return replyLine(ev)
	}
	who := "worker " + firstNonEmpty(ev.FromName, "?") + " (" + ev.From
	if to != ev.To && ev.To != "" {
		// Its own parent is done or gone: whose it was.
		who += ", under " + firstNonEmpty(ev.ToName, ev.To)
	}
	who += ")"
	switch ev.Kind {
	case EvDone:
		did := "is done"
		if ev.Outcome == OutcomeFailed {
			did = "is done and failed"
		}
		if ev.Title != "" {
			return fmt.Sprintf("[stui] %s %s: %s -- stui worker read %s", who, did, clip(ev.Title, 120), ev.From)
		}
		return fmt.Sprintf("[stui] %s %s -- stui worker read %s", who, did, ev.From)
	}
	switch ev.StopReason {
	case StopLimit:
		line := fmt.Sprintf("[stui] %s stopped at its usage limit", who)
		if ev.Why != "" {
			line += ": " + strconv.Quote(clip(ev.Why, 120))
		}
		if r := ResetsLine(ev.ResetsAt, now); r != "" {
			return line + " -- nothing to read; it takes work again after " + r
		}
		return line + " -- nothing to read; it takes work again once the limit resets"
	case StopError:
		if ev.Why != "" {
			return fmt.Sprintf("[stui] %s stopped on an error: %s -- stui worker read %s", who, strconv.Quote(clip(ev.Why, 120)), ev.From)
		}
		return fmt.Sprintf("[stui] %s stopped on an error -- stui worker read %s", who, ev.From)
	case StopCancelled:
		return fmt.Sprintf("[stui] %s was interrupted at its pane, without stui worker done -- stui worker read %s", who, ev.From)
	}
	return fmt.Sprintf("[stui] %s ended its turn without stui worker done -- stui worker read %s", who, ev.From)
}
