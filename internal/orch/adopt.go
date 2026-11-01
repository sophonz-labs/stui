package orch

// Adopting: an agent already running in its own pane, with its conversation
// so far, becomes another agent's worker. stui worker
// adopt does it from a pane. Nothing is started or restarted: the pane gets
// the options a started worker has, and both agents are told.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sophonz-labs/stui/internal/chat"
)

// AdoptOptions is what adopting needs from the running stui.
type AdoptOptions struct {
	// IsOwner says whether a request is the owner's, for the routes only the
	// owner may read (GET /api/orch/history, /api/orch/doc): 404 for any
	// other. nil: everybody past stui's gate is the owner.
	IsOwner func(*http.Request) bool
	// Tmux runs tmux against the server stui shares.
	Tmux func(ctx context.Context, args ...string) (string, error)
	// Agents is agent watch's agent per pane id ("" or absent: no agent).
	Agents func() map[string]string
	// Deliver types a message into a pane's agent, as POST /api/chat/send;
	// nil tells no one.
	Deliver func(ctx context.Context, pane, text string) (int, map[string]any)
	// DeliverOver is Deliver into a box that holds exactly own, stui's own
	// line left there unsent, which it types over (chat DeliverOver); nil:
	// such a line waits for a person to send or clear it.
	DeliverOver func(ctx context.Context, pane, text, own string) (int, map[string]any)
	// State is a pane's agent state and since when (ms), "" for none: how
	// a worker started for another machine is seen to begin (remote.go).
	State func(pane string) (string, int64)
	// Source is what last set a pane's agent state ("interrupt": the
	// person stopped the turn); LastTurn how its conversation says the
	// last turn ended. Both for why a worker stopped (stopreason.go); nil
	// leaves that to the pane's screen.
	Source   func(pane string) string
	LastTurn func(pane string) chat.TurnEnd
	// Self is this machine's slug on the account, "" when it has none;
	// Remote calls the stui of another machine of the account
	// (internal/remote). Both for workers on other machines (remote.go).
	Self   func() string
	Remote func(ctx context.Context, slug, method, path string, body any) (int, []byte, error)
	// MachineName is what a machine is called, for the messages agents
	// read ("Build Mac" rather than its slug); "" when unknown.
	MachineName func(slug string) string
	// LastAsked is the first line of the last prompt in a pane's
	// conversation, "" when it cannot be read: told to the one adopting.
	LastAsked func(pane string) string
	// Submitted is when a prompt was last submitted in a pane (the agent's
	// submit hook), zero for none known: the mail carrier's proof that a
	// line it could not confirm went in after all (mail.go).
	Submitted func(pane string) time.Time
}

// AdoptHow is how to adopt: whether to tell both agents, the role the one
// adopting gives the worker, and whether to ask it for a brief.
type AdoptHow struct {
	Tell    bool
	About   string
	NoBrief bool
}

type Adopter struct {
	o  AdoptOptions
	mu sync.Mutex
	// Workers being started for other machines, by pane (remote.go).
	starting map[string]*remoteStart
	// The parents' mail (mail.go): one goroutine uses it.
	mail mailState
	// The runs' events as the carrier last read them (eventcache.go).
	events EventCache
	// Questions being typed into reading agents, by pane (trees.go).
	asking map[string]*remoteStart
}

func NewAdopter(o AdoptOptions) *Adopter { return &Adopter{o: o} }

var paneID = regexp.MustCompile(`^%[0-9]{1,9}$`)

// pane is a pane's place and run options, from tmux.
type pane struct {
	ID, Session, Run, Role, Parent, Name, Adopted, WindowName string
	Window                                                    int
	// The run it is a worker in when it leads a team of its own (Run is
	// then the team's run): @stui-in-run.
	InRun string
}

// OptInRun is set on a team's lead adopted as a worker: the run of the
// agent it works for. Its @stui-run stays its own team's run.
const OptInRun = "@stui-in-run"

// PaneFormat is the list-panes format ParseLevels reads.
const PaneFormat = "#{pane_id}\t#{session_name}\t#{@stui-run}\t#{@stui-role}\t#{@stui-parent}\t#{@stui-name}\t#{@stui-adopted}\t#{window_index}\t#{@stui-in-run}\t#{window_name}"

const paneFormat = PaneFormat

func parsePanes(out string) map[string]pane {
	ps := map[string]pane{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.SplitN(l, "\t", 10)
		if len(f) < 10 || !paneID.MatchString(f[0]) {
			continue
		}
		if _, seen := ps[f[0]]; seen {
			// A window linked into a view session is listed again.
			continue
		}
		w, _ := strconv.Atoi(f[7])
		ps[f[0]] = pane{ID: f[0], Session: f[1], Run: f[2], Role: f[3], Parent: f[4], Name: f[5], Adopted: f[6], Window: w, InRun: f[8], WindowName: f[9]}
	}
	return ps
}

// Levels is how deep pane is, from list-panes output in PaneFormat: within
// its run (depth) and in the whole chain above it. Each remote is true when
// that count reached a parent on another machine, whose levels above are
// not known here.
func Levels(out, id string) (depth_, chain_ int, depthRemote, chainRemote bool) {
	ps := parsePanes(out)
	return depth(ps, id), chain(ps, id), reachesRemote(ps, id, true), reachesRemote(ps, id, false)
}

// reachesRemote: counting up from id (within its run, or the whole chain)
// ends at a parent on another machine.
func reachesRemote(ps map[string]pane, id string, inRun bool) bool {
	seen := map[string]bool{}
	for cur := id; !seen[cur]; {
		seen[cur] = true
		p := ps[cur]
		if p.Parent == "" || (inRun && p.InRun != "") {
			return false
		}
		if _, local := ps[p.Parent]; !local {
			return strings.Contains(p.Parent, "/")
		}
		cur = p.Parent
	}
	return false
}

func (a *Adopter) panes(ctx context.Context) (map[string]pane, error) {
	out, err := a.o.Tmux(ctx, "list-panes", "-a", "-F", paneFormat)
	if err != nil {
		return nil, err
	}
	return parsePanes(out), nil
}

func (a *Adopter) set(ctx context.Context, id string, kv ...string) error {
	for i := 0; i+1 < len(kv); i += 2 {
		if _, err := a.o.Tmux(ctx, "set-option", "-p", "-t", id, kv[i], kv[i+1]); err != nil {
			return fmt.Errorf("set %s on %s: %w", kv[i], id, err)
		}
	}
	return nil
}

func (a *Adopter) unset(ctx context.Context, id string, names ...string) {
	for _, n := range names {
		a.o.Tmux(ctx, "set-option", "-pu", "-t", id, n)
	}
}

// depth is how many parents are above id within its run: it stops at a
// team's lead, which is its own run's top though it works for someone (its
// @stui-in-run). -1 when they loop.
func depth(ps map[string]pane, id string) int {
	n := 0
	seen := map[string]bool{id: true}
	for cur := id; ps[cur].Parent != "" && ps[cur].InRun == ""; {
		p := ps[cur].Parent
		if seen[p] || n > 32 {
			return -1
		}
		seen[p] = true
		n++
		cur = p
	}
	return n
}

// chain is how many parents are above id, teams' leads included; -1 when
// they loop.
func chain(ps map[string]pane, id string) int {
	n := 0
	seen := map[string]bool{id: true}
	for p := ps[id].Parent; p != ""; p = ps[p].Parent {
		if seen[p] || n > 32 {
			return -1
		}
		seen[p] = true
		n++
	}
	return n
}

// members is how many workers id directs: panes whose parent it is, and the
// workers on other machines of the run it coordinates. One with any leads
// a team.
func members(ps map[string]pane, id string) int {
	n := 0
	for _, p := range ps {
		if p.Parent == id && p.ID != id {
			n++
		}
	}
	if r := ps[id].Run; r != "" {
		if run, err := Load(r); err == nil && run.Coordinator == id {
			for _, w := range run.Workers {
				if w.Machine != "" {
					n++
				}
			}
		}
	}
	return n
}

// TeamOf is the team id leads, from list-panes output in PaneFormat: nil
// unless it was adopted as a worker with workers of its own.
func TeamOf(out, id string) *TeamInfo {
	return teamOf(parsePanes(out), id)
}

func teamOf(ps map[string]pane, id string) *TeamInfo {
	p, ok := ps[id]
	if !ok || p.InRun == "" {
		return nil
	}
	n := members(ps, id)
	if n == 0 {
		return nil
	}
	return &TeamInfo{Run: p.Run, Members: n}
}

// tooDeep is the refusal when a worker would go below the limits.
func tooDeep() error {
	return refuse(409, "too-deep", "too deep: within a team workers go at most %d levels below its coordinator, and the whole chain of agents handing work down is at most %d long", MaxDepth, MaxChain)
}

// height is how many levels of workers are below id.
func height(ps map[string]pane, id string, guard int) int {
	if guard > 32 {
		return 0
	}
	h := 0
	for _, p := range ps {
		if p.Parent == id && p.ID != id {
			h = max(h, 1+height(ps, p.ID, guard+1))
		}
	}
	return h
}

// above reports whether anc is id itself or one of its parents.
func above(ps map[string]pane, anc, id string) bool {
	seen := map[string]bool{}
	for p := id; p != "" && !seen[p]; p = ps[p].Parent {
		if p == anc {
			return true
		}
		seen[p] = true
	}
	return false
}

// AdoptError is a refusal, with the reason in words for the person.
type AdoptError struct {
	Status int
	Reason string
	Msg    string
}

func (e *AdoptError) Error() string { return e.Msg }

func refuse(status int, reason, format string, a ...any) error {
	return &AdoptError{Status: status, Reason: reason, Msg: fmt.Sprintf(format, a...)}
}

// AdoptResult is what adopting did.
type AdoptResult struct {
	Run    string `json:"run"`
	Parent string `json:"parent"`
	Child  string `json:"child"`
	Name   string `json:"name"`
	// Whether each agent took the message, and why not.
	Told map[string]string `json:"told,omitempty"`
	// The child leads a team: its own run, which it keeps.
	Team string `json:"team,omitempty"`
	// What stui knows of the child at once:
	// the workers it directs, its state and since when (ms), the first line
	// of the last thing it was asked, where it works, its role and whether a
	// brief was asked for.
	Members    []string `json:"members,omitempty"`
	State      string   `json:"state,omitempty"`
	Since      int64    `json:"since,omitempty"`
	LastAsked  string   `json:"lastAsked,omitempty"`
	Cwd        string   `json:"cwd,omitempty"`
	Branch     string   `json:"branch,omitempty"`
	About      string   `json:"about"`
	BriefState string   `json:"briefState,omitempty"`
	// The child's lasting key, for a worker on another machine (anchor.go).
	Key string `json:"key,omitempty"`
	// A name the pane still had from before (@stui-name) that its window's
	// title now replaces (adoptName).
	Was string `json:"was,omitempty"`
}

// reply is an AdoptResult as the API answers it.
func (res *AdoptResult) reply() map[string]any {
	out := map[string]any{"ok": true, "run": res.Run, "parent": res.Parent, "child": res.Child, "name": res.Name, "told": res.Told}
	if res.Team != "" {
		out["team"] = res.Team
	}
	for k, v := range map[string]any{"members": res.Members, "state": res.State, "since": res.Since, "lastAsked": res.LastAsked, "cwd": res.Cwd, "branch": res.Branch, "briefState": res.BriefState, "key": res.Key, "was": res.Was} {
		switch x := v.(type) {
		case string:
			if x != "" {
				out[k] = x
			}
		case int64:
			if x != 0 {
				out[k] = x
			}
		case []string:
			if len(x) > 0 {
				out[k] = x
			}
		}
	}
	out["about"] = res.About
	return out
}

// Adopt makes child a worker of parent.
func (a *Adopter) Adopt(ctx context.Context, parent, child string, how AdoptHow) (*AdoptResult, error) {
	tell := how.Tell
	if !paneID.MatchString(parent) || !paneID.MatchString(child) {
		return nil, refuse(400, "bad-pane", "bad pane id")
	}
	if parent == child {
		return nil, refuse(400, "same", "an agent cannot be its own worker")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	ps, err := a.panes(ctx)
	if err != nil {
		return nil, refuse(502, "tmux", "tmux: %v", err)
	}
	pp, ok1 := ps[parent]
	cp, ok2 := ps[child]
	if !ok1 || !ok2 {
		return nil, refuse(404, "gone", "that pane is gone")
	}
	agents := map[string]string{}
	if a.o.Agents != nil {
		agents = a.o.Agents()
	}
	if agents[parent] == "" {
		return nil, refuse(409, "parent-not-agent", "%s runs no coding agent stui knows: only an agent can direct workers", parent)
	}
	if agents[child] == "" {
		return nil, refuse(409, "child-not-agent", "%s runs no coding agent stui knows", child)
	}
	if above(ps, child, parent) {
		return nil, refuse(409, "loop", "%s already works under %s: that would go round", parent, child)
	}
	if cp.Parent == parent {
		res := &AdoptResult{Run: cp.Run, Parent: parent, Child: child, Name: cp.Name}
		if about := CleanAbout(how.About); about != "" {
			if old := a.tmuxOpt(ctx, child, OptAbout); old != about {
				a.treeEvent(ctx, ps, Event{Run: workRunOf(cp), Kind: EvAbout, To: child, ToName: cp.Name, ToKey: a.ensureKey(ctx, child), Was: old, Now: about})
			}
			a.set(ctx, child, OptAbout, about)
			if r, err := Load(cp.Run); err == nil {
				if w := r.Worker(child); w != nil {
					w.About = about
					r.Save()
				}
			}
		}
		a.facts(ctx, ps, res)
		return res, nil
	}
	// A team (an agent with workers of its own) keeps its run and joins
	// the parent's as one worker: it counts from its own lead, and only
	// the whole chain limits how deep it goes.
	n := members(ps, child)
	lead := n > 0 && cp.Run != ""
	d, c, h := depth(ps, parent), chain(ps, parent), height(ps, child, 0)
	inRun := d + 1
	if !lead {
		inRun += h
	}
	if d < 0 || c < 0 || inRun > MaxDepth || c+1+h > MaxChain {
		return nil, tooDeep()
	}

	// The parent's run, made when it has none, as stui worker start does.
	var run *Run
	if pp.Run != "" {
		run, _ = Load(pp.Run)
	}
	if run == nil {
		// Keyed, so Rebind finds its coordinator however its pane's
		// options read (a parent may still name a retired run).
		run = &Run{ID: NewID(), Coordinator: parent, CoordinatorKey: a.ensureKey(ctx, parent), CoordinatorLive: true, Session: pp.Session, Created: Now()}
		if err := run.Save(); err != nil {
			return nil, refuse(500, "save", "%v", err)
		}
		if pp.Role == "" {
			if err := a.set(ctx, parent, "@stui-run", run.ID, "@stui-role", RoleCoordinatorName); err != nil {
				return nil, refuse(502, "tmux", "%v", err)
			}
		}
	}
	name, was := adoptName(cp, child)
	// What it was before, to give back when it is let go.
	if cp.Adopted != "1" {
		a.set(ctx, child, "@stui-prev-run", cp.Run, "@stui-prev-role", cp.Role, "@stui-prev-parent", cp.Parent)
	}
	team := ""
	if lead {
		team = cp.Run
	}
	if err := a.markWorker(ctx, child, run.ID, parent, name, team, how); err != nil {
		return nil, refuse(502, "tmux", "%v", err)
	}
	about := CleanAbout(how.About)
	// Its parent by key, written down here rather than left for Rebind to
	// read off @stui-parent: none for the coordinator. Adopting it again puts
	// right a key an earlier option left (a worker under another worker
	// whose pane took the number), which a restore would otherwise bring
	// back, writing the options again from the record.
	parentKey := ""
	if parent != run.Coordinator {
		parentKey = a.ensureKey(ctx, parent)
		if parentKey == run.CoordinatorKey {
			parentKey = ""
		}
	}
	run.addAdopted(Worker{ID: child, Name: name, Agent: agents[child], Window: cp.Window, Started: Now(), Adopted: true, Team: team, About: about, BriefState: briefState(how), ParentKey: parentKey})
	run.Save()

	res := &AdoptResult{Run: run.ID, Parent: parent, Child: child, Name: name, Team: team, Was: was}
	ev := Event{Run: run.ID, Kind: EvAdopt, To: child, ToName: name, ToKey: a.ensureKey(ctx, child),
		Parent: parent, ParentName: callOf(pp), ParentKey: a.ensureKey(ctx, parent), Was: was, Now: name, About: about}
	if was == "" {
		ev.Now = ""
	}
	if cp.Role == RoleWorkerName && cp.Parent != "" {
		ev.WasParent = cp.Parent
		if paneID.MatchString(cp.Parent) {
			ev.WasParentKey = a.tmuxOpt(ctx, cp.Parent, OptKey)
		}
	}
	a.treeEvent(ctx, ps, ev)
	ps, _ = a.panes(ctx)
	a.facts(ctx, ps, res)
	if tell {
		where := fmt.Sprintf("window %d", cp.Window)
		if cp.Session != pp.Session {
			where = fmt.Sprintf("session %s, window %d", cp.Session, cp.Window)
		}
		pm := fmt.Sprintf("[stui] The %s agent in pane %s (%s, %q) is now your worker, with its own conversation so far. Give it work: stui worker send %s \"<task>\". Its answer: stui worker read %s. Wait for it: stui worker wait %s. The guide: stui skills get orchestrate. Nothing to do now unless the user asked for it.", agents[child], child, where, name, child, child, child)
		cm := fmt.Sprintf("[stui] You are now a worker of the agent in pane %s. Keep what you know. When it gives you a task and you have done it, run: stui worker done --summary \"<what you did, and how you checked it>\". The guide: stui skills get worker. Nothing to do now.", parent)
		if lead {
			pm = fmt.Sprintf("[stui] The %s agent in pane %s (%s, %q) is now your worker: it leads a team of %d workers, which stay its own. Give work to it alone, not to its team: stui worker send %s \"<task>\". It reports once for the team. Its answer: stui worker read %s. Wait for it: stui worker wait %s. The guide: stui skills get orchestrate. Nothing to do now unless the user asked for it.", agents[child], child, where, name, n, child, child, child)
			cm = fmt.Sprintf("[stui] You are now a worker of the agent in pane %s, and your workers stay yours. When it gives you a task, hand parts to your team as before, collect their results, and report once: stui worker done --summary \"<what the team did, and how it was checked>\". The guide: stui skills get worker. Nothing to do now.", parent)
		}
		if !how.NoBrief {
			pm = strings.TrimSuffix(pm, " Nothing to do now unless the user asked for it.") + fmt.Sprintf(" It is asked for a brief of what it has done, once its turn ends: stui worker wait %s --brief. Nothing to do now unless the user asked for it.", child)
		}
		res.Told = a.tellBoth(ctx, parent, pm, "", "")
		res.Told[child] = a.tellChild(ctx, child, cm)
	}
	return res, nil
}

// briefState is the brief state adopting with how starts with.
func briefState(how AdoptHow) string {
	if how.NoBrief {
		return BriefSkipped
	}
	return BriefAsked
}

// markWorker puts a worker's options on child: in run under parent. A
// team's lead (team, its own run) keeps @stui-run and says the run it works
// in with @stui-in-run.
func (a *Adopter) markWorker(ctx context.Context, child, run, parent, name, team string, how AdoptHow) error {
	kv := []string{"@stui-run", run, "@stui-role", RoleWorkerName, "@stui-parent", parent, "@stui-name", name, "@stui-adopted", "1"}
	if team != "" {
		kv[0], kv[1] = OptInRun, run
	}
	brief, clear := briefOptions(how.About, !how.NoBrief)
	if err := a.set(ctx, child, append(kv, brief...)...); err != nil {
		return err
	}
	// @stui-sent too: what an earlier run sent it is not work in this one.
	a.unset(ctx, child, append([]string{"@stui-done", "@stui-summary", "@stui-outcome", "@stui-sent", "@stui-gen", OptTellLater}, clear...)...)
	if team == "" {
		a.unset(ctx, child, OptInRun)
	}
	return nil
}

// addAdopted records an adopted worker, or makes the one already there new.
func (r *Run) addAdopted(w Worker) {
	if x := r.Worker(w.ID); x != nil {
		x.Done, x.Summary, x.Ended, x.Team, x.ParentKey = false, "", 0, w.Team, w.ParentKey
		return
	}
	r.Workers = append(r.Workers, w)
}

// Release lets child go: it is no one's worker, and what it was before it
// was adopted comes back. tell: say so to both.
func (a *Adopter) Release(ctx context.Context, child string, tell bool) (*AdoptResult, error) {
	if !paneID.MatchString(child) {
		return nil, refuse(400, "bad-pane", "bad pane id")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	ps, err := a.panes(ctx)
	if err != nil {
		return nil, refuse(502, "tmux", "tmux: %v", err)
	}
	cp, ok := ps[child]
	if !ok {
		return nil, refuse(404, "gone", "that pane is gone")
	}
	if cp.Parent == "" {
		return &AdoptResult{Child: child}, nil
	}
	opt := func(name string) string {
		v, _ := a.o.Tmux(ctx, "show-options", "-pqv", "-t", child, name)
		return strings.TrimSpace(v)
	}
	prevRun, prevRole, prevParent := opt("@stui-prev-run"), opt("@stui-prev-role"), opt("@stui-prev-parent")
	a.unset(ctx, child, append([]string{"@stui-run", "@stui-role", "@stui-parent", "@stui-adopted", "@stui-done", "@stui-summary", "@stui-outcome", "@stui-gen", "@stui-prev-run", "@stui-prev-role", "@stui-prev-parent", OptInRun}, briefKeys...)...)
	if cp.Adopted == "1" {
		a.unset(ctx, child, "@stui-name", "@stui-task")
	}
	// It directs workers of its own: it goes on doing that.
	if prevRun == "" && height(ps, child, 0) > 0 {
		prevRun, prevRole = cp.Run, RoleCoordinatorName
	}
	if prevRun != "" {
		a.set(ctx, child, "@stui-run", prevRun)
	}
	if prevRole != "" {
		a.set(ctx, child, "@stui-role", prevRole)
	}
	if prevParent != "" && ps[prevParent].ID != "" {
		a.set(ctx, child, "@stui-parent", prevParent)
	}
	// The run it worked in: a team's lead's is @stui-in-run, its own is
	// the team's.
	in := cp.Run
	if cp.InRun != "" {
		in = cp.InRun
	}
	if r, err := Load(in); err == nil {
		kept := r.Workers[:0]
		for _, w := range r.Workers {
			if !(w.ID == child && w.Adopted) {
				kept = append(kept, w)
			}
		}
		r.Workers = kept
		r.Save()
		a.settle(ctx, cp.Parent, child, r)
	}
	ev := Event{Run: in, Kind: EvRelease, To: child, ToName: cp.Name, ToKey: a.tmuxOpt(ctx, child, OptKey), WasParent: cp.Parent}
	if paneID.MatchString(cp.Parent) {
		ev.WasParentKey = a.tmuxOpt(ctx, cp.Parent, OptKey)
		if n := callOf(ps[cp.Parent]); n != "" {
			ev.Title = "no longer a worker of " + n
		}
	}
	a.treeEvent(ctx, ps, ev)
	res := &AdoptResult{Run: in, Parent: cp.Parent, Child: child, Name: cp.Name}
	if tell && !paneID.MatchString(cp.Parent) {
		// Its coordinator is on another machine, whose stui tells it.
		res.Told = a.tellBoth(ctx, "", "",
			child, fmt.Sprintf("[stui] You are no longer a worker of pane %s: no need to report to it. Nothing to do now.", cp.Parent))
	} else if tell {
		res.Told = a.tellBoth(ctx,
			cp.Parent, fmt.Sprintf("[stui] Pane %s (%q) is no longer your worker: do not send it work. Nothing to do now.", child, cp.Name),
			child, fmt.Sprintf("[stui] You are no longer a worker of pane %s: no need to report to it. Nothing to do now.", cp.Parent))
	}
	return res, nil
}

// tellBoth types both messages at once; "ok" or why not, per pane.
func (a *Adopter) tellBoth(ctx context.Context, p1, m1, p2, m2 string) map[string]string {
	told := map[string]string{}
	if a.o.Deliver == nil {
		return told
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, x := range [][2]string{{p1, m1}, {p2, m2}} {
		if x[0] == "" {
			continue
		}
		wg.Add(1)
		go func(pane, text string) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			code, out := a.o.Deliver(ctx, pane, text)
			why := "ok"
			if code != 200 {
				why, _ = out["reason"].(string)
				if why == "" {
					why, _ = out["error"].(string)
				}
				if why == "" {
					why = strconv.Itoa(code)
				}
			}
			mu.Lock()
			told[pane] = why
			mu.Unlock()
		}(x[0], x[1])
	}
	wg.Wait()
	return told
}

// ServeHTTP answers POST /api/orch/adopt {"parent","child","tell"} and
// POST /api/orch/release {"pane","tell"}; tell is true when absent.
func (a *Adopter) ServeHTTP(w http.ResponseWriter, r *http.Request) bool {
	// The run's history and its copies, the owner's alone (history.go).
	if r.URL.Path == "/api/orch/history" || r.URL.Path == "/api/orch/doc" {
		if a.o.IsOwner != nil && !a.o.IsOwner(r) {
			writeJSON(w, 404, map[string]any{"error": "not found"})
			return true
		}
		if r.URL.Path == "/api/orch/history" {
			a.serveHistory(w, r)
		} else {
			a.serveDoc(w, r)
		}
		return true
	}
	// A pane's inbox (inbox.go), the owner's alone as the history is.
	if r.URL.Path == "/api/orch/inbox" || r.URL.Path == "/api/orch/inbox/ack" || r.URL.Path == "/api/orch/reply" {
		if a.o.IsOwner != nil && !a.o.IsOwner(r) {
			writeJSON(w, 404, map[string]any{"error": "not found"})
			return true
		}
		a.serveInbox(w, r)
		return true
	}
	if a.serveParent(w, r) {
		return true
	}
	// Worktrees by their own id, and the agents that read them (trees.go).
	if a.serveTrees(w, r) {
		return true
	}
	if ServeDiff(w, r, func(ctx context.Context, pane string) error {
		_, err := a.o.Tmux(ctx, "kill-pane", "-t", pane)
		return err
	}, a.askerOf) {
		return true
	}
	if r.URL.Path == "/api/orch/about" {
		a.serveAbout(w, r)
		return true
	}
	if r.URL.Path == "/api/orch/name" {
		a.serveName(w, r)
		return true
	}
	if r.URL.Path != "/api/orch/adopt" && r.URL.Path != "/api/orch/release" {
		return false
	}
	if r.Method != "POST" {
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
		return true
	}
	var body struct {
		Parent string `json:"parent"`
		Child  string `json:"child"`
		Pane   string `json:"pane"`
		Tell   *bool  `json:"tell"`
		// The worker's role, and no brief asked of it (adopt-brief.md).
		About   string `json:"about"`
		NoBrief bool   `json:"noBrief"`
		// The pane that asked (stui worker adopt in it); none from an app.
		By string `json:"by"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]any{"error": "bad body"})
		return true
	}
	r = r.WithContext(WithBy(r.Context(), body.By))
	tell := body.Tell == nil || *body.Tell
	how := AdoptHow{Tell: tell, About: body.About, NoBrief: body.NoBrief}
	var res *AdoptResult
	var err error
	switch {
	case r.URL.Path == "/api/orch/adopt":
		res, err = a.Adopt(r.Context(), body.Parent, body.Child, how)
	default:
		res, err = a.Release(r.Context(), body.Pane, tell)
	}
	var ae *AdoptError
	switch {
	case errors.As(err, &ae):
		writeJSON(w, ae.Status, map[string]any{"error": ae.Msg, "reason": ae.Reason})
	case err != nil:
		writeJSON(w, 500, map[string]any{"error": err.Error()})
	default:
		writeJSON(w, 200, res.reply())
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("content-type", "application/json;charset=utf-8")
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// settle undoes what adopting made of a parent when its last worker goes: a
// coordinator only because it adopted (a run with no objective and no tasks,
// and no pane left that it directs) is a plain agent again. One that stui worker start or stui orch
// start made a coordinator keeps its run. gone is the pane just let go.
func (a *Adopter) settle(ctx context.Context, parent, gone string, r *Run) {
	if !paneID.MatchString(parent) || r.Objective != "" || len(r.Tasks) > 0 {
		return
	}
	// Its workers on other machines are only in the run record.
	for _, w := range r.Workers {
		if w.Machine != "" {
			return
		}
	}
	ps, err := a.panes(ctx)
	if err != nil {
		return
	}
	for id, p := range ps {
		if id != gone && p.Parent == parent {
			return
		}
	}
	if pp, ok := ps[parent]; ok && pp.Run == r.ID && pp.Role == RoleCoordinatorName && pp.Parent == "" {
		a.unset(ctx, parent, "@stui-run", "@stui-role")
	}
}

// adoptName is what a pane is called when it is adopted: its window's title
// as it is now (Name), and the @stui-name it still had when that is another.
// An @stui-name on a pane being adopted is left from an adoption before --
// whose record may be gone, and whose name may be another machine's
// (a build machine's pane may come back as "windows-ci"). A pane that is
// already the worker of the one adopting it keeps its name, and is not
// adopted again. With no title worth a name, the old one, then agent-<n>.
func adoptName(cp pane, id string) (name, was string) {
	name = Name(cp.WindowName)
	switch {
	case name == "":
		name = cp.Name
	case cp.Name != "" && !strings.EqualFold(cp.Name, name):
		was = cp.Name
	}
	if name == "" {
		name = "agent-" + strings.TrimPrefix(id, "%")
	}
	return name, was
}

// workRunOf is the run a pane works in: @stui-in-run for a team's lead taken
// in as a worker, else @stui-run.
func workRunOf(p pane) string {
	if p.InRun != "" {
		return p.InRun
	}
	return p.Run
}
