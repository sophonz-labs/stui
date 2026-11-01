package cli

import (
	"strings"
	"testing"

	"github.com/sophonz-labs/stui/internal/orch"
)

// %9 coordinates; %15 is its worker and leads %21; %30 belongs to another
// coordinator, %40. %15 started fix on ci-mac too.
func team() []candidate {
	ps := []orchPane{
		{ID: "%9", Run: "r-1", Role: "coordinator"},
		{ID: "%15", Run: "r-1", Role: "worker", Parent: "%9", Name: "ui-desktop"},
		{ID: "%21", Run: "r-1", Role: "worker", Parent: "%15", Name: "fix"},
		{ID: "%30", Run: "r-2", Role: "worker", Parent: "%40", Name: "fix"},
		{ID: "%31", Run: "r-2", Role: "worker", Parent: "%40", Name: "docs"},
	}
	runs := []*orch.Run{{ID: "r-1", Coordinator: "%9", Workers: []orch.Worker{
		{ID: "%15", Name: "ui-desktop"},
		{ID: "ci-mac/%7", Name: "lint", Machine: "ci-mac", Parent: "%15"},
		{ID: "ci-mac/%8", Name: "web", Machine: "ci-mac"},
	}}}
	return knownWorkers(ps, runs)
}

func ids(cs []candidate) string {
	var s []string
	for _, c := range cs {
		s = append(s, c.ID)
	}
	return strings.Join(s, ",")
}

func TestNameFindsMyTeamFirst(t *testing.T) {
	all := team()
	// fix is %21 under me and %30 elsewhere: mine wins, without asking.
	if got, wide := matchName("fix", "%9", all); ids(got) != "%21" || wide {
		t.Fatalf("fix from %%9: %s wide=%v", ids(got), wide)
	}
	// From %40 it is %30.
	if got, _ := matchName("fix", "%40", all); ids(got) != "%30" {
		t.Fatalf("fix from %%40: %s", ids(got))
	}
	// From a pane that leads neither, both: it must say so, not pick.
	if got, wide := matchName("fix", "%99", all); ids(got) != "%21,%30" || !wide {
		t.Fatalf("fix from %%99: %s wide=%v", ids(got), wide)
	}
	// A grandchild on another machine is in my team.
	if got, _ := matchName("lint", "%9", all); ids(got) != "ci-mac/%7" {
		t.Fatalf("lint: %s", ids(got))
	}
	// A worker of my run on another machine, its parent left out of the
	// record: the coordinator's.
	if got, _ := matchName("WEB", "%9", all); ids(got) != "ci-mac/%8" {
		t.Fatalf("web: %s", ids(got))
	}
	// Someone else's worker, found in the wider search.
	if got, wide := matchName("docs", "%9", all); ids(got) != "%31" || !wide {
		t.Fatalf("docs: %s wide=%v", ids(got), wide)
	}
	// The whole name or nothing.
	if got, _ := matchName("stui", "%9", all); len(got) != 0 {
		t.Fatalf("a prefix matched: %s", ids(got))
	}
}

func TestNameTwiceInMyTeam(t *testing.T) {
	all := append(team(), candidate{ID: "%22", Name: "fix", Parent: "%9"})
	if got, _ := matchName("fix", "%9", all); ids(got) != "%21,%22" {
		t.Fatalf("two of mine: %s", ids(got))
	}
}

func TestPaneIDsAsTheyAre(t *testing.T) {
	for _, in := range []string{"%15", " %15"} {
		if id, err := resolveWorkerID(in); err != nil || id != "%15" {
			t.Fatalf("%q: %q %v", in, id, err)
		}
	}
	if _, err := resolveWorkerID("Not A Name!"); err == nil || !strings.Contains(err.Error(), "machine/name") {
		t.Fatalf("a bad id should say the forms: %v", err)
	}
}

func trow(id, up string) map[string]any { return map[string]any{"id": id, "_up": up} }

func order(rows []map[string]any) string {
	var s []string
	for _, r := range rows {
		s = append(s, strings.Repeat(">", r["depth"].(int)-1)+r["id"].(string))
	}
	return strings.Join(s, " ")
}

func TestListIsATree(t *testing.T) {
	rows := func() []map[string]any {
		return []map[string]any{trow("%15", "%9"), trow("%8", "%9"), trow("%21", "%15"), trow("ci-mac/%7", "%21"), trow("%30", "%40")}
	}
	if got := order(treeRows(rows(), "%9", false, 0)); got != "%15 >%21 >>ci-mac/%7 %8" {
		t.Fatalf("tree: %s", got)
	}
	if got := order(treeRows(rows(), "%9", false, 1)); got != "%15 %8" {
		t.Fatalf("depth 1: %s", got)
	}
	if got := order(treeRows(rows(), "%9", true, 0)); got != "%15 >%21 >>ci-mac/%7 %8 %30" {
		t.Fatalf("all: %s", got)
	}
	// Out of the tree, its parent gone: still listed.
	r := rows()
	r[4]["parentGone"] = true
	if got := order(treeRows(r, "%9", false, 0)); !strings.HasSuffix(got, " %30") {
		t.Fatalf("parent gone: %s", got)
	}
}

func TestTeamWithoutInRun(t *testing.T) {
	ps := []orchPane{{ID: "%15", Role: "worker", Parent: "%9"}, {ID: "%21", Role: "worker", Parent: "%15"}}
	// %15 became a worker before it adopted %21: no @stui-in-run, a team all the same.
	if tm := teamOf(ps[0], ps); tm == nil || tm.Members != 1 {
		t.Fatalf("team: %+v", tm)
	}
	if tm := teamOf(ps[1], ps); tm != nil {
		t.Fatalf("no team: %+v", tm)
	}
}

func TestWorkerVerbHelp(t *testing.T) {
	h := workerVerbHelp("send")
	if !strings.Contains(h, "stui worker send <id>") || !strings.Contains(h, "a name from stui worker list") || strings.Contains(h, "stui worker stop") {
		t.Fatalf("send help:\n%s", h)
	}
	if h := workerVerbHelp("start"); !strings.Contains(h, "--worktree") || !strings.Contains(h, "--fan") {
		t.Fatalf("start help:\n%s", h)
	}
	if h := workerVerbHelp("rename"); !strings.Contains(h, "stui worker rename <id> <name>") || !strings.Contains(h, "keeps its title") {
		t.Fatalf("rename help:\n%s", h)
	}
	if workerVerbHelp("nope") != "" {
		t.Fatal("an unknown verb has no lines")
	}
	if got, ok := helpFor(args{"worker", "send", "--help"}); !ok || got != workerVerbHelp("send") {
		t.Fatalf("helpFor: %q", got)
	}
}

func TestSplitName(t *testing.T) {
	for in, want := range map[string]string{"ui-desktop": "|ui-desktop", "UI-Desktop": "|UI-Desktop", "ci-mac/fix": "ci-mac|fix"} {
		m, n, ok := splitName(in)
		if !ok || m+"|"+n != want {
			t.Fatalf("%q: %q %q %v", in, m, n, ok)
		}
	}
	for _, in := range []string{"", "/fix", "ci-mac/", "two words", "fix!"} {
		if _, _, ok := splitName(in); ok {
			t.Fatalf("%q taken as a name", in)
		}
	}
}
