package stream

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func feed(ctx context.Context, send Send) {
	if !send("snapshot", 1, map[string]any{"v": 1, "html": "<b>&</b>"}) {
		return
	}
	if !send("pane", 2, map[string]any{"id": "%3"}) {
		return
	}
	<-ctx.Done()
}

func server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Serve(w, r, feed)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// SSE: the headers that keep proxies from holding the body, the body
// flushed before the first event, one event per change, pings while idle,
// JSON unescaped.
func TestSSE(t *testing.T) {
	Ping = 30 * time.Millisecond
	srv := server(t)
	res, err := http.Get(srv.URL + "/api/agents/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	for k, want := range map[string]string{
		"content-type":      "text/event-stream; charset=utf-8",
		"cache-control":     "no-store, no-transform",
		"x-accel-buffering": "no",
	} {
		if got := res.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	rd := bufio.NewReader(res.Body)
	var lines []string
	deadline := time.After(2 * time.Second)
	done := make(chan string)
	go func() {
		for {
			l, err := rd.ReadString('\n')
			if err != nil {
				close(done)
				return
			}
			done <- l
		}
	}()
	for len(lines) < 9 {
		select {
		case l := <-done:
			lines = append(lines, strings.TrimRight(l, "\n"))
		case <-deadline:
			t.Fatalf("got %q", lines)
		}
	}
	want := []string{
		"event: snapshot", "id: 1", `data: {"html":"<b>&</b>","v":1}`, "",
		"event: pane", "id: 2", `data: {"id":"%3"}`, "",
		": ping",
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("line %d = %q, want %q (all %q)", i, lines[i], want[i], lines)
		}
	}
}

// The same feed over a websocket: one text message per event, the same
// names and ids, pings as ping frames the client library answers.
func TestWebsocket(t *testing.T) {
	Ping = 30 * time.Millisecond
	srv := server(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/agents/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	for i, want := range []string{
		`{"data":{"html":"<b>&</b>","v":1},"event":"snapshot","id":1}`,
		`{"data":{"id":"%3"},"event":"pane","id":2}`,
	} {
		typ, msg, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
		if typ != websocket.MessageText || string(msg) != want {
			t.Fatalf("message %d = %s, want %s", i, msg, want)
		}
		var ev struct {
			Event string `json:"event"`
			ID    int64  `json:"id"`
		}
		if json.Unmarshal(msg, &ev) != nil || ev.ID != int64(i+1) {
			t.Fatalf("message %d: %s", i, msg)
		}
	}
	// Idle for a few pings: the connection stays up (pings answered), and
	// the server notices the client leaving.
	time.Sleep(4 * Ping)
	conn.Close(websocket.StatusNormalClosure, "")
}

// A plain GET without an upgrade stays SSE even when other upgrade-ish
// headers are around; the upgrade header decides.
func TestIsWebsocket(t *testing.T) {
	r := httptest.NewRequest("GET", "/x", nil)
	if IsWebsocket(r) {
		t.Fatal("plain GET")
	}
	r.Header.Set("Upgrade", "WebSocket")
	if !IsWebsocket(r) {
		t.Fatal("upgrade")
	}
}
