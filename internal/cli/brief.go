package cli

// An adopted worker's role and brief, from the command line: stui worker adopt --role, stui worker
// about, stui worker brief, and how list, read and adopt show them.

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sophonz-labs/stui/internal/orch"
	"golang.org/x/term"
)

// briefRow puts a worker's role and brief in its stui worker list row. A
// worker stui started has its task; one adopted has its role, "" when none
// was given.
func briefRow(row map[string]any, p *orchPane) {
	row["adopted"] = p.Adopted
	row["about"] = p.About
	if p.BriefState != "" {
		row["briefState"] = p.BriefState
		row["brief"] = p.Brief
		if p.BriefAt > 0 {
			row["briefAt"] = p.BriefAt
		}
	}
}

// briefLines are the lines under a worker's line in stui worker list: its
// role ("(no role)" for one adopted without), and its brief's first line or
// how asking for it stands.
func briefLines(r map[string]any) []string {
	var out []string
	about, _ := r["about"].(string)
	adopted, _ := r["adopted"].(bool)
	switch {
	case about != "":
		out = append(out, about)
	case adopted:
		out = append(out, dim+"(no role) — stui worker about "+fmt.Sprint(r["id"])+` "<why it was brought in>"`+reset)
	}
	state, _ := r["briefState"].(string)
	brief, _ := r["brief"].(string)
	at, _ := r["briefAt"].(int64)
	switch state {
	case orch.BriefAnswered:
		first, _, _ := strings.Cut(brief, "\n")
		out = append(out, dim+"brief: "+first+reset)
	case orch.BriefAsked:
		when := ""
		if at > 0 {
			when = " " + ago(at*1000)
		}
		out = append(out, dim+"brief: asked"+when+"; it answers when its turn ends"+reset)
	}
	return out
}

// unavailableLine says why a conversation shows nothing (more), or "".
func unavailableLine(reason string, since int64, cause string) string {
	switch reason {
	case "not-recording":
		when := ""
		if since > 0 {
			when = " since " + time.UnixMilli(since).Format("15:04")
		}
		why := ""
		if cause != "" {
			why = " (it runs with " + cause + ")"
		}
		return "this agent has not written its conversation down" + when + why + ": nothing after that can be shown"
	case "no-transcript":
		return "this agent's conversation file was not found"
	}
	return ""
}

// askRole asks for a role on a terminal; "" when there is none to ask on or
// nothing was typed.
func askRole(id string) string {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return ""
	}
	fmt.Printf("  %s's role — why is it brought in? (Enter to skip) ", id)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(line)
}

// printAdopted is what stui worker adopt prints: who it is now a worker of,
// and what stui knows of it at once.
func printAdopted(id string, res map[string]any) {
	fmt.Printf("  %s (%v) is now a worker of %v in run %v\n", id, res["name"], res["parent"], res["run"])
	if was, _ := res["was"].(string); was != "" {
		fmt.Printf("  named %v from its window; it was %q before, from an earlier adoption (stui worker rename %s %s keeps that)\n", res["name"], was, id, was)
	}
	str := func(k string) string { s, _ := res[k].(string); return s }
	var members []string
	if ms, ok := res["members"].([]any); ok {
		for _, m := range ms {
			members = append(members, fmt.Sprint(m))
		}
	}
	if len(members) > 0 || str("team") != "" {
		n := len(members)
		who := ""
		if n > 0 {
			who = " (" + strings.Join(members, " ") + ")"
		}
		fmt.Printf("    leads a team: %d workers of its own%s. Give work to it, not to them.\n", n, who)
	}
	if s := str("state"); s != "" {
		line := "    " + s
		if since, ok := res["since"].(float64); ok && since > 0 {
			line += " for " + strings.TrimSuffix(ago(int64(since)), " ago")
		}
		if q := str("lastAsked"); q != "" {
			line += fmt.Sprintf("; last asked: %q", q)
		}
		fmt.Println(line)
	}
	if cwd := str("cwd"); cwd != "" {
		where := "    in " + cwd
		if b := str("branch"); b != "" {
			where += " (" + b + ")"
		}
		fmt.Println(where)
	}
	if a := str("about"); a != "" {
		fmt.Printf("    role: %s\n", a)
	} else {
		fmt.Printf("    role: (none). Set it: stui worker about %s \"<why it was brought in>\"\n", id)
	}
	switch str("briefState") {
	case orch.BriefAsked:
		fmt.Printf("    brief: asked. It answers when its turn ends: stui worker wait %s --brief\n", id)
	case orch.BriefSkipped:
		fmt.Println("    brief: not asked (--no-brief)")
	}
	if told, ok := res["told"].(map[string]any); ok {
		for pane, why := range told {
			switch w := fmt.Sprint(why); w {
			case "ok":
			case orch.TurnEnd:
				fmt.Printf("    %s is working: it is told once its turn ends\n", pane)
			default:
				fmt.Printf("    %s was not told (%s)\n", pane, w)
			}
		}
	}
}

// workerAbout is stui worker about <id> "<role>" | --clear.
func workerAbout(a args, rest []string, asJSON bool) {
	clear := a.has("--clear")
	var words []string
	for _, w := range rest {
		if w != "--clear" && w != "--json" {
			words = append(words, w)
		}
	}
	if len(words) == 0 || !isWorkerID(words[0]) || (len(words) < 2 && !clear) {
		die(asJSON, map[string]any{}, `stui worker about <id> "<role>" | --clear`)
	}
	id := words[0]
	about := ""
	if !clear {
		about = orch.CleanAbout(strings.Join(words[1:], " "))
	}
	here(asJSON)
	if paneOpt(id, "@stui-role") != "worker" {
		die(asJSON, map[string]any{"id": id}, "%s is no one's worker", id)
	}
	var err error
	if about == "" {
		_, err = tx("set-option", "-p", "-u", "-t", id, orch.OptAbout)
	} else {
		err = setPaneOpts(id, orch.OptAbout, about)
	}
	if err != nil {
		die(asJSON, map[string]any{"id": id}, "%v", err)
	}
	// The run's record keeps a copy, for when the pane is found again; and
	// its history says it changed, and who changed it (orch/tree.go).
	me := os.Getenv("TMUX_PANE")
	for _, r := range orch.Runs() {
		if w := r.Worker(id); w != nil {
			if w.About != about {
				orch.AppendTreeEvent(orch.Event{Run: r.ID, Kind: orch.EvAbout, From: me, FromName: paneName(me), To: id, ToName: w.Name,
					ToKey: w.Key, Machine: w.Machine, Was: w.About, Now: about})
			}
			w.About = about
			r.Save()
		}
	}
	if asJSON {
		printJSON(map[string]any{"ok": true, "id": id, "about": about})
	} else if about == "" {
		fmt.Printf("  %s has no role now\n", id)
	} else {
		fmt.Printf("  %s: %s\n", id, about)
	}
}

// workerBrief is stui worker brief --summary "<lines>": an adopted worker
// says what it has done and is doing, when stui asks it to.
func workerBrief(a args, asJSON bool) {
	pane, _ := here(asJSON)
	text, ok := a.flag("--summary")
	if !ok || strings.TrimSpace(text) == "" {
		die(asJSON, map[string]any{}, `stui worker brief --summary "<what you have done so far, and what you are doing now>"`)
	}
	if paneOpt(pane, "@stui-role") != "worker" {
		die(asJSON, map[string]any{}, "this pane is no one's worker: there is no one to brief")
	}
	kept := orch.CleanBrief(text)
	now := time.Now().Unix()
	if err := setPaneOpts(pane, orch.OptBrief, kept, orch.OptBriefState, orch.BriefAnswered, orch.OptBriefAt, fmt.Sprint(now)); err != nil {
		die(asJSON, map[string]any{}, "%v", err)
	}
	if id := workRun(pane); id != "" {
		if r, err := orch.Load(id); err == nil {
			if w := r.Worker(pane); w != nil {
				w.Brief, w.BriefState, w.BriefAt = kept, orch.BriefAnswered, now
				r.Save()
			}
		}
	}
	if asJSON {
		printJSON(map[string]any{"ok": true, "id": pane})
	} else {
		fmt.Println("  brief left; go on with what you were doing")
	}
}
