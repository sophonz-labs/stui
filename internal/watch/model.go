// Package watch is agent watch on the stui side: which coding agent runs in
// which tmux pane, whether it is working, waiting or done, what it is asking,
// and answering it.
package watch

import "sort"

// States, section 1.
const (
	Working = "working"
	Waiting = "waiting"
	Done    = "done"
	Error   = "error"
	Idle    = "idle"
	Unknown = "unknown"
)

// Phases of a working pane, section 1: the main agent's turn, or the work it
// handed to subagents after its turn ended.
const (
	PhaseMain      = "main"
	PhaseSubagents = "subagents"
)

// How a prompt ends, section 2.
const (
	AnsweredDesk  = "answered-desk"
	AnsweredPhone = "answered-phone"
	Expired       = "expired"
)

// Subagent states, for SubagentItem.
const (
	SubRunning = "running"
	SubDone    = "done"
	SubLost    = "lost"
)

type Subagents struct {
	Running int `json:"running"`
	Done    int `json:"done"`
	Lost    int `json:"lost"`
	// One per subagent: those running, and those of the current turn that
	// ended (section 1, subagents).
	Items []SubagentItem `json:"items"`
}

// SubagentItem is one subagent a pane's agent started.
type SubagentItem struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Description string `json:"description"`
	State       string `json:"state"`
	StartedAt   int64  `json:"startedAt"`
	EndedAt     *int64 `json:"endedAt"`
}

// Team roles.
const (
	RoleLead     = "lead"
	RoleTeammate = "teammate"
)

// Team is the Claude Code agent team a pane's agent belongs to.
type Team struct {
	Name     string `json:"name"`
	Role     string `json:"role"`
	Member   string `json:"member"`
	LeadPane string `json:"leadPane"`
	// The team is gone (its lead's session ended) while this teammate
	// still runs: nothing will give it work again.
	Ended bool `json:"ended"`
}

type Teammate struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

type Turn struct {
	StartedAt  int64 `json:"startedAt"`
	DurationMs int64 `json:"durationMs"`
}

type Prompt struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Title     string   `json:"title"`
	Question  string   `json:"question"`
	Detail    string   `json:"detail"`
	Options   []Option `json:"options"`
	AllowText bool     `json:"allowText"`
	CreatedAt int64    `json:"createdAt"`
	Agent     string   `json:"agent"`
	Tool      string   `json:"tool,omitempty"`
	// AskUserQuestion's questions, from the one on screen to the last, when
	// stui heard the call (ask.go): what an answer with "answers" answers.
	Questions []AskQuestion `json:"questions,omitempty"`
}

type Pane struct {
	ID         string `json:"id"`
	Session    string `json:"session"`
	Window     int    `json:"window"`
	WindowName string `json:"windowName"`
	Pane       int    `json:"pane"`
	Agent      string `json:"agent"`
	Command    string `json:"command"`
	// The agent's icon, or the command's (internal/cmdicon).
	Icon  string `json:"icon,omitempty"`
	Title string `json:"title"`
	State string `json:"state"`
	Phase string `json:"phase"`
	Since int64  `json:"since"`
	// When its main agent last took a prompt (ms; Claude Code's
	// UserPromptSubmit, heard live), 0 for not heard since stui started: stui
	// worker done tells a report of the last task from one of the new.
	Submitted  int64      `json:"submitted,omitempty"`
	Turn       *Turn      `json:"turn"`
	LastTurnMs *int64     `json:"lastTurnMs"`
	Subagents  Subagents  `json:"subagents"`
	Teammates  []Teammate `json:"teammates"`
	Team       *Team      `json:"team,omitempty"`
	Prompt     *Prompt    `json:"prompt"`
	Source     string     `json:"source"`
	// The pane's program reported its status itself (OSC 7501): state is
	// its word, not read from hooks or the screen. Report is what it said.
	Reported bool    `json:"reported,omitempty"`
	Report   *Report `json:"report,omitempty"`
	// stui orchestration: the pane's place in a run, from
	// its tmux options. Absent for a pane stui worker did not start or mark.
	Orch *Orch `json:"orch,omitempty"`
}

// Orchestration roles.
const (
	RoleCoordinator = "coordinator"
	RoleWorker      = "worker"
)

// Orch is a pane's tmux options @stui-run, @stui-role, @stui-parent, @stui-name,
// @stui-task, @stui-done and @stui-summary: kept by tmux, so they outlive stui and
// go with the pane.
type Orch struct {
	Run     string `json:"run"`
	Role    string `json:"role"`
	Parent  string `json:"parent,omitempty"`
	Name    string `json:"name,omitempty"`
	Task    string `json:"task,omitempty"`
	Done    bool   `json:"done,omitempty"`
	Summary string `json:"summary,omitempty"`
	// The worker's own worktree branch (GET /api/orch/diff reads it), and
	// the fan it is one of.
	Worktree string `json:"worktree,omitempty"`
	Fan      string `json:"fan,omitempty"`
	// A team's lead adopted as a worker: the run it works in
	// (@stui-in-run; Run stays its team's), and its team.
	InRun string    `json:"inRun,omitempty"`
	Team  *OrchTeam `json:"team,omitempty"`
	// Adopted rather than started (@stui-adopted); its role (@stui-about) and
	// its own brief of what it had done (@stui-brief, -state, -at).
	Adopted    bool   `json:"adopted,omitempty"`
	About      string `json:"about,omitempty"`
	Brief      string `json:"brief,omitempty"`
	BriefState string `json:"briefState,omitempty"`
	BriefAt    int64  `json:"briefAt,omitempty"`
}

// OrchTeam is the team a worker leads: its own run and how many workers
// it directs.
type OrchTeam struct {
	Run     string `json:"run"`
	Members int    `json:"members"`
}

// markOrchTeams gives each team's lead (a pane with @stui-in-run and workers
// of its own) its team.
func markOrchTeams(panes []*Pane) {
	n := map[string]int{}
	for _, p := range panes {
		if p.Orch != nil && p.Orch.Parent != "" && p.Orch.Parent != p.ID {
			n[p.Orch.Parent]++
		}
	}
	for _, p := range panes {
		if o := p.Orch; o != nil {
			o.Team = nil
			if o.InRun != "" && n[p.ID] > 0 {
				o.Team = &OrchTeam{Run: o.Run, Members: n[p.ID]}
			}
		}
	}
}

type SubagentCount struct {
	Running int `json:"running"`
	Done    int `json:"done"`
}

// SessionSummary is a session's panes in one line.
type SessionSummary struct {
	Name    string `json:"name,omitempty"`
	State   string `json:"state"`
	Working int    `json:"working"`
	Waiting int    `json:"waiting"`
	// Of the working panes, those in the subagents phase.
	Delegated int `json:"delegated"`
	// subagents when the session is working and every working pane is in
	// the subagents phase; main otherwise.
	Phase     string        `json:"phase"`
	Done      int           `json:"done"`
	Error     int           `json:"error"`
	Idle      int           `json:"idle"`
	Subagents SubagentCount `json:"subagents"`
	Since     int64         `json:"since"`
	// Teammate panes whose team has ended.
	TeamEnded int `json:"teamEnded"`
}

type Snapshot struct {
	V     int             `json:"v"`
	Seq   int64           `json:"seq"`
	At    int64           `json:"at"`
	Hooks map[string]bool `json:"hooks"`
	// "installed", "partial" or "none" per agent stui can hook. "partial":
	// some events stui listens to are not wired to it (stui hooks install adds
	// them), and subagents are not given up on for silence.
	HookState map[string]string `json:"hookState,omitempty"`
	// Each agent's hooks against what stui wants: state (installed, outdated,
	// partial, none), version, required, missing.
	HookSets any              `json:"hookSets,omitempty"`
	Sessions []SessionSummary `json:"sessions"`
	Panes    []Pane           `json:"panes"`
}

// urgency orders states for a session's own: most urgent first.
var urgency = map[string]int{Waiting: 6, Error: 5, Working: 4, Done: 3, Idle: 2, Unknown: 1}

// MostUrgent is the state that wins among these.
func MostUrgent(states ...string) string {
	best := Unknown
	for _, s := range states {
		if urgency[s] > urgency[best] {
			best = s
		}
	}
	return best
}

// Summarize groups panes by session, sorted by name.
func Summarize(panes []Pane) []SessionSummary {
	by := map[string]*SessionSummary{}
	for _, p := range panes {
		s := by[p.Session]
		if s == nil {
			s = &SessionSummary{Name: p.Session, State: Unknown}
			by[p.Session] = s
		}
		switch p.State {
		case Working:
			s.Working++
			if p.Phase == PhaseSubagents {
				s.Delegated++
			}
		case Waiting:
			s.Waiting++
		case Done:
			s.Done++
		case Error:
			s.Error++
		case Idle:
			s.Idle++
		}
		if p.Team != nil && p.Team.Ended {
			s.TeamEnded++
		}
		s.Subagents.Running += p.Subagents.Running
		s.Subagents.Done += p.Subagents.Done
		if urgency[p.State] > urgency[s.State] {
			s.State, s.Since = p.State, p.Since
		} else if p.State == s.State && p.Since > s.Since {
			s.Since = p.Since
		}
	}
	out := make([]SessionSummary, 0, len(by))
	for _, s := range by {
		s.Phase = PhaseMain
		if s.State == Working && s.Working > 0 && s.Delegated == s.Working {
			s.Phase = PhaseSubagents
		}
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
