package restore

// Which line a restored pane gets, and whether stui enters it.
//
// A command a pane was running is never started again: it may have been a
// deploy, a migration, an rm. An agent's resume line is another thing: stui
// wrote it from the snapshot (the agent and its conversation id), and all it
// does is go on with that conversation. So stui enters it -- only when the
// snapshot names the conversation, and Claude Code or Codex still has its
// record. Without the record `--resume` fails anyway; the line is typed and
// the pane says why it was not entered.

import (
	"os"
	"path/filepath"
	"regexp"
)

// What a restore does in one pane.
const (
	// Run: the agent's resume line is typed and entered.
	Run = "run"
	// TypeOnly: the agent's resume line is typed, Enter left to a person.
	TypeOnly = "type"
	// Command: it was running something else, which is shown, not started.
	Command = "command"
)

// Decision is what a restore does in one pane, and why.
type Decision struct {
	Action string
	// The line typed at the prompt (Run, TypeOnly).
	Line string
	// Why a resume line is only typed.
	Why string
	// The conversation's record is not on this machine.
	NoRecord bool
	// Options the agent was started with that Line does not carry: not
	// known to be kept on a resume, or their value could not be read back.
	Left []string
}

// Decide is what a restore does in pane p.
func Decide(p Pane, o Options) Decision {
	line, left := resumeWith(p.Agent, p.Args)
	switch {
	case line != "":
		if p.Agent.ID == "" {
			return Decision{Action: TypeOnly, Line: line, Why: "no conversation id in the snapshot", Left: left}
		}
		if !o.ResumeAgents {
			return Decision{Action: TypeOnly, Line: line, Why: "resuming agents is off", Left: left}
		}
		has := o.HasRecord
		if has == nil {
			has = HasRecord
		}
		if !has(p.Agent) {
			return Decision{Action: TypeOnly, Line: line, Why: "no record of conversation " + p.Agent.ID + " on this machine", NoRecord: true, Left: left}
		}
		return Decision{Action: Run, Line: line, Left: left}
	case p.Agent != nil:
		return Decision{}
	case p.Args != "", p.Command != "" && !IsShell(p.Command):
		return Decision{Action: Command}
	}
	return Decision{}
}

// Counts is how a restore's panes came out.
type Counts struct {
	// Agents whose resume line was entered.
	Resumed int `json:"resumed"`
	// Agents whose line was only typed, and of them those with no record.
	Typed    int `json:"typed"`
	NoRecord int `json:"noRecord"`
	// Panes that were running something else, left to a person.
	Commands int `json:"commands"`
}

func (c *Counts) add(d Decision) {
	switch d.Action {
	case Run:
		c.Resumed++
	case TypeOnly:
		c.Typed++
		if d.NoRecord {
			c.NoRecord++
		}
	case Command:
		c.Commands++
	}
}

// CountPanes is how the panes of the sessions named would come out.
func CountPanes(snap *Snapshot, sessions map[string]bool, o Options) Counts {
	var c Counts
	for _, se := range snap.Sessions {
		if sessions != nil && !sessions[se.Name] {
			continue
		}
		for _, w := range se.Windows {
			for _, p := range w.Panes {
				c.add(Decide(p, o))
			}
		}
	}
	return c
}

var recordID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// HasRecord reports whether the agent's conversation is still kept where
// its CLI keeps it: Claude Code's <config dir>/projects/*/<id>.jsonl
// ($CLAUDE_CONFIG_DIR, else ~/.claude), Codex's
// <home>/sessions/YYYY/MM/DD/rollout-*-<id>.jsonl ($CODEX_HOME, else
// ~/.codex). Only a file with something in it counts.
func HasRecord(a *Agent) bool { return RecordPath(a) != "" }

// RecordPath is the file HasRecord finds, "" when there is none: of
// several (Claude Code keeps one per project folder, and a conversation
// resumed in another folder is in two), the one written last.
func RecordPath(a *Agent) string {
	if a == nil || !recordID.MatchString(a.ID) {
		return ""
	}
	best, bestT := "", int64(0)
	for _, g := range recordGlobs(a.Kind, a.ID) {
		matches, _ := filepath.Glob(g)
		for _, m := range matches {
			if fi, err := os.Stat(m); err == nil && fi.Mode().IsRegular() && fi.Size() > 0 && fi.ModTime().UnixNano() > bestT {
				best, bestT = m, fi.ModTime().UnixNano()
			}
		}
	}
	return best
}

// ClaudeDir and CodexDir are where the two CLIs keep their conversations.
func ClaudeDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

func CodexDir() string {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

func recordGlobs(kind, id string) []string {
	home, _ := os.UserHomeDir()
	var globs []string
	switch kind {
	case "claude":
		for _, d := range []string{os.Getenv("CLAUDE_CONFIG_DIR"), filepath.Join(home, ".claude")} {
			if d != "" {
				globs = append(globs, filepath.Join(d, "projects", "*", id+".jsonl"))
			}
		}
	case "codex":
		globs = append(globs, filepath.Join(CodexDir(), "sessions", "*", "*", "*", "rollout-*"+id+".jsonl"))
	}
	return globs
}
