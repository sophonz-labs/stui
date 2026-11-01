package orch

// Keeping a run's records on its agents when their pane ids change.
//
// Who directs whom lives in tmux pane options, and a run's record names its
// coordinator and workers by pane id (%N). Both are good only as long as
// the tmux server lives: when it dies, the options go with it, and the
// panes stui restore makes in their place get new ids -- while the old ids are
// handed out again to other panes. A coordinator and its workers would
// come back as plain panes that way, their records pointing at nothing.
//
// So every pane in a run gets a lasting key (@stui-key), which the snapshot
// keeps and a restore sets on the pane that takes its place
// (internal/restore), and the records keep that key and the agent's
// conversation id beside the pane id. Rebind, run every few seconds by the
// agent, finds each record's pane by its key -- or, when the agent was
// resumed by hand in some other pane, by its conversation -- writes the new
// pane id into the record and puts the run's options back on the pane.
//
// It never takes a pane from another run, and never undoes a release: a
// pane in the same server that lost its options lost them on purpose.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// OptKey is a pane's lasting key.
const OptKey = "@stui-key"

// NewKey is a new lasting key.
func NewKey() string {
	b := make([]byte, 6)
	rand.Read(b)
	return "k-" + hex.EncodeToString(b)
}

type livePane struct {
	ID, Key, Run, Role, Parent, InRun string
	// "<session>:<window name>".
	Place string
}

const rebindFormat = "#{pane_id}\t#{" + OptKey + "}\t#{@stui-run}\t#{@stui-role}\t#{@stui-parent}\t#{" + OptInRun + "}\t#{session_name}\t#{window_name}"

func parseLive(out string) map[string]*livePane {
	ps := map[string]*livePane{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Split(l, "\t")
		if len(f) < 5 || !paneID.MatchString(f[0]) {
			continue
		}
		in, place := "", ""
		if len(f) > 5 {
			in = f[5]
		}
		if len(f) > 7 && f[6] != "" {
			place = f[6] + ":" + f[7]
		}
		if _, seen := ps[f[0]]; !seen {
			ps[f[0]] = &livePane{ID: f[0], Key: f[1], Run: f[2], Role: f[3], Parent: f[4], InRun: in, Place: place}
		}
	}
	return ps
}

// RebindResult says what Rebind changed, for the log.
type RebindResult struct {
	Keyed int      // panes given a key
	Moved []string // "r-…: %3 -> %17", a record that follows its agent to another pane
	// Workers on other machines whose parent here is in another pane now:
	// their machine is to be told (Adopter.Reparent).
	Reparent []Reparent
	// Runs retired: their coordinator cannot come back (Archive).
	Retired []string
}

// Reparent is one worker on another machine to tell of its parent's pane.
type Reparent struct {
	Run, Child, Parent string
}

type rebinder struct {
	ctx    context.Context
	tmux   func(ctx context.Context, args ...string) (string, error)
	live   map[string]*livePane
	byKey  map[string]*livePane
	byConv map[string]*livePane
	// Panes by place, and how many share each.
	byPlace map[string]*livePane
	places  map[string]int
	convs   map[string]string
	server  string
	// The coordinator's pane as the record had it before this pass.
	oldCoord string
	// The coordinator's pane now, "" when it was not found.
	coordPane string
}

func (b *rebinder) set(id string, kv ...string) {
	for i := 0; i+1 < len(kv); i += 2 {
		b.tmux(b.ctx, "set-option", "-p", "-t", id, kv[i], kv[i+1])
	}
}

// member is what Rebind keeps of a coordinator or worker record.
type member struct {
	ID, Key, Conv, ParentKey, Place, Seen string
	Live                                  bool
}

// find is the live pane a member is in now, or nil.
func (b *rebinder) find(m member, sameServer bool, runID string, claimed map[string]bool) *livePane {
	var p *livePane
	if m.Key != "" {
		p = b.byKey[m.Key]
	}
	if p == nil && sameServer {
		// Records from before keys: the same pane, in the same server.
		if q := b.live[m.ID]; q != nil && (q.Run == runID || q.InRun == runID) && (q.Key == "" || m.Key == "" || q.Key == m.Key) {
			p = q
		}
	}
	if p == nil && m.Conv != "" && m.Live {
		// Its agent resumed by hand in another pane.
		if q := b.byConv[m.Conv]; q != nil && (q.Run == "" || q.Run == runID || q.InRun == runID) && (q.Key == "" || q.Key == m.Key) {
			p = q
		}
	}
	if p == nil && m.Place != "" && m.Live && b.places[m.Place] == 1 {
		// Neither key nor conversation, in the window it was in (a snapshot
		// from before its key, an agent started again there by hand): the
		// only pane there, and no one's.
		if q := b.byPlace[m.Place]; q.Key == "" && q.Run == "" && q.InRun == "" {
			p = q
		}
	}
	if p == nil || claimed[p.ID] {
		return nil
	}
	return p
}

// Rebind keys every pane of a run that has no key yet, and brings the
// records of every run (under Dir) up to date with where their agents are.
// convs is each pane's agent conversation id, as the hooks report it;
// server says which tmux server this is (pid and start), so a pane id seen
// again in another server is not taken for the same pane.
func Rebind(ctx context.Context, tmux func(ctx context.Context, args ...string) (string, error), convs map[string]string, server string) RebindResult {
	return RebindWith(ctx, tmux, convs, server, RebindOptions{})
}

// RebindOptions is what Rebind needs to know to retire a run.
type RebindOptions struct {
	// Whether a pane with this lasting key is in a snapshot a restore can
	// still bring back. nil: a keyed run is never retired.
	Kept func(key string) bool
	Now  func() time.Time
	// When each run was first seen without its coordinator, kept by the
	// caller from pass to pass: a run is retired only once it has been
	// missing on every pass for retireGrace. nil: no run is retired -- one
	// pass alone cannot tell "not here now" from "gone for good".
	Missing map[string]time.Time
}

// retireGrace is how long a run's coordinator must be missing, pass after
// pass in one stui, before the run is retired. A restart of stui starts it
// again, so stui restarting (stui upgrade, a reboot) never retires a run
// whose coordinator is still in tmux.
const retireGrace = 5 * time.Minute

// retireAfter is how old a run must be before it is retired for a missing
// coordinator: a run just made may not have its pane's options yet.
const retireAfter = 10 * time.Minute

// RebindWith is Rebind, retiring runs whose coordinator is gone for good
// (Archive): its pane is not in this server, and no snapshot holds it --
// its id is one a new tmux server hands out again from %0 to other panes.
func RebindWith(ctx context.Context, tmux func(ctx context.Context, args ...string) (string, error), convs map[string]string, server string, o RebindOptions) RebindResult {
	var res RebindResult
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	out, err := tmux(ctx, "list-panes", "-a", "-F", rebindFormat)
	if err != nil {
		return res
	}
	b := newRebinder(ctx, tmux, out, convs, server)
	res.Keyed = b.keyRuns()
	entries, _ := os.ReadDir(Dir())
	for _, e := range entries {
		if !e.IsDir() || !runID.MatchString(e.Name()) {
			continue
		}
		r, err := Load(e.Name())
		if err != nil {
			continue
		}
		moved, changed := b.run(r)
		if b.coordPane == "" && len(b.live) > 0 && b.gone(r, o.Kept) && now().Sub(time.UnixMilli(r.Created)) > retireAfter {
			// Missing now; retired only once it has stayed missing.
			if o.Missing != nil {
				first, seen := o.Missing[r.ID]
				if !seen {
					o.Missing[r.ID] = now()
				} else if now().Sub(first) >= retireGrace {
					delete(o.Missing, r.ID)
					if err := Archive(r.ID, "its coordinator ("+r.Coordinator+") is in no tmux server and no snapshot"); err == nil {
						res.Retired = append(res.Retired, r.ID)
					}
					continue
				}
			}
		} else if o.Missing != nil {
			delete(o.Missing, r.ID)
		}
		if changed {
			res.Moved = append(res.Moved, moved...)
			saveRebound(r)
		}
		res.Reparent = append(res.Reparent, b.remote(r, moved)...)
	}
	return res
}

func newRebinder(ctx context.Context, tmux func(ctx context.Context, args ...string) (string, error), listOut string, convs map[string]string, server string) *rebinder {
	b := &rebinder{ctx: ctx, tmux: tmux, live: parseLive(listOut), convs: convs, server: server,
		byKey: map[string]*livePane{}, byConv: map[string]*livePane{}, byPlace: map[string]*livePane{}, places: map[string]int{}}
	for _, p := range b.live {
		if p.Place != "" {
			b.byPlace[p.Place] = p
			b.places[p.Place]++
		}
		if p.Key != "" {
			b.byKey[p.Key] = p
		}
		if c := convs[p.ID]; c != "" {
			b.byConv[c] = p
		}
	}
	return b
}

// keyRuns gives every pane of a run that has no key one; how many.
func (b *rebinder) keyRuns() int {
	n := 0
	for _, p := range b.live {
		if p.Run != "" && p.Key == "" {
			p.Key = NewKey()
			b.set(p.ID, OptKey, p.Key)
			b.byKey[p.Key] = p
			n++
		}
	}
	return n
}

// RebindRun is Rebind for run id alone, and never retires it: what stui
// project open does once it has made panes with the run's keys, so the
// panes are the run's agents when it returns, not once the keeper looks.
func RebindRun(ctx context.Context, tmux func(ctx context.Context, args ...string) (string, error), server, id string) error {
	r, err := Load(id)
	if err != nil {
		return err
	}
	out, err := tmux(ctx, "list-panes", "-a", "-F", rebindFormat)
	if err != nil {
		return err
	}
	b := newRebinder(ctx, tmux, out, nil, server)
	if _, changed := b.run(r); changed {
		saveRebound(r)
	}
	return nil
}

// gone is whether r's coordinator, not found in this server, cannot come
// back. One with no lasting key is found only as the same pane in the same
// server, which this pass already looked for; one with a key is gone when
// no snapshot holds a pane with it either (a restore sets the key on the
// pane that takes its place).
func (b *rebinder) gone(r *Run, kept func(string) bool) bool {
	if strings.Contains(r.Coordinator, "/") {
		// On another machine (a worker here of a coordinator there): not
		// in this server by nature, and that machine's to say.
		return false
	}
	if r.CoordinatorKey == "" {
		// The same pane in the server it was last seen in, whatever it is
		// in now: not gone. A record that names no server cannot say.
		if q := b.live[r.Coordinator]; q != nil && r.Server != "" && r.Server == b.server {
			return false
		}
		return true
	}
	// Its key is on a pane here: there, whatever run that pane is in now.
	if b.byKey[r.CoordinatorKey] != nil {
		return false
	}
	return kept != nil && !kept(r.CoordinatorKey)
}

// run brings one record up to date; changed when it must be written.
func (b *rebinder) run(r *Run) (moved []string, changed bool) {
	sameServer := r.Server == "" || r.Server == b.server
	claimed := map[string]bool{}
	seen := false

	// confirm takes what the pane says into m, and says whether m moved.
	confirm := func(m *member, p *livePane) {
		seen = true
		claimed[p.ID] = true
		if m.ID != p.ID {
			moved = append(moved, r.ID+": "+m.ID+" -> "+p.ID)
			m.ID = p.ID
			changed = true
		}
		if m.Key != p.Key && p.Key != "" {
			m.Key, changed = p.Key, true
		}
		if c := b.convs[p.ID]; c != "" && c != m.Conv {
			m.Conv, changed = c, true
		}
		if !m.Live {
			m.Live, changed = true, true
		}
		if p.Place != "" && p.Place != m.Place {
			m.Place, changed = p.Place, true
		}
		if m.Seen != b.server {
			m.Seen, changed = b.server, true
		}
	}

	b.oldCoord = r.Coordinator
	coord := member{ID: r.Coordinator, Key: r.CoordinatorKey, Conv: r.CoordinatorConv, Live: r.CoordinatorLive, Place: r.CoordinatorPlace}
	coordPane := ""
	if p := b.find(coord, sameServer, r.ID, claimed); p != nil {
		switch {
		case p.Run == r.ID:
			confirm(&coord, p)
			coordPane = p.ID
		case p.Run != "":
			// Another run's now (a team taken in as a worker): not ours,
			// and not to be taken back after a restore either.
			if coord.Live {
				coord.Live, changed = false, true
			}
		case sameServer && p.ID == coord.ID:
			// The same pane, its options taken away on purpose (settle).
			if coord.Live {
				coord.Live, changed = false, true
			}
		case coord.Live:
			b.set(p.ID, "@stui-run", r.ID)
			p.Run = r.ID
			// A team's lead is a worker of the run above as well: that
			// run's record makes it one, and its role says so.
			if p.Role == "" {
				b.set(p.ID, "@stui-role", RoleCoordinatorName)
				p.Role = RoleCoordinatorName
			}
			confirm(&coord, p)
			coordPane = p.ID
		}
	}
	// A worker on another machine whose record does not say its parent was
	// told the coordinator: say so before the coordinator's pane is
	// written over, or the move reads as already told and is never tried
	// again when telling it fails ("parent gone").
	if coord.ID != r.Coordinator {
		for i := range r.Workers {
			if w := &r.Workers[i]; w.Machine != "" && w.Parent == "" && w.ParentKey == "" {
				w.Parent, changed = r.Coordinator, true
			}
		}
	}
	r.Coordinator, r.CoordinatorKey, r.CoordinatorConv, r.CoordinatorLive, r.CoordinatorPlace = coord.ID, coord.Key, coord.Conv, coord.Live, coord.Place
	b.coordPane = coordPane

	for i := range r.Workers {
		w := &r.Workers[i]
		if w.Machine != "" {
			continue
		}
		m := member{ID: w.ID, Key: w.Key, Conv: w.Conv, ParentKey: w.ParentKey, Live: w.Live, Place: w.Place, Seen: w.Seen}
		p := b.find(m, sameServer, r.ID, claimed)
		if p == nil {
			if m.Live && m.Seen != "" && m.Seen == b.server {
				// Seen in this server and gone from it: closed, not lost.
				w.Live, changed = false, true
			}
			continue
		}
		// The run a worker is in: @stui-run, or @stui-in-run for a team's lead
		// taken in as one worker (its @stui-run is its own team's).
		in, inOpt := p.Run, "@stui-run"
		if w.Team != "" {
			in, inOpt = p.InRun, OptInRun
		}
		parent := coordPane
		if parent == "" && strings.Contains(r.Coordinator, "/") {
			// Its coordinator is on another machine.
			parent = r.Coordinator
		}
		if m.ParentKey != "" && m.ParentKey != coord.Key {
			// A worker it works for, by its key; none is no parent, not
			// the coordinator or a pane that took its number (anchor.go).
			parent = ""
			if q := b.byKey[m.ParentKey]; q != nil {
				parent = q.ID
			}
		}
		switch {
		case in == r.ID:
			confirm(&m, p)
			if q := b.live[p.Parent]; q != nil && q.Key != "" && q.Key != m.ParentKey && q.Key != coord.Key {
				m.ParentKey, changed = q.Key, true
			}
			// Its parent's pane changed under it.
			if b.live[p.Parent] == nil && parent != "" && parent != p.ID {
				b.set(p.ID, "@stui-parent", parent)
				p.Parent = parent
			}
		case in != "":
			// Released and taken in by another run.
			if m.Live {
				m.Live, changed = false, true
			}
		case sameServer && p.ID == m.ID:
			if m.Live {
				m.Live, changed = false, true
			}
		case m.Live:
			kv := []string{inOpt, r.ID, "@stui-role", RoleWorkerName, "@stui-name", w.Name}
			if parent != "" && parent != p.ID {
				kv = append(kv, "@stui-parent", parent)
			}
			if w.Adopted {
				kv = append(kv, "@stui-adopted", "1")
			}
			if w.Done {
				kv = append(kv, "@stui-done", "1", "@stui-summary", w.Summary)
			}
			// Its role and brief (brief.go). One asked for and not
			// answered is asked again: whether it was typed is not kept.
			if w.About != "" {
				kv = append(kv, OptAbout, w.About)
			}
			if w.BriefState != "" {
				kv = append(kv, OptBriefState, w.BriefState, OptBriefAt, strconv.FormatInt(w.BriefAt, 10))
				if w.Brief != "" {
					kv = append(kv, OptBrief, w.Brief)
				}
			}
			if len(w.Owns) > 0 {
				kv = append(kv, OptOwns, strings.Join(w.Owns, ","))
				if w.Worktree != nil && w.Worktree.Dir != "" {
					kv = append(kv, OptRoot, w.Worktree.Dir)
				}
			}
			b.set(p.ID, kv...)
			if w.Team != "" {
				p.InRun = r.ID
			} else {
				p.Run = r.ID
			}
			p.Role, p.Parent = RoleWorkerName, parent
			confirm(&m, p)
		}
		w.ID, w.Key, w.Conv, w.ParentKey, w.Live, w.Place, w.Seen = m.ID, m.Key, m.Conv, m.ParentKey, m.Live, m.Place, m.Seen
	}
	if seen && r.Server != b.server {
		r.Server, changed = b.server, true
	}
	return moved, changed
}

// remote is the workers of r on other machines whose parent here moved to
// another pane: what their machines were told (Worker.Parent, the
// coordinator when not written down) is not where it is now. Records only
// change once a machine has been told, by Adopter.Reparent; one that could
// not be reached is tried again on the next pass.
func (b *rebinder) remote(r *Run, moved []string) []Reparent {
	if b.coordPane == "" {
		return nil
	}
	var out []Reparent
	for _, w := range r.Workers {
		if w.Machine == "" {
			continue
		}
		told := w.Parent
		if told == "" {
			told = b.oldCoord
		}
		want := b.coordPane
		switch {
		case w.ParentKey != "":
			// By its key alone: a parent no pane has the key of is lost,
			// and stays so for a person to see (stui orch status), not
			// handed to the coordinator or to whoever has its number.
			q := b.byKey[w.ParentKey]
			if q == nil {
				continue
			}
			want = q.ID
		case told != b.oldCoord:
			// A worker here leads it: that one's record says where it went.
			want = told
			for _, mv := range moved {
				if from, to, ok := strings.Cut(strings.TrimPrefix(mv, r.ID+": "), " -> "); ok && from == told {
					want = to
				}
			}
			// What it was told is no pane of this run now (the server it
			// was in is gone, its id maybe another pane's): its parent is
			// the coordinator. Otherwise a move found in an earlier pass,
			// whose telling failed, was never tried again (a remote
			// machine's workers left "parent gone" after a restart).
			if q := b.live[want]; want == told && (q == nil || (q.Run != r.ID && q.InRun != r.ID)) {
				want = b.coordPane
			}
		}
		if want != told {
			out = append(out, Reparent{Run: r.ID, Child: w.ID, Parent: want})
		}
	}
	return out
}

// saveRebound writes what Rebind owns of r into the record as it is on
// disk now, not r as it was read: stui worker done may have written the
// record in between, from another process.
func saveRebound(r *Run) {
	cur, err := Load(r.ID)
	if err != nil {
		return
	}
	cur.Coordinator, cur.CoordinatorKey, cur.CoordinatorConv, cur.CoordinatorLive, cur.CoordinatorPlace = r.Coordinator, r.CoordinatorKey, r.CoordinatorConv, r.CoordinatorLive, r.CoordinatorPlace
	cur.Server = r.Server
	// Workers by their place: Rebind adds and removes none.
	if len(cur.Workers) == len(r.Workers) {
		for i := range cur.Workers {
			w, n := &cur.Workers[i], r.Workers[i]
			if w.Started != n.Started || w.Name != n.Name {
				continue
			}
			w.ID, w.Key, w.Conv, w.ParentKey, w.Live, w.Place, w.Seen = n.ID, n.Key, n.Conv, n.ParentKey, n.Live, n.Place, n.Seen
			if w.Machine != "" && w.Parent == "" {
				w.Parent = n.Parent
			}
		}
	}
	cur.Save()
}

// ServerID is how Rebind's server argument is written.
func ServerID(pid int, start int64) string {
	return strconv.Itoa(pid) + ":" + strconv.FormatInt(start, 10)
}

// ParentStatus is a machine's answer about a pane there that a worker on
// another machine names as its parent: GET /api/orch/parents/<pane>?run=…
type ParentStatus struct {
	Pane  string `json:"pane"`
	Alive bool   `json:"alive"`
	// The pane's run, and the run's coordinator pane now when the record of
	// the run asked about is there and its coordinator is alive.
	Run         string `json:"run,omitempty"`
	Coordinator string `json:"coordinator,omitempty"`
}

// Verdict reads a ParentStatus for a worker of run: gone when its parent
// pane is not there any more (or is not in the run), and now the pane its
// run's coordinator is in, when that is another one.
func (s ParentStatus) Verdict(run string) (gone bool, now string) {
	gone = !s.Alive || (run != "" && s.Run != run)
	if s.Coordinator != "" && s.Coordinator != s.Pane {
		now = s.Coordinator
	}
	return gone, now
}

// serveParent answers GET /api/orch/parents/<pane>?run=<run>, for the stui of
// a worker on another machine asking whether its parent here is still
// there (stui worker list says "parent gone" when not). Read only, and
// nothing in it but pane ids, so it does not need accept-remote.
func (a *Adopter) serveParent(w http.ResponseWriter, r *http.Request) bool {
	id, ok := strings.CutPrefix(r.URL.Path, "/api/orch/parents/")
	if !ok {
		return false
	}
	if r.Method != "GET" {
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
		return true
	}
	if !paneID.MatchString(id) {
		writeJSON(w, 400, map[string]any{"error": "bad pane"})
		return true
	}
	ps, err := a.panes(r.Context())
	if err != nil {
		writeJSON(w, 502, map[string]any{"error": err.Error()})
		return true
	}
	p, alive := ps[id]
	st := ParentStatus{Pane: id, Alive: alive, Run: p.Run}
	if run := r.URL.Query().Get("run"); runID.MatchString(run) {
		if rec, err := Load(run); err == nil {
			if _, ok := ps[rec.Coordinator]; ok {
				st.Coordinator = rec.Coordinator
			}
		}
	}
	writeJSON(w, 200, st)
	return true
}

// KeyConv is what a run's record says of the agent in a pane with a
// lasting key: its kind ("" for a coordinator, whose record does not say)
// and its conversation.
type KeyConv struct {
	Kind, Conv string
}

// Keyed is every run's coordinators and workers on this machine by their
// lasting key: their conversations (a restore resumes them by these when
// the hooks' record of them went with the tmux server), and the runs each
// key is in. The newest run's word wins.
func Keyed() (convs map[string]KeyConv, runs map[string][]string) {
	convs, runs = map[string]KeyConv{}, map[string][]string{}
	add := func(run, key, kind, conv string) {
		if key == "" {
			return
		}
		runs[key] = append(runs[key], run)
		if _, ok := convs[key]; !ok && conv != "" {
			convs[key] = KeyConv{Kind: kind, Conv: conv}
		}
	}
	for _, r := range Runs() {
		add(r.ID, r.CoordinatorKey, "", r.CoordinatorConv)
		for _, w := range r.Workers {
			if w.Machine == "" {
				add(r.ID, w.Key, w.Agent, w.Conv)
			}
		}
	}
	return convs, runs
}

// ArchiveDir is where retired runs go: kept, out of every list.
func ArchiveDir() string { return filepath.Join(Dir(), "archive") }

// Archive retires run id: its record says when and why, and it moves under
// ArchiveDir, where nothing lists it, rebinds it or matches a pane to it.
func Archive(id, why string) error {
	r, err := Load(id)
	if err != nil {
		return err
	}
	r.Retired, r.RetiredWhy = Now(), why
	if err := r.Save(); err != nil {
		return err
	}
	if err := os.MkdirAll(ArchiveDir(), 0o700); err != nil {
		return err
	}
	to := filepath.Join(ArchiveDir(), id)
	if err := os.Rename(RunDir(id), to); err != nil {
		return err
	}
	// The copies of its tasks in its workers' projects (projectcopy.go).
	removeCopies(id, to)
	return nil
}

// Unarchive puts retired run id back under Dir, where Rebind finds its
// agents again by their keys. A directory stui kept writing to after the run
// was retired (its events, copies of what was sent) is merged with it:
// events by id, oldest first. It refuses a run that is not archived, and
// one whose record is under Dir already.
func Unarchive(id string) error {
	if !runID.MatchString(id) {
		return os.ErrInvalid
	}
	src, dst := filepath.Join(ArchiveDir(), id), RunDir(id)
	if _, err := os.Stat(filepath.Join(src, "run.json")); err != nil {
		return fmt.Errorf("%s is not archived", id)
	}
	if _, err := os.Stat(filepath.Join(dst, "run.json")); err == nil {
		return fmt.Errorf("%s is not retired: its record is in %s", id, dst)
	}
	if _, err := os.Stat(dst); os.IsNotExist(err) {
		if err := os.Rename(src, dst); err != nil {
			return err
		}
	} else if err := mergeInto(src, dst); err != nil {
		return err
	}
	r, err := Load(id)
	if err != nil {
		return err
	}
	r.Retired, r.RetiredWhy = 0, ""
	if err := r.Save(); err != nil {
		return err
	}
	restoreCopies(id)
	return nil
}

// mergeInto moves what src holds into dst, which has no record: files dst
// does not have are moved, events.jsonl is merged, and src goes.
func mergeInto(src, dst string) error {
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == src {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		to := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(to, 0o700)
		case rel == "events.jsonl":
			return mergeEvents(p, to)
		case rel == copiesFile:
			return appendFile(p, to)
		}
		if _, err := os.Stat(to); err == nil {
			return nil
		}
		return os.Rename(p, to)
	})
	if err != nil {
		return err
	}
	return os.RemoveAll(src)
}

// appendFile adds what from holds to the end of to.
func appendFile(from, to string) error {
	b, err := os.ReadFile(from)
	if err != nil || len(b) == 0 {
		return err
	}
	f, err := os.OpenFile(to, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// mergeEvents writes into to the events of from and of to, each once (by
// id), oldest first. Lines appended to to while it is merged are kept.
func mergeEvents(from, to string) error {
	type line struct {
		id string
		ts int64
		b  []byte
	}
	var all []line
	seen := map[string]bool{}
	read := func(p string) {
		b, _ := os.ReadFile(p)
		for _, l := range bytes.Split(b, []byte("\n")) {
			var ev Event
			if len(l) == 0 || json.Unmarshal(l, &ev) != nil || seen[ev.ID] {
				continue
			}
			seen[ev.ID] = true
			all = append(all, line{ev.ID, ev.TS, append([]byte(nil), l...)})
		}
	}
	read(from)
	read(to)
	sort.SliceStable(all, func(i, j int) bool { return all[i].ts < all[j].ts })
	var out bytes.Buffer
	for _, l := range all {
		out.Write(l.b)
		out.WriteByte('\n')
	}
	tmp := to + ".merge"
	if err := os.WriteFile(tmp, out.Bytes(), 0o600); err != nil {
		return err
	}
	// Appended in between.
	n := len(all)
	read(to)
	if len(all) > n {
		f, err := os.OpenFile(tmp, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		for _, l := range all[n:] {
			f.Write(append(l.b, '\n'))
		}
		f.Close()
	}
	return os.Rename(tmp, to)
}
