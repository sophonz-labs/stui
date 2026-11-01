// Package stream serves stui's event feeds -- agent watch, chat, machine
// metrics -- to a client as Server-Sent Events or, when the request asks
// for an upgrade, over a websocket carrying the same events.
//
// Why a websocket at all: some proxies (Cloudflare's quick tunnels among
// them) hold a streaming response back until a buffer fills, whatever the
// headers say, so SSE never arrives live through them. Websockets go
// through, so the same feed is offered as one: a client that cannot read
// SSE live sends Upgrade: websocket to the same path and gets one text
// message per event, {"event","id","data"}.
package stream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Ping is how often an idle stream says it is still there: under a
// proxy's idle cut (Cloudflare's is 100 s). Over SSE it is a ": ping"
// comment, over a websocket a ping frame.
var Ping = 15 * time.Second

// Send writes one event. It returns false once the client is gone.
type Send func(name string, seq int64, data any) bool

// IsWebsocket says whether r asks for the websocket form of a feed.
func IsWebsocket(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("upgrade"), "websocket")
}

// Serve opens the feed on w and calls run with a sender; run returns when
// it has nothing more to send or send returned false. ctx ends when the
// client goes away. Serve pings on its own while run is busy.
func Serve(w http.ResponseWriter, r *http.Request, run func(ctx context.Context, send Send)) {
	if IsWebsocket(r) {
		serveWebsocket(w, r, run)
		return
	}
	serveSSE(w, r, run)
}

type sender struct {
	mu   sync.Mutex
	gone bool
}

func (s *sender) do(fn func() error) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gone {
		return false
	}
	if err := fn(); err != nil {
		s.gone = true
		return false
	}
	return true
}

func encode(data any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.Encode(data)
	return bytes.TrimRight(buf.Bytes(), "\n")
}

func serveSSE(w http.ResponseWriter, r *http.Request, run func(context.Context, Send)) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		w.Header().Set("content-type", "application/json;charset=utf-8")
		w.WriteHeader(500)
		io.WriteString(w, `{"error":"streaming unsupported"}`)
		return
	}
	h := w.Header()
	h.Set("content-type", "text/event-stream; charset=utf-8")
	h.Set("cache-control", "no-store, no-transform")
	h.Set("x-accel-buffering", "no")
	w.WriteHeader(200)
	flusher.Flush()

	s := &sender{}
	send := func(name string, seq int64, data any) bool {
		return s.do(func() error {
			_, err := fmt.Fprintf(w, "event: %s\nid: %d\ndata: %s\n\n", name, seq, encode(data))
			flusher.Flush()
			return err
		})
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go pinger(ctx, cancel, func() bool {
		return s.do(func() error {
			_, err := io.WriteString(w, ": ping\n\n")
			flusher.Flush()
			return err
		})
	})
	run(ctx, send)
}

func serveWebsocket(w http.ResponseWriter, r *http.Request, run func(context.Context, Send)) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// As the terminals: the token or cookie is the gate, not the origin.
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	// The client sends nothing; reading is what notices it closing and
	// answers its pings.
	ctx := conn.CloseRead(r.Context())
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := &sender{}
	send := func(name string, seq int64, data any) bool {
		return s.do(func() error {
			msg := encode(map[string]any{"event": name, "id": seq, "data": data})
			wctx, done := context.WithTimeout(ctx, 30*time.Second)
			defer done()
			return conn.Write(wctx, websocket.MessageText, msg)
		})
	}
	go pinger(ctx, cancel, func() bool {
		// Ping is answered by the read side; not holding the send lock
		// while waiting keeps events flowing.
		pctx, done := context.WithTimeout(ctx, 30*time.Second)
		defer done()
		return conn.Ping(pctx) == nil
	})
	run(ctx, send)
	conn.Close(websocket.StatusNormalClosure, "")
}

// pinger pings every Ping until ctx ends or a ping fails, which ends ctx.
func pinger(ctx context.Context, cancel context.CancelFunc, ping func() bool) {
	t := time.NewTicker(Ping)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !ping() {
				cancel()
				return
			}
		}
	}
}
