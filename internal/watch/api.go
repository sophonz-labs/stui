package watch

import (
	"context"
	"encoding/json"
	agentstate "github.com/sophonz-labs/stui/internal/state"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/sophonz-labs/stui/internal/hookclient"
	"github.com/sophonz-labs/stui/internal/stream"
)

var answerPath = regexp.MustCompile(`^/api/prompts/([0-9a-f]{24})/answer$`)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("content-type", "application/json;charset=utf-8")
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// ServeHTTP answers agent watch's routes and reports whether r was one.
// The caller has already authenticated the request.
func (w *Watcher) ServeHTTP(rw http.ResponseWriter, r *http.Request) bool {
	path := r.URL.Path
	switch {
	case path == "/api/agents" && r.Method == "GET":
		writeJSON(rw, 200, w.Snapshot())
		return true
	case path == "/api/graph" && r.Method == "GET":
		// One session's delegation graph, or without
		// ?session= the sessions that have any.
		if s := r.URL.Query().Get("session"); s != "" {
			writeJSON(rw, 200, w.Graph(s))
		} else {
			writeJSON(rw, 200, w.GraphList())
		}
		return true
	case path == "/api/agents/stream" && r.Method == "GET":
		w.stream(rw, r)
		return true
	case path == "/api/hook" && r.Method == "POST":
		w.hook(rw, r)
		return true
	}
	if strings.HasPrefix(path, "/api/gates") && w.serveGates(rw, r) {
		return true
	}
	if m := answerPath.FindStringSubmatch(path); m != nil {
		if r.Method != "POST" {
			writeJSON(rw, 405, map[string]any{"error": "method not allowed"})
			return true
		}
		w.answer(rw, r, m[1])
		return true
	}
	return false
}

// local is a request from stui's own programs on this machine: let in by the
// machine's token (state.ByToken; stui's hooks and stui gate ask carry it), not
// forwarded by a proxy. Not by its address: a proxy and a page in a
// browser here connect from loopback too.
func local(r *http.Request) bool {
	return agentstate.ByToken(r) && r.Header.Get("cf-connecting-ip") == ""
}

func (w *Watcher) hook(rw http.ResponseWriter, r *http.Request) {
	if !local(r) {
		writeJSON(rw, 403, map[string]any{"error": "local only"})
		return
	}
	var ev hookclient.Event
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&ev); err != nil || (ev.Pane == "" && !ev.Background) || (ev.Pane != "" && ev.Pane[0] != '%') {
		writeJSON(rw, 400, map[string]any{"error": "bad event"})
		return
	}
	w.Hook(ev)
	writeJSON(rw, 200, map[string]any{"ok": true})
}

func (w *Watcher) answer(rw http.ResponseWriter, r *http.Request, id string) {
	var body map[string]json.RawMessage
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	if dec.Decode(&body) != nil {
		writeJSON(rw, 400, map[string]any{"error": "bad answer"})
		return
	}
	// Every question of an AskUserQuestion at once (ask.go).
	if raw, ok := body["answers"]; ok && string(raw) != "null" {
		var answers []AskAnswer
		if json.Unmarshal(raw, &answers) != nil || len(answers) == 0 {
			writeJSON(rw, 400, map[string]any{"error": "bad answer"})
			return
		}
		status, out := w.AnswerSteps(id, answers)
		writeJSON(rw, status, out)
		return
	}
	var option *int
	var text *string
	if raw, ok := body["option"]; ok && string(raw) != "null" {
		var n int
		if json.Unmarshal(raw, &n) != nil {
			writeJSON(rw, 400, map[string]any{"error": "bad answer"})
			return
		}
		option = &n
	}
	if raw, ok := body["text"]; ok && string(raw) != "null" {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			writeJSON(rw, 400, map[string]any{"error": "bad answer"})
			return
		}
		text = &s
	}
	if option == nil && text == nil {
		writeJSON(rw, 400, map[string]any{"error": "bad answer"})
		return
	}
	if w.isGate(id) {
		status, out := w.AnswerGate(id, option, text)
		writeJSON(rw, status, out)
		return
	}
	status, out := w.Answer(id, option, text)
	writeJSON(rw, status, out)
}

// stream is the agents feed as SSE or, with an
// upgrade, a websocket (internal/stream says why).
func (w *Watcher) stream(rw http.ResponseWriter, r *http.Request) {
	stream.Serve(rw, r, func(ctx context.Context, send stream.Send) {
		snap, events, cancel := w.Subscribe()
		defer cancel()
		if !send("snapshot", snap.Seq, snap) {
			return
		}
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-events:
				if !ok || !send(ev.Name, ev.Seq, ev.Data) {
					return
				}
			}
		}
	})
}
