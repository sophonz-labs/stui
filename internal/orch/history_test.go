package orch

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// historyRun is a run with a send of a copy to a worker here, and a done
// from a worker on another machine.
func historyRun(t *testing.T) (run, sent string) {
	t.Helper()
	r := &Run{ID: NewID(), Objective: "ship it", Coordinator: "%9", Created: Now()}
	r.Save()
	p, err := WriteSent(r.ID, "terminal-go", []byte("# the brief\n\nbody\n"), time.Date(2026, 10, 21, 20, 35, 36, 0, time.Local))
	if err != nil {
		t.Fatal(err)
	}
	AppendEvent(Event{Run: r.ID, Kind: EvSend, TS: 1792582536000, From: "%9", FromName: "coordinator", To: "%12", ToName: "terminal-go", File: p, Title: "the brief"})
	AppendEvent(Event{Run: r.ID, Kind: EvDone, TS: 1792582600000, From: "ci-mac/%31", FromName: "simulator-ci", To: "%9", ToName: "coordinator", Machine: "ci-mac", Title: "built it"})
	return r.ID, filepath.Base(p)
}

func get(t *testing.T, a *Adopter, target string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	if !a.ServeHTTP(w, httptest.NewRequest("GET", target, nil)) {
		t.Fatalf("%s not served", target)
	}
	return w
}

func TestHistoryAPI(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	run, sent := historyRun(t)
	a := NewAdopter(AdoptOptions{})

	w := get(t, a, "/api/orch/history?limit=5")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), Dir()) {
		t.Fatalf("a path in the home went out: %s", w.Body)
	}
	var h struct {
		Events []map[string]any             `json:"events"`
		Runs   map[string]map[string]string `json:"runs"`
	}
	json.Unmarshal(w.Body.Bytes(), &h)
	if len(h.Events) != 2 || h.Runs[run]["objective"] != "ship it" {
		t.Fatalf("%s", w.Body)
	}
	b, _ := json.Marshal(h.Events[0])
	t.Logf("send: %s", b)
	b, _ = json.Marshal(h.Events[1])
	t.Logf("done: %s", b)
	s, d := h.Events[0], h.Events[1]
	if s["at"] != "2026-10-21T11:35:36Z" || s["sent"] != sent || s["run"] != run || s["kind"] != "send" || s["file"] != nil ||
		s["sender"].(map[string]any)["name"] != "coordinator" || s["recipient"].(map[string]any)["pane"] != "%12" {
		t.Errorf("send: %v", s)
	}
	if ds := d["sender"].(map[string]any); ds["machine"] != "ci-mac" || ds["pane"] != "%31" || ds["name"] != "simulator-ci" || d["from"] != "ci-mac/%31" {
		t.Errorf("done from another machine: %v", d)
	}

	// The copy by the name the history gave.
	w = get(t, a, "/api/orch/doc?run="+run+"&file="+sent)
	if w.Code != 200 || w.Header().Get("content-type") != "text/markdown; charset=utf-8" || !strings.HasPrefix(w.Body.String(), "# the brief") {
		t.Fatalf("doc: %d %q %q", w.Code, w.Header().Get("content-type"), w.Body)
	}
}

// file and run are names, never paths: nothing outside the run's sent/ is
// read, whatever they say.
func TestHistoryDocStaysInSent(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	run, _ := historyRun(t)
	os.WriteFile(filepath.Join(t.TempDir(), "secret"), []byte("x"), 0o600)
	outside := filepath.Join(Dir(), "outside.md")
	os.WriteFile(outside, []byte("not a copy"), 0o600)
	os.Symlink(outside, filepath.Join(SentDir(run), "link.md"))
	a := NewAdopter(AdoptOptions{})
	for _, q := range []string{
		"run=" + run + "&file=../../run.json",
		"run=" + run + "&file=../run.json",
		"run=" + run + "&file=/etc/passwd",
		"run=../..&file=run.json",
		"run=" + run + "&file=link.md",
		"run=" + run + "&file=",
		"run=&file=x.md",
	} {
		w := get(t, a, "/api/orch/doc?"+q)
		t.Logf("%-40s %d", q, w.Code)
		if w.Code != 404 || strings.Contains(w.Body.String(), "root:") || strings.Contains(w.Body.String(), "coordinator") || strings.Contains(w.Body.String(), "not a copy") {
			t.Errorf("%s: %d %s", q, w.Code, w.Body)
		}
	}
	if w := get(t, a, "/api/orch/history?run=../.."); w.Code != 404 {
		t.Errorf("history run=../..: %d", w.Code)
	}
}

// Not the owner (a read-only link, when stui has them): both are not there.
func TestHistoryOwnerOnly(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	run, sent := historyRun(t)
	a := NewAdopter(AdoptOptions{IsOwner: func(r *http.Request) bool { return r.Header.Get("x-test-owner") == "1" }})
	for _, target := range []string{"/api/orch/history", "/api/orch/doc?run=" + run + "&file=" + sent} {
		if w := get(t, a, target); w.Code != 404 || strings.Contains(w.Body.String(), "brief") {
			t.Errorf("%s for a guest: %d %s", target, w.Code, w.Body)
		}
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", target, nil)
		req.Header.Set("x-test-owner", "1")
		a.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Errorf("%s for the owner: %d", target, w.Code)
		}
	}
}

// The tree's changes (tree.go) come with the same fields as the rest: who
// did it -- a pane, an app, another machine's stui -- to whom, and when.
func TestHistoryTreeEvents(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	r := &Run{ID: NewID(), Coordinator: "%9", Created: Now()}
	r.Save()
	AppendTreeEvent(Event{Run: r.ID, Kind: EvAdopt, TS: 1792606000000, From: "%9", FromName: "coordinator", To: "%31", ToName: "simulator-ci", Parent: "%9", ParentName: "coordinator"})
	AppendTreeEvent(Event{Run: r.ID, Kind: EvAbout, TS: 1792606001000, By: "app", To: "%31", ToName: "simulator-ci", Now: "runs the simulator"})
	AppendTreeEvent(Event{Run: r.ID, Kind: EvRelease, TS: 1792606002000, By: "main-pc", To: "%31", ToName: "simulator-ci", WasParent: "main-pc/%9"})
	h := HistoryOf([]string{r.ID}, 10)
	for i, want := range []Side{{Name: "coordinator", Pane: "%9"}, {Name: "app"}, {Machine: "main-pc"}} {
		e := h.Events[i]
		if e.At == "" || e.Sender == nil || *e.Sender != want || e.Recipient == nil || e.Recipient.Pane != "%31" {
			t.Errorf("%s: at %q sender %+v recipient %+v", e.Kind, e.At, e.Sender, e.Recipient)
		}
	}
}
