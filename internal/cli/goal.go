package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sophonz-labs/stui/internal/home"
	"github.com/sophonz-labs/stui/internal/hookclient"
)

const goalHelp = `stui goal -- keep a Claude Code pane working at a goal

  stui goal                  the pane's goal, or that it has none
  stui goal set <text…>      give the pane a goal: each time its turn ends, stui's
  stui goal set --file <f>   Stop hook keeps it going with it, until stui goal clear
  stui goal clear            take it off

  --pane %N                another pane of this tmux server (default: this one)

The goal is a file in ~/.stui/goals that the pane's @stui-goal names. Only
Claude Code follows it (stui hooks install). stui usage guard on stops such a
loop when the weekly limit runs too fast.
`

func goalCommand(argv []string) {
	pane := os.Getenv("TMUX_PANE")
	file := ""
	var pos []string
	for i := 0; i < len(argv); i++ {
		switch {
		case argv[i] == "--pane" && i+1 < len(argv):
			pane = argv[i+1]
			i++
		case argv[i] == "--file" && i+1 < len(argv):
			file = argv[i+1]
			i++
		default:
			pos = append(pos, argv[i])
		}
	}
	if os.Getenv("TMUX") == "" || pane == "" {
		fmt.Fprintln(os.Stderr, "stui goal: run this inside a tmux pane (or --pane %N from one on the same server)")
		os.Exit(1)
	}
	if !paneAlive(pane) {
		fmt.Fprintf(os.Stderr, "stui goal: no pane %s on this tmux server\n", pane)
		os.Exit(1)
	}
	cmd := ""
	if len(pos) > 0 {
		cmd, pos = pos[0], pos[1:]
	}
	old := paneOpt(pane, hookclient.OptGoal)
	switch cmd {
	case "":
		if g := hookclient.ReadGoal(old); g != "" {
			fmt.Printf("  %s's goal (%s):\n\n%s\n", pane, old, g)
		} else {
			fmt.Printf("  %s%s has no goal%s\n", dim, pane, reset)
		}
	case "set":
		text := strings.TrimSpace(strings.Join(pos, " "))
		if file != "" {
			b, err := os.ReadFile(file)
			if err != nil {
				fmt.Fprintf(os.Stderr, "stui goal: %v\n", err)
				os.Exit(1)
			}
			text = strings.TrimSpace(string(b))
		}
		if text == "" {
			fmt.Fprintln(os.Stderr, "usage: stui goal set <text…> | --file <f>")
			os.Exit(2)
		}
		dir := hookclient.GoalDir(home.Dir())
		f := filepath.Join(dir, strconv.FormatInt(time.Now().UnixNano(), 10)+".md")
		err := os.MkdirAll(dir, 0o700)
		if err == nil {
			err = os.WriteFile(f, []byte(text+"\n"), 0o600)
		}
		if err == nil {
			err = setPaneOpts(pane, hookclient.OptGoal, f)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "stui goal: %v\n", err)
			os.Exit(1)
		}
		if old != "" && old != f {
			os.Remove(old)
		}
		fmt.Printf("  %s has a goal: its turns go on until stui goal clear%s\n", pane, guardNote())
	case "clear":
		tx("set-option", "-pu", "-t", pane, hookclient.OptGoal)
		if old != "" {
			os.Remove(old)
		}
		fmt.Printf("  %s has no goal\n", pane)
	default:
		fmt.Fprint(os.Stderr, goalHelp)
		os.Exit(2)
	}
}

// guardNote says whether the usage guard watches the loop.
func guardNote() string {
	if usageGuardOn() {
		return " (the usage guard is on)"
	}
	return dim + " (the usage guard is off: stui usage guard on)" + reset
}
