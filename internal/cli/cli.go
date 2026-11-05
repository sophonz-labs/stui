// Package cli is stui's command line: the terminal UI, and the commands the
// coding agents run to hand work to each other on this machine.
//
// The UI and the commands talk to the server stui leaves running in the
// background (internal/server); the first command that needs it starts it.
package cli

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sophonz-labs/stui/internal/home"
	"github.com/sophonz-labs/stui/internal/proc"
	"github.com/sophonz-labs/stui/internal/state"
	"github.com/sophonz-labs/stui/internal/ui"
	"github.com/sophonz-labs/tmui/pkg/config"
	"github.com/sophonz-labs/tmui/pkg/plugin"
	"github.com/sophonz-labs/tmui/pkg/upgrade"
)

const help = `stui — tmux, its coding agents and their CI, in one terminal UI

  stui                the UI: sessions, windows, panes, agents, runs, CI
  stui ui [view]      the same, opening on a view (sessions, agents, orch, ci)
  stui --embedded-tmux  use the tmux bundled in Linux releases
  stui tmux <args>    run the selected tmux (e.g. --embedded-tmux tmux -V)
  stui stop           stop the background server (the UI starts it again)
  stui status         whether the server is running, and the agents' hooks
  stui version        print the version
  stui upgrade        replace stui with the latest release, and restart its
                      server (--check only reports, --version vX.Y.Z pins)

Coding agents
  stui hooks install  have the coding agents here report their turns to stui,
                      so it can tell idle, working and asking apart
                      (stui hooks --help)
  stui plugin install teach Claude Code and Codex to hand work to other agents
                      (stui plugin --help)
  stui orch start     this pane's agent coordinates: it hands work to workers in
                      other windows (stui orch --help)
  stui worker …       start, send to, wait for, adopt and list workers
                      (stui worker --help)
  stui --skill        the guide for the agent running it, in one read
  stui skills get orchestrate | worker | all
                      the guides the agents read (stui skills --help)
  stui usage          the agents' plan limits, as they left them on this machine
  stui goal set <text>
                      keep this Claude pane working at a goal (stui goal --help)
  stui ci             this checkout's GitHub Actions runs, through gh
                      (stui ci --help)

Plugins
  stui ext install <name>
                      another CLI in the UI: its output as a view, its commands
                      on keys and : (stui ext --help)

Settings
  stui config edit    the UI's config.yaml: logo, header, colour, refresh rate,
                      start view, plugins (stui config --help)

stui <command> --help says more about any of them.
`

const hooksHelp = `stui hooks -- the coding agents on this machine reporting to stui

  stui hooks install [agent…]
                      find the coding agents on this machine and have them
                      report to stui, so it can follow a turn as it runs
  stui hooks uninstall [agent…]
                      take stui's hooks out again
  stui hooks status   every agent found, and whether its hooks are in
  stui hooks install --usage
                      be Claude Code's status line, so its 5-hour and weekly
                      limits reach stui; your status line still shows
  stui hooks uninstall --usage
                      give the status line back
`

const usageHelp = `stui usage -- the coding agents' plan limits

  stui usage          the agents' plan limits: used, and when they reset
  stui usage guard on|off|status
                      stop a loop's turn (stui goal, Claude's /goal) when the
                      weekly limit runs faster than the week, or is nearly out
  stui usage poll on|off
                      whether stui asks Anthropic and OpenAI itself (on by default)

Every five minutes the running stui asks the same endpoint Claude Code's and
codex's own /usage ask, with the login each CLI already keeps on this machine.
The token goes only to its own provider; stui never writes or refreshes it.
Claude Code's status line is read too: stui hooks install --usage.
`

// Flags that take a value, so the value is not mistaken for the command.
var valued = map[string]bool{"--title": true, "--session": true, "--command": true, "--from": true, "--agent": true, "--cwd": true,
	"--task": true, "--name": true, "--objective": true, "--summary": true, "--for": true, "--timeout": true, "--check-in": true, "--outcome": true, "--resume": true,
	"--job": true, "-R": true, "-o": true, "--map": true}

type args []string

func (a args) flag(name string) (string, bool) {
	for i, x := range a {
		if x == name && i+1 < len(a) {
			return a[i+1], true
		}
	}
	return "", false
}

func (a args) has(name string) bool {
	for _, x := range a {
		if x == name {
			return true
		}
	}
	return false
}

// after is what follows the first occurrence of word.
func (a args) after(word string) []string {
	for i, x := range a {
		if x == word {
			return a[i+1:]
		}
	}
	return nil
}

func (a args) command() string {
	for i := 0; i < len(a); i++ {
		if valued[a[i]] {
			i++
			continue
		}
		if !strings.HasPrefix(a[i], "-") {
			return a[i]
		}
	}
	return ""
}

func logFile() string { return filepath.Join(home.Dir(), "server.log") }

var version string

func Run(v string, argv []string) {
	version = v
	a := args(argv)
	// stui upgrade --version vX.Y.Z is a pin, not a question.
	if (a.has("--version") || a.has("-v")) && a.command() != "upgrade" {
		fmt.Println(version)
		return
	}
	if text, ok := helpFor(a); ok {
		fmt.Print(text)
		return
	}
	switch cmd := a.command(); cmd {
	case "":
		if a.has("--skill") {
			fmt.Print(skillHere())
			return
		}
		os.Exit(ui.Run(ensureServer(), version, nil))
	case "ui":
		os.Exit(ui.Run(ensureServer(), version, a.after("ui")))
	case "version":
		fmt.Println(version)
	case "upgrade":
		upgradeCommand(a.after("upgrade"))
	case "stop", "exit":
		stopServer()
	case "status":
		status()
	case "hooks":
		hooksCommand(a.after("hooks"))
	case "orch":
		orchCommand(a)
	case "worker":
		workerCommand(a)
	case "task":
		taskCommand(a)
	case "gate":
		gateCommand(a)
	case "skills":
		skillsCommand(a)
	case "plugin":
		pluginCommand(a)
	case "usage":
		usageCommand(a.after("usage"))
	case "goal":
		goalCommand(a.after("goal"))
	case "ci":
		ciCommand(a)
	case "ext":
		os.Exit(plugin.Main("stui", ui.Builtins(), a.after("ext"), os.Stdin, os.Stdout))
	case "config":
		os.Exit(config.Main("stui", ui.StartViews(), a.after("config"), os.Stdout, ui.ConfigFlags...))
	default:
		fmt.Print(help)
		if cmd == "help" {
			os.Exit(0)
		}
		os.Exit(1)
	}
}

// ensureServer is the running server, started in the background when there
// is none.
func ensureServer() *state.Agent {
	if s := state.Running(); s != nil {
		if s.EmbeddedTmux != os.Getenv("STUI_BUNDLED_TMUX") {
			fmt.Fprintln(os.Stderr, "stui: the running background server uses a different tmux runtime; run stui stop, then start stui again with the intended --embedded-tmux option (tmux sessions are kept)")
			os.Exit(1)
		}
		return s
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "stui:", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(home.Dir(), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "stui:", err)
		os.Exit(1)
	}
	log, err := os.OpenFile(logFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, "stui:", err)
		os.Exit(1)
	}
	defer log.Close()
	cmd := exec.Command(exe, "__serve")
	cmd.Stdout, cmd.Stderr = log, log
	// Its own session: closing this terminal does not take it along.
	proc.Detach(cmd)
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "stui: start the server:", err)
		os.Exit(1)
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	for end := time.Now().Add(15 * time.Second); time.Now().Before(end); {
		if s := state.Running(); s != nil {
			return s
		}
		select {
		case <-exited:
			fmt.Fprintf(os.Stderr, "stui: the server stopped as it started; %s ends with:\n\n%s\n", logFile(), tail(logFile(), 10))
			os.Exit(1)
		case <-time.After(100 * time.Millisecond):
		}
	}
	fmt.Fprintf(os.Stderr, "stui: the server did not come up; see %s\n", logFile())
	os.Exit(1)
	return nil
}

func tail(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(b), " \t\r\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func stopServer() {
	s := state.Running()
	if s == nil {
		fmt.Printf("%s  not running%s\n", dim, reset)
		return
	}
	apiDo(s, http.MethodPost, "/api/stop", nil, nil)
	for i := 0; i < 40 && proc.Alive(s.PID); i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if proc.Alive(s.PID) {
		proc.Terminate(s.PID)
	}
	state.Clear()
	fmt.Printf("  %sstopped%s\n", dim, reset)
}

// upgradeCommand replaces the binary, then the server it left running, which
// is still the old one. Stopping it leaves tmux and every agent in it alone.
func upgradeCommand(argv []string) {
	tag, code := upgrade.Main("stui", version, argv, os.Stdout)
	if tag == "" {
		os.Exit(code)
	}
	s := state.Running()
	if s == nil || s.Version == tag {
		return
	}
	fmt.Printf("\nrestarting the server (pid %d, %s) on %s\n", s.PID, firstOf(s.Version, "an older stui"), tag)
	stopServer()
	if n := ensureServer(); n.Version != tag {
		fmt.Fprintf(os.Stderr, "stui: the server came back as %s, not %s; see %s\n", firstOf(n.Version, "an unknown version"), tag, logFile())
		os.Exit(1)
	}
	fmt.Printf("  %sserver running %s%s\n", dim, tag, reset)
}

func status() {
	if s := state.Running(); s != nil {
		fmt.Printf("  server   running (pid %d, 127.0.0.1:%d, since %s)\n", s.PID, s.Port, time.UnixMilli(s.StartedAt).Format("Jan 2 15:04"))
	} else {
		fmt.Printf("  server   %snot running: stui starts it%s\n", dim, reset)
	}
	fmt.Printf("  home     %s\n", home.Dir())
	hooksCommand([]string{"status"})
}
