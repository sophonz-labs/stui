package watch

import (
	"encoding/json"
	"sort"
	"strings"
)

// The delegation graph of a session: which agent handed
// work to which, drawn from what stui already knows of each pane -- the Claude
// agent team it is in (the lead's pane to each teammate's) and the subagents
// its agent started. stui's own workers (stui worker, tmux pane options) join
// later. Built from a snapshot of the panes, so it holds no state of its own.

// Node kinds.
const (
	NodePane     = "pane"     // an agent in a tmux pane: one can go to it
	NodeSubagent = "subagent" // a subagent inside a pane's agent: no pane
	NodeTeammate = "teammate" // an in-process teammate: no pane either
)

// Edge kinds: where the relation comes from.
const (
	EdgeTeam     = "team"
	EdgeSubagent = "subagent"
	// stui worker: the coordinator's pane to each worker it started.
	EdgeOrch = "orch"
)

type GraphNode struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Agent      string `json:"agent,omitempty"`
	Label      string `json:"label"`
	Detail     string `json:"detail,omitempty"`
	State      string `json:"state"`
	Since      int64  `json:"since,omitempty"`
	Window     *int   `json:"window,omitempty"`
	WindowName string `json:"windowName,omitempty"`
	Pane       *int   `json:"pane,omitempty"`
	Role       string `json:"role,omitempty"`
	// The Claude agent team's name; "team" in JSON unless OrchTeam is set
	// (MarshalJSON).
	Team string `json:"-"`
	// The pane a subagent or in-process teammate belongs to.
	Parent    string `json:"parent,omitempty"`
	StartedAt int64  `json:"startedAt,omitempty"`
	EndedAt   *int64 `json:"endedAt,omitempty"`
	// stui orchestration: the run, and what the worker reported when it was done.
	Run     string `json:"run,omitempty"`
	Done    bool   `json:"done,omitempty"`
	Summary string `json:"summary,omitempty"`
	// A worker's own worktree branch, and its fan (GET /api/orch/diff).
	Worktree string `json:"worktree,omitempty"`
	Fan      string `json:"fan,omitempty"`
	// A pane of another session, here because it directs one of this
	// session's agents or works for one (an adopted worker can be anywhere).
	Session string `json:"session,omitempty"`
	// A worker that leads a team of its own (stui orchestration): its run
	// and how many workers it directs. "team" in JSON, as an object.
	OrchTeam *OrchTeam `json:"-"`
	// Adopted, and its role and brief (Orch).
	Adopted    bool   `json:"adopted,omitempty"`
	About      string `json:"about,omitempty"`
	Brief      string `json:"brief,omitempty"`
	BriefState string `json:"briefState,omitempty"`
	BriefAt    int64  `json:"briefAt,omitempty"`
}

// MarshalJSON writes "team" as the Claude agent team's name (a string) or,
// for the lead of a team adopted as a worker, {"run","members"}; the
// object wins when a node is both.
func (n GraphNode) MarshalJSON() ([]byte, error) {
	type plain GraphNode
	out := struct {
		plain
		Team any `json:"team,omitempty"`
	}{plain: plain(n)}
	switch {
	case n.OrchTeam != nil:
		out.Team = n.OrchTeam
	case n.Team != "":
		out.Team = n.Team
	}
	return json.Marshal(out)
}

// UnmarshalJSON reads "team" in either form.
func (n *GraphNode) UnmarshalJSON(b []byte) error {
	type plain GraphNode
	var in struct {
		plain
		Team json.RawMessage `json:"team"`
	}
	if err := json.Unmarshal(b, &in); err != nil {
		return err
	}
	*n = GraphNode(in.plain)
	if len(in.Team) > 0 && in.Team[0] == '{' {
		n.OrchTeam = &OrchTeam{}
		return json.Unmarshal(in.Team, n.OrchTeam)
	}
	if len(in.Team) > 0 && in.Team[0] == '"' {
		return json.Unmarshal(in.Team, &n.Team)
	}
	return nil
}

type GraphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

type Graph struct {
	V       int         `json:"v"`
	Seq     int64       `json:"seq"`
	Session string      `json:"session"`
	Nodes   []GraphNode `json:"nodes"`
	Edges   []GraphEdge `json:"edges"`
}

// GraphSummary is a session that has any delegation, for a list.
type GraphSummary struct {
	Session string `json:"session"`
	Nodes   int    `json:"nodes"`
	Edges   int    `json:"edges"`
	// Agents in the graph that are working, or waiting on the user.
	Working int `json:"working"`
	Waiting int `json:"waiting"`
}

// BuildGraph is session's graph from every listed pane. Every agent pane of
// the session is a node, related or not; edges only where both ends are known.
func BuildGraph(session string, panes []Pane) Graph {
	g := Graph{V: 1, Session: session, Nodes: []GraphNode{}, Edges: []GraphEdge{}}
	in := []Pane{}
	for _, p := range panes {
		if p.Session == session {
			in = append(in, p)
		}
	}
	sort.SliceStable(in, func(i, j int) bool {
		if in[i].Window != in[j].Window {
			return in[i].Window < in[j].Window
		}
		return in[i].Pane < in[j].Pane
	})
	byID := map[string]bool{}
	for _, p := range in {
		byID[p.ID] = true
	}
	for _, p := range in {
		w, pn := p.Window, p.Pane
		n := GraphNode{ID: p.ID, Kind: NodePane, Agent: p.Agent, Label: paneLabel(p), Detail: p.Title, State: p.State,
			Since: p.Since, Window: &w, WindowName: p.WindowName, Pane: &pn}
		if o := p.Orch; o != nil {
			n.Role, n.Run, n.Done, n.Summary, n.Worktree, n.Fan, n.OrchTeam = o.Role, o.Run, o.Done, o.Summary, o.Worktree, o.Fan, o.Team
			n.Adopted, n.About, n.Brief, n.BriefState, n.BriefAt = o.Adopted, o.About, o.Brief, o.BriefState, o.BriefAt
			if o.Task != "" {
				n.Detail = o.Task
			}
			if o.Parent != "" && byID[o.Parent] && o.Parent != p.ID {
				g.Edges = append(g.Edges, GraphEdge{From: o.Parent, To: p.ID, Kind: EdgeOrch})
			}
		}
		if p.Team != nil {
			n.Role, n.Team = p.Team.Role, p.Team.Name
			if p.Team.Role == RoleTeammate && p.Team.LeadPane != "" && byID[p.Team.LeadPane] && p.Team.LeadPane != p.ID {
				g.Edges = append(g.Edges, GraphEdge{From: p.Team.LeadPane, To: p.ID, Kind: EdgeTeam})
			}
		}
		g.Nodes = append(g.Nodes, n)
	}
	// stui orchestration across sessions: the other end comes in as a node of
	// its own session, so the line has somewhere to go.
	all := map[string]Pane{}
	for _, p := range panes {
		if _, seen := all[p.ID]; !seen {
			all[p.ID] = p
		}
	}
	foreign := map[string]bool{}
	addForeign := func(p Pane) {
		if byID[p.ID] || foreign[p.ID] {
			return
		}
		foreign[p.ID] = true
		w, pn := p.Window, p.Pane
		n := GraphNode{ID: p.ID, Kind: NodePane, Agent: p.Agent, Label: paneLabel(p), Detail: p.Title, State: p.State,
			Since: p.Since, Window: &w, WindowName: p.WindowName, Pane: &pn, Session: p.Session}
		if o := p.Orch; o != nil {
			n.Role, n.Run, n.Done, n.Summary, n.Worktree, n.Fan, n.OrchTeam = o.Role, o.Run, o.Done, o.Summary, o.Worktree, o.Fan, o.Team
			n.Adopted, n.About, n.Brief, n.BriefState, n.BriefAt = o.Adopted, o.About, o.Brief, o.BriefState, o.BriefAt
		}
		g.Nodes = append(g.Nodes, n)
	}
	for _, p := range in {
		if o := p.Orch; o != nil && o.Parent != "" && !byID[o.Parent] {
			if q, ok := all[o.Parent]; ok {
				addForeign(q)
				g.Edges = append(g.Edges, GraphEdge{From: q.ID, To: p.ID, Kind: EdgeOrch})
			}
		}
	}
	for _, q := range panes {
		if o := q.Orch; o != nil && o.Parent != "" && byID[o.Parent] && q.Session != session && !byID[q.ID] {
			if !foreign[q.ID] {
				addForeign(q)
				g.Edges = append(g.Edges, GraphEdge{From: o.Parent, To: q.ID, Kind: EdgeOrch})
			}
		}
	}
	for _, p := range in {
		// In-process teammates: listed on the lead, with no pane of their
		// own (one with a pane is already a node, by its member name).
		if p.Team != nil && p.Team.Role == RoleLead {
			withPane := map[string]bool{}
			for _, q := range in {
				if q.Team != nil && q.Team.LeadPane == p.ID {
					withPane[q.Team.Member] = true
				}
			}
			for _, t := range p.Teammates {
				if withPane[t.Name] {
					continue
				}
				id := p.ID + "/tm-" + t.Name
				g.Nodes = append(g.Nodes, GraphNode{ID: id, Kind: NodeTeammate, Agent: p.Agent, Label: t.Name, State: t.State, Role: RoleTeammate, Team: p.Team.Name, Parent: p.ID})
				g.Edges = append(g.Edges, GraphEdge{From: p.ID, To: id, Kind: EdgeTeam})
			}
		}
		items := append([]SubagentItem(nil), p.Subagents.Items...)
		sort.SliceStable(items, func(i, j int) bool { return items[i].StartedAt < items[j].StartedAt })
		for _, s := range items {
			id := p.ID + "/" + s.ID
			label := s.Type
			if label == "" {
				label = "subagent"
			}
			g.Nodes = append(g.Nodes, GraphNode{ID: id, Kind: NodeSubagent, Agent: p.Agent, Label: label, Detail: s.Description, State: s.State,
				Parent: p.ID, StartedAt: s.StartedAt, EndedAt: s.EndedAt})
			g.Edges = append(g.Edges, GraphEdge{From: p.ID, To: id, Kind: EdgeSubagent})
		}
	}
	return g
}

// paneLabel names a pane's agent in the graph: its team name for it, else
// its window's name, else the agent.
//
// A worker's own name (`@stui-name`) is not here on purpose. stui names a
// worker's window after the worker when it starts it, so the two agree until
// somebody renames the window -- and a window renamed since is the name that
// person chose for that pane. Every other surface shows it: the window tabs,
// the sidebar's rows (which read /api/windows), and the app's graph nodes for
// panes on *other* machines, which have always preferred the window's name.
// Only a local graph node used to answer with the worker's name, so renaming
// a window changed it everywhere but there.
//
// The name stays the worker's handle (`stui worker send api`); it is the label
// that follows the window.
func paneLabel(p Pane) string {
	if p.Team != nil && p.Team.Member != "" {
		return p.Team.Member
	}
	if strings.TrimSpace(p.WindowName) != "" {
		return p.WindowName
	}
	if p.Orch != nil && p.Orch.Name != "" {
		return p.Orch.Name
	}
	if p.Agent != "" {
		return p.Agent
	}
	return p.Command
}

// Graphs is each session that has any delegation, in name order.
func Graphs(panes []Pane) []GraphSummary {
	seen := map[string]bool{}
	names := []string{}
	for _, p := range panes {
		if !seen[p.Session] {
			seen[p.Session] = true
			names = append(names, p.Session)
		}
	}
	sort.Strings(names)
	out := []GraphSummary{}
	for _, s := range names {
		g := BuildGraph(s, panes)
		if len(g.Edges) == 0 {
			continue
		}
		sum := GraphSummary{Session: s, Nodes: len(g.Nodes), Edges: len(g.Edges)}
		for _, n := range g.Nodes {
			switch n.State {
			case Working, SubRunning:
				sum.Working++
			case Waiting:
				sum.Waiting++
			}
		}
		out = append(out, sum)
	}
	return out
}

// Graph is GET /api/graph?session=s.
func (w *Watcher) Graph(session string) Graph {
	w.mu.Lock()
	defer w.mu.Unlock()
	g := BuildGraph(session, w.listLocked())
	g.Seq = w.seq
	return g
}

// GraphList is GET /api/graph without a session.
func (w *Watcher) GraphList() map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	return map[string]any{"v": 1, "seq": w.seq, "sessions": Graphs(w.listLocked())}
}
