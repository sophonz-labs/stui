package orch

import (
	"context"
	"testing"
	"time"
)

// rebindAt is one pass of the keeper at now, with what the snapshots hold
// (kept) and what it remembers between passes (missing).
func rebindAt(f *fakeTmux, now time.Time, kept func(string) bool, missing map[string]time.Time) RebindResult {
	return RebindWith(context.Background(), f.run, nil, "1:1", RebindOptions{Kept: kept, Now: func() time.Time { return now }, Missing: missing})
}

// A coordinator adopted workers under %8, whose @stui-run
// still named a run that had been retired. Adopt made a run of %8's own
// with no coordinatorKey, and left %8's options as they were (it is a
// worker of that other run). Rebind could not find a keyless coordinator
// whose pane is in another run, and retired the run 10 minutes after it was
// made -- with %8 there all along.
func TestAdoptUnderAPaneOfAGoneRunIsNotRetired(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	f := newFake([3]string{"%8", "default", "mobile"}, [3]string{"%31", "default", "Simulator / CI"})
	f.opts["%8"] = map[string]string{"@stui-run": "r-0000dead", "@stui-role": RoleWorkerName, "@stui-parent": "%9", "@stui-name": "mobile", OptKey: "k-mobile"}
	a := NewAdopter(AdoptOptions{Tmux: f.run, Agents: func() map[string]string { return map[string]string{"%8": "claude", "%31": "claude"} }})
	res, err := a.Adopt(context.Background(), "%8", "%31", AdoptHow{})
	if err != nil {
		t.Fatal(err)
	}
	onPane := func(k string) bool {
		for _, o := range f.opts {
			if o[OptKey] == k {
				return true
			}
		}
		return false
	}
	missing := map[string]time.Time{}
	for _, after := range []time.Duration{time.Second, 11 * time.Minute, 30 * time.Minute} {
		r := rebindAt(f, time.Now().Add(after), onPane, missing)
		if len(r.Retired) > 0 {
			t.Fatalf("%v after adopting, with %%8 there: retired %v", after, r.Retired)
		}
	}
	if _, err := Load(res.Run); err != nil {
		t.Fatalf("the run is gone: %v", err)
	}
}

// A pass that lists no pane at all -- a tmux server starting, or stui itself
// restarting -- says nothing of who is gone: it retires nothing.
func TestNoRetireWhenTmuxListsNoPane(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	ago := time.Now().Add(-time.Hour).UnixMilli()
	(&Run{ID: "r-00000011", Coordinator: "%9", Created: ago}).Save()
	(&Run{ID: "r-00000012", Coordinator: "%9", CoordinatorKey: "k-c", CoordinatorLive: true, Created: ago}).Save()
	f := newFake()
	missing := map[string]time.Time{}
	for _, after := range []time.Duration{0, 10 * time.Minute} {
		if r := rebindAt(f, time.Now().Add(after), func(string) bool { return false }, missing); len(r.Retired) > 0 {
			t.Fatalf("no pane listed, yet retired %v", r.Retired)
		}
	}
}

// A coordinator missing from one pass -- stui just restarted, its options not
// put back yet -- is not retired then: only once it has been missing on
// every pass for retireGrace. One that is really gone still is (no
// regression), and one that comes back in between is not.
func TestRetireOnlyAfterMissingForAWhile(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	ago := time.Now().Add(-time.Hour).UnixMilli()
	(&Run{ID: "r-00000021", Coordinator: "%9", CoordinatorKey: "k-gone", CoordinatorLive: true, Created: ago}).Save()
	(&Run{ID: "r-00000022", Coordinator: "%7", CoordinatorKey: "k-back", CoordinatorLive: true, Created: ago}).Save()
	f := newFake([3]string{"%1", "s", "shell"})
	no := func(string) bool { return false }
	missing := map[string]time.Time{}
	t0 := time.Now()
	if r := rebindAt(f, t0, no, missing); len(r.Retired) > 0 {
		t.Fatalf("retired on the first pass that missed them: %v", r.Retired)
	}
	// k-back comes back.
	f.order = append(f.order, "%7")
	f.where["%7"] = [2]string{"s", "lead"}
	f.opts["%7"] = map[string]string{OptKey: "k-back", "@stui-run": "r-00000022", "@stui-role": RoleCoordinatorName}
	rebindAt(f, t0.Add(time.Minute), no, missing)
	r := rebindAt(f, t0.Add(retireGrace+time.Minute), no, missing)
	if len(r.Retired) != 1 || r.Retired[0] != "r-00000021" {
		t.Fatalf("after the grace: retired %v, want r-00000021 only", r.Retired)
	}
}
