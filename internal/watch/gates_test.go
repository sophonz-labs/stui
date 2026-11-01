package watch

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestADecisionGateAskedAndAnswered(t *testing.T) {
	w, _, sink := setup(t, "2.1.283")
	var told []Gate
	w.opts.GateAnswered = func(g Gate) string { told = append(told, g); return "ok" }
	w.opts.GateFile = filepath.Join(t.TempDir(), "gates.json")

	g, err := w.OpenGate("%1", "Image strategy", "How should images be handled?", []string{"A: keep them", "B: OCR to text"}, true)
	if err != nil {
		t.Fatal(err)
	}
	p := pane(t, w, "%1")
	if p.State != Waiting || p.Prompt == nil || p.Prompt.ID != g.ID || p.Prompt.Tool != "stui gate" || len(p.Prompt.Options) != 2 {
		t.Fatalf("the pane asks: %+v %+v", p.State, p.Prompt)
	}
	if ev := sink.of("prompt-opened"); len(ev) != 1 || ev[0].id != g.ID {
		t.Fatalf("pushed once: %+v", sink.events)
	}
	// Through the same route the phone answers prompts by.
	answer := func(body string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/prompts/"+g.ID+"/answer", bytes.NewBufferString(body))
		w.ServeHTTP(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	if code, _ := answer(`{"option": 7}`); code != 400 {
		t.Fatalf("an option it does not have: %d", code)
	}
	code, out := answer(`{"option": 1, "text": "only screenshots"}`)
	if code != 200 || out["answer"] != "B: OCR to text — only screenshots" || out["told"] != "ok" {
		t.Fatalf("%d %v", code, out)
	}
	if len(told) != 1 || told[0].Pane != "%1" || told[0].AnswerText() == "" {
		t.Fatalf("the agent is told: %+v", told)
	}
	if p := pane(t, w, "%1"); p.Prompt != nil {
		t.Fatalf("no longer asking: %+v", p.Prompt)
	}
	if code, _ := answer(`{"option": 0}`); code != 409 {
		t.Fatalf("answered twice: %d", code)
	}
	// Kept across a restart.
	w2 := New(Options{Tmux: w.opts.Tmux, ClaudeDir: t.TempDir(), GateFile: w.opts.GateFile})
	if gs := w2.Gates(""); len(gs) != 1 || gs[0].Status != GateAnswered || gs[0].Told != "ok" {
		t.Fatalf("%+v", gs)
	}
}

func TestGatesRefuseAndCancel(t *testing.T) {
	w, _, _ := setup(t, "2.1.283")
	if _, err := w.OpenGate("%1", "", "", []string{"a"}, false); err == nil {
		t.Error("no question")
	}
	if _, err := w.OpenGate("%1", "", "Which?", nil, false); err == nil {
		t.Error("no options and no text")
	}
	if _, err := w.OpenGate("%2", "", "Which?", []string{"a"}, false); err == nil {
		t.Error("a shell's pane")
	}
	a, _ := w.OpenGate("%1", "", "First?", []string{"a"}, false)
	b, _ := w.OpenGate("%1", "", "Second?", []string{"a"}, false)
	gs := w.Gates("%1")
	if len(gs) != 2 || gs[0].ID != b.ID || gs[1].Status != GateCancelled {
		t.Fatalf("a newer gate replaces an older one: %+v", gs)
	}
	_ = a
	if code, _ := w.CancelGate(b.ID); code != 200 {
		t.Fatal(code)
	}
	if p := pane(t, w, "%1"); p.Prompt != nil {
		t.Fatal("cancelled: not asking")
	}
	_ = context.Background
	_ = http.MethodGet
}
