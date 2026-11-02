package cli

// --help, decided in one place.
//
// Each command used to look for --help itself, and most did not: stui upgrade
// --help replaced the binary, stui rollback --help rolled back, stui logout
// --help signed out, stui worker stop %3 --help --yes closed a pane. Agents
// learn a command by asking it for help, so they were the first to be
// caught. Run asks helpFor before it picks a command: with --help or -h
// anywhere among the arguments (not as the value of a flag like --summary),
// it prints that command's help and runs nothing.

import "strings"

// ownHelp is the commands with a help text of their own.
var ownHelp = map[string]string{
	"gate": gateHelp, "task": taskHelp, "ci": ciHelp,
	"orch": orchHelp, "worker": orchHelp, "skills": skillsHelp, "plugin": pluginHelp, "ext": extHelp,
	"hooks": hooksHelp, "usage": usageHelp, "goal": goalHelp,
}

const pluginHelp = `stui plugin -- the orchestration skills, in the coding agents

  stui plugin install     put them in: Claude Code gets the "stui" plugin (new sessions
                        have /stui:orchestrate), Codex gets the skills in ~/.codex/skills
  stui plugin status      whether the plugin's files, the Codex skills and Claude Code's
                        plugin are in
  stui plugin uninstall   take them out of both

With them an agent knows how to hand work to other agents through stui: the
commands are stui orch and stui worker (stui orch --help), and what the agents read
is stui skills get orchestrate | worker.
`

const extHelp = `stui ext -- plugins: other CLIs in the UI

  stui ext list                    the plugins, and what was left out of them
  stui ext install [-y] <name | path | url>
                                 a name comes from github.com/sophonz-labs/tui-plugins;
                                 it prints every command the plugin will run and asks first
  stui ext remove <name>

A plugin is a plugin.json in ~/.config/stui/plugins/<name>/ (or under
$XDG_CONFIG_HOME); ~/.config/tui-plugins/ is shared with tmui. Inside the UI,
:ext lists them. The format: github.com/sophonz-labs/tui-plugins/blob/main/SPEC.md.
(stui plugin is something else: the orchestration skills for coding agents.)
`

const skillsHelp = `stui skills -- the guides the coding agents read

  stui skills (or stui --skill)   the guide for the agent running it, in one read: the
                              worker's when stui started it (STUI_RUN is set), the
                              coordinator's otherwise
  stui skills get orchestrate   the coordinator's: starting workers, handing them work,
                              waiting for them, cleaning up, workers on other machines
  stui skills get worker        the worker's: reading the task, reporting back
  stui skills get all           both, the coordinator's first

They are this stui's own, so they describe the stui that is installed. stui plugin
install puts them in Claude Code and Codex (stui plugin --help).
`

// helpAlias is another name of a command.
var helpAlias = map[string]string{"exit": "stop"}

// wantsHelp is whether --help or -h is among a, not as a flag's value.
func wantsHelp(a args) bool {
	for i := 0; i < len(a); i++ {
		if valued[a[i]] {
			i++
			continue
		}
		if a[i] == "--help" || a[i] == "-h" {
			return true
		}
	}
	return false
}

// helpFor is the help to print instead of running a, and whether a asks
// for help at all: --help or -h, or stui help [command].
func helpFor(a args) (string, bool) {
	cmd := a.command()
	if strings.HasPrefix(cmd, "__") {
		// stui's own: run by stui, with arguments stui chose.
		return "", false
	}
	if cmd == "help" {
		if rest := a.after("help"); len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			return commandHelp(rest[0]), true
		}
		return help, true
	}
	if !wantsHelp(a) {
		return "", false
	}
	if cmd == "" {
		return help, true
	}
	if cmd == "worker" {
		if rest := a.after("worker"); len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			if t := workerVerbHelp(rest[0]); t != "" {
				return t, true
			}
		}
	}
	return commandHelp(cmd), true
}

// workerVerbHelp is what stui worker <verb> --help prints: that verb's lines
// of the orchestration help, and what an id may be. "" for a verb it does
// not list.
func workerVerbHelp(verb string) string {
	var out []string
	in := false
	for _, l := range strings.Split(orchHelp, "\n") {
		if strings.HasPrefix(l, "  stui ") {
			f := strings.Fields(l)
			in = len(f) > 2 && f[1] == "worker" && f[2] == verb
			if in {
				out = append(out, l)
			}
			continue
		}
		if in && strings.HasPrefix(l, "      ") {
			out = append(out, l)
			continue
		}
		in = false
	}
	if len(out) == 0 {
		return ""
	}
	ids := orchHelp[strings.Index(orchHelp, "Ids: "):]
	ids = ids[:strings.Index(ids, "\n\n")]
	return strings.Join(out, "\n") + "\n\n" + ids + "\n\n  stui worker --help: every orchestration command\n"
}

// commandHelp is cmd's own help, else its lines of stui's help, else all of it.
func commandHelp(cmd string) string {
	if c, ok := helpAlias[cmd]; ok {
		cmd = c
	}
	if t, ok := ownHelp[cmd]; ok {
		return t
	}
	if t := helpLines(cmd); t != "" {
		return t + "\n  stui --help: every command\n"
	}
	return help
}

// helpLines is what stui's help says about stui <cmd>: its lines, and the lines
// under them that go on with it.
func helpLines(cmd string) string {
	var out []string
	in := false
	for _, l := range strings.Split(help, "\n") {
		if strings.HasPrefix(l, "  stui ") {
			f := strings.Fields(l)
			in = len(f) > 1 && f[1] == cmd
			if in {
				out = append(out, l)
			}
			continue
		}
		if in && strings.HasPrefix(l, "      ") {
			out = append(out, l)
			continue
		}
		in = false
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n") + "\n"
}
