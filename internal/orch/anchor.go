package orch

// What a worker on another machine is known by when pane ids change.
//
// A worker on machine B of a coordinator on A is "<B>/%7" in A's record,
// and B's pane says its parent is "<A>/%14". Both are pane ids, which a tmux
// server hands out again after a restart. After A is rebooted, B's pane
// may still point at A's %14 -- by then another session's agent, in
// another run -- so what it reported would be typed there.
//
// So both sides keep a lasting key (rebind.go) beside the id. A keeps its
// parent pane's key (Worker.ParentKey) and B's pane's (Worker.Key); B gives
// its pane a key when it is adopted, and answers GET
// /api/orch/workers/by-key/<key> with the pane that has it now. A parent
// that no pane here has the key of is lost: nothing is typed into a pane
// for having its old number, and stui orch status says so for a person to
// adopt it again.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// PaneAnchor is what says which run a pane is in, and which pane it is.
type PaneAnchor struct {
	Key, Run, InRun string
}

// AnchorFormat is the list-panes format ParseAnchors reads.
const AnchorFormat = "#{pane_id}\t#{" + OptKey + "}\t#{@stui-run}\t#{" + OptInRun + "}"

// ParseAnchors reads list-panes -F AnchorFormat.
func ParseAnchors(out string) map[string]PaneAnchor {
	ps := map[string]PaneAnchor{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Split(l, "\t")
		if len(f) < 4 || !paneID.MatchString(f[0]) {
			continue
		}
		if _, seen := ps[f[0]]; !seen {
			ps[f[0]] = PaneAnchor{Key: f[1], Run: f[2], InRun: f[3]}
		}
	}
	return ps
}

func (a *Adopter) anchors(ctx context.Context) map[string]PaneAnchor {
	if a.o.Tmux == nil {
		return nil
	}
	out, err := a.o.Tmux(ctx, "list-panes", "-a", "-F", AnchorFormat)
	if err != nil {
		return nil
	}
	return ParseAnchors(out)
}

// ParentHere is the pane here that w, a worker of r on another machine,
// reports to now, and false when there is none: its parent's key is on no
// pane, or (a record from before keys) the pane its id names is in no run
// of r's -- its number handed to another.
func (r *Run) ParentHere(w *Worker, ps map[string]PaneAnchor) (string, bool) {
	inRun := func(id string) bool {
		p, ok := ps[id]
		return ok && (p.Run == r.ID || p.InRun == r.ID)
	}
	// By key, and in r: a pane with the key that works in another run now
	// (its agent taken into a new run) is not r's to report to.
	byKey := func(key string) (string, bool) {
		for id, p := range ps {
			if p.Key == key {
				return id, inRun(id)
			}
		}
		return "", false
	}
	if w.ParentKey != "" {
		return byKey(w.ParentKey)
	}
	parent := w.Parent
	if parent == "" || parent == r.Coordinator {
		if r.CoordinatorKey != "" {
			return byKey(r.CoordinatorKey)
		}
		parent = r.Coordinator
	}
	if !inRun(parent) {
		return "", false
	}
	if k := ps[parent].Key; k != "" && parent == r.Coordinator && r.CoordinatorKey != "" && k != r.CoordinatorKey {
		return "", false
	}
	return parent, true
}

// keyPane is the pane here with lasting key key, "" when none has it.
func (a *Adopter) keyPane(ctx context.Context, key string) string {
	if key == "" {
		return ""
	}
	for id, p := range a.anchors(ctx) {
		if p.Key == key {
			return id
		}
	}
	return ""
}

// ensureKey is pane id's lasting key, given one when it has none.
func (a *Adopter) ensureKey(ctx context.Context, id string) string {
	if k := a.tmuxOpt(ctx, id, OptKey); k != "" {
		return k
	}
	k := NewKey()
	if a.set(ctx, id, OptKey, k) != nil {
		return ""
	}
	return k
}

// serveByKey answers GET /api/orch/workers/by-key/<key> on B: the pane
// that is a worker for another machine and has that key now. A asks it
// when the pane its record names is gone or is another one.
func (a *Adopter) serveByKey(w http.ResponseWriter, r *http.Request, key string) {
	if r.Method != "GET" {
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
		return
	}
	key, _ = url.PathUnescape(key)
	id := a.keyPane(r.Context(), key)
	if id == "" {
		writeJSON(w, 404, map[string]any{"error": "no pane has that key", "reason": "gone"})
		return
	}
	if _, err := a.remotePane(r.Context(), id); err != nil {
		writeJSON(w, 404, map[string]any{"error": err.Error(), "reason": "gone"})
		return
	}
	writeJSON(w, 200, map[string]any{"pane": id, "key": key})
}

// follow is where worker w of run on machine slug is now, by its key: the
// new "<slug>/%N" when its pane there changed, "" when it did not or that
// cannot be told (no key, an stui there from before keys, unreachable).
func (a *Adopter) follow(ctx context.Context, slug, key string) string {
	if key == "" || a.o.Remote == nil {
		return ""
	}
	code, raw, err := a.o.Remote(ctx, slug, "GET", "/api/orch/workers/by-key/"+url.PathEscape(key), nil)
	if err != nil || code != 200 {
		return ""
	}
	var got struct {
		Pane string `json:"pane"`
	}
	if json.Unmarshal(raw, &got) != nil || !paneID.MatchString(got.Pane) {
		return ""
	}
	return slug + "/" + got.Pane
}

// moveRemote writes that worker from of run is to now in its record.
func (a *Adopter) moveRemote(run, from, to string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, err := Load(run)
	if err != nil {
		return
	}
	if w := r.Worker(from); w != nil && r.Worker(to) == nil {
		w.ID = to
		r.Save()
	}
}
