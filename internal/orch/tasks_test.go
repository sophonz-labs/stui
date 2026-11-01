package orch

import "testing"

func TestTaskOrder(t *testing.T) {
	r := &Run{ID: "r-00000000"}
	if err := r.AddTask(Task{Name: "schema"}); err != nil {
		t.Fatal(err)
	}
	if err := r.AddTask(Task{Name: "api", Deps: []string{"schema"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.AddTask(Task{Name: "ui", Deps: []string{"api", "schema"}}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Task{{Name: "api"}, {Name: "x", Deps: []string{"nope"}}, {Name: "y", Deps: []string{"y"}}, {Name: "z", Deps: []string{"api", "api"}}, {}} {
		if r.AddTask(bad) == nil {
			t.Errorf("took %+v", bad)
		}
	}
	state := func(n string) string { return r.TaskState(r.Task(n)) }
	if state("schema") != TaskReady || state("api") != TaskWaiting || state("ui") != TaskWaiting {
		t.Fatal(state("schema"), state("api"), state("ui"))
	}
	// schema runs, then is done: api is ready, ui still waits for it.
	r.Task("schema").Worker = "%1"
	r.Workers = append(r.Workers, Worker{ID: "%1"})
	if state("schema") != TaskRunning || state("api") != TaskWaiting {
		t.Fatal(state("schema"), state("api"))
	}
	r.Worker("%1").Done = true
	if state("schema") != TaskDone || state("api") != TaskReady || state("ui") != TaskWaiting {
		t.Fatal(state("schema"), state("api"), state("ui"))
	}
	if w := r.WaitingFor(r.Task("ui")); len(w) != 1 || w[0] != "api" {
		t.Fatal(w)
	}
	// A blocked task holds back what depends on it.
	r.Task("api").Worker = "%2"
	r.Workers = append(r.Workers, Worker{ID: "%2", Done: true, Summary: "blocked: no schema access"})
	if state("api") != TaskBlocked || state("ui") != TaskWaiting {
		t.Fatal(state("api"), state("ui"))
	}
}
