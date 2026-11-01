package hooks

// Which coding agents are on this machine, and which of them stui can hook.
//
// Two different questions, kept apart on purpose. stui can *find* far more
// agents than it can *hook*: hooking one needs a place in its configuration
// where a command can be registered against its events, and only some have
// that. Listing an agent stui cannot hook is still worth doing -- "found, but
// not yet" is an answer, and silence is not.
//
// Adding an agent is a row in Agents(): how to find it, and an Install when
// there is a way in. Nothing else in stui needs to know the list.

import (
	"os"
	"os/exec"
	"path/filepath"
)

// Presence is how an agent was found, or that it was not.
type Presence int

const (
	Absent Presence = iota
	// Its configuration directory is here, or its command is on PATH.
	Present
)

// State is what stui has done about an agent it can hook.
type State int

const (
	// stui has no hooks for this agent yet.
	Unsupported State = iota
	NotInstalled
	// This stui's version, with events stui listens to taken out since.
	Partly
	// Written by an older stui: fewer events, or another command.
	Outdated
	Installed
)

// Agent is one coding agent stui knows how to look for.
type Agent struct {
	ID   string
	Name string
	// Where its configuration lives, relative to home, for finding it. The
	// first that exists counts.
	Dirs []string
	// Its command, for finding it on PATH.
	Bin string
	// nil when stui has no way to register hooks with it yet.
	Install   func(Env) (Result, error)
	Uninstall func(Env) (Result, error)
	// Installed, Outdated, Partly or NotInstalled. nil alongside a nil
	// Install.
	Status func(Env) State
	// The set as it is, against what this stui wants. nil alongside a nil
	// Install.
	Info func(Env) SetInfo
	// Said when there is no Install: what would have to exist for one.
	Why string
}

// Hookable is whether stui can register hooks with this agent.
func (a Agent) Hookable() bool { return a.Install != nil }

// Found is whether this agent is on the machine at all.
func (a Agent) Found(e Env) Presence {
	if a.Bin != "" {
		if _, err := exec.LookPath(a.Bin); err == nil {
			return Present
		}
	}
	for _, d := range a.Dirs {
		if fi, err := os.Stat(filepath.Join(e.Home, d)); err == nil && fi.IsDir() {
			return Present
		}
	}
	return Absent
}

// Agents is every agent stui looks for, in the order it reports them.
//
// The ones with no Install are here because being told "opencode is here, stui
// cannot report from it yet" is better than stui appearing not to have looked.
func Agents() []Agent {
	return []Agent{
		{
			ID: "claude", Name: "Claude Code", Bin: "claude", Dirs: []string{".claude"},
			Install: InstallClaude, Uninstall: UninstallClaude,
			Info: func(e Env) SetInfo {
				info, _ := ClaudeInfo(e)
				return info
			},
			Status: func(e Env) State {
				info, err := ClaudeInfo(e)
				if err != nil {
					return NotInstalled
				}
				return stateOf(info.State)
			},
		},
		{
			ID: "codex", Name: "Codex", Bin: "codex", Dirs: []string{".codex"},
			Install: InstallCodex, Uninstall: UninstallCodex,
			Info:   CodexInfo,
			Status: func(e Env) State { return stateOf(CodexInfo(e).State) },
		},
		{
			ID: "gemini", Name: "Gemini CLI", Bin: "gemini", Dirs: []string{".gemini"},
			Install: InstallGemini, Uninstall: UninstallGemini,
			Info: func(e Env) SetInfo {
				info, _ := GeminiInfo(e)
				return info
			},
			Status: func(e Env) State {
				info, err := GeminiInfo(e)
				if err != nil {
					return NotInstalled
				}
				return stateOf(info.State)
			},
		},
		{
			// Its plugins are modules, not commands: stui writes one
			// (opencode.go).
			ID: "opencode", Name: "opencode", Bin: "opencode",
			Dirs:    []string{".config/opencode", ".local/share/opencode"},
			Install: InstallOpencode, Uninstall: UninstallOpencode,
			Info:   OpencodeInfo,
			Status: func(e Env) State { return stateOf(OpencodeInfo(e).State) },
		},
		{
			// Its extensions are modules, as opencode's plugins are: stui
			// writes one (pi.go). tmux may show its pane as node; its
			// SessionStart names it.
			ID: "pi", Name: "Pi", Bin: "pi", Dirs: []string{".pi/agent"},
			Install: InstallPi, Uninstall: UninstallPi,
			Info:   PiInfo,
			Status: func(e Env) State { return stateOf(PiInfo(e).State) },
		},
		{
			// config.toml's [[hooks]] tables (kimi.go). Its process names
			// itself kimi-code.
			ID: "kimi", Name: "Kimi Code", Bin: "kimi", Dirs: []string{".kimi-code"},
			Install: InstallKimi, Uninstall: UninstallKimi,
			Info:   KimiInfo,
			Status: func(e Env) State { return stateOf(KimiInfo(e).State) },
		},
		{
			// Claude Code's settings.json, in ~/.qwen (qwen.go).
			ID: "qwen", Name: "Qwen Code", Bin: "qwen", Dirs: []string{".qwen"},
			Install: InstallQwen, Uninstall: UninstallQwen,
			Info: func(e Env) SetInfo {
				info, _ := QwenInfo(e)
				return info
			},
			Status: func(e Env) State {
				info, err := QwenInfo(e)
				if err != nil {
					return NotInstalled
				}
				return stateOf(info.State)
			},
		},
		// Found but not hookable yet. Each needs its own way in, and stui will
		// not pretend to have one it has not been written and tested against.
		{
			ID: "copilot", Name: "GitHub Copilot CLI", Bin: "copilot",
			Dirs: []string{".copilot", ".config/github-copilot"},
			Why:  "no place to register a command against its events yet",
		},
		{
			ID: "aider", Name: "Aider", Bin: "aider", Dirs: []string{".aider"},
			Why: "it has no event hooks",
		},
		{
			ID: "goose", Name: "goose", Bin: "goose", Dirs: []string{".config/goose"},
			Why: "no place to register a command against its events yet",
		},
	}
}

// Find returns the agent with this id, or false.
func Find(id string) (Agent, bool) {
	for _, a := range Agents() {
		if a.ID == id {
			return a, true
		}
	}
	return Agent{}, false
}

// StateName is s as the API says it: "installed", "outdated", "partial" or
// "none".
func StateName(s State) string {
	switch s {
	case Installed:
		return "installed"
	case Outdated:
		return "outdated"
	case Partly:
		return "partial"
	}
	return "none"
}

func stateOf(name string) State {
	switch name {
	case "installed":
		return Installed
	case "outdated":
		return Outdated
	case "partial":
		return Partly
	}
	return NotInstalled
}

// States is what stui has done about each agent it can hook, by id, as
// StateName says it. "partial" is a Claude whose settings run stui for some
// events and not others (an install from before stui listened to them): its
// turns and subagents are followed only in part until stui hooks install
// adds the rest.
//
// The words apps before versions know: an outdated set is "partial" here,
// which is what it is to them -- something stui hooks install puts right.
// Sets says which.
func States(e Env) map[string]string {
	out := map[string]string{}
	for _, a := range Agents() {
		if a.Hookable() && a.Status != nil {
			s := a.Status(e)
			if s == Outdated {
				s = Partly
			}
			out[a.ID] = StateName(s)
		}
	}
	return out
}

// Sets is each hookable agent's set against what this stui wants, by id
// (hookSets in GET /api/version and GET /api/agents).
func Sets(e Env) map[string]SetInfo {
	out := map[string]SetInfo{}
	for _, a := range Agents() {
		if a.Hookable() && a.Info != nil {
			out[a.ID] = a.Info(e)
		}
	}
	return out
}

// Incomplete is every agent whose stui hooks are outdated or miss events stui
// listens to: what stui hooks install would change.
func Incomplete(e Env) []Agent {
	var out []Agent
	for _, a := range Agents() {
		if !a.Hookable() || a.Status == nil {
			continue
		}
		if s := a.Status(e); s == Partly || s == Outdated {
			out = append(out, a)
		}
	}
	return out
}

// Hookable is every agent on this machine that stui can hook and has not
// already hooked -- what a first run offers to do.
func Hookable(e Env) []Agent {
	var out []Agent
	for _, a := range Agents() {
		if a.Hookable() && a.Found(e) == Present && a.Status(e) != Installed {
			out = append(out, a)
		}
	}
	return out
}
