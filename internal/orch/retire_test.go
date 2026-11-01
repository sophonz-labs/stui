package orch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Runs from before a restart whose coordinators are gone are retired, not
// matched to the panes that got their ids; a run just made, or one a
// snapshot can still bring back, is not.
func TestRetireRunsWhoseCoordinatorIsGone(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	ago := time.Now().Add(-24 * time.Hour).UnixMilli()
	old := &Run{ID: "r-00000001", Coordinator: "%0", Session: "s", Created: ago,
		Workers: []Worker{{ID: "%2", Name: "mobile", Agent: "claude", Started: ago}}}
	young := &Run{ID: "r-00000002", Coordinator: "%5", Session: "s", Created: Now()}
	keyedGone := &Run{ID: "r-00000003", Coordinator: "%6", CoordinatorKey: "k-gone", CoordinatorLive: true, Created: ago}
	keyedKept := &Run{ID: "r-00000004", Coordinator: "%7", CoordinatorKey: "k-kept", CoordinatorLive: true, Created: ago}
	for _, r := range []*Run{old, young, keyedGone, keyedKept} {
		r.Save()
	}
	// The new server: %0 and %2 are other panes now, %0 another run's.
	f := newFake([3]string{"%0", "s", "lead"}, [3]string{"%2", "s", "shell"})
	f.opts["%0"]["@stui-run"] = "r-0000000f"
	missing := map[string]time.Time{}
	opts := RebindOptions{Kept: func(k string) bool { return k == "k-kept" }, Missing: missing}
	// The first pass that misses them only notes it.
	if res := RebindWith(context.Background(), f.run, nil, "9:9", opts); len(res.Retired) != 0 {
		t.Fatalf("retired on the first pass: %v", res.Retired)
	}
	later := time.Now().Add(retireGrace + time.Second)
	opts.Now = func() time.Time { return later }
	res := RebindWith(context.Background(), f.run, nil, "9:9", opts)
	retired := map[string]bool{}
	for _, id := range res.Retired {
		retired[id] = true
	}
	if !retired[old.ID] || !retired[keyedGone.ID] || retired[young.ID] || retired[keyedKept.ID] {
		t.Fatalf("retired %v", res.Retired)
	}
	if f.opts["%0"]["@stui-run"] != "r-0000000f" || f.opts["%2"]["@stui-run"] != "" {
		t.Fatalf("panes touched: %v", f.opts)
	}
	if _, err := Load(old.ID); err == nil {
		t.Fatal("still listed")
	}
	b, err := os.ReadFile(filepath.Join(ArchiveDir(), old.ID, "run.json"))
	if err != nil || len(b) == 0 {
		t.Fatalf("not archived: %v", err)
	}
	for _, r := range Runs() {
		if r.ID == old.ID || r.ID == keyedGone.ID {
			t.Fatalf("Runs lists %s", r.ID)
		}
	}
	// No Kept (Rebind): keyed runs are never retired.
	keyedGone.Save()
	if res := Rebind(context.Background(), f.run, nil, "9:9"); len(res.Retired) != 0 {
		t.Fatalf("Rebind retired %v", res.Retired)
	}
}

// A worker on another machine is told its parent's new pane again on every
// pass until telling it works: when the record named no parent, and when
// the parent it names is a pane of a server that is gone.
func TestReparentTriedAgain(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	ctx := context.Background()
	r := &Run{ID: "r-00000005", Coordinator: "%1", CoordinatorKey: "k-c", CoordinatorLive: true, Created: Now(),
		Workers: []Worker{
			{ID: "ci-mac/%7", Name: "sim", Machine: "ci-mac", Started: 1},
			{ID: "ci-mac/%9", Name: "old", Machine: "ci-mac", Started: 2, Parent: "%22"},
		}}
	r.Save()
	f := newFake([3]string{"%8", "app", "lead"})
	f.opts["%8"][OptKey] = "k-c"
	for pass := 1; pass <= 2; pass++ {
		res := Rebind(ctx, f.run, nil, "2:2")
		got := map[string]string{}
		for _, rp := range res.Reparent {
			got[rp.Child] = rp.Parent
		}
		if got["ci-mac/%7"] != "%8" || got["ci-mac/%9"] != "%8" {
			t.Fatalf("pass %d: %+v", pass, res.Reparent)
		}
	}
}
