package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/sophonz-labs/stui/internal/hookclient"
	"github.com/sophonz-labs/stui/internal/hooks"
)

// stui hooks install [agent…] | uninstall [agent…] | status | auto [on|off]
// stui hooks install --usage | uninstall --usage: stui as Claude Code's status
// line, for its plan limits (internal/usage); asked for on its own.
//
// Driven by the registry in internal/hooks/agents.go rather than a pair of
// names written out here, so an agent stui learns to hook is one row there.
func hooksCommand(argv []string) {
	sub := ""
	if len(argv) > 0 {
		sub = argv[0]
	}
	e := hooks.DefaultEnv()
	usageOnly := false
	if len(argv) > 0 {
		kept := argv[:1:1]
		for _, a := range argv[1:] {
			if a == "--usage" {
				usageOnly = true
				continue
			}
			kept = append(kept, a)
		}
		argv = kept
	}

	// Named agents, or every one stui can hook that is on this machine.
	pick := func() ([]hooks.Agent, bool) {
		names := argv[1:]
		if len(names) == 0 {
			var out []hooks.Agent
			for _, a := range hooks.Agents() {
				if a.Hookable() && a.Found(e) == hooks.Present {
					out = append(out, a)
				}
			}
			if len(out) == 0 {
				fmt.Printf("  %sno agent stui can hook was found on this machine%s\n", dim, reset)
			}
			return out, true
		}
		var out []hooks.Agent
		for _, n := range names {
			a, ok := hooks.Find(strings.ToLower(n))
			switch {
			case !ok:
				fmt.Fprintf(os.Stderr, "  no agent called %q — %s\n", n, known())
				return nil, false
			case !a.Hookable():
				fmt.Fprintf(os.Stderr, "  stui cannot hook %s: %s\n", a.Name, a.Why)
				return nil, false
			}
			out = append(out, a)
		}
		return out, true
	}

	report := func(what string, res hooks.Result, err error) bool {
		switch {
		case err != nil:
			fmt.Printf("  %s: %v\n", what, err)
			return false
		case res.Note != "":
			fmt.Printf("  %s: %s%s%s\n", what, dim, res.Note, reset)
		case res.Changed:
			fmt.Printf("  %s: updated %s\n", what, res.File)
			if res.Backup != "" {
				fmt.Printf("  %s  backup %s%s\n", dim, res.Backup, reset)
			}
		default:
			fmt.Printf("  %s: %salready as it should be%s\n", what, dim, reset)
		}
		return true
	}

	if usageOnly {
		switch sub {
		case "install":
			res, err := hooks.InstallStatusline(e)
			if !report("Claude Code status line", res, err) {
				os.Exit(1)
			}
			fmt.Printf("\n  %sClaude's 5-hour and weekly limits reach stui after its next message (stui usage shows them). What was your status line still shows: stui runs it. Undo with: stui hooks uninstall --usage%s\n", dim, reset)
		case "uninstall", "remove":
			res, err := hooks.UninstallStatusline(e)
			if !report("Claude Code status line", res, err) {
				os.Exit(1)
			}
		default:
			fmt.Println("  stui hooks install --usage | stui hooks uninstall --usage")
			os.Exit(1)
		}
		return
	}

	switch sub {
	case "install":
		chosen, fine := pick()
		if !fine {
			os.Exit(1)
		}
		ok := true
		for _, a := range chosen {
			res, err := a.Install(e)
			ok = report(a.Name, res, err) && ok
		}
		if len(chosen) > 0 {
			// Asked for, so stui may keep them current from here on.
			hooks.RecordConsent(e)
			fmt.Printf("\n  %sSessions started from now on report to stui; restart running ones to pick up new hooks. Remove with: stui hooks uninstall%s\n", dim, reset)
		}
		if !ok {
			os.Exit(1)
		}
	case "uninstall", "remove":
		chosen, fine := pick()
		if !fine {
			os.Exit(1)
		}
		ok := true
		for _, a := range chosen {
			res, err := a.Uninstall(e)
			ok = report(a.Name, res, err) && ok
			// Everything stui put in Claude's settings, the status line too.
			if a.ID == "claude" && hooks.StatuslineOn(e) {
				res, err := hooks.UninstallStatusline(e)
				ok = report("Claude Code status line", res, err) && ok
			}
		}
		if !ok {
			os.Exit(1)
		}
	case "status", "":
		for _, a := range hooks.Agents() {
			found := a.Found(e) == hooks.Present
			switch {
			case !found && !a.Hookable():
				continue // not here and nothing stui could do anyway
			case !found:
				fmt.Printf("  %-20s %snot on this machine%s\n", a.Name, dim, reset)
			case !a.Hookable():
				fmt.Printf("  %-20s %sfound; stui has no hooks for it yet (%s)%s\n", a.Name, dim, a.Why, reset)
			default:
				info := a.Info(e)
				fmt.Printf("  %-20s %s\n", a.Name, describeSet(info, a.ID))
				if info.State != "none" {
					hookHealth(e, a)
				}
			}
		}
		if hooks.StatuslineOn(e) {
			fmt.Printf("  %-20s on %s(stui hooks uninstall --usage)%s\n", "plan limits", dim, reset)
		} else {
			fmt.Printf("  %-20s %soff — stui hooks install --usage (Claude Code; Codex needs nothing)%s\n", "plan limits", dim, reset)
		}
		fmt.Printf("\n  %s%s%s\n", dim, describeAuto(e), reset)
	case "auto":
		switch {
		case len(argv) > 1 && (argv[1] == "on" || argv[1] == "off"):
			if err := hooks.SetAuto(e, argv[1] == "on"); err != nil {
				fmt.Fprintf(os.Stderr, "  %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("  %s\n", describeAuto(e))
		case len(argv) == 1:
			fmt.Printf("  %s\n", describeAuto(e))
		default:
			fmt.Println("  stui hooks auto [on|off]")
			os.Exit(1)
		}
	default:
		fmt.Printf("  stui hooks install | uninstall | status | auto [on|off]  [%s]   install --usage | uninstall --usage\n", known())
		os.Exit(1)
	}
}

// hookHealth says, under an agent whose hooks are installed, whether they
// run here and what the last one did: a hook stui hook could not help --
// one the agent cannot start, one outside tmux -- otherwise leaves no trace.
func hookHealth(e hooks.Env, a hooks.Agent) {
	pad := strings.Repeat(" ", 23)
	if a.ID == "codex" {
		if p, ok := hooks.ProbeCodex(e); ok && !p.OK {
			fmt.Printf("%s%sdoes not run here%s: %s %q exits %d", pad, red, reset, strings.Join(p.Shell, " "), p.Command, p.Exit)
			if p.Output != "" {
				fmt.Printf(" (%s)", p.Output)
			}
			fmt.Printf(" — stui hooks install codex writes it again\n")
		}
	}
	r, ok := hookclient.ReadLast(e.StateDir, a.ID)
	if !ok {
		fmt.Printf("%s%shas not run since this stui began keeping track%s\n", pad, dim, reset)
		return
	}
	what := map[string]string{
		hookclient.Delivered:   "stui took it",
		hookclient.Spooled:     "stui was not running; kept for when it is",
		hookclient.Dropped:     "stui did not take a tool hook in time; counted in the agent's log",
		hookclient.OutsideTmux: "outside tmux, so nothing was sent (no pane to say it happened in)",
		hookclient.Unreadable:  "its input was not something stui reads",
	}[r.Outcome]
	if what == "" {
		what = r.Outcome
	}
	ev := ""
	if r.Event != "" {
		ev = " (" + r.Event + ")"
	}
	color := dim
	if r.Outcome == hookclient.OutsideTmux || r.Outcome == hookclient.Unreadable {
		color = yellow
	}
	fmt.Printf("%s%slast ran %s%s: %s%s\n", pad, color, ago(r.At), ev, what, reset)
}

// known is the agents stui can hook, for an error that says what would work.
func known() string {
	var ids []string
	for _, a := range hooks.Agents() {
		if a.Hookable() {
			ids = append(ids, a.ID)
		}
	}
	return strings.Join(ids, ", ")
}

// describeSet is one agent's line in stui hooks status.
func describeSet(info hooks.SetInfo, id string) string {
	switch info.State {
	case "installed":
		return fmt.Sprintf("installed %s(v%d)%s", dim, info.Version, reset)
	case "outdated":
		v := "an older stui"
		if info.Version > 0 {
			v = fmt.Sprintf("v%d", info.Version)
		}
		return fmt.Sprintf("%soutdated%s (%s; this stui wants v%d) — stui hooks install updates them", yellow, reset, v, info.Required)
	case "partial":
		// opencode's plugin is a file: one changed by hand may still send
		// every event.
		if len(info.Missing) == 0 {
			return fmt.Sprintf("%schanged by hand%s (not as this stui writes it) — stui hooks install writes it again", yellow, reset)
		}
		return fmt.Sprintf("%spartly installed%s (no %s) — stui hooks install", yellow, reset, strings.Join(info.Missing, ", "))
	}
	return fmt.Sprintf("%snot installed%s — stui hooks install %s", dim, reset, id)
}

// describeAuto says whether outdated hooks are updated on their own here.
func describeAuto(e hooks.Env) string {
	switch {
	case !hooks.AutoWanted(e):
		return "automatic updates: off — stui hooks auto on"
	case !hooks.Consented(e):
		return "automatic updates: on once stui's hooks are installed here"
	}
	return "automatic updates: on — stui updates outdated hooks when it starts, with a backup (stui hooks auto off)"
}
