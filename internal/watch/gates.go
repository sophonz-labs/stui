package watch

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Decision gates: an agent that needs a
// person's decision asks for one with stui gate ask, and stops its turn. The
// gate is a prompt like the ones read off the screen, so it goes the same
// way: a prompt with the options to pick from, stui task
// wait's "a worker asks". The answer comes back through the same
// POST /api/prompts/<id>/answer, and stui types it into the agent's pane as its
// next message (Options.GateAnswered), so it carries on with it.
//
// A gate lives beside the screen's prompts, not among them: nothing on the
// screen shows it, so the screen never closes it. While one is open its pane
// reads as waiting with the gate as its prompt, unless the screen has a
// prompt of its own. Gates are kept in a file so a restart of stui keeps them.

// Gate is one decision asked for.
type Gate struct {
	ID        string   `json:"id"`
	Pane      string   `json:"pane"`
	Title     string   `json:"title"`
	Question  string   `json:"question"`
	Options   []string `json:"options"`
	AllowText bool     `json:"allowText"`
	CreatedAt int64    `json:"createdAt"`
	// open, answered or cancelled.
	Status     string `json:"status"`
	Answer     string `json:"answer,omitempty"`
	Choice     *int   `json:"choice,omitempty"`
	AnsweredAt int64  `json:"answeredAt,omitempty"`
	// Whether the answer was typed into the agent, and if not, why.
	Told string `json:"told,omitempty"`
	// The order gates were asked in (two in one millisecond keep it).
	Seq int64 `json:"seq"`
	// Where it was asked, as it was then: the pane may be gone by the time
	// anyone reads the record.
	Session    string `json:"session,omitempty"`
	Window     *int   `json:"window,omitempty"`
	WindowName string `json:"windowName,omitempty"`
	PaneIndex  *int   `json:"paneIndex,omitempty"`
	Agent      string `json:"agent,omitempty"`
	// Why a cancelled one was: "replaced" by a newer question, or
	// "cancelled" (taken back).
	CancelReason string `json:"cancelReason,omitempty"`
	// The document it is kept as (Options.GateSettled), once it is; and
	// why keeping it failed, until it does.
	Recorded    string `json:"recorded,omitempty"`
	RecordError string `json:"recordError,omitempty"`
}

const (
	GateOpen      = "open"
	GateAnswered  = "answered"
	GateCancelled = "cancelled"
	maxGates      = 200
)

// GateTool is a gate's prompt's Tool: how a gate is told from a prompt read
// off the screen, which has the same kind ("question").
const GateTool = "stui gate"

var gatePath = regexp.MustCompile(`^/api/gates/([0-9a-f]{24})(/cancel)?$`)

// prompt is the gate as a prompt, for the pane's view and the push.
func (g *Gate) prompt(agent string) Prompt {
	opts := make([]Option, len(g.Options))
	for i, o := range g.Options {
		opts[i] = Option{Label: o}
	}
	return Prompt{
		ID: g.ID, Kind: "question", Title: g.Title, Question: g.Question, Options: opts,
		AllowText: g.AllowText, CreatedAt: g.CreatedAt, Agent: agent, Tool: GateTool,
	}
}

// AnswerText is what the agent is told.
func (g *Gate) AnswerText() string {
	return fmt.Sprintf("Decision gate %s answered: %s. Carry on with that.", g.ID, g.Answer)
}

func (w *Watcher) gatesFile() string {
	if w.opts.GateFile != "" {
		return w.opts.GateFile
	}
	return ""
}

func (w *Watcher) loadGates() {
	f := w.gatesFile()
	if f == "" {
		return
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return
	}
	var gs []*Gate
	if json.Unmarshal(b, &gs) != nil {
		return
	}
	for _, g := range gs {
		w.gates[g.ID] = g
		w.gateSeq = max(w.gateSeq, g.Seq)
	}
}

func (w *Watcher) saveGatesLocked() {
	f := w.gatesFile()
	if f == "" {
		return
	}
	gs := make([]*Gate, 0, len(w.gates))
	for _, g := range w.gates {
		gs = append(gs, g)
	}
	b, _ := json.Marshal(gs)
	os.MkdirAll(filepath.Dir(f), 0o700)
	tmp := f + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, f)
	}
}

// openGateLocked is pane's open gate, or nil.
func (w *Watcher) openGateLocked(pane string) *Gate {
	var found *Gate
	for _, g := range w.gates {
		if g.Pane == pane && g.Status == GateOpen && (found == nil || g.Seq > found.Seq) {
			found = g
		}
	}
	return found
}

// OpenGate asks for a decision for the agent in pane. A gate it replaces is
// kept as a document before it returns.
func (w *Watcher) OpenGate(pane, title, question string, options []string, allowText bool) (*Gate, error) {
	g, err := w.openGate(pane, title, question, options, allowText)
	if err == nil {
		w.recordGates()
	}
	return g, err
}

func (w *Watcher) openGate(pane, title, question string, options []string, allowText bool) (*Gate, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return nil, errors.New("a gate needs a question")
	}
	if len(options) == 0 && !allowText {
		return nil, errors.New("a gate needs options, or --text for a free answer")
	}
	if len(options) > 9 {
		return nil, errors.New("at most nine options")
	}
	for i, o := range options {
		options[i] = clipText(strings.TrimSpace(o), 200)
		if options[i] == "" {
			return nil, errors.New("an empty option")
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	p := w.panes[pane]
	if p == nil || p.Agent == "" {
		return nil, fmt.Errorf("%s is not an agent's pane stui watches", pane)
	}
	// One open gate a pane: a new question replaces an older one.
	if old := w.openGateLocked(pane); old != nil {
		old.Status, old.CancelReason = GateCancelled, "replaced"
		if w.opts.Sink != nil {
			w.opts.Sink.PromptCleared(p.view(w.now()), old.ID, "replaced")
		}
	}
	if title == "" {
		title = "Decision"
	}
	g := &Gate{
		ID: newID(), Pane: pane, Title: clipText(title, 120), Question: clipText(question, 2000),
		Options: options, AllowText: allowText, CreatedAt: w.now().UnixMilli(), Status: GateOpen,
		Session: p.Session, WindowName: p.WindowName, Agent: p.Agent,
	}
	win, idx := p.Window, p.Pane.Pane
	g.Window, g.PaneIndex = &win, &idx
	w.gateSeq++
	g.Seq = w.gateSeq
	if len(w.gates) >= maxGates {
		w.pruneGatesLocked()
	}
	w.gates[g.ID] = g
	p.gate = g
	w.saveGatesLocked()
	if w.opts.Sink != nil {
		w.opts.Sink.PromptOpened(p.view(w.now()), g.prompt(p.Agent))
	}
	w.publishLocked(p)
	return g, nil
}

// pruneGatesLocked drops the oldest settled gate that is kept as a
// document: the file only holds the newest, and the documents are the
// record. One not kept yet (its writing failed) stays, over maxGates if it
// must, rather than be lost; with nothing keeping gates (no GateSettled)
// the oldest settled one goes.
func (w *Watcher) pruneGatesLocked() {
	var oldest *Gate
	for _, g := range w.gates {
		kept := g.Recorded != "" || w.opts.GateSettled == nil
		if g.Status != GateOpen && kept && (oldest == nil || g.Seq < oldest.Seq) {
			oldest = g
		}
	}
	if oldest != nil {
		delete(w.gates, oldest.ID)
	}
}

// isGate says whether id is a gate's.
func (w *Watcher) isGate(id string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.gates[id] != nil
}

// AnswerGate settles gate id with an option (0-based) or text, and has the
// agent told.
func (w *Watcher) AnswerGate(id string, option *int, text *string) (int, map[string]any) {
	w.mu.Lock()
	g := w.gates[id]
	if g == nil {
		w.mu.Unlock()
		return 404, map[string]any{"error": "no such prompt"}
	}
	if g.Status != GateOpen {
		w.mu.Unlock()
		return 409, map[string]any{"error": "answered", "reason": g.Status}
	}
	var parts []string
	if option != nil {
		if *option < 0 || *option >= len(g.Options) {
			w.mu.Unlock()
			return 400, map[string]any{"error": "bad answer"}
		}
		n := *option
		g.Choice = &n
		parts = append(parts, g.Options[n])
	}
	if text != nil && strings.TrimSpace(*text) != "" {
		if !g.AllowText && option == nil {
			w.mu.Unlock()
			return 400, map[string]any{"error": "bad answer"}
		}
		parts = append(parts, strings.TrimSpace(*text))
	}
	if len(parts) == 0 {
		w.mu.Unlock()
		return 400, map[string]any{"error": "bad answer"}
	}
	g.Status, g.Answer, g.AnsweredAt = GateAnswered, strings.Join(parts, " — "), w.now().UnixMilli()
	p := w.panes[g.Pane]
	if p != nil && p.gate == g {
		p.gate = nil
	}
	if p != nil {
		if w.opts.Sink != nil {
			w.opts.Sink.PromptCleared(p.view(w.now()), g.ID, AnsweredPhone)
		}
		w.publishLocked(p)
	}
	w.saveGatesLocked()
	gc := *g
	w.mu.Unlock()
	// Typed into the agent outside the lock: it waits for the input box.
	told := "no agent to tell"
	if w.opts.GateAnswered != nil {
		told = w.opts.GateAnswered(gc)
	}
	w.mu.Lock()
	g.Told = told
	w.saveGatesLocked()
	w.mu.Unlock()
	out := map[string]any{"ok": true, "id": id, "answer": gc.Answer, "told": told}
	// Kept now, in this call, so the record never waits on anything else.
	// The answer stands either way (it is typed in already); a failure is
	// said here and tried again with the next gate that settles.
	if w.opts.GateSettled != nil {
		if err, failed := w.recordGates()[id]; failed {
			out["recordError"] = err.Error()
		} else {
			w.mu.Lock()
			out["recorded"] = g.Recorded
			w.mu.Unlock()
		}
	}
	return 200, out
}

// CancelGate takes a gate back. It is kept as a document too: a question
// asked and dropped is part of the record.
func (w *Watcher) CancelGate(id string) (int, map[string]any) {
	status, out := w.cancelGate(id)
	if status == 200 && w.opts.GateSettled != nil {
		if err, failed := w.recordGates()[id]; failed {
			out["recordError"] = err.Error()
		}
	}
	return status, out
}

func (w *Watcher) cancelGate(id string) (int, map[string]any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	g := w.gates[id]
	if g == nil {
		return 404, map[string]any{"error": "no such gate"}
	}
	if g.Status == GateOpen {
		g.Status, g.CancelReason = GateCancelled, "cancelled"
		if p := w.panes[g.Pane]; p != nil {
			if p.gate == g {
				p.gate = nil
			}
			if w.opts.Sink != nil {
				w.opts.Sink.PromptCleared(p.view(w.now()), g.ID, "cancelled")
			}
			w.publishLocked(p)
		}
		w.saveGatesLocked()
	}
	return 200, map[string]any{"ok": true, "id": id, "status": g.Status}
}

// Gates is every gate, newest first, or one pane's.
func (w *Watcher) Gates(pane string) []Gate {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []Gate
	for _, g := range w.gates {
		if pane == "" || g.Pane == pane {
			out = append(out, *g)
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Seq > out[j-1].Seq; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// serveGates is /api/gates and /api/gates/<id>[/cancel].
func (w *Watcher) serveGates(rw http.ResponseWriter, r *http.Request) bool {
	path := r.URL.Path
	if path == "/api/gates" {
		switch r.Method {
		case "GET":
			writeJSON(rw, 200, map[string]any{"gates": w.Gates(r.URL.Query().Get("pane"))})
		case "POST":
			if !local(r) {
				writeJSON(rw, 403, map[string]any{"error": "local only"})
				return true
			}
			var body struct {
				Pane      string   `json:"pane"`
				Title     string   `json:"title"`
				Question  string   `json:"question"`
				Options   []string `json:"options"`
				AllowText bool     `json:"allowText"`
			}
			if json.NewDecoder(http.MaxBytesReader(rw, r.Body, 64<<10)).Decode(&body) != nil {
				writeJSON(rw, 400, map[string]any{"error": "bad gate"})
				return true
			}
			g, err := w.OpenGate(body.Pane, body.Title, body.Question, body.Options, body.AllowText)
			if err != nil {
				writeJSON(rw, 400, map[string]any{"error": err.Error()})
				return true
			}
			writeJSON(rw, 200, g)
		default:
			writeJSON(rw, 405, map[string]any{"error": "method not allowed"})
		}
		return true
	}
	m := gatePath.FindStringSubmatch(path)
	if m == nil {
		return false
	}
	switch {
	case m[2] == "/cancel" && r.Method == "POST":
		status, out := w.CancelGate(m[1])
		writeJSON(rw, status, out)
	case m[2] == "" && r.Method == "GET":
		w.mu.Lock()
		g := w.gates[m[1]]
		var gc Gate
		if g != nil {
			gc = *g
		}
		w.mu.Unlock()
		if g == nil {
			writeJSON(rw, 404, map[string]any{"error": "no such gate"})
		} else {
			writeJSON(rw, 200, gc)
		}
	default:
		writeJSON(rw, 405, map[string]any{"error": "method not allowed"})
	}
	return true
}

// RecordGates keeps every settled gate not kept yet as a document: at
// start, for gates settled before stui kept them or while keeping failed.
func (w *Watcher) RecordGates() { w.recordGates() }

// recordGates keeps the settled gates not kept yet, oldest first, outside
// the lock (keeping one asks where its pane is, which takes it). It says
// which failed, and why.
func (w *Watcher) recordGates() map[string]error {
	failed := map[string]error{}
	if w.opts.GateSettled == nil {
		return failed
	}
	w.recording.Lock()
	defer w.recording.Unlock()
	w.mu.Lock()
	var todo []Gate
	for _, g := range w.gates {
		if g.Status != GateOpen && g.Recorded == "" {
			todo = append(todo, *g)
		}
	}
	w.mu.Unlock()
	sort.Slice(todo, func(i, j int) bool { return todo[i].Seq < todo[j].Seq })
	for _, gc := range todo {
		doc, err := w.opts.GateSettled(gc)
		if err == nil && doc == "" {
			err = errors.New("no document came back")
		}
		w.mu.Lock()
		if g := w.gates[gc.ID]; g != nil {
			if err != nil {
				g.RecordError = err.Error()
			} else {
				g.Recorded, g.RecordError = doc, ""
			}
		}
		w.mu.Unlock()
		if err != nil {
			failed[gc.ID] = err
		}
	}
	if len(todo) > 0 {
		w.mu.Lock()
		w.saveGatesLocked()
		w.mu.Unlock()
	}
	if len(failed) > 0 && w.opts.Logf != nil {
		for id, err := range failed {
			w.opts.Logf("  stui gate: %d settled gates not kept as documents yet (%s: %v)", len(failed), id, err)
			break
		}
	}
	return failed
}
