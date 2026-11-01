package orch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// Fanning out: one task given to several
// workers at once, each in its own worktree from the same commit; their
// results are compared side by side and one is picked. Picking merges it and
// removes the others' worktrees and windows.

// FanMember is one worker of a fan, with how much it changed.
type FanMember struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Agent   string `json:"agent"`
	Done    bool   `json:"done"`
	Summary string `json:"summary,omitempty"`
	Branch  string `json:"branch,omitempty"`
	// git diff --shortstat from where it started, and files not committed.
	Stat  string `json:"stat"`
	Dirty bool   `json:"dirty"`
}

// FanOf is the fan worker id belongs to in r, its members in start order.
func (r *Run) FanOf(id string) (string, []*Worker) {
	w := r.Worker(id)
	if w == nil || w.Fan == "" {
		return "", nil
	}
	var out []*Worker
	for i := range r.Workers {
		if r.Workers[i].Fan == w.Fan {
			out = append(out, &r.Workers[i])
		}
	}
	return w.Fan, out
}

// Member describes w for a comparison.
func Member(w *Worker) FanMember {
	m := FanMember{ID: w.ID, Name: w.Name, Agent: w.Agent, Done: w.Done, Summary: w.Summary}
	if w.Worktree != nil {
		m.Branch = w.Worktree.Branch
		m.Stat, _ = git(w.Worktree.Path, "diff", "--shortstat", w.Worktree.Base)
		m.Dirty, _ = w.Worktree.Dirty()
	}
	return m
}

// PickResult is what picking did.
type PickResult struct {
	Merge   *MergeResult `json:"merge"`
	Removed []string     `json:"removed"`
	Closed  []string     `json:"closed"`
	// The ids of the worktrees removed (trees.go), and the reading agents
	// that were reading them and were closed with them (stui worker ask).
	Trees   []string `json:"trees,omitempty"`
	Readers []string `json:"readers,omitempty"`
}

// Pick merges worker id's worktree (committing what it left when commit),
// then, unless keep, removes the other fan members' worktrees and closes
// their panes with closePane. On a conflict nothing else happens.
func (r *Run) Pick(id string, commit, keep bool, closePane func(pane string) error) (*PickResult, error) {
	w := r.Worker(id)
	if w == nil || w.Worktree == nil {
		return nil, fmt.Errorf("%s has no worktree", id)
	}
	dirty, err := w.Worktree.Dirty()
	if err != nil {
		return nil, err
	}
	if dirty {
		if !commit {
			return nil, fmt.Errorf("%s has changes not committed: ask it to commit, or pick with commit", id)
		}
		if err := w.Worktree.Commit("stui worker " + w.Name); err != nil {
			return nil, err
		}
	}
	m, err := w.Worktree.Merge(false)
	res := &PickResult{Merge: m}
	if err != nil || len(m.Conflicts) > 0 {
		return res, err
	}
	if keep {
		return res, nil
	}
	_, members := r.FanOf(id)
	for _, o := range members {
		if o.ID == id {
			continue
		}
		if closePane != nil && closePane(o.ID) == nil {
			res.Closed = append(res.Closed, o.ID)
		}
		if o.Worktree != nil && o.Worktree.Remove(true, false) == nil {
			res.Removed = append(res.Removed, o.Worktree.Path)
			res.Trees = append(res.Trees, o.Worktree.TreeID())
			o.Worktree = nil
		}
	}
	return res, r.Save()
}

// FindWorker is the run and record of worker pane, looking in every run
// (the newest first).
func FindWorker(pane string) (*Run, *Worker) {
	entries, _ := os.ReadDir(Dir())
	var best *Run
	for _, e := range entries {
		if !e.IsDir() || !runID.MatchString(e.Name()) {
			continue
		}
		r, err := Load(e.Name())
		if err != nil || r.Worker(pane) == nil {
			continue
		}
		if best == nil || r.Created > best.Created {
			best = r
		}
	}
	if best == nil {
		return nil, nil
	}
	return best, best.Worker(pane)
}

// Changes is one worker's diff and its fan, for GET /api/orch/diff.
type Changes struct {
	// The worktree's own id, <run>/<worker> (trees.go).
	Tree string `json:"tree"`
	// What the diff is from: "commit" (Base, a commit the person knows) or
	// "snapshot" (stui's copy of a folder git does not know, taken at
	// Snapshot, ms), and that said for a person (BaseLine).
	BaseKind string      `json:"baseKind"`
	Snapshot int64       `json:"snapshot,omitempty"`
	BaseLine string      `json:"baseLine"`
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	Run      string      `json:"run"`
	Branch   string      `json:"branch"`
	Base     string      `json:"base"`
	Path     string      `json:"path"`
	Repo     string      `json:"repo"`
	Status   string      `json:"status"`
	Diff     string      `json:"diff"`
	Done     bool        `json:"done"`
	Summary  string      `json:"summary,omitempty"`
	Fan      string      `json:"fan,omitempty"`
	Members  []FanMember `json:"members,omitempty"`
	// The diff was cut at maxDiff bytes.
	Truncated bool `json:"truncated,omitempty"`
}

const maxDiff = 2 << 20

// CloseReaders closes the reading agent of each worktree res removed, with
// readerOf (the pane reading a worktree, "" for none) and closePane: a
// worktree gone with its reader still open would be a window reading
// nothing.
func (res *PickResult) CloseReaders(readerOf func(tree string) string, closePane func(pane string) error) {
	if readerOf == nil || closePane == nil {
		return
	}
	for _, t := range res.Trees {
		if p := readerOf(t); p != "" && closePane(p) == nil {
			res.Readers = append(res.Readers, p)
		}
	}
}

// ServeDiff answers GET /api/orch/diff?pane=%N (or ?tree=<run>/<worker>,
// a worktree whose worker may be gone) and POST /api/orch/pick
// {"pane","commit","keep"}; false for any other path.
// readerOf is the pane reading a worktree (trees.go), for a pick to close
// what read the worktrees it removes; nil closes none.
func ServeDiff(w http.ResponseWriter, r *http.Request, closePane func(ctx context.Context, pane string) error, readerOf func(ctx context.Context, tree string) string) bool {
	switch r.URL.Path {
	case "/api/orch/diff":
		if r.Method != "GET" {
			writeJSON(w, 405, map[string]any{"error": "method not allowed"})
			return true
		}
		pane := r.URL.Query().Get("pane")
		var run *Run
		var wk *Worker
		if tree := r.URL.Query().Get("tree"); tree != "" {
			run, wk = FindTree(tree)
		} else {
			run, wk = FindWorker(pane)
		}
		if wk == nil || wk.Worktree == nil {
			writeJSON(w, 404, map[string]any{"error": "no worker with a worktree there"})
			return true
		}
		status, diff, err := wk.Worktree.Changes(false)
		if err != nil {
			writeJSON(w, 500, map[string]any{"error": err.Error()})
			return true
		}
		c := Changes{
			Tree: wk.Worktree.TreeID(), BaseKind: wk.Worktree.BaseKind(), Snapshot: wk.Worktree.Snapshot, BaseLine: wk.Worktree.BaseLine(),
			ID: wk.ID, Name: wk.Name, Run: run.ID, Branch: wk.Worktree.Branch, Base: wk.Worktree.Base,
			Path: wk.Worktree.Path, Repo: wk.Worktree.Repo, Status: status, Diff: diff, Done: wk.Done, Summary: wk.Summary,
		}
		if len(c.Diff) > maxDiff {
			c.Diff, c.Truncated = c.Diff[:maxDiff], true
		}
		if fan, members := run.FanOf(wk.ID); fan != "" {
			c.Fan = fan
			for _, m := range members {
				c.Members = append(c.Members, Member(m))
			}
		}
		writeJSON(w, 200, c)
		return true
	case "/api/orch/pick":
		if r.Method != "POST" {
			writeJSON(w, 405, map[string]any{"error": "method not allowed"})
			return true
		}
		var body struct {
			Pane   string `json:"pane"`
			Commit bool   `json:"commit"`
			Keep   bool   `json:"keep"`
		}
		if decodeSmall(r, &body) != nil {
			writeJSON(w, 400, map[string]any{"error": "bad body"})
			return true
		}
		run, wk := FindWorker(body.Pane)
		if wk == nil {
			writeJSON(w, 404, map[string]any{"error": "no such worker"})
			return true
		}
		res, err := run.Pick(body.Pane, body.Commit, body.Keep, func(p string) error { return closePane(r.Context(), p) })
		if err == nil && res != nil && readerOf != nil {
			res.CloseReaders(func(t string) string { return readerOf(r.Context(), t) }, func(p string) error { return closePane(r.Context(), p) })
		}
		switch {
		case err != nil:
			writeJSON(w, 409, map[string]any{"error": err.Error(), "result": res})
		case res.Merge != nil && len(res.Merge.Conflicts) > 0:
			writeJSON(w, 409, map[string]any{"error": fmt.Sprintf("%s conflicts with %s in %s, so nothing was merged: have the worker merge %s into its branch and resolve them, then merge again",
				wk.Worktree.Branch, res.Merge.Into, strings.Join(res.Merge.Conflicts, ", "), res.Merge.Into), "result": res})
		default:
			writeJSON(w, 200, map[string]any{"ok": true, "result": res})
		}
		return true
	}
	return false
}

func decodeSmall(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(v)
}
