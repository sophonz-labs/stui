package orch

// A run's history as clients read it:
// stui orch history --json and GET /api/orch/history give the same JSON, so
// no app parses two shapes. The events are as events.jsonl has them, with
// what an app needs to draw a line beside them: the time as RFC3339, each
// side as {name, pane, machine}, and the handed-over copy by its file name.
// GET /api/orch/doc gives that copy's text.

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Side is one side of an event.
type Side struct {
	Name    string `json:"name,omitempty"`
	Pane    string `json:"pane,omitempty"`
	Machine string `json:"machine,omitempty"`
}

// HistoryEvent is an event with what the apps draw it by. From, To and the
// rest stay as they are, for the readers that read them already.
type HistoryEvent struct {
	Event
	// When, as RFC3339 (UTC): an app draws it in the device's time zone.
	At string `json:"at"`
	// Who to whom: for done and idle, the worker and its parent; for the
	// rest, who asked and the worker.
	Sender    *Side `json:"sender,omitempty"`
	Recipient *Side `json:"recipient,omitempty"`
	// The copy it handed over, by its file name under the run's sent/:
	// what GET /api/orch/doc?run=&file= takes.
	Sent string `json:"sent,omitempty"`
}

// History is stui orch history --json.
type History struct {
	Events []HistoryEvent `json:"events"`
	// What each run is for: {"r-…": {"objective"}}.
	Runs map[string]map[string]string `json:"runs"`
}

// HistoryOf is the last limit events of runs, oldest first.
func HistoryOf(runs []string, limit int) History {
	var evs []Event
	for _, r := range runs {
		evs = append(evs, ReadEvents(r)...)
	}
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].TS < evs[j].TS })
	if limit > 0 && len(evs) > limit {
		evs = evs[len(evs)-limit:]
	}
	// A side's name from the run's other events, for a line written
	// without it (a delivered line from before fromName).
	named := map[string]string{}
	for _, ev := range evs {
		if ev.FromName != "" {
			named[ev.Run+" "+ev.From] = ev.FromName
		}
		if ev.ToName != "" {
			named[ev.Run+" "+ev.To] = ev.ToName
		}
	}
	h := History{Events: []HistoryEvent{}, Runs: map[string]map[string]string{}}
	for _, ev := range evs {
		if ev.Kind == EvDelivered && ev.FromName == "" {
			ev.FromName = named[ev.Run+" "+ev.From]
		}
		he := HistoryEvent{Event: ev, At: time.UnixMilli(ev.TS).UTC().Format(time.RFC3339)}
		// The machine a bare id is on: Machine names one side, by kind.
		fromM, toM := "", ""
		switch ev.Kind {
		case EvDone, EvIdle:
			fromM = ev.Machine
		default:
			toM = ev.Machine
		}
		he.Sender = side(ev.From, firstNonEmpty(ev.FromName, named[ev.Run+" "+ev.From]), fromM)
		he.Recipient = side(ev.To, firstNonEmpty(ev.ToName, named[ev.Run+" "+ev.To]), toM)
		// A change of the tree no pane asked for (tree.go): who did, by By --
		// "app", or the slug of the machine whose stui asked.
		if he.Sender == nil && ev.By != "" {
			if ev.By == "app" {
				he.Sender = &Side{Name: "app"}
			} else {
				he.Sender = &Side{Machine: ev.By}
			}
		}
		// A copy under the run's sent/, by its name: the path is the home it
		// was written in, which may not be where stui's home is now.
		if d := filepath.Dir(ev.File); ev.File != "" && ev.Run != "" && filepath.Base(d) == "sent" && filepath.Base(filepath.Dir(d)) == ev.Run {
			he.Sent = filepath.Base(ev.File)
		}
		h.Events = append(h.Events, he)
		if _, ok := h.Runs[ev.Run]; !ok && ev.Run != "" {
			obj := ""
			if r, err := Load(ev.Run); err == nil {
				obj = r.Objective
			}
			h.Runs[ev.Run] = map[string]string{"objective": obj}
		}
	}
	return h
}

// side is id ("%12", "ci-mac/%7") as a Side; nil for none.
func side(id, name, machine string) *Side {
	if id == "" && name == "" {
		return nil
	}
	s := &Side{Name: name, Pane: id, Machine: machine}
	if m, p, ok := strings.Cut(id, "/"); ok {
		s.Machine, s.Pane = m, p
	}
	return s
}

// historyDays is how far back GET /api/orch/history reads runs.
const historyDays = 30

// serveHistory answers GET /api/orch/history?limit=N[&run=r-…]: History,
// every recent run's (all=1 says the same; a page has no pane to narrow it
// by), or run's. The copies' paths are left out: a phone has no use for a
// path in the machine's home, and /api/orch/doc takes the file name.
func (a *Adopter) serveHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeJSON(w, 400, map[string]any{"error": "limit: a number from 1"})
			return
		}
		limit = min(n, 1000)
	}
	runs := RecentRuns(historyDays * 24 * time.Hour)
	if id := r.URL.Query().Get("run"); id != "" {
		if !runID.MatchString(id) {
			writeJSON(w, 404, map[string]any{"error": "no such run"})
			return
		}
		runs = []string{id}
	}
	h := HistoryOf(runs, limit)
	for i := range h.Events {
		h.Events[i].File = ""
	}
	writeJSON(w, 200, h)
}

// serveDoc answers GET /api/orch/doc?run=r-…&file=<name>: a copy under the
// run's sent/, as markdown. file is a name, never a path: it is read only
// when it is exactly one of the names in that directory, a plain file, so
// "..", an absolute path or a link out of it finds nothing.
func (a *Adopter) serveDoc(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
		return
	}
	run, file := r.URL.Query().Get("run"), r.URL.Query().Get("file")
	notFound := func() { writeJSON(w, 404, map[string]any{"error": "no such copy"}) }
	if !runID.MatchString(run) || file == "" || file != filepath.Base(file) {
		notFound()
		return
	}
	ents, err := os.ReadDir(SentDir(run))
	if err != nil {
		notFound()
		return
	}
	for _, e := range ents {
		if e.Name() != file || !e.Type().IsRegular() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(SentDir(run), e.Name()))
		if err != nil {
			notFound()
			return
		}
		w.Header().Set("content-type", "text/markdown; charset=utf-8")
		w.Header().Set("cache-control", "no-store")
		w.Write(b)
		return
	}
	notFound()
}
