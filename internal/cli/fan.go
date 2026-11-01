package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/sophonz-labs/stui/internal/chat"
	"github.com/sophonz-labs/stui/internal/orch"
	"github.com/sophonz-labs/stui/internal/state"
)

// stui worker start --fan, stui worker fan, stui worker pick (orch/fan.go).

func workerFanStart(a args, pane string, s *state.Agent, agentArg, nArg string, asJSON bool) {
	receipt := map[string]any{"stage": "check"}
	n, err := strconv.Atoi(nArg)
	if err != nil || n < 2 || n > 8 {
		die(asJSON, receipt, "--fan takes 2 to 8 workers")
	}
	var agents []string
	for _, x := range strings.Split(agentArg, ",") {
		if x = strings.TrimSpace(x); x != "" {
			if !chat.CanSend(x) {
				die(asJSON, receipt, "--agent %s, or a list of them (claude,codex)", chat.TypedWords())
			}
			agents = append(agents, x)
		}
	}
	if len(agents) == 0 {
		die(asJSON, receipt, "--agent claude, codex, or a list of them (claude,codex)")
	}
	text := readTask(a, asJSON, receipt)
	base, _ := a.flag("--name")
	base = orch.Name(base)
	if base == "" {
		base = orch.Name(orch.FirstLine(text))
	}
	if base == "" {
		base = "fan"
	}
	if len(base) > 20 {
		base = strings.TrimRight(base[:20], "-")
	}
	cwd, hasCwd := a.flag("--cwd")
	var owns []string
	if o, ok := a.flag("--owns"); ok {
		owns = orch.ParseOwns(o)
	}
	// Every member where a single worker would go: --machine puts them all
	// on that machine (each in its own worktree there), as it does one.
	machine, remote := a.flag("--machine")
	var ids []string
	for i := 0; i < n; i++ {
		sp := startSpec{
			agent: agents[i%len(agents)], text: text, name: fmt.Sprintf("%s-%d", base, i+1),
			owns: owns, worktree: true, anySize: a.has("--any-size"), cwd: cwd, hasCwd: hasCwd, fan: base, split: a.has("--split"),
		}
		r := map[string]any{"stage": "check", "fan": base, "of": n}
		if remote {
			ids = append(ids, workerStartRemote(pane, sp, machine, r, asJSON))
		} else {
			ids = append(ids, startWorker(pane, s, sp, r, asJSON))
		}
	}
	if !asJSON {
		fmt.Printf("  fan %s: %s. Compare with stui worker fan %s, pick one with stui worker pick <id>\n", base, strings.Join(ids, ", "), ids[0])
	}
}

func workerFanList(rest []string, asJSON bool) {
	pane, _ := here(asJSON)
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		rest[0] = resolveWorker(rest[0], asJSON)
	}
	var run *orch.Run
	if len(rest) > 0 && strings.HasPrefix(rest[0], "%") {
		run, _ = orch.FindWorker(rest[0])
	} else if id := paneOpt(pane, "@stui-run"); id != "" {
		run, _ = orch.Load(id)
	}
	if run == nil {
		die(asJSON, map[string]any{}, "no run here")
	}
	fans := map[string][]orch.FanMember{}
	var order []string
	for i := range run.Workers {
		w := &run.Workers[i]
		if w.Fan == "" {
			continue
		}
		if len(rest) > 0 && strings.HasPrefix(rest[0], "%") {
			if f, _ := run.FanOf(rest[0]); f != w.Fan {
				continue
			}
		}
		if _, ok := fans[w.Fan]; !ok {
			order = append(order, w.Fan)
		}
		fans[w.Fan] = append(fans[w.Fan], orch.Member(w))
	}
	if asJSON {
		printJSON(map[string]any{"run": run.ID, "fans": fans})
		return
	}
	if len(order) == 0 {
		fmt.Println("  no fans")
		return
	}
	for _, f := range order {
		fmt.Printf("  %s\n", f)
		for _, m := range fans[f] {
			st := "working"
			if m.Done {
				st = "done"
			}
			stat := m.Stat
			if stat == "" {
				stat = "no changes"
			}
			if m.Dirty {
				stat += " (+ not committed)"
			}
			fmt.Printf("    %-5s %-8s %-7s %s\n", m.ID, m.Agent, st, stat)
			if m.Summary != "" {
				fmt.Printf("          %s\n", m.Summary)
			}
		}
	}
}

func workerPick(a args, rest []string, asJSON bool) {
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		rest[0] = resolveWorker(rest[0], asJSON)
	}
	if len(rest) > 0 && isWorkerID(rest[0]) && !strings.HasPrefix(rest[0], "%") {
		die(asJSON, map[string]any{"id": rest[0]}, "%s is on another machine, and so is its worktree: picking it from here is not done yet. Read its result with stui worker read %s", rest[0], rest[0])
	}
	if len(rest) == 0 || !strings.HasPrefix(rest[0], "%") {
		die(asJSON, map[string]any{}, "stui worker pick <id> [--commit] [--keep]")
	}
	here(asJSON)
	run, w := orch.FindWorker(rest[0])
	if w == nil {
		die(asJSON, map[string]any{}, "%s is not a worker in any run", rest[0])
	}
	res, err := run.Pick(rest[0], a.has("--commit"), a.has("--keep"), func(p string) error {
		_, err := tx("kill-pane", "-t", p)
		return err
	})
	if err == nil && res != nil {
		res.CloseReaders(askerPane, func(p string) error { _, err := tx("kill-pane", "-t", p); return err })
	}
	receipt := map[string]any{"id": rest[0], "result": res}
	if err != nil {
		die(asJSON, receipt, "%v", err)
	}
	if res.Merge != nil && len(res.Merge.Conflicts) > 0 {
		die(asJSON, receipt, "%s conflicts with %s in %s: nothing was merged and nothing removed. Have it merge %s into its branch and resolve them, then pick again",
			w.Worktree.Branch, res.Merge.Into, strings.Join(res.Merge.Conflicts, ", "), res.Merge.Into)
	}
	if asJSON {
		receipt["ok"] = true
		printJSON(receipt)
		return
	}
	fmt.Printf("  merged %s (%d commits) into %s\n", w.Worktree.Branch, res.Merge.Commits, res.Merge.Into)
	if len(res.Closed) > 0 {
		fmt.Printf("  closed %s, removed their worktrees\n", strings.Join(res.Closed, ", "))
	}
	if len(res.Readers) > 0 {
		fmt.Printf("  closed %s, which were reading them\n", strings.Join(res.Readers, ", "))
	}
}
