package hookclient

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sophonz-labs/stui/internal/usage"
)

// Claude Code's Stop hook can keep a turn going ({"decision":"block",
// "reason": ...}: Claude reads the reason and goes on) or end a loop that
// would go on ({"continue":false}). That is the whole of a loop, and stui
// answers that hook already, so a loop needs nothing more of stui than this:
//
//   - the pane has a goal (stui goal): the turn is kept going, the goal its
//     reason;
//   - the turn is a loop's -- a goal's, or one Claude's own /goal or another
//     Stop hook kept going (stop_hook_active) -- and the usage guard is on
//     and says stop: the loop ends, with why;
//   - anything else: nothing is printed, and Claude decides.
//
// Only Claude's answer is followed. opencode's and Pi's modules run stui hook
// with its output thrown away (hooks/opencode.js, pi.js: stdio "ignore"),
// and Codex's /goal does not stop for a Stop hook (usage-coach, measured).

// OptGoal is the pane option naming the pane's goal file.
const OptGoal = "@stui-goal"

// GoalDir is where goal files are: ~/.stui/goals.
func GoalDir(home string) string { return filepath.Join(home, "goals") }

// maxGoal is as much of a goal file as is read.
const maxGoal = 64 << 10

// ReadGoal is the goal in file, "" when there is none.
func ReadGoal(file string) string {
	if file == "" {
		return ""
	}
	f, err := os.Open(file)
	if err != nil {
		return ""
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, maxGoal))
	return strings.TrimSpace(string(b))
}

// goalReason is what Claude reads when its turn is kept going.
func goalReason(goal string) string {
	return goal + "\n\n(stui goal: this pane has a goal, so stui kept the turn going. When the goal is met, or cannot be met, run `" + stCommand() + " goal clear` and stop.)"
}

// stCommand is this stui, by its path: the agent's shell may find another stui
// first, one with no stui goal (measured: its zsh put an older stui ahead of
// the PATH the hook had, and the loop could not end).
func stCommand() string {
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return "stui"
}

// stopDecision is the answer to a Claude Stop hook, or nil. goal is the
// pane's goal ("" none); guard is the usage guard's word and whether it is
// on, asked only when the turn is a loop's.
func stopDecision(kind, event string, raw []byte, goal func() string, guard func() (usage.Verdict, bool)) []byte {
	if kind != "claude" || event != "Stop" {
		return nil
	}
	var in struct {
		Active bool `json:"stop_hook_active"`
	}
	json.Unmarshal(raw, &in)
	g := goal()
	if g == "" && !in.Active {
		return nil
	}
	if v, on := guard(); on && v.Stop {
		b, _ := json.Marshal(map[string]any{
			"continue":   false,
			"stopReason": "stui usage guard: " + v.Reason + ". The loop is stopped: stui usage guard off to go on anyway.",
		})
		return b
	}
	if g == "" {
		return nil
	}
	b, _ := json.Marshal(map[string]any{"decision": "block", "reason": goalReason(g)})
	return b
}

// paneGoal reads the pane's goal.
func paneGoal(pane string) func() string {
	return func() string {
		v := paneOptions(pane, OptGoal)
		if v == nil {
			return ""
		}
		return ReadGoal(v[0])
	}
}

// homeGuard is the usage guard's word on this Claude, from what stui keeps in
// home.
func homeGuard(home string) func() (usage.Verdict, bool) {
	return func() (usage.Verdict, bool) {
		e := usage.DefaultEnv(home)
		c := usage.ReadGuard(e)
		if !c.On {
			return usage.Verdict{}, false
		}
		profile := ""
		if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
			profile = filepath.Base(d)
		}
		return usage.GuardFor(e, c, "claude", profile), true
	}
}
