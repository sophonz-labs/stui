package orch

import (
	"context"
	"testing"
)

// A team: %2 coordinates its own run with %3 as its worker; %1 is another
// agent with nothing yet.
func teamFake(t *testing.T) (*fakeTmux, *Adopter) {
	t.Helper()
	t.Setenv("STUI_HOME", t.TempDir())
	f := newFake([3]string{"%1", "work", "top"}, [3]string{"%2", "work", "lead"}, [3]string{"%3", "work", "member"}, [3]string{"%4", "work", "helper"})
	f.opts["%2"]["@stui-run"], f.opts["%2"]["@stui-role"] = "r-22222222", RoleCoordinatorName
	f.opts["%3"]["@stui-run"], f.opts["%3"]["@stui-role"], f.opts["%3"]["@stui-parent"], f.opts["%3"]["@stui-name"] = "r-22222222", RoleWorkerName, "%2", "member"
	(&Run{ID: "r-22222222", Coordinator: "%2", Session: "work", Workers: []Worker{{ID: "%3", Name: "member"}}}).Save()
	a := NewAdopter(AdoptOptions{
		Tmux: f.run,
		Agents: func() map[string]string {
			return map[string]string{"%1": "claude", "%2": "claude", "%3": "codex", "%4": "claude"}
		},
	})
	return f, a
}

func TestAdoptTeamKeepsItsRun(t *testing.T) {
	f, a := teamFake(t)
	ctx := context.Background()
	res, err := a.Adopt(ctx, "%1", "%2", AdoptHow{Tell: false})
	if err != nil {
		t.Fatal(err)
	}
	o := f.opts["%2"]
	if o["@stui-run"] != "r-22222222" || o[OptInRun] != res.Run || o["@stui-parent"] != "%1" || o["@stui-role"] != RoleWorkerName || o["@stui-adopted"] != "1" {
		t.Fatalf("lead options: %v", o)
	}
	if res.Team != "r-22222222" || res.Run == "r-22222222" {
		t.Fatalf("result: %+v", res)
	}
	r, err := Load(res.Run)
	if err != nil || r.Worker("%2") == nil || !r.Worker("%2").Adopted || r.Worker("%2").Team != "r-22222222" || r.Worker("%3") != nil {
		t.Fatalf("parent's run: %v %+v", err, r)
	}
	// The team is as it was.
	if f.opts["%3"]["@stui-parent"] != "%2" || f.opts["%3"]["@stui-run"] != "r-22222222" {
		t.Fatalf("member: %v", f.opts["%3"])
	}
	out, _ := f.run(ctx, "list-panes")
	if tm := TeamOf(out, "%2"); tm == nil || tm.Run != "r-22222222" || tm.Members != 1 {
		t.Fatalf("team: %+v", tm)
	}
	if TeamOf(out, "%1") != nil || TeamOf(out, "%3") != nil {
		t.Fatal("only the lead has a team")
	}

	// Depth counts within each run: the member is one below its lead, so
	// it may take a worker of its own (two levels in the team, three in
	// the chain).
	if d, c, _, _ := Levels(out, "%3"); d != 1 || c != 2 {
		t.Fatalf("member levels: %d %d", d, c)
	}
	if _, err := a.Adopt(ctx, "%3", "%4", AdoptHow{Tell: false}); err != nil {
		t.Fatalf("a member's worker: %v", err)
	}
	if f.opts["%4"]["@stui-run"] != "r-22222222" || f.opts["%4"][OptInRun] != "" {
		t.Fatalf("member's worker: %v", f.opts["%4"])
	}

	// Let the lead go: it coordinates its own run again, the team stays.
	if _, err := a.Release(ctx, "%4", false); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Release(ctx, "%2", false); err != nil {
		t.Fatal(err)
	}
	o = f.opts["%2"]
	if o[OptInRun] != "" || o["@stui-parent"] != "" || o["@stui-run"] != "r-22222222" || o["@stui-role"] != RoleCoordinatorName || o["@stui-adopted"] != "" {
		t.Fatalf("released lead: %v", o)
	}
	if f.opts["%3"]["@stui-parent"] != "%2" {
		t.Fatalf("member after release: %v", f.opts["%3"])
	}
	if r, _ := Load(res.Run); r.Worker("%2") != nil {
		t.Fatalf("still in the parent's run: %+v", r.Workers)
	}
	// %1 was a coordinator only because it adopted the team.
	if f.opts["%1"]["@stui-run"] != "" || f.opts["%1"]["@stui-role"] != "" {
		t.Fatalf("parent kept its run: %v", f.opts["%1"])
	}
}

// Teams in teams: each run may be shallow, the whole chain still stops at
// MaxChain.
func TestAdoptChainLimit(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	f := newFake([3]string{"%1", "w", "a"}, [3]string{"%2", "w", "b"}, [3]string{"%3", "w", "c"}, [3]string{"%4", "w", "d"}, [3]string{"%5", "w", "e"}, [3]string{"%6", "w", "f"})
	// %2, %3 and %4 each lead a team adopted by the one above.
	for i, id := range []string{"%2", "%3", "%4"} {
		up := []string{"%1", "%2", "%3"}[i]
		f.opts[id] = map[string]string{"@stui-run": "r-0000000" + string(rune('2'+i)), "@stui-role": RoleWorkerName, "@stui-parent": up, OptInRun: "r-00000001", "@stui-adopted": "1"}
	}
	a := NewAdopter(AdoptOptions{Tmux: f.run, Agents: func() map[string]string {
		return map[string]string{"%1": "claude", "%2": "claude", "%3": "claude", "%4": "claude", "%5": "claude", "%6": "claude"}
	}})
	ctx := context.Background()
	// Under %4: its own run's first level, the chain's fourth.
	if _, err := a.Adopt(ctx, "%4", "%5", AdoptHow{Tell: false}); err != nil {
		t.Fatalf("chain of 4: %v", err)
	}
	// Under %5: two levels in %4's run, but five in the chain.
	if _, err := a.Adopt(ctx, "%5", "%6", AdoptHow{Tell: false}); err == nil || err.(*AdoptError).Reason != "too-deep" {
		t.Fatalf("chain of 5: %v", err)
	}
}
