package watch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sophonz-labs/stui/internal/cmdicon"
	"github.com/sophonz-labs/stui/internal/hookclient"
	"github.com/sophonz-labs/stui/internal/mux"
)

// Tmux runs one tmux command against the watched server.
type Tmux interface {
	Run(ctx context.Context, args ...string) (string, error)
}

// Sink hears state changes and prompts as they happen. Must not block.
type Sink interface {
	StateChanged(sessions []SessionSummary, cause Pane)
	PromptOpened(p Pane, pr Prompt)
	PromptCleared(p Pane, id, reason string)
	TurnDone(p Pane, durationMs int64, result string, sub TurnSubagents)
	// The main agent ended its turn with subagents still running: the work
	// goes on in them, and turn-done comes when they are through.
	TurnDelegated(p Pane, running int, turnMs int64)
}

// TurnSubagents is what a finished turn's subagents came to: those that
// finished, and those stui gave up on.
type TurnSubagents struct {
	Done int `json:"done"`
	Lost int `json:"lost"`
}

type Options struct {
	Tmux Tmux
	Sink Sink
	// Where decision gates are kept across a restart (gates.go); none: in
	// memory only.
	GateFile string
	// Tells an agent its gate's answer (types it into its pane), and says
	// how that went.
	GateAnswered func(Gate) string
	// Keeps a settled gate (answered or cancelled) as a document, in the
	// vault's critic folder, and gives its id. Those documents are the
	// record of what was decided, and what a later agent picks the work up
	// from; GateFile only holds the newest maxGates. A gate is let go from
	// there only once it is kept (gates.go). nil: gates are not kept, and
	// the oldest settled one goes as before.
	GateSettled func(Gate) (string, error)
	// The tmux socket path this watcher watches, to ignore hooks from panes
	// on another server. "" accepts any.
	Socket string
	// Which agents have stui hooks installed, for the snapshot.
	Hooks func() map[string]bool
	// How fully: "installed", "partial", "none" (hookState); nil leaves it
	// out.
	HookStates func() map[string]string
	// Each agent's hooks against what stui wants (hookSets); nil leaves it
	// out.
	HookSets func() any
	Now      func() time.Time
	// Claude's config directory, for its agent teams (team.go). Hooks add
	// the ones their Claude runs with. Default: $CLAUDE_CONFIG_DIR or
	// ~/.claude.
	ClaudeDir string
	// A pane's foreground command line, by its tty. Default: ps.
	Args func(tty string) []string
	// When a pane's foreground process started, by its tty, to tell the
	// agent a saved record was written for from a new one. Default: ps.
	ProcStart func(tty string) (time.Time, bool)
	// The foreground process group on a tty, its leader first, with when
	// each started: which Claude a pane runs, for its session file
	// (claudesession.go). Default: ps.
	Procs func(tty string) []Proc
	// Where the watch keeps its per-pane subagent state across restarts
	// (persist.go); "" keeps it in memory only.
	StatePath string
	// Where stui hook spools the events no agent took (hookclient.Spool),
	// played back once the panes are known (spool.go); "" plays nothing.
	SpoolDir string
	// Where a line goes when the spool held events that are not played
	// back, or tool hooks were dropped (spool.go); nil says nothing.
	Logf func(format string, a ...any)
	// Where stui hook counts the tool hooks no agent took in time
	// (hookclient.DroppedFile); "" counts nothing.
	DroppedFile string
	// Tmux is psmux: a server per session, panes named "<session>:%n"
	// (psmux.go).
	Psmux bool
}

// Timing, shortened by tests.
var (
	settle    = 400 * time.Millisecond
	settleMax = time.Second
	// After a turn ends, Claude keeps its spinner up for a moment (its Stop
	// hooks run under it): screen activity this soon is that, not a new
	// turn.
	stopGrace = time.Second
	// With hooks, a working pane whose screen has shown no turn for this
	// long has stopped without saying so (Esc: Claude sends no Stop).
	screenQuiet = 5 * time.Second
	answerWait  = 2 * time.Second
	// A question that leaves the screen is gone only once it has stayed
	// away this long: an agent redrawing (a resize clears the screen and
	// prints the conversation again before the question) is not an answer.
	promptGone = 3 * time.Second
	// ...and once the screen has stopped changing (a window being dragged
	// redraws it over and over), unless it has been away this long, or the
	// screen shows the agent at work.
	promptGoneMax = 15 * time.Second
	// A question read off a screen still being drawn may be half of one:
	// it is read again once the screen is still, for at most this long.
	unsettledMax = 2 * time.Second
	// The same question back this soon after it left the screen, with no
	// sign from the agent's hooks that it moved on, is the same prompt: it
	// gets its id back rather than a new one (a new id is a new
	// notification on the phone).
	promptRevive = 30 * time.Second
	doneToIdle   = 30 * time.Minute
	promptMaxAge = 24 * time.Hour
	// A running subagent that has sent no hook of its own for this long is
	// lost (it died without SubagentStop: the laptop slept, the process was
	// killed). Longer while it is inside a tool call or waiting on a
	// permission, which can legitimately be silent for a long time.
	subagentQuiet     = 10 * time.Minute
	subagentToolQuiet = 30 * time.Minute
	// Wall-clock time between two ticks beyond this is time the machine was
	// asleep (or stui was stopped): it does not count as a subagent's silence.
	sleepGap = 30 * time.Second
	// How long turn-done for a turn that had subagents is held at least:
	// Claude submits each background agent's result as a task-notification
	// prompt, within a second of the agent or the main agent stopping, and
	// such a prompt in this time is the same turn going on.
	delegateSettle = 3 * time.Second
	// Every turn-done and turn-delegated is held until the screen has shown
	// no turn (no spinner, no Stop hooks running) for doneSettle, and at
	// most doneHoldMax. Claude submits a queued prompt right after its Stop
	// hooks: a real prompt in that time withdraws the held notice, since
	// the turn did not end for anyone waiting on it. The screen is read
	// every holdPoll while a notice is held.
	doneSettle  = 1500 * time.Millisecond
	doneHoldMax = 20 * time.Second
	holdPoll    = 250 * time.Millisecond
)

// heldTurn is a finished turn whose turn-done has not gone yet.
type heldTurn struct {
	start         time.Time
	durMs         int64
	result        string
	sub           TurnSubagents
	delegatedSent bool
	// When it was held, on the real clock (the hold is timed by timers).
	at time.Time
	// The least time it is held.
	min time.Duration
}

// heldDelegate is a turn-delegated not sent yet: held like turn-done, in
// case a queued prompt follows the Stop.
type heldDelegate struct {
	running int
	turnMs  int64
	at      time.Time
}

// subRun is one running subagent: when stui last heard from it, and whether
// that was it going into a tool (or a permission prompt). heard is set
// once one of its own hooks (not SubagentStart) arrived: only such a
// subagent can be given up on for silence. Without the tool hooks wired to
// stui (a partial install) a subagent says nothing between SubagentStart and
// SubagentStop, and its silence is not a sign it died.
type subRun struct {
	seen   int64
	inTool bool
	heard  bool
}

type promptState struct {
	Prompt
	pane        string
	fingerprint string
	// The screen read it came from, for Same.
	parsed *Parsed
	// While open: since when the screen has not shown it.
	goneAt time.Time
	// When it ended.
	endedAt time.Time
	// open, answering, ended
	status string
	reason string
	// The answer carried out, for a retry to be told it already was.
	answer string
	mu     sync.Mutex
}

type paneState struct {
	Pane
	// Its open decision gate (gates.go), shown as its prompt when the
	// screen has none.
	gate          *Gate
	agentFromHook bool
	// When the last hook heard live was sent: a spooled event from before
	// it is history the pane has moved past.
	liveHookAt int64
	// The agent's own id for its conversation, from its hooks: what
	// `claude --resume` / `codex resume` take. For restoring a pane after
	// the multiplexer died.
	resumeID string
	// Where the agent writes this conversation down, and the directory it
	// runs in, from its hooks: for the chat view. In memory only -- never
	// in the state file or an event.
	transcript, cwd string
	// When the agent's current conversation began here, for finding a
	// Codex rollout by its directory and time.
	chatSince time.Time
	// When the main agent last took a prompt (UserPromptSubmit), on the
	// real clock: how a chat send knows it arrived (chat.go).
	submitAt time.Time
	// The watcher's own command guess, before hooks said otherwise.
	cmdAgent string
	notified bool // rang a bell or sent an OSC notification
	listed   bool

	lastOutput, pendingSince time.Time
	// Since when a prompt read has been put off because the screen was
	// still being drawn.
	unsettled   time.Time
	timer       *time.Timer
	lastCapture time.Time

	turnStart time.Time
	// When the last turn ended here, and the hook time of the last Stop:
	// a hook from before it (they are separate processes and can arrive
	// out of order) does not start a turn.
	stoppedAt time.Time
	stopAt    int64
	// Since when a working pane's screen has shown no turn in progress.
	quietSince time.Time
	running    map[string]*subRun
	// Subagents stui gave up on, in case one turns out to be alive after all.
	lost map[string]bool
	// Each subagent, for subagents.items, and the main agent's Agent tool
	// calls waiting for theirs to start.
	subItems map[string]*SubagentItem
	spawns   []spawn
	// The agent team this pane's Claude is in (team.go).
	teamArgs *teamArgs
	argsKey  string
	argsAt   time.Time
	tty      string
	// The main agent's turn has ended (Stop) with subagents still running:
	// the pane is working, in the subagents phase, until they are through.
	delegated bool
	// turn-delegated was sent for this turn (once per turn).
	delegatedSent bool
	// Subagents given up on during this turn.
	turnLost int
	// A finished turn whose turn-done is held until the screen settles, in
	// case a task notification takes it up again or a queued prompt
	// withdraws it; and a turn-delegated held the same way.
	held    *heldTurn
	heldDel *heldDelegate
	// While a notice is held: since when (real clock) the screen has shown
	// no turn, and the timer that reads it again.
	holdQuiet time.Time
	holdTimer *time.Timer
	prompt    *promptState
	// The AskUserQuestion call the main agent is in (ask.go).
	ask *askCall
	// The last PermissionRequest hook: what the prompt on screen is for.
	permTool, permSummary string
	permAt                time.Time
	// The last hook that says the agent moved on (a turn started or ended,
	// a tool ran, a new permission was asked): a question ended before it
	// is not the one on screen now, however alike they look.
	movedOn time.Time
	// The last prompt that ended as answered-desk, in case it was only
	// redrawn.
	lastDesk *promptState

	osc oscScanner
	// What the pane's program last said about itself (OSC 7501), and the
	// command it said it as: while set, its word is the state, whatever
	// hooks or the screen show. A clear, or another command in the pane,
	// ends it.
	reported  *Report
	reportCmd string
}

// srcReport is the source of a state the program reported (OSC 7501).
const srcReport = "report"

// StreamEvent is one SSE event (section 3).
type StreamEvent struct {
	Name string
	Seq  int64
	Data any
}

type Watcher struct {
	opts Options

	// recording is held while settled gates are written down, so one is
	// not written twice (recordGates).
	recording sync.Mutex

	mu      sync.Mutex
	panes   map[string]*paneState
	prompts map[string]*promptState
	seq     int64
	subs    map[chan StreamEvent]struct{}
	// Asked for a poll: a hook or output from a pane not seen yet.
	pollNow chan struct{}
	// Wall-clock ms of the last tick, to tell sleep from silence.
	lastTick int64
	// Claude config directories whose teams stui reads.
	claudeDirs map[string]bool
	// Decision gates by id (gates.go), and the last one's number.
	gates   map[string]*Gate
	gateSeq int64
	// Background sessions' names, read from their state files (bgsession.go).
	bgNames map[string]bgEntry
	// Polls in a row each team config has been missing.
	teamMissing map[string]int

	// persist.go: a change waiting to be saved, saves one at a time, the
	// last one written; records read at start, until their panes are
	// accounted for.
	dirty          chan struct{}
	saveMu         sync.Mutex
	lastSaved      []byte
	restore        map[string]*savedPane
	restoreSavedAt int64
	restoreGap     int64
	// Subagent ids looked up in Claude's own records (recover.go).
	adoptTried map[string]bool
	// How often each listed subagent without a description was looked up
	// there (descFill).
	descTried map[string]int
	// Which pane last spoke for each conversation ("agent:session id"),
	// for the chat view (chat.go).
	chatOwner map[string]string
	// Claude session files read, by path (claudesession.go).
	claudeSessMu sync.Mutex
	claudeSess   map[string]claudeSessionEntry
	// Each tty's foreground processes, read a moment ago.
	fgProcs map[string]fgProcsEntry
	// psmux: each server's session, by server pid (psmux.go).
	psmuxServers map[string]string
}

func New(o Options) *Watcher {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Hooks == nil {
		o.Hooks = func() map[string]bool { return map[string]bool{} }
	}
	if o.ClaudeDir == "" {
		o.ClaudeDir = defaultClaudeDir()
	}
	if o.Args == nil {
		o.Args = cmdicon.ForegroundArgs
	}
	if o.ProcStart == nil {
		o.ProcStart = foregroundStart
	}
	if o.Procs == nil {
		o.Procs = foregroundProcs
	}
	w := &Watcher{
		claudeDirs: map[string]bool{o.ClaudeDir: true},
		bgNames:    map[string]bgEntry{},
		gates:      map[string]*Gate{},
		opts:       o,
		panes:      map[string]*paneState{},
		prompts:    map[string]*promptState{},
		subs:       map[chan StreamEvent]struct{}{},
		pollNow:    make(chan struct{}, 1),
		dirty:      make(chan struct{}, 1),
		adoptTried: map[string]bool{},
		descTried:  map[string]int{},
	}
	w.loadState()
	w.loadGates()
	return w
}

// sameFile compares socket paths the way the OS sees them: /tmp and
// /private/tmp are one directory on macOS.
func sameFile(a, b string) bool {
	if a == b {
		return true
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

func (w *Watcher) now() time.Time { return w.opts.Now() }
func (w *Watcher) ms() int64      { return w.now().UnixMilli() }

func newID() string {
	var b [12]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (w *Watcher) askPoll() {
	select {
	case w.pollNow <- struct{}{}:
	default:
	}
}

// ---- publishing

// view is the pane as the API shows it. Called with w.mu held.
func (p *paneState) view(now time.Time) Pane {
	v := p.Pane
	if v.Teammates == nil {
		v.Teammates = []Teammate{}
	} else {
		v.Teammates = append([]Teammate{}, v.Teammates...)
	}
	if !p.turnStart.IsZero() && (v.State == Working || v.State == Waiting) {
		v.Turn = &Turn{StartedAt: p.turnStart.UnixMilli(), DurationMs: now.Sub(p.turnStart).Milliseconds()}
	} else {
		v.Turn = nil
	}
	if p.prompt != nil && p.prompt.status != "ended" {
		pr := p.prompt.Prompt
		pr.Options = append([]Option{}, pr.Options...)
		v.Prompt = &pr
	} else if p.gate != nil && p.gate.Status == GateOpen {
		pr := p.gate.prompt(v.Agent)
		v.Prompt = &pr
		v.State = Waiting
	} else {
		v.Prompt = nil
	}
	v.Subagents.Items = p.subItemsView()
	if p.Team != nil {
		t := *p.Team
		v.Team = &t
	}
	if p.reported != nil {
		r := *p.reported
		v.Reported, v.Report = true, &r
	}
	v.Icon = cmdicon.Resolve(v.Agent, v.Command, nil).Icon
	v.Phase = PhaseMain
	if p.delegated && v.State == Working {
		v.Phase = PhaseSubagents
	}
	return v
}

// PaneAgents is the agent kind of every pane known to run one, by pane id,
// for the window list's icons.
func (w *Watcher) PaneAgents() map[string]string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := map[string]string{}
	for id, p := range w.panes {
		if p.Agent != "" {
			out[id] = p.Agent
		}
	}
	return out
}

// PaneState is one pane's agent state and when it came to it (ms): ""
// when no agent is known there. For a worker started from another machine
// (orch/remote.go).
func (w *Watcher) PaneState(id string) (string, int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if p := w.panes[id]; p != nil && p.Agent != "" {
		return p.State, p.Since
	}
	return "", 0
}

// PaneSource is what last set the state of pane id's agent ("interrupt":
// the person stopped its turn), "" for none.
func (w *Watcher) PaneSource(id string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if p := w.panes[id]; p != nil && p.Agent != "" {
		return p.Source
	}
	return ""
}

// PaneStates is the agent and its state in every pane known to run one, by
// pane id, for what closing a window would stop (GET /api/close).
func (w *Watcher) PaneStates() map[string]mux.AgentState {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := map[string]mux.AgentState{}
	for id, p := range w.panes {
		if p.Agent != "" {
			out[id] = mux.AgentState{Agent: p.Agent, State: p.State}
		}
	}
	return out
}

// AgentResume is a pane's agent and the id its conversation resumes by.
type AgentResume struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
}

// PaneResume is every pane with an agent, keyed by pane id, with the
// conversation id its hooks gave when there was one.
func (w *Watcher) PaneResume() map[string]AgentResume {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := map[string]AgentResume{}
	for id, p := range w.panes {
		if p.Agent != "" && p.Agent != "other" {
			out[id] = AgentResume{Kind: p.Agent, ID: p.resumeID}
		}
	}
	return out
}

func (w *Watcher) visible(p *paneState) bool {
	return p.Agent != "" || p.notified || p.reported != nil || (p.prompt != nil && p.prompt.status != "ended")
}

func (w *Watcher) listLocked() []Pane {
	now := w.now()
	out := []Pane{}
	for _, p := range w.panes {
		if w.visible(p) {
			out = append(out, p.view(now))
		}
	}
	sortPanes(out)
	return out
}

func sortPanes(ps []Pane) {
	for i := 1; i < len(ps); i++ {
		for j := i; j > 0 && lessPane(ps[j], ps[j-1]); j-- {
			ps[j], ps[j-1] = ps[j-1], ps[j]
		}
	}
}

func lessPane(a, b Pane) bool {
	if a.Session != b.Session {
		return a.Session < b.Session
	}
	if a.Window != b.Window {
		return a.Window < b.Window
	}
	return a.Pane < b.Pane
}

func (w *Watcher) broadcastLocked(name string, data any) {
	w.seq++
	ev := StreamEvent{Name: name, Seq: w.seq, Data: data}
	for ch := range w.subs {
		select {
		case ch <- ev:
		default:
			// A reader that fell this far behind gets a fresh snapshot
			// when it reconnects; it is dropped rather than waited on.
			close(ch)
			delete(w.subs, ch)
		}
	}
}

// publishLocked tells subscribers and the sink that p changed.
func (w *Watcher) publishLocked(p *paneState) {
	visible := w.visible(p)
	if visible {
		w.broadcastLocked("pane", p.view(w.now()))
	} else if p.listed {
		w.broadcastLocked("pane-removed", map[string]string{"id": p.ID, "session": p.Session})
	}
	p.listed = visible
	w.markDirtyLocked()
	sessions := Summarize(w.listLocked())
	w.broadcastLocked("sessions", sessions)
	if w.opts.Sink != nil {
		w.opts.Sink.StateChanged(sessions, p.view(w.now()))
	}
}

// setState moves p to state s. Called with w.mu held. While the program
// reports its own status, only its reports move it.
func (p *paneState) setState(s, source string, now time.Time) bool {
	if p.reported != nil && source != srcReport {
		return false
	}
	if p.State == s {
		p.Source = source
		return false
	}
	p.State, p.Since, p.Source = s, now.UnixMilli(), source
	return true
}

// Snapshot is GET /api/agents.
func (w *Watcher) Snapshot() Snapshot {
	hooks, states, sets := w.opts.Hooks(), w.hookStates(), w.hookSets()
	w.mu.Lock()
	defer w.mu.Unlock()
	panes := w.listLocked()
	return Snapshot{V: 1, Seq: w.seq, At: w.ms(), Hooks: hooks, HookState: states, HookSets: sets, Sessions: Summarize(panes), Panes: panes}
}

func (w *Watcher) hookSets() any {
	if w.opts.HookSets == nil {
		return nil
	}
	return w.opts.HookSets()
}

func (w *Watcher) hookStates() map[string]string {
	if w.opts.HookStates == nil {
		return nil
	}
	return w.opts.HookStates()
}

// Panes is every listed pane as GET /api/agents gives it.
func (w *Watcher) Panes() []Pane {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.listLocked()
}

// Sessions is each session's summary.
func (w *Watcher) Sessions() map[string]SessionSummary {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := map[string]SessionSummary{}
	for _, s := range Summarize(w.listLocked()) {
		out[s.Name] = s
	}
	return out
}

// Subscribe starts a stream: the snapshot first, then every change. cancel
// ends it; the channel is also closed if the reader falls behind.
func (w *Watcher) Subscribe() (Snapshot, <-chan StreamEvent, func()) {
	hooks, states, sets := w.opts.Hooks(), w.hookStates(), w.hookSets()
	ch := make(chan StreamEvent, 256)
	w.mu.Lock()
	panes := w.listLocked()
	snap := Snapshot{V: 1, Seq: w.seq, At: w.ms(), Hooks: hooks, HookState: states, HookSets: sets, Sessions: Summarize(panes), Panes: panes}
	w.subs[ch] = struct{}{}
	w.mu.Unlock()
	return snap, ch, func() {
		w.mu.Lock()
		if _, ok := w.subs[ch]; ok {
			delete(w.subs, ch)
			close(ch)
		}
		w.mu.Unlock()
	}
}

// ---- prompts

// endPromptLocked ends p's prompt with reason, once.
func (w *Watcher) endPromptLocked(p *paneState, reason string) {
	ps := p.prompt
	if ps == nil || ps.status == "ended" {
		return
	}
	ps.status, ps.reason, ps.endedAt, ps.goneAt = "ended", reason, w.now(), time.Time{}
	if reason == AnsweredDesk {
		p.lastDesk = ps
	}
	if w.opts.Sink != nil {
		w.opts.Sink.PromptCleared(p.view(w.now()), ps.ID, reason)
	}
}

// adopt takes a newer read of the same question on screen: answers check
// against it and pick with its keys (a menu's depend on the cursor).
func (ps *promptState) adopt(parsed *Parsed) {
	ps.parsed, ps.fingerprint, ps.goneAt = parsed, parsed.Fingerprint, time.Time{}
	if len(parsed.Options) == len(ps.Options) {
		for i := range ps.Options {
			ps.Options[i].Keys = parsed.Options[i].Keys
		}
	}
}

// reviveLocked gives p's last prompt back its id when the screen shows it
// again soon after it left, with no hook since saying the agent moved on: it
// was redrawn, not answered. It reports whether it did.
func (w *Watcher) reviveLocked(p *paneState, parsed *Parsed) bool {
	ps, now := p.lastDesk, w.now()
	if ps == nil || ps.status != "ended" || ps.reason != AnsweredDesk || now.Sub(ps.endedAt) > promptRevive ||
		!p.movedOn.Before(ps.endedAt) || !Same(ps.parsed, parsed) || w.prompts[ps.ID] != ps {
		return false
	}
	ps.status, ps.reason, ps.endedAt = "open", "", time.Time{}
	ps.adopt(parsed)
	p.prompt, p.lastDesk = ps, nil
	if p.turnStart.IsZero() {
		p.turnStart = now
	}
	p.setState(Waiting, "screen", now)
	if w.opts.Sink != nil {
		// The same id again: a listener does not alert twice for it.
		w.opts.Sink.PromptOpened(p.view(now), ps.Prompt)
	}
	return true
}

func (w *Watcher) openPromptLocked(p *paneState, parsed *Parsed) {
	now := w.now()
	ps := &promptState{
		Prompt: Prompt{
			ID: newID(), Kind: parsed.Kind, Title: parsed.Title, Question: parsed.Question,
			Detail: parsed.Detail, Options: parsed.Options, AllowText: parsed.AllowText,
			CreatedAt: now.UnixMilli(), Agent: p.Agent,
		},
		pane: p.ID, fingerprint: parsed.Fingerprint, parsed: parsed, status: "open",
	}
	if ps.Agent == "" {
		ps.Agent = "other"
	}
	// What the permission is for, from the hook, which has it whole.
	if p.permTool != "" && now.Sub(p.permAt) < time.Minute {
		ps.Tool = p.permTool
		if s := strings.TrimSpace(p.permSummary); s != "" && !strings.Contains(ps.Detail, s) {
			if ps.Detail == "" {
				ps.Detail = clipText(s, 4000)
			} else {
				ps.Detail = clipText(s+"\n"+ps.Detail, 4000)
			}
		}
	}
	if ps.Options == nil {
		ps.Options = []Option{}
	}
	if qs := askFor(p.ask, parsed, now); qs != nil {
		ps.Questions, ps.Tool = qs, "AskUserQuestion"
	}
	p.prompt = ps
	w.prompts[ps.ID] = ps
	if p.turnStart.IsZero() {
		p.turnStart = now
	}
	p.setState(Waiting, "screen", now)
	if w.opts.Sink != nil {
		w.opts.Sink.PromptOpened(p.view(now), ps.Prompt)
	}
}

// analyze takes a fresh capture of p's screen into account.
func (w *Watcher) analyze(id, screen string) {
	agent, title := "", ""
	w.mu.Lock()
	if p := w.panes[id]; p != nil {
		agent, title = p.Agent, p.Title
	}
	w.mu.Unlock()
	parsed := ParseScreen(screen, agent)
	seen := readScreen(screen, title, agent)

	w.mu.Lock()
	defer w.mu.Unlock()
	p := w.panes[id]
	if p == nil {
		return
	}
	now := w.now()
	p.lastCapture = now
	changed := false
	if ps := p.prompt; ps != nil && ps.status == "answering" {
		// The answer in progress decides; it is looking at the screen too.
		return
	}
	// Output in the last moment: the agent may be halfway through drawing.
	moving := now.Sub(p.lastOutput) < settle
	if parsed != nil {
		p.quietSince = time.Time{}
		if ps := p.prompt; ps != nil && ps.status == "open" && (ps.fingerprint == parsed.Fingerprint || Same(ps.parsed, parsed)) {
			// Still the same question, however it was redrawn.
			ps.adopt(parsed)
			p.unsettled = time.Time{}
			return
		}
		// Only prompts of an agent, or from a program that asked for
		// attention -- not every numbered menu anywhere.
		if p.Agent == "" && !p.notified {
			return
		}
		// A new question is read off a still screen, not one being drawn.
		if moving {
			if p.unsettled.IsZero() {
				p.unsettled = now
			}
			if now.Sub(p.unsettled) < unsettledMax {
				w.scheduleCaptureLocked(p, settle+50*time.Millisecond)
				return
			}
		}
		p.unsettled = time.Time{}
		w.endPromptLocked(p, AnsweredDesk)
		if !w.reviveLocked(p, parsed) {
			w.openPromptLocked(p, parsed)
		}
		w.publishLocked(p)
		return
	}
	if ps := p.prompt; ps != nil && ps.status == "open" {
		// Not on screen: gone once it has stayed away for promptGone; until
		// then the agent may only be redrawing (hooks that say it moved on
		// end it at once).
		if ps.goneAt.IsZero() {
			ps.goneAt = now
		}
		away := now.Sub(ps.goneAt)
		if left := promptGone - away; left > 0 {
			w.scheduleCaptureLocked(p, left+50*time.Millisecond)
			return
		}
		if moving && !seen.busy && away < promptGoneMax {
			w.scheduleCaptureLocked(p, settle+50*time.Millisecond)
			return
		}
		w.endPromptLocked(p, AnsweredDesk)
		changed = true
		if p.State == Waiting {
			p.setState(Working, "screen", now)
		}
	}
	if p.Agent != "" {
		changed = w.screenTurnLocked(p, seen, now) || changed
	}
	if changed {
		w.publishLocked(p)
	}
}

// screenTurnLocked is the screen's say on p's turn: a turn in progress on
// screen is one, whatever the hooks said last -- Claude starts turns
// without UserPromptSubmit (a Stop hook that blocks, a background task's
// result, a scheduled wakeup) -- and a screen that has shown none for a
// while has none. It reports whether p changed.
func (w *Watcher) screenTurnLocked(p *paneState, seen screenState, now time.Time) bool {
	if seen.busy {
		p.quietSince = time.Time{}
	}
	switch {
	case seen.busy && p.State == Working && p.delegated:
		// The main agent at work again while its subagents run (a
		// background agent's result starts a turn with no prompt) -- unless
		// this is the turn that just ended, still on screen.
		if since := now.Sub(p.stoppedAt); seen.stopping || since < stopGrace {
			wait := stopGrace - since
			if seen.stopping || wait < 100*time.Millisecond {
				wait = stopGrace
			}
			w.scheduleCaptureLocked(p, wait+50*time.Millisecond)
			return false
		}
		w.resumeTurnLocked(p, "screen")
		return true
	case seen.busy && p.State == Working:
		return false
	case seen.busy && p.State == Waiting:
		// A question answered at the desk.
		if p.prompt == nil || p.prompt.status == "ended" {
			return p.setState(Working, "screen", now)
		}
		return false
	case seen.busy:
		// done, error, idle, unknown: a turn stui was not told of, unless it
		// is the one that just ended, still on screen.
		if since := now.Sub(p.stoppedAt); seen.stopping || (!p.stoppedAt.IsZero() && since < stopGrace) {
			wait := stopGrace - since
			if seen.stopping || wait < 100*time.Millisecond {
				wait = stopGrace
			}
			w.scheduleCaptureLocked(p, wait+50*time.Millisecond)
			return false
		}
		if p.held != nil {
			// The main agent taking its subagents' results up.
			w.reopenTurnLocked(p, "screen", now)
		} else {
			w.startTurnLocked(p, "screen", now, false)
		}
		return true
	case p.State == Working && !p.agentFromHook:
		// The turn ended on screen; with hooks, Stop says so first.
		w.finishTurnLocked(p, Done, "screen")
		return true
	case p.State == Working:
		// With hooks: only a screen that plainly shows the agent idle, for
		// a while. A Claude screen without its input box is one stui cannot
		// read (a full-screen dialog), not an idle one.
		if p.Agent == "claude" && !seen.inputBox {
			p.quietSince = time.Time{}
			return false
		}
		// Kimi's screen is one stui has never been shown, so a quiet one says
		// nothing; its hooks end every turn (Stop, StopFailure, Interrupt).
		if p.Agent == "kimi" {
			p.quietSince = time.Time{}
			return false
		}
		// Work handed to subagents is still work. The main agent's own screen
		// goes quiet while they run -- it has nothing to print until they
		// answer -- and ending the turn on that quiet calls a pane idle with
		// two agents going inside it.
		if len(p.running) > 0 {
			p.quietSince = time.Time{}
			return false
		}
		if p.quietSince.IsZero() {
			p.quietSince = now
			w.scheduleCaptureLocked(p, screenQuiet+50*time.Millisecond)
			return false
		}
		if now.Sub(p.quietSince) < screenQuiet {
			w.scheduleCaptureLocked(p, screenQuiet-now.Sub(p.quietSince)+50*time.Millisecond)
			return false
		}
		w.finishTurnLocked(p, Done, "screen")
		return true
	}
	return false
}

// startTurnLocked starts a turn in p, found by source. A real prompt
// (prompted: UserPromptSubmit that is not a task notification) withdraws a
// held turn-done: the turn before it was followed at once by another the
// user queued or typed, and nobody is waiting to hear it ended. A turn the
// screen or a tool call found sends the held one now.
func (w *Watcher) startTurnLocked(p *paneState, source string, now time.Time, prompted bool) {
	w.endPromptLocked(p, AnsweredDesk)
	if prompted {
		w.dropHeldLocked(p)
	} else {
		w.releaseHeldLocked(p)
	}
	p.turnStart = now
	p.quietSince = time.Time{}
	p.Subagents.Done = 0
	p.clearEndedSubs()
	p.delegated, p.delegatedSent, p.turnLost = false, false, 0
	p.setState(Working, source, now)
}

// reopenTurnLocked takes the held turn up again: a task notification right
// after it ended is the same turn going on.
func (w *Watcher) reopenTurnLocked(p *paneState, source string, now time.Time) {
	h := p.held
	p.held, p.heldDel = nil, nil
	w.endPromptLocked(p, AnsweredDesk)
	p.turnStart = h.start
	p.quietSince = time.Time{}
	p.Subagents.Done = h.sub.Done
	p.turnLost = h.sub.Lost
	p.delegated, p.delegatedSent = false, h.delegatedSent
	p.setState(Working, source, now)
}

// releaseHeldLocked sends the held turn-delegated and turn-done now, if
// there are any.
func (w *Watcher) releaseHeldLocked(p *paneState) {
	h, d := p.held, p.heldDel
	p.held, p.heldDel = nil, nil
	if d != nil {
		p.delegatedSent = true
	}
	if w.opts.Sink == nil {
		return
	}
	if d != nil {
		w.opts.Sink.TurnDelegated(p.view(w.now()), d.running, d.turnMs)
	}
	// A turn that ended on a decision gate is not done: the gate's push
	// already asks for what it waits on.
	if h != nil && (p.gate == nil || p.gate.Status != GateOpen) {
		w.opts.Sink.TurnDone(p.view(w.now()), h.durMs, h.result, h.sub)
	}
}

// dropHeldLocked withdraws the held turn-done and turn-delegated: they are
// never sent.
func (w *Watcher) dropHeldLocked(p *paneState) {
	p.held, p.heldDel = nil, nil
}

// holdLocked starts reading p's screen until the held notices can go
// (holdTick).
func (w *Watcher) holdLocked(p *paneState) {
	p.holdQuiet = time.Time{}
	if p.holdTimer != nil {
		p.holdTimer.Stop()
	}
	id := p.ID
	p.holdTimer = time.AfterFunc(holdPoll, func() { w.holdTick(id) })
}

// holdTick reads p's screen while a notice is held, and sends what is held
// once the screen has shown no turn for doneSettle (and the turn's least
// hold is over), or once it has been held doneHoldMax.
func (w *Watcher) holdTick(id string) {
	w.mu.Lock()
	p := w.panes[id]
	if p == nil || (p.held == nil && p.heldDel == nil) {
		if p != nil {
			p.holdTimer = nil
		}
		w.mu.Unlock()
		return
	}
	title, agent := p.Title, p.Agent
	w.mu.Unlock()
	screen, ok := w.capture(id)
	seen := readScreen(screen, title, agent)

	w.mu.Lock()
	defer w.mu.Unlock()
	p = w.panes[id]
	if p == nil || (p.held == nil && p.heldDel == nil) {
		if p != nil {
			p.holdTimer = nil
		}
		return
	}
	now := time.Now()
	if ok && seen.busy {
		p.holdQuiet = time.Time{}
	} else if p.holdQuiet.IsZero() {
		p.holdQuiet = now
	}
	var at time.Time
	least := doneSettle
	if p.held != nil {
		at, least = p.held.at, max(p.held.min, doneSettle)
	}
	if d := p.heldDel; d != nil && (at.IsZero() || d.at.Before(at)) {
		at = d.at
	}
	held := now.Sub(at)
	if held >= doneHoldMax || (!p.holdQuiet.IsZero() && now.Sub(p.holdQuiet) >= doneSettle && held >= least) {
		p.holdTimer = nil
		w.releaseHeldLocked(p)
		return
	}
	p.holdTimer = time.AfterFunc(holdPoll, func() { w.holdTick(id) })
}

// resumeTurnLocked is the main agent back at work on a delegated turn: the
// same turn, in the main phase again. A turn-delegated still held is
// withdrawn: the main agent did not leave the work to its subagents after
// all (one comes again if it stops with them still running).
func (w *Watcher) resumeTurnLocked(p *paneState, source string) {
	p.heldDel = nil
	p.delegated = false
	p.quietSince = time.Time{}
	p.Source = source
}

// delegateTurnLocked is the main agent ending its turn while subagents it
// started still run: the turn goes on in them. The pane stays working, in
// the subagents phase; turn-delegated is sent once per turn, and turn-done
// waits for the last subagent (or the main agent's next Stop with none
// running).
func (w *Watcher) delegateTurnLocked(p *paneState, source string) {
	now := w.now()
	w.endPromptLocked(p, AnsweredDesk)
	p.stoppedAt = now
	p.quietSince = time.Time{}
	p.delegated = true
	p.setState(Working, source, now)
	if source == "replay" {
		// Long past: what it would have said is not news.
		p.delegatedSent = true
	}
	if p.delegatedSent || p.heldDel != nil {
		return
	}
	// Held until the screen settles, like turn-done: a queued prompt right
	// after this Stop withdraws it.
	p.heldDel = &heldDelegate{running: len(p.running), turnMs: now.Sub(p.turnStart).Milliseconds(), at: time.Now()}
	w.holdLocked(p)
}

// finishTurnLocked ends the turn in p with result (Done or Error). A turn
// already ended -- the screen saw it before Stop came -- is not reported
// twice. Its turn-done is held until the screen settles (holdTick). A turn
// ended because its last subagent went silent (source "timeout") sends
// nothing: stui only gave up listening, and the agent may well be at work.
func (w *Watcher) finishTurnLocked(p *paneState, result, source string) {
	now := w.now()
	// A turn-delegated not sent yet: the turn-done says it all, or, for a
	// timeout, nothing is said.
	p.heldDel = nil
	w.endPromptLocked(p, AnsweredDesk)
	p.stoppedAt = now
	p.quietSince = time.Time{}
	p.delegated = false
	if p.turnStart.IsZero() && (p.State == Done || p.State == Error) {
		if result == Error {
			p.setState(result, source, now)
		}
		return
	}
	var d int64
	start := p.turnStart
	if !p.turnStart.IsZero() {
		d = now.Sub(p.turnStart).Milliseconds()
		p.LastTurnMs = &d
	}
	p.turnStart = time.Time{}
	p.setState(result, source, now)
	sub := TurnSubagents{Done: p.Subagents.Done, Lost: p.turnLost}
	delegatedSent := p.delegatedSent
	p.delegatedSent, p.turnLost = false, 0
	if source == "timeout" || source == "interrupt" || source == "replay" {
		w.dropHeldLocked(p)
		return
	}
	// A turn-done still held from before goes now; this one takes its place.
	w.releaseHeldLocked(p)
	h := &heldTurn{start: start, durMs: d, result: result, sub: sub, delegatedSent: delegatedSent, at: time.Now()}
	if delegatedSent || sub.Done > 0 {
		// Its subagents' results may still come back as task notifications,
		// which take the turn up again.
		h.min = delegateSettle
	}
	p.held = h
	w.holdLocked(p)
}

// ---- hooks

// Hook takes an event stui hook forwarded (section 3, POST /api/hook).
func (w *Watcher) Hook(ev hookclient.Event) {
	if ev.Pane == "" {
		// A background session's: to the pane showing it, or nowhere.
		sid, _ := ev.Data["session_id"].(string)
		dir, _ := ev.Data["config_dir"].(string)
		if !ev.Background || ev.Kind != "claude" || w.opts.Psmux {
			return
		}
		if ev.Pane = w.bgPane(sid, dir); ev.Pane == "" {
			return
		}
		ev.Socket = ""
	}
	if w.opts.Socket != "" && ev.Socket != "" && !sameFile(ev.Socket, w.opts.Socket) {
		return
	}
	if w.opts.Psmux {
		if ev.Pane = w.psmuxPane(ev.Socket, ev.Pane); ev.Pane == "" {
			return
		}
	}
	if ev.Replay && !replayable(ev.Event) {
		return
	}
	str := func(k string) string {
		s, _ := ev.Data[k].(string)
		return s
	}
	// A subagent stui has no record of, heard from: one that started before
	// this stui did. Claude's own record of it says what it is (recover.go),
	// read before taking the lock.
	aid, sid := str("agent_id"), str("session_id")
	var found, fill *foundSubagent
	if ev.Kind == "claude" && aid != "" && sid != "" && ev.Event != "SubagentStart" {
		w.mu.Lock()
		need := ev.Event != "SubagentStop" && w.unknownSubLocked(ev.Pane, aid)
		// One stui lists without a description (its Agent call never reached
		// stui): Claude's record has it (descFill).
		blank := !need && w.blankDescLocked(ev.Pane, aid)
		var dirs []string
		if need {
			if len(w.adoptTried) >= 1024 {
				w.adoptTried = map[string]bool{}
			}
			w.adoptTried[aid] = true
		}
		if blank {
			if len(w.descTried) >= 1024 {
				w.descTried = map[string]int{}
			}
			w.descTried[aid]++
		}
		if need || blank {
			for d := range w.claudeDirs {
				dirs = append(dirs, d)
			}
		}
		w.mu.Unlock()
		if need {
			found = findSubagent(dirs, sid, aid)
		} else if blank {
			fill = findSubagent(dirs, sid, aid)
		}
	}
	w.mu.Lock()
	p := w.panes[ev.Pane]
	if p == nil {
		// A pane the last poll did not see yet: kept, and filled in by the
		// next one, which is asked for now.
		p = &paneState{Pane: Pane{ID: ev.Pane, State: Unknown}}
		w.panes[ev.Pane] = p
		p.gate = w.openGateLocked(ev.Pane)
		w.askPoll()
	}
	// A spooled event ends or settles what happened while no agent was
	// listening; it never tells anybody (spool.go).
	src := "hook"
	if ev.Replay {
		src = "replay"
		started := !p.turnStart.IsZero() && p.turnStart.UnixMilli() > ev.At
		if ev.At < p.liveHookAt || started {
			w.mu.Unlock()
			return
		}
	} else {
		p.liveHookAt = max(p.liveHookAt, ev.At)
	}
	// The record from before stui started, settled by the conversation the
	// hook names: the same one keeps it, another drops it.
	if s := w.restore[ev.Pane]; s != nil && ev.Kind == "claude" && sid != "" && s.ResumeID != "" {
		delete(w.restore, ev.Pane)
		if s.ResumeID == sid {
			w.applyRestoreLocked(p, s)
		}
	}
	now := w.now()
	agent := ev.Kind
	switch agent {
	case "claude", "codex", "gemini", "opencode", "kimi", "qwen", "pi":
	default:
		agent = "other"
	}
	p.Agent, p.agentFromHook = agent, true
	if d := str("config_dir"); d != "" && len(w.claudeDirs) < 8 && filepath.IsAbs(d) {
		w.claudeDirs[filepath.Clean(d)] = true
	}
	prevResume := p.resumeID
	switch {
	case ev.Kind == "codex" && str("thread-id") != "":
		p.resumeID = str("thread-id")
	case str("session_id") != "" && str("agent_id") == "":
		// A subagent's hooks carry its parent's session; still, only the
		// main agent's own events name the conversation to resume.
		p.resumeID = str("session_id")
	}
	// Played back from the spool, an event is old news: the chat view goes
	// on with what the live hooks said (its paths are not spooled anyway).
	if str("agent_id") == "" && !ev.Replay {
		w.noteTranscriptLocked(p, ev, prevResume, now)
	}
	capture := time.Duration(-1)
	if str("agent_id") == "" {
		switch ev.Event {
		case "UserPromptSubmit", "PreToolUse", "PostToolUse", "PostToolUseFailure", "PermissionRequest",
			"Stop", "StopFailure", "SessionStart", "SessionEnd", "BeforeAgent", "AfterAgent", "BeforeTool", "AfterTool",
			"agent-turn-complete", "Interrupt":
			p.movedOn = now
		}
	}
	// The main agent starting a subagent: its type and description.
	if tool := str("tool_name"); ev.Kind == "claude" && str("agent_id") == "" && (tool == "Agent" || tool == "Task") {
		switch ev.Event {
		case "PreToolUse", "PostToolUse", "PostToolUseFailure":
			p.noteSpawnLocked(ev.Event, ev.Data, now)
		}
	}
	if ev.Kind == "claude" && str("agent_id") == "" && str("tool_name") == "AskUserQuestion" {
		p.noteAskLocked(ev.Event, ev.Data, now)
	}
	if found != nil && p.running[aid] == nil && !p.lost[aid] && p.subItems[aid] == nil {
		w.adoptSubagentLocked(p, aid, str("agent_type"), found, now)
	}
	if fill != nil {
		descFill(p, aid, fill)
	}
	// A subagent's own hooks say it is alive.
	if id := str("agent_id"); id != "" && (ev.Kind == "claude" || ev.Kind == "codex" || ev.Kind == "qwen") && ev.Event != "SubagentStart" && ev.Event != "SubagentStop" {
		w.subagentSeenLocked(p, id, ev.Event)
	}

	key := ev.Kind + ":" + ev.Event
	// Codex's hooks are Claude's in name, payload and meaning; the ones it
	// shares go the same way.
	if ev.Kind == "codex" {
		switch ev.Event {
		case "SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PermissionRequest",
			"Stop", "SubagentStart", "SubagentStop", "SessionEnd":
			key = "claude:" + ev.Event
		}
	}
	// opencode's plugin (hooks/opencode.js) and Pi's extension (hooks/pi.js)
	// translate their events into Claude's names before they send them, so
	// they mean what Claude's do.
	if ev.Kind == "opencode" || ev.Kind == "pi" {
		switch ev.Event {
		case "SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PermissionRequest",
			"Stop", "StopFailure", "SessionEnd":
			key = "claude:" + ev.Event
		}
	}
	// Kimi Code's and Qwen Code's hooks are Claude's in name, payload and
	// meaning (hooks/kimi.go, hooks/qwen.go). Kimi's Notification is not
	// (a background task's status), and stui does not install it.
	if ev.Kind == "kimi" || ev.Kind == "qwen" {
		switch ev.Event {
		case "SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PostToolUseFailure",
			"PermissionRequest", "Stop", "StopFailure", "SubagentStart", "SubagentStop", "SessionEnd":
			key = "claude:" + ev.Event
		case "Notification":
			if ev.Kind == "qwen" {
				key = "claude:Notification"
			}
		case "TurnStarted":
			// Kimi starts turns without a prompt too (a task, a trigger), and
			// fires this after UserPromptSubmit: a turn begins if none runs,
			// as a tool hook begins one.
			key = "claude:PreToolUse"
		}
	}
	switch key {
	case "kimi:PermissionResult", "pi:PermissionResult":
		// The person answered: the tool runs, or the model hears it was
		// refused. Either way the turn goes on. (Pi's is any dialog an
		// extension opened in the turn closing.)
		if p.State == Waiting {
			p.setState(Working, src, now)
		}
	case "codex:Interrupt", "opencode:Interrupt", "kimi:Interrupt", "pi:Interrupt":
		// Esc in Codex, Kimi and Pi (twice in opencode): the turn is over because
		// the person stopped it, at the machine. Nobody is told it finished.
		p.stopAt = max(p.stopAt, ev.At)
		w.finishTurnLocked(p, Idle, "interrupt")
	case "claude:SessionStart":
		if p.State == Unknown || p.State == "" {
			p.setState(Idle, src, now)
		}
	case "claude:UserPromptSubmit":
		// A background agent's result is submitted as a prompt. While a turn
		// is running or handed off, or just ended with its turn-done held,
		// it is that turn going on; otherwise it starts one.
		task, _ := ev.Data["task_notification"].(bool)
		if !task && !ev.Replay && str("agent_id") == "" {
			p.submitAt = time.Now()
			p.Submitted = p.submitAt.UnixMilli()
		}
		switch {
		case task && (p.State == Working || p.State == Waiting):
			if p.delegated && p.State == Working {
				w.resumeTurnLocked(p, src)
			}
		case task && p.held != nil:
			w.reopenTurnLocked(p, src, now)
		default:
			w.startTurnLocked(p, src, now, !task)
		}
	case "gemini:BeforeAgent":
		w.startTurnLocked(p, src, now, true)
	case "claude:PreToolUse", "claude:PostToolUse", "claude:PostToolUseFailure",
		"gemini:BeforeTool", "gemini:AfterTool":
		// The main agent using a tool is running a turn, whether or not a
		// prompt started it. A subagent's (agent_id set) says nothing about
		// the main agent, nor does one from before the last Stop.
		if str("agent_id") == "" && (ev.At == 0 || ev.At > p.stopAt) {
			if p.held != nil && p.State != Working && p.State != Waiting {
				w.reopenTurnLocked(p, src, now)
			} else if p.State != Working && p.State != Waiting {
				w.startTurnLocked(p, src, now, false)
			} else if p.delegated && p.State == Working {
				w.resumeTurnLocked(p, src)
			}
		}
	case "claude:PermissionRequest":
		p.permTool, p.permSummary, p.permAt = str("tool_name"), str("tool_summary"), now
		p.setState(Waiting, src, now)
		capture = 150 * time.Millisecond
	case "claude:Notification", "gemini:Notification":
		switch str("notification_type") {
		case "idle_prompt":
			// Claude has sat at its prompt for a minute: whatever turn stui
			// thought was running is over.
			// Not while subagents carry the turn: the main agent sits at its
			// prompt waiting for them.
			if p.State == Working && !p.delegated {
				w.finishTurnLocked(p, Done, src)
			}
			if p.State == Done || p.State == Error {
				p.setState(Idle, src, now)
			}
		case "auth_success":
		default:
			// permission_prompt, elicitation_dialog, agent_needs_input,
			// and whatever comes next: someone is wanted.
			p.setState(Waiting, src, now)
			capture = 150 * time.Millisecond
		}
	case "claude:Stop":
		p.stopAt = max(p.stopAt, ev.At)
		if len(p.running) > 0 && !p.turnStart.IsZero() {
			w.delegateTurnLocked(p, src)
		} else {
			w.finishTurnLocked(p, Done, src)
		}
	case "gemini:AfterAgent", "codex:agent-turn-complete":
		p.stopAt = max(p.stopAt, ev.At)
		w.finishTurnLocked(p, Done, src)
	case "claude:StopFailure":
		p.stopAt = max(p.stopAt, ev.At)
		w.finishTurnLocked(p, Error, src)
	case "claude:SubagentStart":
		if id := str("agent_id"); id != "" {
			if p.running == nil {
				p.running = map[string]*subRun{}
			}
			p.running[id] = &subRun{seen: now.UnixMilli()}
			delete(p.lost, id)
			p.subStartLocked(id, str("agent_type"), now)
		}
	case "claude:SubagentStop":
		// Only agents it saw start: Claude also reports helpers it never
		// announced (the title writer).
		id := str("agent_id")
		if p.running[id] != nil {
			delete(p.running, id)
			p.subEndLocked(id, SubDone, now)
			p.Subagents.Done++
			if len(p.running) == 0 && p.delegated {
				// The last one: the turn is over (held a moment: the main
				// agent takes the result up at once, as a task notification).
				p.Subagents.Running = 0
				w.finishTurnLocked(p, Done, src)
			}
		} else if p.lost[id] {
			// One stui had given up on, finishing after all.
			delete(p.lost, id)
			p.subEndLocked(id, SubDone, now)
			p.Subagents.Lost = max(p.Subagents.Lost-1, 0)
			p.Subagents.Done++
		}
	case "claude:SessionEnd":
		p.Subagents.Lost += len(p.running)
		for id := range p.running {
			p.subEndLocked(id, SubLost, now)
		}
		p.spawns = nil
		p.running, p.lost = nil, nil
		p.delegated, p.delegatedSent, p.turnLost = false, false, 0
		p.heldDel = nil
		w.releaseHeldLocked(p)
		w.endPromptLocked(p, Expired)
		p.agentFromHook = false
		p.Agent = p.cmdAgent
		p.turnStart = time.Time{}
		p.setState(Unknown, src, now)
	case "claude:TeammateIdle":
		p.setTeammate(str("teammate_name"), Idle)
	case "claude:TaskCreated":
		p.setTeammate(str("teammate_name"), Working)
	}
	p.Subagents.Running = len(p.running)
	w.publishLocked(p)
	if capture >= 0 {
		w.scheduleCaptureLocked(p, capture)
		// The prompt can take a moment to be drawn.
		id := p.ID
		time.AfterFunc(capture+700*time.Millisecond, func() { w.captureNow(id) })
	}
	w.mu.Unlock()
}

// subagentSeenLocked notes a hook from subagent id: alive, and whether it is
// now inside a tool call or waiting on a permission. A subagent stui had given
// up on is running again.
func (w *Watcher) subagentSeenLocked(p *paneState, id, event string) {
	s := p.running[id]
	if s == nil {
		if !p.lost[id] {
			return
		}
		delete(p.lost, id)
		p.Subagents.Lost = max(p.Subagents.Lost-1, 0)
		if p.turnLost > 0 {
			p.turnLost--
		}
		s = &subRun{heard: true}
		p.subReviveLocked(id)
		if p.running == nil {
			p.running = map[string]*subRun{}
		}
		p.running[id] = s
	}
	s.seen = w.ms()
	s.heard = true
	switch event {
	case "PreToolUse", "PermissionRequest":
		s.inTool = true
	case "PostToolUse", "PostToolUseFailure":
		s.inTool = false
	}
}

// ageSubagentsLocked gives up on p's subagents that have been silent too
// long (section 1, lost subagents); the last one going ends a delegated
// turn. It reports whether p changed.
func (w *Watcher) ageSubagentsLocked(p *paneState, now int64) bool {
	if len(p.running) == 0 {
		return false
	}
	if p.State == Waiting {
		// Someone is wanted: time spent waiting on them is not silence.
		for _, s := range p.running {
			s.seen = max(s.seen, now)
		}
		return false
	}
	changed := false
	for id, s := range p.running {
		if !s.heard {
			// Never heard from past its start: its hooks may not reach stui.
			// It ends by SubagentStop or SessionEnd.
			continue
		}
		limit := subagentQuiet
		if s.inTool {
			limit = subagentToolQuiet
		}
		if now-s.seen < limit.Milliseconds() {
			continue
		}
		delete(p.running, id)
		p.subEndLocked(id, SubLost, time.UnixMilli(now))
		if p.lost == nil {
			p.lost = map[string]bool{}
		}
		if len(p.lost) < 256 {
			p.lost[id] = true
		}
		p.Subagents.Lost++
		p.turnLost++
		changed = true
	}
	if !changed {
		return false
	}
	p.Subagents.Running = len(p.running)
	if len(p.running) == 0 && p.delegated {
		w.finishTurnLocked(p, Done, "timeout")
	}
	return true
}

func (p *paneState) setTeammate(name, state string) {
	if name == "" {
		return
	}
	for i := range p.Teammates {
		if p.Teammates[i].Name == name {
			p.Teammates[i].State = state
			return
		}
	}
	p.Teammates = append(p.Teammates, Teammate{Name: name, State: state})
}

// ---- output and captures

// Output takes what a pane printed, as the control client saw it.
func (w *Watcher) Output(id string, data []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	p := w.panes[id]
	if p == nil {
		w.askPoll()
		return
	}
	now := w.now()
	p.lastOutput = now
	bell, notes, reports := p.osc.scan(data)
	if w.reportLocked(p, reports, now) {
		w.publishLocked(p)
	}
	attention := bell || len(notes) > 0
	if attention && !p.notified && p.Agent == "" {
		p.notified = true
	}
	if p.Agent == "" && !p.notified {
		return
	}
	if attention {
		w.scheduleCaptureLocked(p, 100*time.Millisecond)
		return
	}
	if p.pendingSince.IsZero() {
		p.pendingSince = now
	}
	wait := settle
	if left := settleMax - now.Sub(p.pendingSince); left < wait {
		wait = max(left, 0)
	}
	w.scheduleCaptureLocked(p, wait)
}

func (w *Watcher) scheduleCaptureLocked(p *paneState, after time.Duration) {
	if p.timer != nil {
		p.timer.Stop()
	}
	id := p.ID
	p.timer = time.AfterFunc(after, func() {
		w.mu.Lock()
		if q := w.panes[id]; q != nil {
			q.pendingSince = time.Time{}
			q.timer = nil
		}
		w.mu.Unlock()
		w.captureNow(id)
	})
}

// reportLocked applies the program's own status reports (OSC 7501) to p:
// the root record's word becomes the state; a clear hands the state back
// to hooks and the screen, read again now. Records with an id are parts of
// the program's work and do not speak for the pane. It says whether p
// changed.
func (w *Watcher) reportLocked(p *paneState, reports []statusReport, now time.Time) bool {
	changed := false
	for _, r := range reports {
		if r.id != "" {
			continue
		}
		if r.State == "clear" {
			if p.reported != nil {
				p.reported, changed = nil, true
				w.scheduleCaptureLocked(p, 0)
			}
			continue
		}
		rep := r.Report
		rep.At = now.UnixMilli()
		if p.reported == nil || !sameReport(*p.reported, rep) {
			changed = true
		}
		p.reported, p.reportCmd = &rep, p.Command
		if p.setState(stateOf(rep.State), srcReport, now) {
			changed = true
		}
	}
	return changed
}

func sameReport(a, b Report) bool {
	return a.State == b.State && a.Kind == b.Kind && a.App == b.App && a.Title == b.Title && a.Msg == b.Msg &&
		(a.Progress == nil) == (b.Progress == nil) && (a.Progress == nil || *a.Progress == *b.Progress)
}

func (w *Watcher) capture(id string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := w.opts.Tmux.Run(ctx, "capture-pane", "-p", "-t", id)
	return out, err == nil
}

func (w *Watcher) captureNow(id string) {
	if screen, ok := w.capture(id); ok {
		w.analyze(id, screen)
	}
}

// ---- answering

// Answer carries out POST /api/prompts/<id>/answer: the status and body.
func (w *Watcher) Answer(id string, option *int, text *string) (int, map[string]any) {
	return w.carryOut(id, answerKey(option, text), func(pr Prompt) ([][]string, bool) {
		return answerKeys(pr, option, text)
	}, 150*time.Millisecond)
}

// carryOut carries out an answer to prompt id: keys builds the send-keys
// groups from the prompt, key names the answer for a retry, and gap is the
// time between groups.
func (w *Watcher) carryOut(id, key string, keys func(Prompt) ([][]string, bool), gap time.Duration) (int, map[string]any) {
	w.mu.Lock()
	ps := w.prompts[id]
	w.mu.Unlock()
	if ps == nil {
		return 404, map[string]any{"error": "no such prompt"}
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()

	w.mu.Lock()
	status, reason, done := ps.status, ps.reason, ps.answer
	w.mu.Unlock()
	if status == "ended" {
		if reason == AnsweredPhone && done == key {
			return 200, map[string]any{"ok": true, "id": id, "already": true}
		}
		return 409, map[string]any{"error": "answered", "reason": reason}
	}

	if _, ok := keys(ps.Prompt); !ok {
		return 400, map[string]any{"error": "bad answer"}
	}

	// Still on screen? Anything else there and no key is sent. A screen
	// showing nothing may be the agent redrawing: read again for a moment.
	var now *Parsed
	for try := 0; ; try++ {
		screen, _ := w.capture(ps.pane)
		if now = ParseScreen(screen, ps.Agent); now != nil || try >= 3 {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	w.mu.Lock()
	p := w.panes[ps.pane]
	same := now != nil && (now.Fingerprint == ps.fingerprint || Same(ps.parsed, now))
	if same {
		ps.adopt(now)
	}
	if !same || p == nil || ps.status != "open" {
		if p != nil {
			w.endPromptLocked(p, AnsweredDesk)
			if p.State == Waiting && now == nil {
				p.setState(Working, "screen", w.now())
			}
			w.publishLocked(p)
		}
		w.mu.Unlock()
		return 409, map[string]any{"error": "stale"}
	}
	ps.status = "answering"
	steps, _ := keys(ps.Prompt)
	w.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second+time.Duration(len(steps))*gap)
	defer cancel()
	for i, step := range steps {
		args := append([]string{"send-keys", "-t", ps.pane}, step...)
		w.opts.Tmux.Run(ctx, args...)
		// A choice that takes text opens a field first; a question's next
		// step has to be drawn before its keys go.
		if i < len(steps)-1 {
			time.Sleep(gap)
		}
	}

	confirmed := false
	deadline := time.Now().Add(answerWait)
	for time.Now().Before(deadline) {
		time.Sleep(150 * time.Millisecond)
		screen, ok := w.capture(ps.pane)
		if !ok {
			break
		}
		if q := ParseScreen(screen, ps.Agent); q == nil || (q.Fingerprint != ps.fingerprint && !Same(ps.parsed, q)) {
			confirmed = true
			break
		}
	}

	w.mu.Lock()
	ps.status = "open" // so endPromptLocked ends it
	ps.answer = key
	if p := w.panes[ps.pane]; p != nil && p.prompt == ps {
		w.endPromptLocked(p, AnsweredPhone)
		if p.State == Waiting {
			p.setState(Working, "screen", w.now())
		}
		w.publishLocked(p)
	} else {
		ps.status, ps.reason = "ended", AnsweredPhone
	}
	w.mu.Unlock()
	// Whatever the screen shows now decides the state from here.
	go w.captureNow(ps.pane)
	return 200, map[string]any{"ok": true, "id": id, "confirmed": confirmed}
}

func answerKey(option *int, text *string) string {
	k := "-"
	if option != nil {
		k = itoa(*option)
	}
	if text != nil {
		k += "\x00" + *text
	}
	return k
}

func itoa(n int) string {
	if n < 0 {
		return "-" + itoa(-n)
	}
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

// cleanText keeps newlines and printable text.
func cleanText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '\n' || r >= 0x20 && r != 0x7f && !(r >= 0x80 && r < 0xa0) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// answerKeys is the send-keys argument lists for an answer, in order.
func answerKeys(pr Prompt, option *int, text *string) ([][]string, bool) {
	var steps [][]string
	var opt *Option
	if option != nil {
		if *option < 0 || *option >= len(pr.Options) {
			return nil, false
		}
		opt = &pr.Options[*option]
	}
	if text != nil {
		t := cleanText(*text)
		if len([]rune(t)) > 4000 || strings.TrimSpace(t) == "" {
			return nil, false
		}
		if opt == nil {
			if !pr.AllowText {
				return nil, false
			}
			for i := range pr.Options {
				if pr.Options[i].Input {
					opt = &pr.Options[i]
					break
				}
			}
			if opt == nil && pr.Kind != "text" {
				return nil, false
			}
		} else if !opt.Input {
			return nil, false
		}
		if opt != nil {
			steps = append(steps, opt.Keys)
		}
		// Claude takes Alt+Enter as a newline; elsewhere a newline would
		// submit, so it becomes a space.
		lines := strings.Split(t, "\n")
		for i, l := range lines {
			if i > 0 {
				if pr.Agent == "claude" {
					steps = append(steps, []string{"M-Enter"})
				} else {
					steps = append(steps, []string{"-l", " "})
				}
			}
			if l != "" {
				steps = append(steps, []string{"-l", l})
			}
		}
		steps = append(steps, []string{"Enter"})
		return steps, true
	}
	if opt == nil {
		return nil, false
	}
	return [][]string{opt.Keys}, true
}

// ---- housekeeping

// tick ages states: done becomes idle after a while, and prompts expire.
func (w *Watcher) tick() {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	nowMs := now.UnixMilli()
	// Time asleep is not silence: a gap between ticks moves every running
	// subagent's clock forward by it.
	if gap := nowMs - w.lastTick; w.lastTick != 0 && gap > sleepGap.Milliseconds() {
		for _, p := range w.panes {
			for _, s := range p.running {
				s.seen += gap
			}
		}
	}
	w.lastTick = nowMs
	for _, p := range w.panes {
		if w.ageSubagentsLocked(p, nowMs) {
			w.publishLocked(p)
		}
		if (p.State == Done || p.State == Error) && now.Sub(time.UnixMilli(p.Since)) > doneToIdle {
			p.setState(Idle, p.Source, now)
			w.publishLocked(p)
		}
		if ps := p.prompt; ps != nil && ps.status == "open" && now.Sub(time.UnixMilli(ps.CreatedAt)) > promptMaxAge {
			w.endPromptLocked(p, Expired)
			w.publishLocked(p)
		}
	}
	// Ended prompts are remembered for an hour, for late answers to get a
	// 409 rather than a 404.
	for id, ps := range w.prompts {
		if ps.status == "ended" && now.Sub(time.UnixMilli(ps.CreatedAt)) > time.Hour+promptMaxAge {
			delete(w.prompts, id)
		}
	}
}
