package cli

import (
	"strings"
	"testing"

	"github.com/sophonz-labs/stui/internal/orch"
)

// The panes after a restore renumbered them: the worker mobile was %0 and
// is %8; %0 is another session where claude was resumed; %11 is a
// worker of another run.
func restoredPanes() map[string]targetPane {
	return map[string]targetPane{
		"%0":  {ID: "%0"},
		"%2":  {ID: "%2", Run: "r-1", Role: orch.RoleCoordinatorName, Key: "k-c"},
		"%8":  {ID: "%8", Run: "r-1", Role: orch.RoleWorkerName, Key: "k-m", Name: "mobile"},
		"%9":  {ID: "%9", Run: "r-2", InRun: "r-1", Role: orch.RoleWorkerName, Key: "k-l", Name: "lead"},
		"%11": {ID: "%11", Run: "r-9", Role: orch.RoleWorkerName, Key: "k-x", Name: "other"},
	}
}

func TestJudgeTarget(t *testing.T) {
	ps := restoredPanes()
	rec := &orch.Run{ID: "r-1", Workers: []orch.Worker{{ID: "%0", Name: "mobile", Key: "k-m"}}}
	runs := []string{"r-1", ""}

	why, now, mine := judgeTarget(runs, []*orch.Run{rec}, ps, "%0")
	if why == "" || now != "%8" || strings.Join(mine, ",") != "%8 mobile,%9 lead" {
		t.Fatalf("old id: %q %q %v", why, now, mine)
	}
	if why, _, _ := judgeTarget(runs, nil, ps, "%11"); !strings.Contains(why, "another run") {
		t.Fatalf("another run's worker: %q", why)
	}
	if why, _, _ := judgeTarget(runs, nil, ps, "%77"); !strings.Contains(why, "no pane %77") {
		t.Fatalf("no pane: %q", why)
	}
	for _, ok := range []string{"%8", "%9", "%2"} {
		if why, _, _ := judgeTarget(runs, nil, ps, ok); why != "" {
			t.Errorf("%s is in the run: %q", ok, why)
		}
	}
	// A team's lead sends to its own workers: its own run is @stui-run.
	if why, _, _ := judgeTarget([]string{"r-9", "r-1"}, nil, ps, "%11"); why != "" {
		t.Errorf("lead to its team: %q", why)
	}
	// A pane that directs no one has nothing to judge by, but a pane that
	// is not there is still not there.
	if why, _, _ := judgeTarget([]string{"", ""}, nil, ps, "%0"); why != "" {
		t.Errorf("no run: %q", why)
	}
	if why, _, _ := judgeTarget([]string{"", ""}, nil, ps, "%77"); why == "" {
		t.Error("no run, no pane: sent")
	}
}
