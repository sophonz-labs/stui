package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type rig struct {
	mu      sync.Mutex
	sources map[string]Source
	moved   map[string]string
	owner   bool
	srv     *httptest.Server
	svc     *Service
}

func newRig(t *testing.T) *rig {
	t.Helper()
	pollEvery, settleFor, drainWithin, reconcileEvery = 5*time.Millisecond, 10*time.Millisecond, 50*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() {
		pollEvery, settleFor, drainWithin, reconcileEvery = 50*time.Millisecond, 40*time.Millisecond, 250*time.Millisecond, time.Second
	})
	r := &rig{sources: map[string]Source{}, moved: map[string]string{}, owner: true}
	r.svc = New(Options{
		Source: func(p string) (Source, string, bool) {
			r.mu.Lock()
			defer r.mu.Unlock()
			s, ok := r.sources[p]
			return s, r.moved[p], ok
		},
		IsOwner: func(*http.Request) bool {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.owner
		},
	})
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !r.svc.ServeHTTP(w, req) {
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *rig) set(pane string, s Source) {
	r.mu.Lock()
	r.sources[pane] = s
	r.mu.Unlock()
}

func (r *rig) get(t *testing.T, path string, out any) int {
	t.Helper()
	res, err := http.Get(r.srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		json.NewDecoder(res.Body).Decode(out)
	}
	return res.StatusCode
}

type sse struct {
	name string
	data json.RawMessage
}

func (r *rig) stream(t *testing.T, pane string) (<-chan sse, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", r.srv.URL+"/api/chat/stream?pane="+pane, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("content-type"), "text/event-stream") {
		t.Fatalf("%d %s", res.StatusCode, res.Header.Get("content-type"))
	}
	ch := make(chan sse, 100)
	go func() {
		defer close(ch)
		defer res.Body.Close()
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(nil, 4<<20)
		var ev sse
		for sc.Scan() {
			l := sc.Text()
			switch {
			case strings.HasPrefix(l, "event: "):
				ev.name = l[7:]
			case strings.HasPrefix(l, "data: "):
				ev.data = json.RawMessage(l[6:])
			case l == "" && ev.name != "":
				ch <- ev
				ev = sse{}
			}
		}
	}()
	return ch, cancel
}

func next(t *testing.T, ch <-chan sse) sse {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("stream ended")
		}
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("no event")
	}
	return sse{}
}

func userRec(id, text string) string {
	return fmt.Sprintf(`{"type":"user","uuid":%q,"timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":%q}}`+"\n", id, text)
}

func asstRec(id, text, stop string) string {
	return fmt.Sprintf(`{"type":"assistant","uuid":%q,"timestamp":"2026-01-01T00:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":%q}],"stop_reason":%q}}`+"\n", id, text, stop)
}

func TestChatSnapshotAndAuth(t *testing.T) {
	r := newRig(t)
	path := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(path, []byte(userRec("u1", "hello")+asstRec("a1", strings.Repeat("y", 70000), "end_turn")), 0o600)
	r.set("%3", Source{Agent: "claude", SessionID: "s", Path: path})
	r.set("%4", Source{Agent: "gemini", SessionID: "g"})
	r.set("%5", Source{Agent: "claude", SessionID: "nothing-here", ClaudeDirs: []string{t.TempDir()}})

	var snap Snapshot
	if c := r.get(t, "/api/chat?pane=%253&tail=40", &snap); c != 200 {
		t.Fatal(c)
	}
	if snap.Pane != "%3" || snap.Agent != "claude" || snap.SessionID != "s" || len(snap.Messages) != 2 || snap.HasMore || snap.Turn == nil || snap.Turn.State != TurnCompleted {
		t.Fatalf("%+v", snap)
	}
	if b := snap.Messages[1].Blocks[0]; !b.Clipped || b.Size != 70000 || len(b.Text) != 64000 {
		t.Fatalf("text not clipped: %v %d", b.Clipped, b.Size)
	}
	// The block whole.
	var full struct {
		ID    string `json:"id"`
		I     int    `json:"i"`
		Block Block  `json:"block"`
	}
	if c := r.get(t, "/api/chat/block?pane=%253&id=a1&i=0", &full); c != 200 || len(full.Block.Text) != 70000 || full.Block.Clipped {
		t.Fatalf("%d %d", c, len(full.Block.Text))
	}
	if c := r.get(t, "/api/chat/block?pane=%253&id=nope", nil); c != 404 {
		t.Fatal(c)
	}

	for pane, want := range map[string]string{"%254": UnsupportedAgent, "%255": NoTranscript, "%259": NoAgent} {
		var s Snapshot
		r.get(t, "/api/chat?pane="+pane, &s)
		if s.Unavailable != want || s.Messages == nil {
			t.Fatalf("%s: %+v", pane, s)
		}
	}
	r.mu.Lock()
	r.moved["%3"] = "%7"
	r.mu.Unlock()
	r.get(t, "/api/chat?pane=%253", &snap)
	if snap.Unavailable != Moved || snap.MovedTo != "%7" {
		t.Fatalf("%+v", snap)
	}
	r.mu.Lock()
	delete(r.moved, "%3")
	r.mu.Unlock()

	for path, want := range map[string]int{
		"/api/chat?pane=3":             400,
		"/api/chat?pane=%253&tail=x":   400,
		"/api/chat?pane=%253&before=5": 400,
		"/api/chat/stream?pane=%25%25": 400,
	} {
		if c := r.get(t, path, nil); c != want {
			t.Fatalf("%s: %d", path, c)
		}
	}
	res, _ := http.Post(r.srv.URL+"/api/chat?pane=%253", "application/json", nil)
	if res.StatusCode != 405 {
		t.Fatal(res.StatusCode)
	}
	// A read-only share sees no conversation.
	r.mu.Lock()
	r.owner = false
	r.mu.Unlock()
	for _, p := range []string{"/api/chat?pane=%253", "/api/chat/stream?pane=%253", "/api/chat/block?pane=%253&id=a1"} {
		var e map[string]string
		if c := r.get(t, p, &e); c != 403 || e["error"] != "read-only" {
			t.Fatalf("%s: %d %v", p, c, e)
		}
	}
}

// The stream: a snapshot, then what is added in order -- messages in
// batches, the turn as it moves -- a record written again as replace, and a
// file that is replaced as reset and a new snapshot.
func TestChatStream(t *testing.T) {
	r := newRig(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	os.WriteFile(path, []byte(userRec("u1", "hello")+asstRec("a1", "hi", "end_turn")), 0o600)
	r.set("%3", Source{Agent: "claude", SessionID: "s", Path: path})

	ch, stop := r.stream(t, "%253")
	defer stop()
	ev := next(t, ch)
	var snap Snapshot
	snap = Snapshot{}
	json.Unmarshal(ev.data, &snap)
	if ev.name != "snapshot" || len(snap.Messages) != 2 || snap.Turn.State != TurnCompleted {
		t.Fatalf("%s %+v", ev.name, snap)
	}
	// A second listener shares the reader.
	ch2, stop2 := r.stream(t, "%253")
	if ev := next(t, ch2); ev.name != "snapshot" {
		t.Fatal(ev.name)
	}
	r.svc.mu.Lock()
	hubs := len(r.svc.hubs)
	r.svc.mu.Unlock()
	if hubs != 1 {
		t.Fatalf("%d readers", hubs)
	}
	stop2()

	// A prompt, written in two pieces, and the reply.
	rec := userRec("u2", "again")
	appendTo(t, path, rec[:20])
	time.Sleep(30 * time.Millisecond)
	appendTo(t, path, rec[20:]+asstRec("a2", "working on it", "tool_use"))
	var got []string
	for len(got) < 3 {
		ev := next(t, ch)
		switch ev.name {
		case "append":
			var b struct{ Messages []Message }
			json.Unmarshal(ev.data, &b)
			for _, m := range b.Messages {
				got = append(got, "append:"+m.ID)
			}
		case "turn":
			var tr Turn
			json.Unmarshal(ev.data, &tr)
			got = append(got, "turn:"+tr.ID+":"+tr.State)
		default:
			t.Fatalf("unexpected %s", ev.name)
		}
	}
	if strings.Join(got, " ") != "append:u2 turn:u2:working append:a2" {
		t.Fatal(got)
	}
	// The same record again (a rewrite of a2), then the turn ends.
	appendTo(t, path, asstRec("a2", "done now", "end_turn"))
	if ev := next(t, ch); ev.name != "replace" || !strings.Contains(string(ev.data), "done now") {
		t.Fatalf("%s %s", ev.name, ev.data)
	}
	if ev := next(t, ch); ev.name != "turn" || !strings.Contains(string(ev.data), `"completed"`) {
		t.Fatalf("%s %s", ev.name, ev.data)
	}

	// /clear: another file.
	second := filepath.Join(dir, "t.jsonl")
	os.WriteFile(second, []byte(userRec("v1", "fresh")), 0o600)
	r.set("%3", Source{Agent: "claude", SessionID: "t", Path: second})
	if ev := next(t, ch); ev.name != "reset" || !strings.Contains(string(ev.data), "switched") {
		t.Fatalf("%s %s", ev.name, ev.data)
	}
	ev = next(t, ch)
	snap = Snapshot{}
	json.Unmarshal(ev.data, &snap)
	if ev.name != "snapshot" || snap.SessionID != "t" || len(snap.Messages) != 1 || snap.Messages[0].ID != "v1" {
		t.Fatalf("%s %+v", ev.name, snap)
	}
	// Rewritten under the reader (cut short and written again).
	os.WriteFile(second, []byte(userRec("w1", "other")), 0o600)
	if ev := next(t, ch); ev.name != "reset" {
		t.Fatalf("%s %s", ev.name, ev.data)
	}
	ev = next(t, ch)
	snap = Snapshot{}
	json.Unmarshal(ev.data, &snap)
	if ev.name != "snapshot" || len(snap.Messages) != 1 || snap.Messages[0].ID != "w1" {
		t.Fatalf("%s %+v", ev.name, snap)
	}

	// Gone: the agent left the pane.
	r.mu.Lock()
	delete(r.sources, "%3")
	r.mu.Unlock()
	if ev := next(t, ch); ev.name != "reset" || !strings.Contains(string(ev.data), NoAgent) {
		t.Fatalf("%s %s", ev.name, ev.data)
	}
	ev = next(t, ch)
	snap = Snapshot{}
	json.Unmarshal(ev.data, &snap)
	if snap.Unavailable != NoAgent {
		t.Fatalf("%+v", snap)
	}
	// And back.
	r.set("%3", Source{Agent: "claude", SessionID: "t", Path: second})
	ev = next(t, ch)
	snap = Snapshot{}
	json.Unmarshal(ev.data, &snap)
	if ev.name != "snapshot" || snap.Unavailable != "" || len(snap.Messages) != 1 {
		t.Fatalf("%s %+v", ev.name, snap)
	}

	stop()
	deadline := time.Now().Add(2 * time.Second)
	for {
		r.svc.mu.Lock()
		n := len(r.svc.hubs)
		r.svc.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reader left running with nobody listening")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestChatNotice(t *testing.T) {
	r := newRig(t)
	res, err := http.Get(r.srv.URL + "/api/chat/notice")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.Contains(string(b), "Copyright (c) 2026 Lovecast Inc.") {
		t.Fatalf("%d %s", res.StatusCode, b)
	}
}

// A subagent's own conversation: its file beside the main one, all of it
// sidechain lines, read on its own and streamed as it grows.
func TestChatSubagent(t *testing.T) {
	r := newRig(t)
	dir := t.TempDir()
	main := filepath.Join(dir, "s.jsonl")
	os.WriteFile(main, []byte(userRec("u1", "hello")+asstRec("a1", "hi", "end_turn")), 0o600)
	side := func(rec string) string {
		return strings.Replace(rec, `{"type"`, `{"isSidechain":true,"agentId":"abc123","type"`, 1)
	}
	subFile := filepath.Join(dir, "s", "subagents", "agent-abc123.jsonl")
	os.MkdirAll(filepath.Dir(subFile), 0o700)
	os.WriteFile(subFile, []byte(side(userRec("su1", "find the handlers"))+side(asstRec("sa1", "looking", "tool_use"))), 0o600)
	r.set("%3", Source{Agent: "claude", SessionID: "s", Path: main})
	r.set("%4", Source{Agent: "codex", SessionID: "c", Path: main})

	var snap Snapshot
	if c := r.get(t, "/api/chat?pane=%253&subagent=abc123", &snap); c != 200 {
		t.Fatal(c)
	}
	if snap.Subagent != "abc123" || snap.Agent != "claude" || snap.Unavailable != "" || len(snap.Messages) != 2 || snap.Messages[0].ID != "su1" {
		t.Fatalf("%+v", snap)
	}
	// The main conversation is as it was.
	var top Snapshot
	r.get(t, "/api/chat?pane=%253", &top)
	if top.Subagent != "" || len(top.Messages) != 2 || top.Messages[0].ID != "u1" {
		t.Fatalf("%+v", top)
	}
	var full struct {
		Block Block `json:"block"`
	}
	if c := r.get(t, "/api/chat/block?pane=%253&subagent=abc123&id=sa1&i=0", &full); c != 200 || full.Block.Text != "looking" {
		t.Fatalf("%d %+v", c, full)
	}
	for q, want := range map[string]string{"pane=%253&subagent=nope": NoTranscript, "pane=%254&subagent=abc123": UnsupportedAgent} {
		var s Snapshot
		r.get(t, "/api/chat?"+q, &s)
		if s.Unavailable != want {
			t.Fatalf("%s: %+v", q, s)
		}
	}
	// Not a name stui reads a file by.
	if c := r.get(t, "/api/chat?pane=%253&subagent=../x", nil); c != 400 {
		t.Fatal(c)
	}

	ch, cancel := r.stream(t, "%253&subagent=abc123")
	defer cancel()
	if ev := next(t, ch); ev.name != "snapshot" || !strings.Contains(string(ev.data), `"subagent":"abc123"`) {
		t.Fatalf("%s %s", ev.name, ev.data)
	}
	f, _ := os.OpenFile(subFile, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(side(asstRec("sa2", "found them", "end_turn")))
	f.Close()
	for {
		ev := next(t, ch)
		if ev.name == "append" {
			if !strings.Contains(string(ev.data), "found them") {
				t.Fatalf("%s", ev.data)
			}
			break
		}
	}
}
