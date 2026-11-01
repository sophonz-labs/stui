package cli

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/sophonz-labs/stui/internal/orch"
)

// workerTree is stui worker diff, merge and clean: a worker's worktree, read,
// taken into this checkout, and removed (orch/worktree.go). It is named by
// its worker or by its own id, <run>/<worker>, which still finds it once
// its worker's pane is gone (orch/trees.go).
func workerTree(a args, verb string, rest []string, asJSON bool) {
	r, w, id := findTree(rest, verb, asJSON)
	wt := w.Worktree
	switch verb {
	case "diff":
		status, diff, err := wt.Changes(a.has("--stat"))
		if err != nil {
			die(asJSON, map[string]any{}, "%v", err)
		}
		if asJSON {
			printJSON(map[string]any{"id": id, "tree": wt.TreeID(), "branch": wt.Branch, "base": wt.Base, "baseKind": wt.BaseKind(), "snapshot": wt.Snapshot, "baseLine": wt.BaseLine(), "path": wt.Path, "status": status, "diff": diff})
			return
		}
		fmt.Printf("  %s (%s) on %s, %s, in %s\n", wt.TreeID(), w.Name, wt.Branch, wt.BaseLine(), wt.Path)
		if status != "" {
			fmt.Println("  not committed:")
			for _, l := range strings.Split(status, "\n") {
				fmt.Println("    " + l)
			}
		}
		if diff != "" {
			fmt.Println(diff)
		}
	case "merge":
		dirty, err := wt.Dirty()
		if err != nil {
			die(asJSON, map[string]any{}, "%v", err)
		}
		if dirty {
			if !a.has("--commit") {
				die(asJSON, map[string]any{"id": id}, "%s has changes not committed in its worktree: ask it to commit (stui worker send), or add --commit to commit them as they are", id)
			}
			msg := "stui worker " + w.Name
			if t := strings.TrimSpace(paneOpt(id, "@stui-task")); t != "" {
				msg += ": " + t
			}
			if err := wt.Commit(msg); err != nil {
				die(asJSON, map[string]any{"id": id}, "commit: %v", err)
			}
		}
		res, err := wt.Merge(a.has("--squash"))
		receipt := map[string]any{"id": id, "branch": wt.Branch, "into": res.Into, "merged": res.Merged, "conflicts": res.Conflicts}
		if err != nil {
			die(asJSON, receipt, "%v", err)
		}
		if len(res.Conflicts) > 0 {
			die(asJSON, receipt, "%s conflicts with %s in %s: nothing was merged. Have the worker bring its branch up to date and resolve them: stui worker send %s \"merge %s into your branch, resolve %s, commit\", then stui worker merge %s again",
				wt.Branch, res.Into, strings.Join(res.Conflicts, ", "), id, res.Into, strings.Join(res.Conflicts, ", "), id)
		}
		if asJSON {
			receipt["ok"] = true
			printJSON(receipt)
		} else if a.has("--squash") {
			fmt.Printf("  %s's %d commits are staged in %s as one change: commit it\n", wt.Branch, res.Commits, res.Into)
		} else {
			fmt.Printf("  merged %s (%d commits) into %s\n", wt.Branch, res.Commits, res.Into)
		}
	case "clean":
		if err := wt.Remove(a.has("--force"), a.has("--keep-branch")); err != nil {
			die(asJSON, map[string]any{"id": id}, "%v (changes not committed or not merged? --force removes them)", err)
		}
		w.Worktree = nil
		r.Save()
		// Its reader has nothing left to read.
		asker := closeAsker(wt.TreeID())
		if asJSON {
			printJSON(map[string]any{"ok": true, "id": id, "tree": wt.TreeID(), "removed": wt.Path, "closed": asker})
		} else {
			fmt.Printf("  removed %s\n", wt.Path)
			if asker != "" {
				fmt.Printf("  closed %s, which was reading it\n", asker)
			}
		}
	}
}

// findTree is the run, worker and id rest[0] names for stui worker verb: a
// worktree's id, or a worker as stui worker takes one.
func findTree(rest []string, verb string, asJSON bool) (*orch.Run, *orch.Worker, string) {
	if len(rest) > 0 {
		rest[0] = treeByName(rest[0])
	}
	if len(rest) > 0 && orch.IsTreeID(rest[0]) {
		r, w := orch.FindTree(rest[0])
		if w == nil {
			die(asJSON, map[string]any{"tree": rest[0]}, "no worktree %s on this machine (stui worker trees lists them)", rest[0])
		}
		return r, w, w.ID
	}
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		rest[0] = resolveWorker(rest[0], asJSON)
	}
	if len(rest) > 0 && isWorkerID(rest[0]) && !strings.HasPrefix(rest[0], "%") {
		die(asJSON, map[string]any{"id": rest[0]}, "%s is on another machine, and so is its worktree: stui worker %s from here is not done yet. Read its result with stui worker read %s", rest[0], verb, rest[0])
	}
	if len(rest) == 0 || !strings.HasPrefix(rest[0], "%") {
		die(asJSON, map[string]any{}, "stui worker %s <id|tree>", verb)
	}
	pane, _ := here(asJSON)
	id := rest[0]
	runID := paneOpt(id, "@stui-run")
	if runID == "" {
		runID = paneOpt(pane, "@stui-run")
	}
	r, err := orch.Load(runID)
	if err != nil {
		die(asJSON, map[string]any{}, "no run for %s: %v", id, err)
	}
	w := r.Worker(id)
	if w == nil || w.Worktree == nil {
		die(asJSON, map[string]any{}, "%s has no worktree (stui worker start --worktree makes one)", id)
	}
	return r, w, id
}

// treeByName is the id of the one worktree whose worker is called name,
// for a worker whose pane is gone (stui worker list no longer has it); name
// itself when it is not a bare name or names no worktree, or two.
func treeByName(name string) string {
	if name == "" || strings.ContainsAny(name, "%/") || strings.HasPrefix(name, "-") {
		return name
	}
	found := ""
	for _, t := range orch.Trees() {
		if t.Worker == name {
			if found != "" {
				return name // two: stui worker's own lookup says which
			}
			found = t.ID
		}
	}
	if found == "" {
		return name
	}
	return found
}

// askerPane is the pane reading worktree tree (stui worker ask), "" for none.
func askerPane(tree string) string {
	out, err := tx("list-panes", "-a", "-F", "#{pane_id}\t#{"+orch.OptAsk+"}")
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

// closeAsker closes the reading agent of worktree tree, if one is open.
func closeAsker(tree string) string {
	if p := askerPane(tree); p != "" {
		if _, err := tx("kill-pane", "-t", p); err == nil {
			return p
		}
	}
	return ""
}

// workerTrees is stui worker trees: every worktree on this machine.
func workerTrees(asJSON bool) {
	trees := orch.Trees()
	askers := map[string]string{}
	if out, err := tx("list-panes", "-a", "-F", "#{pane_id}\t#{"+orch.OptAsk+"}"); err == nil {
		for _, l := range strings.Split(out, "\n") {
			if p, t, ok := strings.Cut(strings.TrimSpace(l), "\t"); ok && t != "" {
				askers[t] = p
			}
		}
	}
	for i := range trees {
		trees[i].Asker = askers[trees[i].ID]
	}
	if asJSON {
		if trees == nil {
			trees = []orch.Tree{}
		}
		printJSON(map[string]any{"trees": trees})
		return
	}
	if len(trees) == 0 {
		fmt.Println("  no worktrees (stui worker start --worktree makes one)")
		return
	}
	for _, t := range trees {
		state := "working"
		if t.Done {
			state = "done"
		}
		if _, err := tx("display-message", "-p", "-t", t.Pane, "#{pane_id}"); err != nil {
			state += ", its pane gone"
		}
		if t.Missing {
			state = "its folder is gone"
		}
		base := fmt.Sprintf("commit %.8s", t.Base)
		if t.BaseKind == "snapshot" {
			base = "snapshot " + time.UnixMilli(t.Snapshot).Format("2006-01-02 15:04")
		}
		fmt.Printf("  %-28s %-9s %s (%s)\n", t.ID, t.Pane, state, base)
		fmt.Printf("  %-28s %s\n", "", t.Path)
		if t.Asker != "" {
			fmt.Printf("  %-28s read by %s (stui worker ask %s)\n", "", t.Asker, t.ID)
		}
	}
}

// workerAsk is stui worker ask <id|tree> ["<question>"] [--agent a]: a
// reading agent in the worktree, in a window of its own (orch/trees.go).
func workerAsk(a args, rest []string, asJSON bool) {
	if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
		die(asJSON, map[string]any{}, "stui worker ask <id|tree> [\"<question>\"] [--agent claude]")
	}
	tree := treeByName(rest[0])
	if !orch.IsTreeID(tree) {
		tree = resolveWorker(tree, asJSON)
		if isWorkerID(tree) && !strings.HasPrefix(tree, "%") {
			die(asJSON, map[string]any{"id": tree}, "%s is on another machine, and so is its worktree: ask it there", tree)
		}
	}
	var q []string
	for _, w := range rest[1:] {
		if strings.HasPrefix(w, "--") {
			break
		}
		q = append(q, w)
	}
	pane, s := here(asJSON)
	body := map[string]any{"tree": tree, "question": strings.Join(q, " ")}
	if agent, ok := a.flag("--agent"); ok {
		body["agent"] = agent
	}
	if pane != "" {
		body["session"] = baseSession(pane)
	}
	var res orch.AskResult
	code, err := apiDo(s, "POST", "/api/orch/ask", body, &res)
	if err != nil || code != 200 {
		why := res.Error
		if err != nil {
			why = err.Error()
		} else if why == "" && code == 404 {
			why = "this stui does not ask yet: a stui server older than this binary is running; stui stop, then try again"
		}
		die(asJSON, map[string]any{"tree": tree}, "not asked (%d): %s", code, why)
	}
	// Typed once the agent's input is there.
	stage, why := res.Stage, ""
	for end := time.Now().Add(95 * time.Second); stage == "deliver" && time.Now().Before(end); time.Sleep(500 * time.Millisecond) {
		var st struct{ Stage, Error string }
		if c, err := apiDo(s, "GET", "/api/orch/ask?pane="+url.QueryEscape(res.Pane), nil, &st); err == nil && c == 200 {
			stage, why = st.Stage, st.Error
		}
	}
	res.Stage, res.Error = stage, why
	if asJSON {
		printJSON(res)
		if stage != "asked" {
			os.Exit(1)
		}
		return
	}
	if stage != "asked" {
		die(false, nil, "the window is open (%s, window %d) but the question was not typed: %s", res.Pane, res.Window, why)
	}
	how := "asked in its window"
	if !res.Reused {
		how = "opened " + res.Agent + " to read it"
	}
	fmt.Printf("  %s: %s, %s window %d (%s)\n  its changes: %s\n", res.Tree, how, res.Session, res.Window, res.Pane, res.File)
}
