package orch

import (
	"context"
	"testing"
)

// adopted sets up what adopting %0 under %2 leaves: the options on both
// panes and the run's record.
func adopted(t *testing.T, f *fakeTmux) *Run {
	t.Helper()
	r := &Run{ID: "r-0000000a", Coordinator: "%2", Session: "stui", Created: Now(),
		Workers: []Worker{{ID: "%0", Name: "mobile", Agent: "claude", Started: 1, Adopted: true, Done: true, Summary: "did it"}}}
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	f.opts["%2"] = map[string]string{"@stui-run": r.ID, "@stui-role": RoleCoordinatorName}
	f.opts["%0"] = map[string]string{"@stui-run": r.ID, "@stui-role": RoleWorkerName, "@stui-parent": "%2", "@stui-name": "mobile", "@stui-adopted": "1", "@stui-done": "1"}
	return r
}

// The tmux server died and stui restore made the panes again: new ids, the
// keys put back on them, the run's options gone -- and one of the old ids
// handed to a pane that has nothing to do with the run.
func TestRebindAfterRestore(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	before := newFake([3]string{"%0", "stui", "mobile"}, [3]string{"%2", "stui", "server"})
	r := adopted(t, before)
	res := Rebind(context.Background(), before.run, map[string]string{"%0": "conv-mobile"}, "100:1")
	if res.Keyed != 2 || len(res.Moved) != 0 {
		t.Fatalf("first pass: %+v", res)
	}
	r, _ = Load(r.ID)
	kc, kw := before.opts["%2"][OptKey], before.opts["%0"][OptKey]
	if kc == "" || kw == "" || r.CoordinatorKey != kc || !r.CoordinatorLive || r.Server != "100:1" {
		t.Fatalf("coordinator not recorded: %+v", r)
	}
	if w := r.Workers[0]; w.Key != kw || w.Conv != "conv-mobile" || !w.Live {
		t.Fatalf("worker not recorded: %+v", w)
	}

	after := newFake([3]string{"%0", "default", "notes"}, [3]string{"%8", "stui", "mobile"}, [3]string{"%9", "stui", "server"})
	after.opts["%8"][OptKey] = kw
	after.opts["%9"][OptKey] = kc
	res = Rebind(context.Background(), after.run, nil, "200:2")
	if len(res.Moved) != 2 {
		t.Fatalf("moved: %+v", res)
	}
	r, _ = Load(r.ID)
	if r.Coordinator != "%9" || r.Workers[0].ID != "%8" || r.Server != "200:2" {
		t.Fatalf("record: coordinator %s, worker %s, server %s", r.Coordinator, r.Workers[0].ID, r.Server)
	}
	if o := after.opts["%9"]; o["@stui-run"] != r.ID || o["@stui-role"] != RoleCoordinatorName {
		t.Fatalf("coordinator options: %v", o)
	}
	o := after.opts["%8"]
	if o["@stui-run"] != r.ID || o["@stui-role"] != RoleWorkerName || o["@stui-parent"] != "%9" || o["@stui-name"] != "mobile" ||
		o["@stui-adopted"] != "1" || o["@stui-done"] != "1" || o["@stui-summary"] != "did it" {
		t.Fatalf("worker options: %v", o)
	}
	if len(after.opts["%0"]) != 0 {
		t.Fatalf("the pane that got the old id was touched: %v", after.opts["%0"])
	}
	// Settled: nothing more to do.
	if res := Rebind(context.Background(), after.run, nil, "200:2"); res.Keyed != 0 || len(res.Moved) != 0 {
		t.Fatalf("again: %+v", res)
	}
}

// A pane in the same server whose options were taken away (a release, a
// coordinator settled) is left as it is: Rebind does not undo it.
func TestRebindLeavesWhatWasUndone(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	f := newFake([3]string{"%0", "s", "w"}, [3]string{"%2", "s", "c"})
	r := adopted(t, f)
	Rebind(context.Background(), f.run, nil, "1:1")
	delete(f.opts["%2"], "@stui-run")
	delete(f.opts["%2"], "@stui-role")
	Rebind(context.Background(), f.run, nil, "1:1")
	if f.opts["%2"]["@stui-run"] != "" {
		t.Fatalf("put back: %v", f.opts["%2"])
	}
	if r, _ = Load(r.ID); r.CoordinatorLive {
		t.Fatalf("still live: %+v", r)
	}
	// And a restore later does not bring it back either.
	g := newFake([3]string{"%5", "s", "c"})
	g.opts["%5"][OptKey] = f.opts["%2"][OptKey]
	Rebind(context.Background(), g.run, nil, "2:2")
	if g.opts["%5"]["@stui-run"] != "" {
		t.Fatalf("brought back after a restore: %v", g.opts["%5"])
	}
}

// An agent resumed by hand in another window is found by its
// conversation.
func TestRebindFollowsConversation(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	f := newFake([3]string{"%0", "s", "w"}, [3]string{"%2", "s", "c"})
	r := adopted(t, f)
	Rebind(context.Background(), f.run, map[string]string{"%0": "c-w", "%2": "c-c"}, "1:1")
	g := newFake([3]string{"%30", "s", "c"}, [3]string{"%31", "s", "w"})
	Rebind(context.Background(), g.run, map[string]string{"%30": "c-c", "%31": "c-w"}, "2:2")
	r, _ = Load(r.ID)
	if r.Coordinator != "%30" || r.Workers[0].ID != "%31" || g.opts["%31"]["@stui-parent"] != "%30" {
		t.Fatalf("not followed: %s %s %v", r.Coordinator, r.Workers[0].ID, g.opts["%31"])
	}
}

// A pane another run has now is not taken back.
func TestRebindDoesNotSteal(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	f := newFake([3]string{"%0", "s", "w"}, [3]string{"%2", "s", "c"})
	r := adopted(t, f)
	Rebind(context.Background(), f.run, nil, "1:1")
	f.opts["%0"]["@stui-run"] = "r-0000000b"
	Rebind(context.Background(), f.run, nil, "1:1")
	if r, _ = Load(r.ID); r.Workers[0].Live {
		t.Fatalf("still claims it: %+v", r.Workers[0])
	}
	g := newFake([3]string{"%4", "s", "w"})
	g.opts["%4"][OptKey] = f.opts["%0"][OptKey]
	Rebind(context.Background(), g.run, nil, "2:2")
	if g.opts["%4"]["@stui-run"] != "" {
		t.Fatalf("taken back: %v", g.opts["%4"])
	}
}

func TestParentVerdict(t *testing.T) {
	for _, c := range []struct {
		st   ParentStatus
		run  string
		gone bool
		now  string
	}{
		{ParentStatus{Pane: "%1", Alive: true, Run: "r-1", Coordinator: "%1"}, "r-1", false, ""},
		{ParentStatus{Pane: "%1", Alive: false}, "r-1", true, ""},
		{ParentStatus{Pane: "%1", Alive: false, Coordinator: "%8"}, "r-1", true, "%8"},
		// Its id given to some other pane.
		{ParentStatus{Pane: "%1", Alive: true, Run: ""}, "r-1", true, ""},
	} {
		if gone, now := c.st.Verdict(c.run); gone != c.gone || now != c.now {
			t.Errorf("%+v: %v %q", c.st, gone, now)
		}
	}
}

// A team taken in as one worker: its lead keeps its own run in @stui-run and
// says the run above in @stui-in-run. After a restore both records find it,
// and each puts back only its own options.
func TestRebindTeamLead(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	f := newFake([3]string{"%1", "s", "top"}, [3]string{"%2", "s", "lead"}, [3]string{"%3", "s", "member"})
	above := &Run{ID: "r-000000a1", Coordinator: "%1", Created: 2, Workers: []Worker{{ID: "%2", Name: "lead", Started: 1, Adopted: true, Team: "r-000000b2"}}}
	team := &Run{ID: "r-000000b2", Coordinator: "%2", Created: 1, Workers: []Worker{{ID: "%3", Name: "member", Started: 1}}}
	above.Save()
	team.Save()
	f.opts["%1"] = map[string]string{"@stui-run": above.ID, "@stui-role": RoleCoordinatorName}
	f.opts["%2"] = map[string]string{"@stui-run": team.ID, OptInRun: above.ID, "@stui-role": RoleWorkerName, "@stui-parent": "%1", "@stui-name": "lead", "@stui-adopted": "1"}
	f.opts["%3"] = map[string]string{"@stui-run": team.ID, "@stui-role": RoleWorkerName, "@stui-parent": "%2", "@stui-name": "member"}
	Rebind(context.Background(), f.run, nil, "1:1")
	if a, _ := Load(above.ID); !a.Workers[0].Live {
		t.Fatalf("lead not a live worker of the run above: %+v", a.Workers[0])
	}
	if tm, _ := Load(team.ID); !tm.CoordinatorLive || !tm.Workers[0].Live {
		t.Fatalf("team record: %+v", tm)
	}

	g := newFake([3]string{"%7", "s", "top"}, [3]string{"%8", "s", "lead"}, [3]string{"%9", "s", "member"})
	for from, to := range map[string]string{"%1": "%7", "%2": "%8", "%3": "%9"} {
		g.opts[to][OptKey] = f.opts[from][OptKey]
	}
	Rebind(context.Background(), g.run, nil, "2:2")
	o := g.opts["%8"]
	if o["@stui-run"] != team.ID || o[OptInRun] != above.ID || o["@stui-role"] != RoleWorkerName || o["@stui-parent"] != "%7" {
		t.Fatalf("lead options: %v", o)
	}
	if o := g.opts["%9"]; o["@stui-run"] != team.ID || o["@stui-parent"] != "%8" {
		t.Fatalf("member options: %v", o)
	}
	if a, _ := Load(above.ID); a.Coordinator != "%7" || a.Workers[0].ID != "%8" {
		t.Fatalf("run above: %+v", a)
	}
	if tm, _ := Load(team.ID); tm.Coordinator != "%8" || tm.Workers[0].ID != "%9" {
		t.Fatalf("team run: %+v", tm)
	}
}
