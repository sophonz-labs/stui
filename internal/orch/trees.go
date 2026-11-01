package orch

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A worktree opened by its own id, <run>/<worker>: read, asked about, merged and removed whether its worker
// is working, done, or its pane long gone. The record stays the worker's
// (run.json); the id is how it is found without the worker's pane.
//
// Asking is a reading agent of its own, in a window opened in the worktree
// with the worktree's changes in its first prompt. It is not the worker:
// nothing is typed into the worker's pane, so a turn in progress goes on,
// and a worktree whose worker ended can still be asked. Nor is it a worker
// of the run -- no @stui-run, no record -- so it is never told as stopping
// short or counted. Its pane carries @stui-ask <tree id>, and asking the same
// worktree again types into that window instead of opening another.

// OptAsk marks a reading agent's pane with the worktree it reads.
const OptAsk = "@stui-ask"

var treeID = regexp.MustCompile(`^(r-[0-9a-f]{8})/([a-z0-9][a-z0-9-]{0,63})$`)

// IsTreeID says whether s is a worktree's id, <run>/<worker>.
func IsTreeID(s string) bool { return treeID.MatchString(s) }

// Tree is one worktree, for stui worker trees and GET /api/orch/trees.
type Tree struct {
	ID     string `json:"id"`
	Run    string `json:"run"`
	Worker string `json:"worker"` // its worker's name
	Pane   string `json:"pane"`   // its worker's pane, which may be gone
	Agent  string `json:"agent,omitempty"`
	Branch string `json:"branch"`
	Base   string `json:"base"`
	// "commit" or "snapshot", and when the snapshot was taken (ms).
	BaseKind string `json:"baseKind"`
	Snapshot int64  `json:"snapshot,omitempty"`
	Path     string `json:"path"`
	Dir      string `json:"dir"`
	Repo     string `json:"repo"`
	Done     bool   `json:"done"`
	Summary  string `json:"summary,omitempty"`
	Fan      string `json:"fan,omitempty"`
	// The pane of the agent reading it (stui worker ask), "" for none.
	Asker string `json:"asker,omitempty"`
	// The worktree's folder is not there any more.
	Missing bool `json:"missing,omitempty"`
}

func worktreeOf(r *Run, w *Worker) Tree {
	t := w.Worktree
	return Tree{
		ID: t.TreeID(), Run: r.ID, Worker: w.Name, Pane: w.ID, Agent: w.Agent, Branch: t.Branch, Base: t.Base,
		BaseKind: t.BaseKind(), Snapshot: t.Snapshot, Path: t.Path, Dir: t.Dir, Repo: t.Repo,
		Done: w.Done, Summary: w.Summary, Fan: w.Fan, Missing: !exists(t.Path),
	}
}

// Trees is every worktree in this machine's runs, the newest run first.
func Trees() []Tree {
	entries, _ := os.ReadDir(Dir())
	var runs []*Run
	for _, e := range entries {
		if !e.IsDir() || !runID.MatchString(e.Name()) {
			continue
		}
		if r, err := Load(e.Name()); err == nil {
			runs = append(runs, r)
		}
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].Created > runs[j].Created })
	var out []Tree
	for _, r := range runs {
		for i := range r.Workers {
			if r.Workers[i].Worktree != nil {
				out = append(out, worktreeOf(r, &r.Workers[i]))
			}
		}
	}
	return out
}

// FindTree is the run and worker of worktree id (<run>/<worker>), or of
// the worker in pane id; nil when there is none.
func FindTree(id string) (*Run, *Worker) {
	if m := treeID.FindStringSubmatch(id); m != nil {
		r, err := Load(m[1])
		if err != nil {
			return nil, nil
		}
		for i := range r.Workers {
			if t := r.Workers[i].Worktree; t != nil && t.TreeID() == id {
				return r, &r.Workers[i]
			}
		}
		return nil, nil
	}
	r, w := FindWorker(id)
	if w == nil || w.Worktree == nil {
		return nil, nil
	}
	return r, w
}

// BaseLine says what a worktree's diff is from, for a person: the commit,
// or the snapshot and when.
func (w *Worktree) BaseLine() string {
	if w.Shadow == "" {
		return fmt.Sprintf("from commit %.8s", w.Base)
	}
	return fmt.Sprintf("from stui's snapshot of %s at %s (not a git repository)", w.Repo, time.UnixMilli(w.Snapshot).Format("2006-01-02 15:04:05"))
}

// AskRequest is POST /api/orch/ask.
type AskRequest struct {
	Tree     string `json:"tree"` // a worktree id, or a worker's pane
	Question string `json:"question"`
	// The agent to read it with; none: its worker's, else claude.
	Agent string `json:"agent"`
	// The session to open its window in; none: the run's.
	Session string `json:"session"`
}

// AskResult is where the question went.
type AskResult struct {
	Tree    string `json:"tree"`
	Pane    string `json:"pane"`
	Window  int    `json:"window"`
	Session string `json:"session"`
	Agent   string `json:"agent"`
	// The window was already open, and the question was typed into it.
	Reused bool `json:"reused"`
	// The file of the worktree's changes the agent was pointed at.
	File string `json:"file"`
	// "deliver" while it is being typed, then "asked" or "failed".
	Stage string `json:"stage"`
	Error string `json:"error,omitempty"`
}

// readOnly is agent run so it reads and does not write, for the agents
// whose flag for that is known; any other command is run as given.
func readOnly(agent string) string {
	switch agent {
	case "claude":
		return "claude --permission-mode plan"
	case "codex":
		return "codex --sandbox read-only"
	case "pi":
		// Only its reading tools (Pi's own example of a review run);
		// MCP tools are not declared to it either.
		return "pi --tools read,grep,find,ls"
	}
	return agent
}

// askFile writes what tree has changed for its reader to read, and gives
// the path the reader is pointed at (a copy inside the worktree when there
// can be one: projectcopy.go).
func askFile(r *Run, w *Worker) (string, error) {
	t := w.Worktree
	status, diff, err := t.Changes(false)
	if err != nil {
		return "", err
	}
	if len(diff) > maxDiff {
		diff = diff[:maxDiff] + "\n[cut at 2 MiB]\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# What %s changed\n\nWorktree %s (id %s), branch %s, %s.\n", w.Name, t.Path, t.TreeID(), t.Branch, t.BaseLine())
	if w.Summary != "" {
		fmt.Fprintf(&b, "\nIts worker's own summary: %s\n", w.Summary)
	}
	b.WriteString("\n## Not committed (git status --short)\n\n```\n")
	if status == "" {
		status = "(nothing)"
	}
	b.WriteString(status + "\n```\n\n## The diff, committed or not\n\n```diff\n" + diff + "\n```\n")
	rel := filepath.Join("ask", w.Name+".md")
	file := filepath.Join(RunDir(r.ID), rel)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(file, []byte(b.String()), 0o600); err != nil {
		return "", err
	}
	return Readable(ProjectCopy(t.Dir, r.ID, rel, []byte(b.String())), file), nil
}

// askerOf is the pane already reading tree, "" for none.
func (a *Adopter) askerOf(ctx context.Context, tree string) string {
	out, err := a.o.Tmux(ctx, "list-panes", "-a", "-F", "#{pane_id}\t#{"+OptAsk+"}")
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(out, "\n") {
		if p, t, ok := strings.Cut(strings.TrimSpace(l), "\t"); ok && t == tree {
			return p
		}
	}
	return ""
}

// Ask puts req's question to a reading agent in its worktree, opening the
// window for it unless one is open already. It never types into the
// worktree's worker. The question is typed in the background; AskStage
// says how that went.
func (a *Adopter) Ask(ctx context.Context, req AskRequest) (*AskResult, int, error) {
	run, wk := FindTree(req.Tree)
	if wk == nil {
		return nil, 404, fmt.Errorf("no worktree %s on this machine (stui worker trees lists them)", req.Tree)
	}
	t := wk.Worktree
	if !exists(t.Dir) {
		return nil, 410, fmt.Errorf("the worktree %s is not there any more", t.Path)
	}
	file, err := askFile(run, wk)
	if err != nil {
		return nil, 500, fmt.Errorf("could not read the worktree's changes: %w", err)
	}
	q := strings.TrimSpace(req.Question)
	res := &AskResult{Tree: t.TreeID(), File: file, Stage: "deliver"}

	a.mu.Lock()
	defer a.mu.Unlock()
	var msg string
	if p := a.askerOf(ctx, res.Tree); p != "" {
		res.Pane, res.Reused = p, true
		if out, err := a.o.Tmux(ctx, "display-message", "-p", "-t", p, "#{window_index}\t#{session_name}"); err == nil {
			w, s, _ := strings.Cut(strings.TrimSpace(out), "\t")
			res.Window, _ = strconv.Atoi(w)
			res.Session = s
		}
		if q == "" {
			res.Stage = "asked" // nothing to type: the window is there
			return res, 200, nil
		}
		msg = fmt.Sprintf("What the worktree has changed, as of now, is in %s again (it may have moved on). %s", file, q)
	} else {
		agent := req.Agent
		if agent == "" {
			agent = wk.Agent
		}
		if agent == "" {
			agent = "claude"
		}
		res.Agent = agent
		sess := req.Session
		if sess == "" {
			sess = run.Session
		}
		if sess == "" {
			sess = "stui"
		}
		res.Session = sess
		name := "ask-" + wk.Name
		if len(name) > 24 {
			name = name[:24]
		}
		format := "#{pane_id}\t#{window_index}"
		var args []string
		if _, err := a.o.Tmux(ctx, "has-session", "-t", "="+sess); err != nil {
			args = []string{"new-session", "-d", "-P", "-F", format, "-s", sess, "-n", name, "-c", t.Dir}
		} else {
			args = []string{"new-window", "-d", "-P", "-F", format, "-t", "=" + sess + ":", "-n", name, "-c", t.Dir}
		}
		out, err := a.o.Tmux(ctx, append(args, readOnly(agent))...)
		if err != nil {
			return nil, 500, fmt.Errorf("could not open a window to ask in: %w", err)
		}
		id, win, _ := strings.Cut(strings.TrimSpace(out), "\t")
		res.Pane = id
		res.Window, _ = strconv.Atoi(win)
		if _, err := a.o.Tmux(ctx, "set-option", "-p", "-t", id, OptAsk, res.Tree); err != nil {
			a.o.Tmux(ctx, "kill-pane", "-t", id)
			return nil, 500, err
		}
		then := "Then say in a few lines what changed, and wait for questions."
		if q != "" {
			then = "Then answer this: " + q
		}
		msg = fmt.Sprintf("You are here to answer questions about the changes in this worktree, not to change it: do not edit, commit or merge anything. It is worker %s's worktree %s, branch %s, %s. What it changed (files not committed, then the diff) is in %s ; read that first. %s",
			wk.Name, t.Path, t.Branch, t.BaseLine(), file, then)
	}
	if a.asking == nil {
		a.asking = map[string]*remoteStart{}
	}
	st := &remoteStart{stage: "deliver"}
	a.asking[res.Pane] = st
	go a.deliverAsk(res.Pane, msg, st)
	return res, 200, nil
}

// deliverAsk types msg once the reading agent's input is there.
func (a *Adopter) deliverAsk(pane, msg string, st *remoteStart) {
	set := func(stage, err string) {
		a.mu.Lock()
		st.stage, st.err = stage, err
		a.mu.Unlock()
	}
	if a.o.Deliver == nil {
		set("failed", "this stui cannot type into an agent")
		return
	}
	ctx := context.Background()
	var last string
	for end := time.Now().Add(90 * time.Second); time.Now().Before(end); time.Sleep(time.Second) {
		code, res := a.o.Deliver(ctx, pane, msg)
		if code == 200 && res["delivered"] != nil {
			set("asked", "")
			return
		}
		last = fmt.Sprintf("%d %v", code, res["error"])
		if _, err := a.o.Tmux(ctx, "display-message", "-p", "-t", pane, "#{pane_id}"); err != nil {
			set("failed", "the reading agent exited before the question was typed: "+last)
			return
		}
	}
	set("failed", "the question was not typed in 90 s: "+last)
}

// AskStage is how the question typed into pane went: "deliver", "asked"
// or "failed" with why; "" for a pane no question was put to.
func (a *Adopter) AskStage(pane string) (stage, why string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if st := a.asking[pane]; st != nil {
		return st.stage, st.err
	}
	return "", ""
}

// serveTrees answers GET /api/orch/trees, POST /api/orch/ask (AskRequest)
// and GET /api/orch/ask?pane=%N; false for any other path.
func (a *Adopter) serveTrees(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/api/orch/trees":
		if r.Method != "GET" {
			writeJSON(w, 405, map[string]any{"error": "method not allowed"})
			return true
		}
		trees := Trees()
		if len(trees) > 0 {
			askers := map[string]string{}
			if out, err := a.o.Tmux(r.Context(), "list-panes", "-a", "-F", "#{pane_id}\t#{"+OptAsk+"}"); err == nil {
				for _, l := range strings.Split(out, "\n") {
					if p, t, ok := strings.Cut(strings.TrimSpace(l), "\t"); ok && t != "" {
						askers[t] = p
					}
				}
			}
			for i := range trees {
				trees[i].Asker = askers[trees[i].ID]
			}
		}
		if trees == nil {
			trees = []Tree{}
		}
		writeJSON(w, 200, map[string]any{"trees": trees})
		return true
	case "/api/orch/ask":
		if r.Method == "GET" {
			pane := r.URL.Query().Get("pane")
			stage, why := a.AskStage(pane)
			if stage == "" {
				writeJSON(w, 404, map[string]any{"error": "no question was put to " + pane})
				return true
			}
			writeJSON(w, 200, map[string]any{"pane": pane, "stage": stage, "error": why})
			return true
		}
		if r.Method != "POST" {
			writeJSON(w, 405, map[string]any{"error": "method not allowed"})
			return true
		}
		var req AskRequest
		if decodeSmall(r, &req) != nil {
			writeJSON(w, 400, map[string]any{"error": "bad body"})
			return true
		}
		res, code, err := a.Ask(r.Context(), req)
		if err != nil {
			writeJSON(w, code, map[string]any{"error": err.Error()})
			return true
		}
		writeJSON(w, 200, res)
		return true
	}
	return false
}
