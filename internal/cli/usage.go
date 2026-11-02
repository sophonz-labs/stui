package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sophonz-labs/stui/internal/home"
	"github.com/sophonz-labs/stui/internal/hooks"
	"github.com/sophonz-labs/stui/internal/usage"
)

// stui usage: what GET /api/usage says, read here. stui usage poll on|off:
// whether stui asks the providers itself (usage/poll.go).
func usageCommand(argv []string) {
	e := usage.DefaultEnv(home.Dir())
	e.ClaudeFeed = func() bool { return hooks.StatuslineOn(hooks.DefaultEnv()) }
	if len(argv) > 0 && argv[0] == "guard" {
		usageGuard(e, argv[1:])
		return
	}
	if len(argv) > 0 && argv[0] == "poll" {
		var err error
		switch {
		case len(argv) > 1 && argv[1] == "on":
			err = usage.SetPoll(e, true)
		case len(argv) > 1 && argv[1] == "off":
			err = usage.SetPoll(e, false)
		case len(argv) > 1:
			fmt.Fprintln(os.Stderr, "usage: stui usage poll [on|off]")
			os.Exit(2)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "stui usage poll: %v\n", err)
			os.Exit(1)
		}
		if usage.PollOff(e) {
			fmt.Printf("  %-12s off — stui reads only what the agents hand over (stui usage poll on)\n", "poll")
		} else {
			fmt.Printf("  %-12s on — stui asks Anthropic and OpenAI every %s with the logins Claude Code and codex keep here; the token goes only to its own provider (stui usage poll off)\n", "poll", usage.PollEvery)
		}
		if len(argv) > 1 {
			fmt.Printf("  %sa running stui picks it up within %s%s\n", dim, usage.PollEvery, reset)
		}
		return
	}
	rep := usage.Read(e)
	if len(rep.Agents) == 0 {
		fmt.Printf("  %sneither Claude Code nor Codex is on this machine%s\n", dim, reset)
		return
	}
	for _, a := range rep.Agents {
		name := usage.AgentName(a.Agent)
		if a.Profile != "" {
			name += " (" + a.Profile + ")"
		}
		if a.Account != nil {
			who := strings.TrimSpace(a.Account.Label + " " + a.Account.Plan)
			if who != "" {
				fmt.Printf("  %s%s · %s%s\n", dim, name, who, reset)
			}
		}
		switch a.State {
		case "setup":
			fmt.Printf("  %-12s %snot given to stui — stui hooks install --usage%s\n", name, dim, reset)
			continue
		case "waiting":
			fmt.Printf("  %-12s %snothing yet: shows after its next message (a plan login only, not an API key)%s\n", name, dim, reset)
			continue
		}
		for _, w := range a.Windows {
			bar := meter(w.UsedPercent)
			when := ""
			switch {
			case w.Reset:
				when = "reset since"
			case w.ResetsAt > 0:
				when = "resets " + time.UnixMilli(w.ResetsAt).Local().Format("Mon 15:04")
			}
			win := usage.WindowName(w.Minutes)
			if w.Label != "" {
				win += " " + w.Label
			}
			if w.Kind == "spend" {
				win = "spend"
			}
			fmt.Printf("  %-12s %-14s %s %5.1f%%  %s%s%s\n", name, win, bar, w.UsedPercent, dim, when, reset)
			name = ""
		}
		if a.ObservedAt > 0 {
			from := ""
			switch a.Source {
			case usage.SourcePoll:
				from = ", asked " + map[string]string{"claude": "Anthropic", "codex": "OpenAI"}[a.Agent]
			case usage.SourceStatusline:
				from = ", from the status line"
			case usage.SourceSessionFile:
				from = ", from its session file"
			}
			fmt.Printf("  %-12s %sas of %s%s%s\n", "", dim, time.UnixMilli(a.ObservedAt).Local().Format("Jan 2 15:04"), from, reset)
		}
	}
}

// stui usage guard on|off|status (usage/guard.go): usage-coach's coach guard.
func usageGuard(e usage.Env, argv []string) {
	act := "status"
	if len(argv) > 0 {
		act = argv[0]
	}
	switch act {
	case "on", "off":
		if err := usage.SetGuard(e, act == "on"); err != nil {
			fmt.Fprintf(os.Stderr, "stui usage guard: %v\n", err)
			os.Exit(1)
		}
	case "status":
	default:
		fmt.Fprintln(os.Stderr, "usage: stui usage guard [on|off|status]")
		os.Exit(2)
	}
	c := usage.ReadGuard(e)
	if c.On {
		fmt.Printf("  %-12s on — a loop's turn (stui goal, Claude's /goal) is stopped when the weekly limit runs too fast (stui usage guard off)\n", "guard")
	} else {
		fmt.Printf("  %-12s off — loops are not stopped for usage (stui usage guard on)\n", "guard")
	}
	for _, g := range usage.GuardStatus(c, usage.Read(e), time.Now()).Agents {
		name := usage.AgentName(g.Agent)
		if g.Profile != "" {
			name += " (" + g.Profile + ")"
		}
		what := ""
		switch g.State {
		case "nodata":
			what = yellow + "no data: " + g.Reason + " — a loop is let go on" + reset
		case "stop":
			what = red + "would stop: " + g.Reason + reset
		default:
			what = fmt.Sprintf("goes on: %.0f%% of the week left, %.0f%% of its time", *g.Left*100, *g.TimeLeft*100)
		}
		fmt.Printf("  %-12s %s\n", name, what)
		fmt.Printf("  %-12s %sstops when left < time left − %.0f%% or left < %.0f%%%s\n", "", dim, g.M*100, g.Floor*100, reset)
		if !g.Guarded {
			fmt.Printf("  %-12s %s%s does not stop for a Stop hook: not guarded%s\n", "", dim, usage.AgentName(g.Agent), reset)
		}
	}
	fmt.Printf("  %ssettings: %s (\"agents\": {\"claude\": {\"m\": 0.1, \"floor\": 0.1}})%s\n", dim, usage.GuardPath(e), reset)
}

func usageGuardOn() bool { return usage.ReadGuard(usage.DefaultEnv(home.Dir())).On }

func meter(p float64) string {
	n := int(p/10 + 0.5)
	n = max(0, min(10, n))
	c := ""
	switch {
	case p >= 95:
		c = red
	case p >= 80:
		c = yellow
	}
	return c + strings.Repeat("█", n) + reset + dim + strings.Repeat("░", 10-n) + reset
}
