package watch

// What the watch knows of each pane's subagents outlives stui (section 1,
// "Across a restart"). An auto-upgrade, stui restart or stui upgrade starts a
// new stui while the agents in the panes go on; without this, every subagent
// started before it was gone from the lists, and a pane with five running
// showed only the ones started since.
//
// The file is small (a pane's items are capped at maxSubItems), written
// whole to a temporary file and renamed over the old one, mode 0600, at
// most once per saveEvery and only when something in it changed, and once
// more on the way out. It holds what the API already serves (type, the
// short description, states, times) plus the conversation id the hooks
// gave (PaneResume serves that too), never a prompt.
//
// On start it is read once and held aside until each pane is accounted
// for: the first poll keeps a pane's record when the pane is still there,
// is not back at a shell, and its foreground agent started before the file
// was written (pane ids and ttys are reused by a new tmux server, a process
// start time is not); a main-agent hook naming the same conversation keeps
// it sooner, one naming another drops it. A record of a pane the poll does
// not list is dropped. Time stui was down is not silence for the lost-
// subagent timeout, as time asleep is not; a file older than
// subagentToolQuiet is not read at all.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// stateVersion is the file's schema. Any other is ignored, not migrated:
// the worst that costs is one restart's worth of subagents.
const stateVersion = 1

// saveEvery is how long a change waits for the next before the file is
// written.
var saveEvery = 2 * time.Second

// restoreMaxAge: a file older than this is from a share long stopped.
var restoreMaxAge = 30 * time.Minute

// StatePath is where the watch of the tmux server at socket keeps its
// state, under dir (~/.stui): one file per server, since two shares can
// watch two.
func StatePath(dir, socket string) string {
	if socket == "" {
		return ""
	}
	h := sha256.Sum256([]byte(socket))
	return filepath.Join(dir, "watch", "panes-"+hex.EncodeToString(h[:6])+".json")
}

type stateFile struct {
	V       int         `json:"v"`
	Socket  string      `json:"socket"`
	SavedAt int64       `json:"savedAt"`
	Panes   []savedPane `json:"panes"`
}

type savedRun struct {
	Seen   int64 `json:"seen"`
	InTool bool  `json:"inTool,omitempty"`
	// One of its own hooks reached stui: it can be given up on for silence.
	Heard bool `json:"heard,omitempty"`
}

type savedSpawn struct {
	ToolUseID string `json:"toolUseId,omitempty"`
	Type      string `json:"type,omitempty"`
	Desc      string `json:"desc,omitempty"`
	Ref       string `json:"ref,omitempty"`
	At        int64  `json:"at"`
}

type savedPane struct {
	ID       string `json:"id"`
	TTY      string `json:"tty,omitempty"`
	Command  string `json:"command,omitempty"`
	Agent    string `json:"agent"`
	FromHook bool   `json:"fromHook,omitempty"`
	ResumeID string `json:"resumeId,omitempty"`

	State      string `json:"state"`
	Since      int64  `json:"since"`
	Source     string `json:"source,omitempty"`
	TurnStart  int64  `json:"turnStart,omitempty"`
	LastTurnMs *int64 `json:"lastTurnMs,omitempty"`
	Delegated  bool   `json:"delegated,omitempty"`
	// turn-delegated already went for this turn.
	DelegatedSent bool `json:"delegatedSent,omitempty"`
	TurnLost      int  `json:"turnLost,omitempty"`

	Done      int                 `json:"done"`
	Lost      int                 `json:"lost"`
	Items     []SubagentItem      `json:"items,omitempty"`
	Running   map[string]savedRun `json:"running,omitempty"`
	LostIDs   []string            `json:"lostIds,omitempty"`
	Spawns    []savedSpawn        `json:"spawns,omitempty"`
	Teammates []Teammate          `json:"teammates,omitempty"`
}

// ---- saving

// markDirtyLocked asks for a save soon.
func (w *Watcher) markDirtyLocked() {
	if w.opts.StatePath == "" {
		return
	}
	select {
	case w.dirty <- struct{}{}:
	default:
	}
}

// saveLoop writes the file after changes, until ctx ends, and once more
// then.
func (w *Watcher) saveLoop(done <-chan struct{}) {
	if w.opts.StatePath == "" {
		return
	}
	for {
		select {
		case <-done:
			w.Flush()
			return
		case <-w.dirty:
		}
		select {
		case <-done:
			w.Flush()
			return
		case <-time.After(saveEvery):
		}
		w.Flush()
	}
}

// Flush writes the state file now, if it changed. Safe to call from
// anywhere, any number of times; stui's ways out call it before they exit.
func (w *Watcher) Flush() {
	path := w.opts.StatePath
	if path == "" {
		return
	}
	w.saveMu.Lock()
	defer w.saveMu.Unlock()
	w.mu.Lock()
	panes := w.savedPanesLocked()
	at := w.ms()
	w.mu.Unlock()
	body, err := json.Marshal(panes)
	if err != nil || bytes.Equal(body, w.lastSaved) {
		return
	}
	b, err := json.Marshal(stateFile{V: stateVersion, Socket: w.opts.Socket, SavedAt: at, Panes: panes})
	if err != nil {
		return
	}
	if writeAtomic(path, b) == nil {
		w.lastSaved = body
	}
}

func writeAtomic(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".panes-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// savedPanesLocked is every agent pane worth a record, in pane order. A
// record still waiting for its pane to be accounted for is kept as it was.
func (w *Watcher) savedPanesLocked() []savedPane {
	out := []savedPane{}
	for _, p := range w.panes {
		if p.Agent == "" || len(out) >= 256 {
			continue
		}
		s := savedPane{
			ID: p.ID, TTY: p.tty, Command: p.Command, Agent: p.Agent, FromHook: p.agentFromHook, ResumeID: p.resumeID,
			State: p.State, Since: p.Since, Source: p.Source, LastTurnMs: p.LastTurnMs,
			Delegated: p.delegated, DelegatedSent: p.delegatedSent, TurnLost: p.turnLost,
			Done: p.Subagents.Done, Lost: p.Subagents.Lost,
			Teammates: append([]Teammate(nil), p.Teammates...),
		}
		if !p.turnStart.IsZero() {
			s.TurnStart = p.turnStart.UnixMilli()
		}
		s.Items = p.subItemsView()
		if len(s.Items) == 0 {
			s.Items = nil
		}
		if len(p.running) > 0 {
			s.Running = map[string]savedRun{}
			for id, r := range p.running {
				s.Running[id] = savedRun{Seen: r.seen, InTool: r.inTool, Heard: r.heard}
			}
		}
		for id := range p.lost {
			s.LostIDs = append(s.LostIDs, id)
		}
		sort.Strings(s.LostIDs)
		for _, sp := range p.spawns {
			s.Spawns = append(s.Spawns, savedSpawn{ToolUseID: sp.toolUseID, Type: sp.typ, Desc: sp.desc, Ref: sp.ref, At: sp.at.UnixMilli()})
		}
		out = append(out, s)
	}
	for id, s := range w.restore {
		if w.panes[id] == nil && len(out) < 256 {
			out = append(out, *s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ---- loading

// loadState reads the file into w.restore. Anything off -- missing,
// unreadable, another schema, another server, too old -- is no records.
func (w *Watcher) loadState() {
	path := w.opts.StatePath
	if path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) > 4<<20 {
		return
	}
	var f stateFile
	if json.Unmarshal(b, &f) != nil || f.V != stateVersion || f.Socket != w.opts.Socket {
		return
	}
	now := w.ms()
	if f.SavedAt <= 0 || f.SavedAt > now+time.Minute.Milliseconds() || now-f.SavedAt > restoreMaxAge.Milliseconds() {
		return
	}
	w.restore = map[string]*savedPane{}
	for i := range f.Panes {
		s := f.Panes[i]
		if s.ID == "" || len(w.restore) >= 256 {
			continue
		}
		w.restore[s.ID] = &s
	}
	w.restoreSavedAt = f.SavedAt
	// Down time is not silence.
	w.restoreGap = max(now-f.SavedAt, 0)
}

// keepRestore decides, at the first poll, whether the pane in row is the
// one the record s was written for.
func (w *Watcher) keepRestore(s *savedPane, r paneRow, start time.Time, startKnown bool) bool {
	if isShell(r.command) {
		return false
	}
	if s.TTY != "" && r.tty != "" && s.TTY != r.tty {
		return false
	}
	if startKnown && start.UnixMilli() > w.restoreSavedAt+2000 {
		// A process that started after the file was written: not the
		// agent whose subagents these are.
		return false
	}
	return true
}

// applyRestoreLocked brings record s into p, alongside whatever p learned
// since stui started.
func (w *Watcher) applyRestoreLocked(p *paneState, s *savedPane) {
	if p.resumeID == "" {
		p.resumeID = s.ResumeID
	}
	if s.FromHook && !p.agentFromHook && s.Agent != "" {
		p.Agent, p.agentFromHook = s.Agent, true
	}
	if p.subItems == nil {
		p.subItems = map[string]*SubagentItem{}
	}
	for _, it := range s.Items {
		if p.subItems[it.ID] != nil || it.ID == "" {
			continue
		}
		v := it
		if it.EndedAt != nil {
			e := *it.EndedAt
			v.EndedAt = &e
		}
		p.subItems[it.ID] = &v
	}
	for id, r := range s.Running {
		if p.running[id] != nil || p.subItems[id] == nil || p.subItems[id].State != SubRunning {
			continue
		}
		if p.running == nil {
			p.running = map[string]*subRun{}
		}
		p.running[id] = &subRun{seen: r.Seen + w.restoreGap, inTool: r.InTool, heard: r.Heard}
	}
	for _, id := range s.LostIDs {
		if p.running[id] == nil && len(p.lost) < 256 {
			if p.lost == nil {
				p.lost = map[string]bool{}
			}
			p.lost[id] = true
		}
	}
	for _, sp := range s.Spawns {
		if len(p.spawns) < 16 {
			p.spawns = append(p.spawns, spawn{toolUseID: sp.ToolUseID, typ: sp.Type, desc: sp.Desc, ref: sp.Ref, at: time.UnixMilli(sp.At)})
		}
	}
	p.pruneSubItems()
	p.Subagents.Done += s.Done
	p.Subagents.Lost += s.Lost
	p.turnLost += s.TurnLost
	if len(p.Teammates) == 0 && len(s.Teammates) > 0 {
		p.Teammates = append([]Teammate(nil), s.Teammates...)
	}
	// The turn, when stui knows of none since it started.
	if p.State == Unknown || p.State == "" {
		switch s.State {
		case Working, Waiting:
			if s.TurnStart > 0 && p.turnStart.IsZero() {
				p.turnStart = time.UnixMilli(s.TurnStart)
				p.delegated, p.delegatedSent = s.Delegated && len(p.running) > 0, s.DelegatedSent
				// A question is read off the screen again.
				p.State, p.Since, p.Source = Working, s.Since, s.Source
			}
		case Done, Error, Idle:
			p.State, p.Since, p.Source = s.State, s.Since, s.Source
		}
		if p.LastTurnMs == nil && s.LastTurnMs != nil {
			d := *s.LastTurnMs
			p.LastTurnMs = &d
		}
	}
	p.Subagents.Running = len(p.running)
}
