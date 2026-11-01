package orch

import (
	"encoding/json"
	"net/http"
	"strings"
)

// serveReply is POST /api/orch/reply {"id","text"}: a person answers a
// question from a client. The answer is from "app".
func serveReply(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
		return
	}
	var body struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.ID == "" || strings.TrimSpace(body.Text) == "" {
		writeJSON(w, 400, map[string]any{"error": `{"id":"<question id>","text":"<answer>"}`})
		return
	}
	ask, ok := FindEvent(body.ID)
	if !ok || ask.Kind != EvAsk {
		writeJSON(w, 404, map[string]any{"error": "no such question"})
		return
	}
	if _, done := ReplyTo(ask); done {
		writeJSON(w, 409, map[string]any{"error": "answered"})
		return
	}
	ev, err := Reply(ask, "", "", "app", body.Text)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "id": ev.ID, "to": ask.From, "waiting": IsWaiting(ask.From)})
}

// serveInbox is GET /api/orch/inbox?pane=%N (the mail owed to that pane,
// not taken) and POST /api/orch/inbox/ack {"pane","ids"} or {"pane","all":
// true} (taken, as stui worker inbox takes it).
func (a *Adopter) serveInbox(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/orch/reply" {
		serveReply(w, r)
		return
	}
	ctx := r.Context()
	out, err := a.o.Tmux(ctx, "list-panes", "-a", "-F", mailFormat)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	owner := mailOwner(parseMailPanes(out), a.o.State)
	if r.URL.Path == "/api/orch/inbox" {
		if r.Method != "GET" {
			writeJSON(w, 405, map[string]any{"error": "method not allowed"})
			return
		}
		pane := r.URL.Query().Get("pane")
		if !paneID.MatchString(pane) {
			writeJSON(w, 400, map[string]any{"error": "pane is %N"})
			return
		}
		mail := Inbox(pane, owner)
		if mail == nil {
			mail = []Mail{}
		}
		writeJSON(w, 200, map[string]any{"pane": pane, "mail": mail})
		return
	}
	if r.Method != "POST" {
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
		return
	}
	var body struct {
		Pane string   `json:"pane"`
		IDs  []string `json:"ids"`
		All  bool     `json:"all"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || !paneID.MatchString(body.Pane) || (!body.All && len(body.IDs) == 0) {
		writeJSON(w, 400, map[string]any{"error": `{"pane":"%N","ids":[…]} or {"pane":"%N","all":true}`})
		return
	}
	want := map[string]bool{}
	for _, id := range body.IDs {
		want[id] = true
	}
	var take []Mail
	for _, m := range Inbox(body.Pane, owner) {
		if body.All || want[m.ID] {
			take = append(take, m)
		}
	}
	acked := Ack(body.Pane, take)
	if acked == nil {
		acked = []string{}
	}
	writeJSON(w, 200, map[string]any{"ok": true, "acked": acked})
}
