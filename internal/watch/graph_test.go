package watch

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGraphFromTeamsAndSubagents(t *testing.T) {
	end := int64(30)
	panes := []Pane{
		{ID: "%12", Session: "work", Window: 1, Pane: 0, Agent: "claude", State: Working, WindowName: "lead",
			Team:      &Team{Name: "session-1", Role: RoleLead, Member: "team-lead", LeadPane: "%12"},
			Teammates: []Teammate{{Name: "tester", State: "working"}, {Name: "notes", State: "idle"}},
			Subagents: Subagents{Running: 1, Items: []SubagentItem{
				{ID: "b", Type: "Explore", Description: "find handlers", State: SubRunning, StartedAt: 20},
				{ID: "a", Type: "Plan", State: SubDone, StartedAt: 10, EndedAt: &end},
			}}},
		{ID: "%13", Session: "work", Window: 1, Pane: 1, Agent: "claude", State: Idle,
			Team: &Team{Name: "session-1", Role: RoleTeammate, Member: "tester", LeadPane: "%12"}},
		{ID: "%20", Session: "work", Window: 2, Pane: 0, Agent: "codex", State: Waiting, WindowName: "api"},
		{ID: "%30", Session: "other", Window: 0, Pane: 0, Agent: "claude", State: Done},
	}
	g := BuildGraph("work", panes)
	ids := []string{}
	for _, n := range g.Nodes {
		ids = append(ids, n.ID)
	}
	want := []string{"%12", "%13", "%20", "%12/tm-notes", "%12/a", "%12/b"}
	if len(ids) != len(want) {
		t.Fatalf("nodes %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("nodes %v, want %v", ids, want)
		}
	}
	edges := map[string]string{}
	for _, e := range g.Edges {
		edges[e.From+">"+e.To] = e.Kind
	}
	for k, kind := range map[string]string{"%12>%13": EdgeTeam, "%12>%12/tm-notes": EdgeTeam, "%12>%12/a": EdgeSubagent, "%12>%12/b": EdgeSubagent} {
		if edges[k] != kind {
			t.Errorf("edge %s = %q, want %q (all %v)", k, edges[k], kind, edges)
		}
	}
	if len(g.Edges) != 4 {
		t.Errorf("edges %v", g.Edges)
	}
	// The teammate with a pane is named by its team name, not twice.
	if g.Nodes[1].Label != "tester" || g.Nodes[1].Role != RoleTeammate {
		t.Errorf("teammate pane %+v", g.Nodes[1])
	}
	if g.Nodes[2].Label != "api" || g.Nodes[2].Window == nil || *g.Nodes[2].Window != 2 {
		t.Errorf("plain pane %+v", g.Nodes[2])
	}
}

func TestGraphsListsOnlyDelegation(t *testing.T) {
	panes := []Pane{
		{ID: "%1", Session: "a", Agent: "claude", State: Working, Subagents: Subagents{Items: []SubagentItem{{ID: "x", State: SubRunning}}}},
		{ID: "%2", Session: "b", Agent: "codex", State: Waiting},
	}
	got := Graphs(panes)
	if len(got) != 1 || got[0].Session != "a" || got[0].Nodes != 2 || got[0].Working != 2 {
		t.Fatalf("%+v", got)
	}
}

func TestGraphEdgeNeedsBothEnds(t *testing.T) {
	// A teammate whose lead pane is gone (or in another session) has no edge.
	panes := []Pane{{ID: "%5", Session: "s", State: Idle, Team: &Team{Role: RoleTeammate, Member: "t", LeadPane: "%9", Ended: true}}}
	if g := BuildGraph("s", panes); len(g.Edges) != 0 || len(g.Nodes) != 1 {
		t.Fatalf("%+v", g)
	}
}

func TestGraphOrchWorkers(t *testing.T) {
	panes := []Pane{
		{ID: "%1", Session: "w", Window: 0, Agent: "claude", State: Working, Orch: &Orch{Run: "r-1", Role: RoleCoordinator}},
		{ID: "%7", Session: "w", Window: 1, Agent: "codex", State: Done, Orch: &Orch{Run: "r-1", Role: RoleWorker, Parent: "%1", Name: "tests", Task: "Add tests", Done: true, Summary: "12 tests"}},
	}
	g := BuildGraph("w", panes)
	if len(g.Edges) != 1 || g.Edges[0] != (GraphEdge{From: "%1", To: "%7", Kind: EdgeOrch}) {
		t.Fatalf("edges %+v", g.Edges)
	}
	w := g.Nodes[1]
	if w.Label != "tests" || w.Detail != "Add tests" || !w.Done || w.Summary != "12 tests" || w.Role != RoleWorker || w.Run != "r-1" {
		t.Fatalf("worker %+v", w)
	}
}

func TestParseOrch(t *testing.T) {
	if parseOrch("") != nil || parseOrch(strings.Repeat(osep, 14)) != nil {
		t.Fatal("no run should be nil")
	}
	o := parseOrch(strings.Join([]string{"r-1", "worker", "%1", "tests", "1", "Add tests", "stui/r-1/tests", "fix", "r-0", "1",
		"desktop sidebar and themes", "answered", "1792229000", `did the sidebar\nnow the theme`, "done: 12 |^| and more"}, osep))
	if o == nil || o.InRun != "r-0" || o.Run != "r-1" || o.Parent != "%1" || !o.Done || o.Worktree != "stui/r-1/tests" || o.Fan != "fix" || o.Summary != "done: 12 |^| and more" {
		t.Fatalf("%+v", o)
	}
	if !o.Adopted || o.About != "desktop sidebar and themes" || o.BriefState != "answered" || o.BriefAt != 1792229000 || o.Brief != "did the sidebar\nnow the theme" {
		t.Fatalf("role and brief: %+v", o)
	}
	g := BuildGraph("s", []Pane{{ID: "%2", Session: "s", Agent: "claude", Orch: o}})
	if !g.Nodes[0].Adopted || g.Nodes[0].About != o.About || g.Nodes[0].Brief != o.Brief || g.Nodes[0].BriefState != "answered" {
		t.Fatalf("graph node: %+v", g.Nodes[0])
	}
}

func TestGraphOrchAcrossSessions(t *testing.T) {
	panes := []Pane{
		{ID: "%1", Session: "a", Agent: "claude", State: Working, Orch: &Orch{Run: "r-1", Role: RoleCoordinator}},
		{ID: "%9", Session: "b", Window: 2, Agent: "claude", State: Idle, Orch: &Orch{Run: "r-1", Role: RoleWorker, Parent: "%1", Name: "docs"}},
	}
	// Each session's graph has the other end, marked with its session.
	for _, c := range []struct{ session, other, otherSession string }{{"a", "%9", "b"}, {"b", "%1", "a"}} {
		g := BuildGraph(c.session, panes)
		if len(g.Nodes) != 2 || len(g.Edges) != 1 || g.Edges[0].From != "%1" || g.Edges[0].To != "%9" || g.Edges[0].Kind != EdgeOrch {
			t.Fatalf("%s: %+v %+v", c.session, g.Nodes, g.Edges)
		}
		for _, n := range g.Nodes {
			want := ""
			if n.ID == c.other {
				want = c.otherSession
			}
			if n.Session != want {
				t.Fatalf("%s: node %s session %q", c.session, n.ID, n.Session)
			}
		}
	}
}

// A team's lead adopted as a worker: "team" is {run, members}; a Claude
// agent team's node keeps its name there.
func TestGraphOrchTeam(t *testing.T) {
	panes := []Pane{
		{ID: "%1", Session: "s", Agent: "claude", Orch: &Orch{Run: "r-1", Role: RoleCoordinator}},
		{ID: "%2", Session: "s", Agent: "claude", Orch: &Orch{Run: "r-2", Role: RoleWorker, Parent: "%1", InRun: "r-1"}},
		{ID: "%3", Session: "s", Agent: "codex", Orch: &Orch{Run: "r-2", Role: RoleWorker, Parent: "%2"}},
		{ID: "%4", Session: "s", Agent: "codex", Orch: &Orch{Run: "r-2", Role: RoleWorker, Parent: "%2"}},
		{ID: "%5", Session: "s", Agent: "claude", Team: &Team{Name: "crew", Role: RoleLead, Member: "team-lead", LeadPane: "%5"}},
	}
	ptrs := []*Pane{}
	for i := range panes {
		ptrs = append(ptrs, &panes[i])
	}
	markOrchTeams(ptrs)
	if tm := panes[1].Orch.Team; tm == nil || tm.Run != "r-2" || tm.Members != 2 {
		t.Fatalf("lead's team: %+v", tm)
	}
	if panes[0].Orch.Team != nil || panes[2].Orch.Team != nil {
		t.Fatal("not leads of an adopted team")
	}
	b, _ := json.Marshal(BuildGraph("s", panes))
	var g struct {
		Nodes []map[string]any `json:"nodes"`
	}
	json.Unmarshal(b, &g)
	team := map[string]any{}
	for _, n := range g.Nodes {
		team[n["id"].(string)] = n["team"]
	}
	if tm, ok := team["%2"].(map[string]any); !ok || tm["run"] != "r-2" || tm["members"] != float64(2) {
		t.Fatalf("%%2 team: %v", team["%2"])
	}
	if team["%5"] != "crew" || team["%1"] != nil {
		t.Fatalf("teams: %v", team)
	}
	var back Graph
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	for _, n := range back.Nodes {
		if n.ID == "%2" && (n.OrchTeam == nil || n.OrchTeam.Members != 2) || n.ID == "%5" && n.Team != "crew" {
			t.Fatalf("read back: %+v", n)
		}
	}
}

// A window renamed after its worker started is the name the person chose, so
// the graph follows it. The worker's own name stays its handle and remains
// the label while the window has none.
func TestPaneLabelFollowsARenamedWindow(t *testing.T) {
	worker := func(window, name string) Pane {
		p := Pane{ID: "%2", Agent: "claude", Command: "claude", WindowName: window}
		if name != "" {
			p.Orch = &Orch{Name: name, Role: "worker", Run: "r1"}
		}
		return p
	}
	for _, c := range []struct {
		name string
		pane Pane
		want string
	}{
		{"as stui started it, the two agree", worker("api", "api"), "api"},
		{"renamed since: the window wins", worker("renamed-by-hand", "api"), "renamed-by-hand"},
		{"no window name: the worker's own", worker("", "api"), "api"},
		{"neither: the agent", worker("", ""), "claude"},
		{"a team member's name comes first", func() Pane {
			p := worker("renamed-by-hand", "api")
			p.Team = &Team{Member: "scout"}
			return p
		}(), "scout"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := paneLabel(c.pane); got != c.want {
				t.Fatalf("paneLabel = %q, want %q", got, c.want)
			}
		})
	}
}
