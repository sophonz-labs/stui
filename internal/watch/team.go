package watch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Claude Code agent teams (experimental, CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS).
// In tmux split-pane mode the lead splits its own window and starts each
// teammate in a pane of its own:
//
//	cd <cwd> && env CLAUDECODE=1 CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1 … \
//	  claude --agent-id <name>@<team> --agent-name <name> --team-name <team> \
//	  --agent-color <c> --parent-session-id <lead's session id> …
//
// and keeps the team in <claude config dir>/teams/<team>/config.json:
// {name, leadAgentId, leadSessionId, members: [{agentId, name, agentType,
// tmuxPaneId, backendType, …}]}, the lead's own entry with tmuxPaneId
// "leader", in-process teammates with "in-process". The team is
// session-<first 8 of the lead's session id>; its directory goes when the
// lead's session ends. A teammate still running after that is orphaned:
// nothing gives it work again, and its pane stays until someone closes it.
// (Claude Code 2.1.288 and https://code.claude.com/docs/en/agent-teams.)
//
// stui finds a pane's team two ways: the team config naming the pane, and the
// teammate's own command line (which outlives the config). The lead's pane
// is the one whose Claude hooks carry the team's leadSessionId.

type teamArgs struct{ team, member, parent string }

// parseTeamArgs reads a teammate's flags off its command line, or nil when
// it is not one.
func parseTeamArgs(argv []string) *teamArgs {
	var t teamArgs
	agentID := false
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		flag, val, eq := strings.Cut(a, "=")
		if !eq {
			if i+1 < len(argv) {
				val = argv[i+1]
			} else {
				val = ""
			}
		}
		take := func() string {
			if !eq {
				i++
			}
			return strings.Trim(val, `'"`)
		}
		switch flag {
		case "--agent-id":
			take()
			agentID = true
		case "--team-name":
			t.team = take()
		case "--agent-name":
			t.member = take()
		case "--parent-session-id":
			t.parent = take()
		}
	}
	if !agentID || t.team == "" {
		return nil
	}
	return &t
}

type teamMember struct {
	AgentID     string `json:"agentId"`
	Name        string `json:"name"`
	AgentType   string `json:"agentType"`
	TmuxPaneID  string `json:"tmuxPaneId"`
	BackendType string `json:"backendType"`
}

type teamConfig struct {
	Name          string       `json:"name"`
	LeadSessionID string       `json:"leadSessionId"`
	Members       []teamMember `json:"members"`
}

// teamSet is what the team directories held at one poll.
type teamSet struct {
	byName map[string]*teamConfig
	// A teams directory exists: a team missing from it has ended (rather
	// than living in a config directory stui does not know of).
	dirExists bool
}

// defaultClaudeDir is where Claude keeps its config for stui's own user.
func defaultClaudeDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(h, ".claude")
}

// loadTeams reads every team config under dirs.
func loadTeams(dirs []string) teamSet {
	set := teamSet{byName: map[string]*teamConfig{}}
	for _, d := range dirs {
		if d == "" {
			continue
		}
		root := filepath.Join(d, "teams")
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		set.dirExists = true
		for n, e := range entries {
			if n >= 64 {
				break
			}
			if !e.IsDir() {
				continue
			}
			f := filepath.Join(root, e.Name(), "config.json")
			info, err := os.Stat(f)
			if err != nil || info.Size() > 1<<20 {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			var c teamConfig
			if json.Unmarshal(b, &c) != nil {
				continue
			}
			if c.Name == "" {
				c.Name = e.Name()
			}
			set.byName[c.Name] = &c
		}
	}
	return set
}

// argsRecheck is how often a Claude pane that is not a teammate has its
// command line read again.
const argsRecheck = 30 * time.Second

// needArgs reports whether p's command line is worth reading at this poll.
func needArgs(p *paneState, command, tty string, now time.Time) bool {
	if AgentFor(command) != "claude" || tty == "" {
		return false
	}
	if p == nil {
		return true
	}
	if p.argsKey != command+"|"+tty {
		return true
	}
	return p.teamArgs == nil && now.Sub(p.argsAt) > argsRecheck
}

func sameTeam(a, b *Team) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// assignTeamsLocked works out every pane's team from set, and reports the
// panes whose team changed.
func (w *Watcher) assignTeamsLocked(set teamSet) []*paneState {
	// The lead's pane: the one whose main agent's session is the team's.
	paneOf := map[string]string{}
	for id, p := range w.panes {
		if p.Agent == "claude" && p.resumeID != "" {
			paneOf[p.resumeID] = id
		}
	}
	// A config missing at two polls in a row is gone, not being rewritten.
	missing := map[string]int{}
	gone := func(team string) bool {
		if set.byName[team] != nil {
			return false
		}
		missing[team] = w.teamMissing[team] + 1
		return missing[team] >= 2
	}
	defer func() { w.teamMissing = missing }()
	isClaude := func(id string) bool {
		q := w.panes[id]
		return q != nil && (q.Agent == "claude" || q.cmdAgent == "claude")
	}
	var changed []*paneState
	for id, p := range w.panes {
		var t *Team
		if isClaude(id) {
			for _, c := range set.byName {
				for _, m := range c.Members {
					if m.TmuxPaneID == id {
						t = &Team{Name: c.Name, Role: RoleTeammate, Member: m.Name, LeadPane: paneOf[c.LeadSessionID]}
					}
				}
			}
			if t == nil && p.teamArgs != nil {
				a := p.teamArgs
				t = &Team{Name: a.team, Role: RoleTeammate, Member: a.member, LeadPane: paneOf[a.parent]}
				if set.dirExists && gone(a.team) {
					t.Ended = true
				}
			}
			if t == nil && p.resumeID != "" {
				for _, c := range set.byName {
					if c.LeadSessionID == p.resumeID && len(c.Members) > 1 {
						t = &Team{Name: c.Name, Role: RoleLead, Member: "team-lead", LeadPane: id}
					}
				}
			}
			if t == nil && p.Team != nil && p.Team.Role == RoleTeammate && set.byName[p.Team.Name] == nil {
				// Known to be a teammate, and its team is gone.
				old := *p.Team
				old.Ended = old.Ended || gone(old.Name)
				t = &old
			}
			if t != nil && t.Role == RoleTeammate {
				if t.LeadPane == "" && p.Team != nil && p.Team.Name == t.Name {
					t.LeadPane = p.Team.LeadPane
				}
				// The lead's pane gone, or its Claude with it.
				if t.LeadPane != "" && !isClaude(t.LeadPane) {
					t.Ended = true
				}
			}
		}
		if !sameTeam(t, p.Team) {
			p.Team = t
			changed = append(changed, p)
		}
	}
	return changed
}
