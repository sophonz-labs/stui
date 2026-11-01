package orch

// A worker's name, changed (stui worker rename). The name is a label: adopt
// takes it from the tmux window title, which is whatever the agent or the
// person set, so it can look like a machine's name and not be one.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MaxNameLen is how long a worker's name may be.
const MaxNameLen = 24

// CheckName is s as a worker's name: lower case, and what is not a letter,
// digit or dash becomes a dash, as Name does with a window title ("iOS CI"
// is ios-ci). It refuses what is left empty, and a name over MaxNameLen
// rather than cut it short without saying so.
func CheckName(s string) (string, error) {
	n := strings.Trim(nameRE.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-"), "-")
	switch {
	case n == "":
		return "", refuse(400, "bad-name", "a name needs a letter or a digit (letters, digits and dashes)")
	case len(n) > MaxNameLen:
		return "", refuse(400, "bad-name", "%q is %d characters; a name is at most %d", n, len(n), MaxNameLen)
	}
	return n, nil
}

// nameHolder is the worker other than id already called name: a worker
// pane here, or a worker on another machine in this machine's run records.
// "" when none is. Two workers with one name answer to neither (stui worker
// send <name> does nothing and lists them), so a rename does not make one.
//
// Only workers that are there count: a pane here that is a worker, and a
// record of a worker on another machine that its machine still has (alive;
// nil counts every record). A record whose pane is gone holds no name.
func nameHolder(ps map[string]pane, runs []*Run, id, name string, alive func(Worker) bool) string {
	for _, p := range ps {
		if p.ID != id && p.Role == RoleWorkerName && strings.EqualFold(p.Name, name) {
			return p.ID
		}
	}
	for _, r := range runs {
		for _, w := range r.Workers {
			if w.Machine != "" && w.ID != id && strings.EqualFold(w.Name, name) && (alive == nil || alive(w)) {
				return w.ID
			}
		}
	}
	return ""
}

// RenameResult is what a rename did.
type RenameResult struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Was  string `json:"was"`
}

// SetName gives worker id (a pane here, or "<machine>/%<pane>", whose
// machine keeps it) a new name. Only @stui-name changes: the tmux window keeps
// its title, which is the person's, and a window may hold more than one
// worker. The run records keep the name too, for a restore to put back.
func (a *Adopter) SetName(ctx context.Context, id, name string) (*RenameResult, error) {
	name, err := CheckName(name)
	if err != nil {
		return nil, err
	}
	ps, err := a.panes(ctx)
	if err != nil {
		return nil, refuse(502, "tmux", "tmux: %v", err)
	}
	runs := Runs()
	res := &RenameResult{ID: id, Name: name}
	if slug, pn, ok := remoteChild(id); ok {
		for _, r := range runs {
			if w := r.Worker(id); w != nil {
				res.Was = w.Name
			}
		}
		if h := nameHolder(ps, runs, id, name, a.remoteAlive(ctx)); h != "" {
			return nil, taken(name, h)
		}
		var got RenameResult
		err := a.remoteCall(ctx, slug, "POST", "/api/orch/workers/"+url.PathEscape(pn)+"/name", map[string]any{"name": name}, &got)
		var ae *AdoptError
		if errors.As(err, &ae) && ae.Status == 405 {
			return nil, refuse(501, "too-old", "%s's stui does not rename workers yet: restart it there", slug)
		}
		if err != nil {
			return nil, err
		}
		if got.Was != "" {
			res.Was = got.Was
		}
	} else {
		if !paneID.MatchString(id) {
			return nil, refuse(400, "bad-pane", "bad pane id")
		}
		p, ok := ps[id]
		if !ok || p.Role != RoleWorkerName {
			return nil, refuse(409, "not-worker", "%s is no one's worker", id)
		}
		res.Was = p.Name
		if h := nameHolder(ps, runs, id, name, a.remoteAlive(ctx)); h != "" {
			return nil, taken(name, h)
		}
		if err := a.set(ctx, id, "@stui-name", name); err != nil {
			return nil, refuse(502, "tmux", "%v", err)
		}
	}
	keepName(runs, id, name)
	if !strings.EqualFold(res.Was, name) {
		ev := Event{Kind: EvRename, To: id, ToName: name, Was: res.Was, Now: name}
		if p, ok := ps[id]; ok {
			ev.Run, ev.ToKey = workRunOf(p), a.tmuxOpt(ctx, id, OptKey)
		}
		for _, r := range runs {
			if w := r.Worker(id); w != nil && ev.Run == "" {
				ev.Run, ev.ToKey, ev.Machine = r.ID, w.Key, w.Machine
			}
		}
		a.treeEvent(ctx, ps, ev)
	}
	return res, nil
}

func taken(name, holder string) error {
	return refuse(409, "name-taken", "%s is already called %q; two workers with one name answer to neither, so pick another", holder, name)
}

// keepName puts the name in every run record that holds worker id.
func keepName(runs []*Run, id, name string) {
	for _, r := range runs {
		if w := r.Worker(id); w != nil && w.Name != name {
			w.Name = name
			r.Save()
		}
	}
}

// renameHere is POST /api/orch/workers/<pane>/name from the machine the
// worker works for: the same checks as SetName, among the workers here.
func (a *Adopter) renameHere(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		Name string `json:"name"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body) != nil {
		writeJSON(w, 400, map[string]any{"error": "bad body"})
		return
	}
	p, err := a.remotePane(r.Context(), id)
	if err != nil {
		writeJSON(w, 404, map[string]any{"error": err.Error()})
		return
	}
	if !askedByParent(r, p) {
		writeJSON(w, 409, map[string]any{"error": fmt.Sprintf("%s is not a worker of %s now (it works for %s)", id, r.Header.Get(FromHeader), p.Parent), "reason": "not-your-worker"})
		return
	}
	res, err := a.SetName(r.Context(), id, body.Name)
	writeResult(w, res, err)
}

// serveName answers POST /api/orch/name {"pane","name"}: stui worker rename,
// for a pane here or "<machine>/%<pane>".
func (a *Adopter) serveName(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
		return
	}
	var body struct {
		Pane string `json:"pane"`
		Name string `json:"name"`
		By   string `json:"by"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body) != nil {
		writeJSON(w, 400, map[string]any{"error": "bad body"})
		return
	}
	res, err := a.SetName(WithBy(r.Context(), body.By), body.Pane, body.Name)
	writeResult(w, res, err)
}

func writeResult(w http.ResponseWriter, res *RenameResult, err error) {
	var ae *AdoptError
	switch {
	case errors.As(err, &ae):
		writeJSON(w, ae.Status, map[string]any{"error": ae.Msg, "reason": ae.Reason})
	case err != nil:
		writeJSON(w, 502, map[string]any{"error": err.Error()})
	default:
		writeJSON(w, 200, map[string]any{"ok": true, "id": res.ID, "name": res.Name, "was": res.Was})
	}
}

// remoteAlive is whether a record's worker on another machine is there:
// its machine has the pane, as a worker for another machine, with the
// record's key when it keeps one, and by the same name. One that cannot be
// asked (unreachable, an stui from before) counts as there.
func (a *Adopter) remoteAlive(ctx context.Context) func(Worker) bool {
	return func(w Worker) bool {
		_, pn, ok := remoteChild(w.ID)
		if !ok || a.o.Remote == nil {
			return true
		}
		cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		code, raw, err := a.o.Remote(cctx, w.Machine, "GET", "/api/orch/workers/"+url.PathEscape(pn), nil)
		if err != nil {
			return true
		}
		if code == 404 {
			return false
		}
		var rw RemoteWorker
		if code != 200 || json.Unmarshal(raw, &rw) != nil {
			return true
		}
		switch {
		case rw.Exited:
			return false
		case w.Key != "" && rw.Key != "" && rw.Key != w.Key:
			return false
		case rw.Name != "" && !strings.EqualFold(rw.Name, w.Name):
			return false
		}
		return true
	}
}
