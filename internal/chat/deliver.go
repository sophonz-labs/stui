package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
)

// Deliver types text into pane's agent as POST /api/chat/send does, for stui
// itself (orchestration telling an agent something). The status and the
// answer that route would give; 501 when stui cannot type here.
func (s *Service) Deliver(ctx context.Context, pane, text string) (int, map[string]any) {
	return s.deliver(ctx, pane, text, "")
}

// DeliverOver is Deliver into a box that holds exactly own -- stui's own
// line from before, typed and never submitted -- which it types over. A
// box with anything else in it is refused as ever (busy-input).
func (s *Service) DeliverOver(ctx context.Context, pane, text, own string) (int, map[string]any) {
	return s.deliver(ctx, pane, text, own)
}

func (s *Service) deliver(ctx context.Context, pane, text, own string) (int, map[string]any) {
	if !s.sends() {
		return 501, map[string]any{"error": "stui cannot type into panes here"}
	}
	b, _ := json.Marshal(map[string]any{"text": text})
	r := httptest.NewRequest("POST", "/api/chat/send?pane="+url.QueryEscape(pane), bytes.NewReader(b)).WithContext(ctx)
	r = r.WithContext(context.WithValue(ctx, replaceKey{}, own))
	w := httptest.NewRecorder()
	if !paneRE.MatchString(pane) {
		return 400, map[string]any{"error": "bad pane"}
	}
	s.send(w, r, pane)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// replaceKey carries DeliverOver's own line to send: only stui's own calls
// can say it, never a request's body.
type replaceKey struct{}
