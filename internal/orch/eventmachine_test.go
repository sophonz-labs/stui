package orch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// An event's Machine is the machine of its remote side, which depends on
// the kind: To for a send (cli/remote.go), From for a done or idle from a
// worker on another machine. %8 is a pane here, not on ci-mac.
func TestEventMachineIsTheRemoteSide(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	run := &Run{ID: NewID(), Coordinator: "%8", Created: Now()}
	run.Workers = []Worker{
		{ID: "ci-mac/%34", Name: "action-runner", Machine: "ci-mac", Parent: "%8", Adopted: true},
		{ID: "ci-mac/%35", Name: "sim", Machine: "ci-mac", Parent: "%8", Adopted: true},
	}
	if err := run.Save(); err != nil {
		t.Fatal(err)
	}
	ended := time.Now().Add(-10 * time.Second).UnixMilli()
	// %8 is in the run here.
	tm := newFake([3]string{"%8", "app", "lead"})
	tm.opts["%8"]["@stui-run"] = run.ID
	a := NewAdopter(AdoptOptions{
		Tmux: tm.run,
		Self: func() string { return "home-pc" },
		Remote: func(_ context.Context, slug, _, path string, _ any) (int, []byte, error) {
			if slug != "ci-mac" {
				t.Errorf("asked %s", slug)
			}
			rw := RemoteWorker{Done: true, Summary: "built it"}
			if strings.HasSuffix(path, "%2535") {
				rw = RemoteWorker{State: "done", Since: ended, Sent: ended - 1000}
			}
			b, _ := json.Marshal(rw)
			return 200, b, nil
		},
	})
	a.askElsewhere(context.Background(), map[string]mailPane{"%8": {ID: "%8", Role: RoleCoordinatorName, Name: "mobile"}})

	got := map[string]Event{}
	for _, ev := range ReadEvents(run.ID) {
		got[ev.Kind+" "+ev.From] = ev
	}
	for _, k := range []string{EvDone + " ci-mac/%34", EvIdle + " ci-mac/%35"} {
		ev, ok := got[k]
		if !ok {
			t.Fatalf("no %s in %v", k, got)
		}
		t.Logf("%s: from=%s to=%s machine=%s", ev.Kind, ev.From, ev.To, ev.Machine)
		if ev.To != "%8" || ev.Machine != "ci-mac" || !strings.HasPrefix(ev.From, ev.Machine+"/") {
			t.Errorf("%s: machine %q is not From's (%s); To is %s", k, ev.Machine, ev.From, ev.To)
		}
	}
}
