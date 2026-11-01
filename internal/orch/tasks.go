package orch

import (
	"fmt"
	"strings"
)

// A run's tasks with what each waits for:
// a task starts only once every task it depends on is done. The order is
// stui's to keep, not the coordinator's memory: stui task start refuses a task
// whose dependencies are not done, and stui task wait says when one becomes
// ready.

type Task struct {
	Name string `json:"name"`
	// Tasks that must be done first, by name. They exist when this one is
	// created, so there is never a loop.
	Deps []string `json:"deps,omitempty"`
	// The task file (WriteTask).
	File string `json:"file"`
	// One of chat.Typed (claude, codex, opencode), for stui task start; empty takes the start's --agent.
	Agent string `json:"agent,omitempty"`
	// Paths the worker that takes it may change (owns.go).
	Owns []string `json:"owns,omitempty"`
	// The worker that takes it works in a worktree of its own (worktree.go).
	Worktree bool `json:"worktree,omitempty"`
	// The worker's pane once started.
	Worker  string `json:"worker,omitempty"`
	Created int64  `json:"created"`
}

// Task states.
const (
	TaskWaiting = "waiting" // a dependency is not done
	TaskReady   = "ready"   // may start
	TaskRunning = "running"
	TaskDone    = "done"
	TaskBlocked = "blocked" // its worker reported "blocked:"; what depends on it waits
)

func (r *Run) Task(name string) *Task {
	for i := range r.Tasks {
		if r.Tasks[i].Name == name {
			return &r.Tasks[i]
		}
	}
	return nil
}

// AddTask adds a task; its dependencies must be tasks of the run already.
func (r *Run) AddTask(t Task) error {
	if t.Name == "" {
		return fmt.Errorf("a task needs a name")
	}
	if r.Task(t.Name) != nil {
		return fmt.Errorf("task %q exists", t.Name)
	}
	seen := map[string]bool{}
	for _, d := range t.Deps {
		if d == t.Name {
			return fmt.Errorf("task %q cannot wait for itself", t.Name)
		}
		if r.Task(d) == nil {
			return fmt.Errorf("no task %q to wait for (create it first)", d)
		}
		if seen[d] {
			return fmt.Errorf("%q twice in --deps", d)
		}
		seen[d] = true
	}
	r.Tasks = append(r.Tasks, t)
	return nil
}

// TaskState is where task t stands: from its worker's report in the run, or
// from its dependencies when not started.
func (r *Run) TaskState(t *Task) string {
	if t.Worker != "" {
		w := r.Worker(t.Worker)
		switch {
		case w == nil || !w.Done:
			return TaskRunning
		case strings.HasPrefix(strings.ToLower(strings.TrimSpace(w.Summary)), "blocked"):
			return TaskBlocked
		default:
			return TaskDone
		}
	}
	for _, d := range t.Deps {
		if dt := r.Task(d); dt == nil || r.TaskState(dt) != TaskDone {
			return TaskWaiting
		}
	}
	return TaskReady
}

// Waiting is what task t still waits for: its dependencies not done.
func (r *Run) WaitingFor(t *Task) []string {
	var out []string
	for _, d := range t.Deps {
		if dt := r.Task(d); dt == nil || r.TaskState(dt) != TaskDone {
			out = append(out, d)
		}
	}
	return out
}
