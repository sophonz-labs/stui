package metrics

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/sophonz-labs/stui/internal/stream"
)

// ServeHTTP answers GET /api/metrics and GET /api/metrics/stream and says
// whether r was one of them. The caller has already authenticated r.
func (b *Buffer) ServeHTTP(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/api/metrics":
	case "/api/metrics/stream":
	default:
		return false
	}
	if r.Method != "GET" {
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
		return true
	}
	if r.URL.Path == "/api/metrics" {
		writeJSON(w, 200, b.Snapshot())
	} else {
		b.stream(w, r)
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("content-type", "application/json;charset=utf-8")
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// stream is one snapshot event and then a sample event per sample, each
// with its seq as the id. A reader that comes back reconnects and starts
// again from a snapshot; there is no resuming. SSE
// or, with an upgrade, a websocket (internal/stream says why).
func (b *Buffer) stream(w http.ResponseWriter, r *http.Request) {
	stream.Serve(w, r, func(ctx context.Context, send stream.Send) {
		snap, events, cancel := b.Subscribe()
		defer cancel()
		if !send("snapshot", snap.Seq, snap) {
			return
		}
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-events:
				if !ok || !send("sample", ev.Seq, ev.Sample) {
					return
				}
			}
		}
	})
}
