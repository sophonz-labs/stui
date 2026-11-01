package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sophonz-labs/stui/internal/chat"
	"github.com/sophonz-labs/stui/internal/orch"
)

// stui task: a run's tasks and the order they go in. A task waits for the tasks it depends on; stui task start
// refuses it until they are done, and stui task wait says when one is ready.

const taskHelp = `stui task -- what a run does, and in what order

  stui task create <name> --task <file|-> [--deps a,b] [--agent claude|codex|opencode|pi] [--owns "src/x/**,docs/x.md"] [--worktree]
  stui task list [--ready]                     every task and where it stands
  stui task start <name> [--agent claude|codex|opencode|pi] [--split] [--cwd dir]
                                             a worker takes it; refused until its --deps are done
  stui task wait [--timeout 15m]               until a task is ready, a worker asks, or all are done

A task is waiting (a dependency is not done), ready, running, done, or blocked
(its worker reported "blocked:"; what depends on it keeps waiting).
Exit codes of wait: 0 something is ready or all are done, 3 a worker is asking,
4 nothing can move (blocked), 2 timed out. Every command takes --json.
`

func taskCommand(a args) {
	sub := a.after("task")
	asJSON := a.has("--json")
	if len(sub) == 0 {
		fmt.Print(taskHelp)
		return
	}
	switch sub[0] {
	case "create":
		taskCreate(a, sub[1:], asJSON)
	case "list":
		taskList(a, asJSON)
	case "start":
		taskStart(a, sub[1:], asJSON)
	case "wait":
		taskWait(a, asJSON)
	default:
		fmt.Print(taskHelp)
		os.Exit(1)
	}
}

// taskFile is where a task's own text is kept, before a worker's
// obligations are added to its copy.
func taskFile(run, name string) string {
	return filepath.Join(orch.RunDir(run), "tasks", name+".md")
}

func taskCreate(a args, rest []string, asJSON bool) {
	receipt := map[string]any{}
	if why := checkFlags(rest, taskCreateFlags, nil); why != "" {
		die(asJSON, receipt, "%s", why)
	}
	if len(rest) == 0 || strings.HasPrefix(rest[0], "--") {
		die(asJSON, receipt, "stui task create <name> --task <file|-> [--deps a,b]")
	}
	name := orch.Name(rest[0])
	if name == "" {
		die(asJSON, receipt, "a task name is letters, digits and dashes")
	}
	pane, _ := here(asJSON)
	text := readTask(a, asJSON, receipt)
	run, err := ensureRun(pane, orch.FirstLine(text))
	if err != nil {
		die(asJSON, receipt, "%v", err)
	}
	t := orch.Task{Name: name, Created: orch.Now()}
	if d, ok := a.flag("--deps"); ok {
		for _, x := range strings.Split(d, ",") {
			if x = orch.Name(x); x != "" {
				t.Deps = append(t.Deps, x)
			}
		}
	}
	if ag, ok := a.flag("--agent"); ok {
		if !chat.CanSend(ag) {
			die(asJSON, receipt, "--agent %s", chat.TypedWords())
		}
		t.Agent = ag
	}
	if o, ok := a.flag("--owns"); ok {
		t.Owns = orch.ParseOwns(o)
	}
	t.Worktree = a.has("--worktree")
	t.File = taskFile(run.ID, name)
	if err := run.AddTask(t); err != nil {
		die(asJSON, receipt, "%v", err)
	}
	if err := os.MkdirAll(filepath.Dir(t.File), 0o700); err != nil {
		die(asJSON, receipt, "%v", err)
	}
	if err := os.WriteFile(t.File, []byte(text), 0o600); err != nil {
		die(asJSON, receipt, "write the task: %v", err)
	}
	if err := run.Save(); err != nil {
		die(asJSON, receipt, "%v", err)
	}
	st := run.TaskState(run.Task(name))
	if asJSON {
		printJSON(map[string]any{"ok": true, "run": run.ID, "task": name, "state": st, "deps": t.Deps})
	} else {
		fmt.Printf("  task %s (%s)", name, st)
		if len(t.Deps) > 0 {
			fmt.Printf(", after %s", strings.Join(t.Deps, ", "))
		}
		fmt.Println()
	}
}

// taskRun is the run this pane coordinates, with its tasks.
func taskRun(asJSON bool) (string, *orch.Run) {
	pane, _ := here(asJSON)
	id := paneOpt(pane, "@stui-run")
	if id == "" {
		die(asJSON, map[string]any{}, "this pane has no run: stui task create makes one")
	}
	r, err := orch.Load(id)
	if err != nil {
		die(asJSON, map[string]any{}, "run %s: %v", id, err)
	}
	return pane, r
}

func taskRows(r *orch.Run) []map[string]any {
	var rows []map[string]any
	for i := range r.Tasks {
		t := &r.Tasks[i]
		row := map[string]any{"name": t.Name, "state": r.TaskState(t), "deps": t.Deps}
		if w := r.WaitingFor(t); len(w) > 0 {
			row["waitingFor"] = w
		}
		if t.Worker != "" {
			row["worker"] = t.Worker
			if w := r.Worker(t.Worker); w != nil && w.Summary != "" {
				row["summary"] = w.Summary
			}
		}
		if len(t.Owns) > 0 {
			row["owns"] = t.Owns
		}
		rows = append(rows, row)
	}
	return rows
}

func taskList(a args, asJSON bool) {
	_, r := taskRun(asJSON)
	rows := taskRows(r)
	if a.has("--ready") {
		var ready []map[string]any
		for _, row := range rows {
			if row["state"] == orch.TaskReady {
				ready = append(ready, row)
			}
		}
		rows = ready
	}
	if asJSON {
		printJSON(map[string]any{"run": r.ID, "tasks": rows})
		return
	}
	if len(rows) == 0 {
		fmt.Println("  no tasks")
		return
	}
	for _, row := range rows {
		line := fmt.Sprintf("  %-20v %-8v", row["name"], row["state"])
		if w, ok := row["waitingFor"].([]string); ok {
			line += " after " + strings.Join(w, ", ")
		}
		if w, ok := row["worker"]; ok {
			line += fmt.Sprintf(" %v", w)
		}
		if s, ok := row["summary"]; ok {
			line += fmt.Sprintf(" %v", s)
		}
		fmt.Println(line)
	}
}

func taskStart(a args, rest []string, asJSON bool) {
	receipt := map[string]any{"stage": "check"}
	if why := checkFlags(rest, taskStartFlags, taskStartHints); why != "" {
		die(asJSON, receipt, "%s", why)
	}
	if len(rest) == 0 || strings.HasPrefix(rest[0], "--") {
		die(asJSON, receipt, "stui task start <name>")
	}
	pane, r := taskRun(asJSON)
	_, s := here(asJSON)
	t := r.Task(orch.Name(rest[0]))
	if t == nil {
		die(asJSON, receipt, "no task %q (stui task list)", rest[0])
	}
	receipt["task"] = t.Name
	switch st := r.TaskState(t); st {
	case orch.TaskReady:
	case orch.TaskWaiting:
		receipt["waitingFor"] = r.WaitingFor(t)
		die(asJSON, receipt, "task %s waits for %s: not done yet (stui task wait)", t.Name, strings.Join(r.WaitingFor(t), ", "))
	default:
		die(asJSON, receipt, "task %s is %s already (worker %s)", t.Name, st, t.Worker)
	}
	agent := t.Agent
	if ag, ok := a.flag("--agent"); ok {
		agent = ag
	}
	if !chat.CanSend(agent) {
		die(asJSON, receipt, "which agent? --agent %s (or stui task create --agent)", chat.TypedWords())
	}
	text, err := os.ReadFile(t.File)
	if err != nil {
		die(asJSON, receipt, "read the task: %v", err)
	}
	sp := startSpec{agent: agent, text: string(text), name: t.Name, owns: t.Owns, split: a.has("--split"), worktree: t.Worktree || a.has("--worktree")}
	sp.cwd, sp.hasCwd = a.flag("--cwd")
	// The task is the worker's once it has started.
	name := t.Name
	id := startWorker(pane, s, sp, receipt, asJSON)
	if r2, err := orch.Load(r.ID); err == nil {
		if t2 := r2.Task(name); t2 != nil {
			t2.Worker = id
			r2.Save()
		}
	}
}

func taskWait(a args, asJSON bool) {
	_, r := taskRun(asJSON)
	timeout := 15 * time.Minute
	if t, ok := a.flag("--timeout"); ok {
		if d, err := time.ParseDuration(t); err == nil {
			timeout = d
		}
	}
	_, s := here(asJSON)
	end := time.Now().Add(timeout)
	for {
		if r2, err := orch.Load(r.ID); err == nil {
			r = r2
		}
		states := agentPanes(s)
		ready, running, blocked, done := 0, 0, 0, 0
		asking := []string{}
		for i := range r.Tasks {
			t := &r.Tasks[i]
			switch r.TaskState(t) {
			case orch.TaskReady:
				ready++
			case orch.TaskRunning:
				running++
				if states[t.Worker].State == "waiting" {
					asking = append(asking, t.Worker)
				}
			case orch.TaskBlocked:
				blocked++
			case orch.TaskDone:
				done++
			}
		}
		code := -1
		why := ""
		switch {
		case len(r.Tasks) == 0:
			code, why = 0, "no tasks"
		case ready > 0:
			code, why = 0, fmt.Sprintf("%d ready", ready)
		case done == len(r.Tasks):
			code, why = 0, "all done"
		case len(asking) > 0:
			code, why = 3, "a worker asks: "+strings.Join(asking, ", ")
		case running == 0:
			code, why = 4, fmt.Sprintf("nothing can move: %d blocked", blocked)
		case time.Now().After(end):
			code, why = 2, "timed out"
		}
		if code >= 0 {
			if asJSON {
				printJSON(map[string]any{"run": r.ID, "why": why, "tasks": taskRows(r)})
			} else {
				fmt.Println("  " + why)
				for _, row := range taskRows(r) {
					fmt.Printf("  %-20v %v\n", row["name"], row["state"])
				}
			}
			os.Exit(code)
		}
		time.Sleep(2 * time.Second)
	}
}
