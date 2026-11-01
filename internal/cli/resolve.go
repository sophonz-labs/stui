package cli

// A worker by the name stui worker list shows, as well as by its pane id.
// Every stui worker verb that takes an
// id turns it into one here first and then goes on as before.

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/sophonz-labs/stui/internal/orch"
	"github.com/sophonz-labs/stui/internal/state"
)

// idForms is what an id may be, for usage lines and help.
const idForms = "id: %N, a name from stui worker list, machine/%N, machine/name"

// candidate is a worker a name may mean.
type candidate struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Machine string `json:"machine,omitempty"`
	Parent  string `json:"parent,omitempty"`
	Status  string `json:"status,omitempty"`
}

// knownWorkers is every worker this machine knows of: its worker panes, and
// the workers on other machines its runs' records hold (with the pane here
// that is each one's parent).
func knownWorkers(ps []orchPane, runs []*orch.Run) []candidate {
	var out []candidate
	for _, p := range ps {
		if p.Role == "worker" {
			out = append(out, candidate{ID: p.ID, Name: p.Name, Parent: p.Parent})
		}
	}
	seen := map[string]bool{}
	for _, r := range runs {
		for _, w := range r.Workers {
			if w.Machine == "" || seen[w.ID] {
				continue
			}
			seen[w.ID] = true
			parent := w.Parent
			if parent == "" {
				parent = r.Coordinator
			}
			out = append(out, candidate{ID: w.ID, Name: w.Name, Machine: w.Machine, Parent: parent})
		}
	}
	return out
}

// under is whether worker c's chain of parents (here) reaches self.
func under(c candidate, self string, parentOf map[string]string) bool {
	p := c.Parent
	for i := 0; i < 16 && p != ""; i++ {
		if p == self {
			return true
		}
		p = parentOf[p]
	}
	return false
}

// matchName is the workers name means: those under self first (at any
// depth, on any machine), and only when none is, every worker known here.
// wide is true when they came from the second.
func matchName(name, self string, all []candidate) (got []candidate, wide bool) {
	parentOf := map[string]string{}
	for _, c := range all {
		if c.Machine == "" {
			parentOf[c.ID] = c.Parent
		}
	}
	var mine, any []candidate
	for _, c := range all {
		if !strings.EqualFold(c.Name, name) {
			continue
		}
		any = append(any, c)
		if under(c, self, parentOf) {
			mine = append(mine, c)
		}
	}
	if len(mine) > 0 {
		return mine, false
	}
	return any, len(any) > 0
}

// resolveWorker is the worker id arg means: "%7" and "<machine>/%7" as
// they are (the machine by slug, name or host name), a name, or
// "<machine>/<name>". It dies when arg means no worker, or more than one.
func resolveWorker(arg string, asJSON bool) string {
	id, err := resolveWorkerID(arg)
	if err == nil {
		return id
	}
	receipt := map[string]any{"arg": arg}
	var amb *ambiguous
	switch e := err.(type) {
	case *ambiguous:
		amb = e
	case *noWorker:
		receipt["reason"] = "no-such-worker"
		if len(e.names) > 0 {
			receipt["names"] = e.names
			die(asJSON, receipt, "no worker is called %q (%s)", arg, strings.Join(e.names, ", "))
		}
		die(asJSON, receipt, "no worker is called %q", arg)
	default:
		die(asJSON, receipt, "%v", err)
	}
	statuses(amb.cands)
	receipt["reason"], receipt["candidates"] = "ambiguous-name", amb.cands
	if asJSON {
		die(asJSON, receipt, "%q is %d workers; say which", arg, len(amb.cands))
	}
	fmt.Fprintf(os.Stderr, "  %q is %d workers; say which:\n", arg, len(amb.cands))
	w := 4
	for _, c := range amb.cands {
		w = max(w, len(c.ID))
	}
	for _, c := range amb.cands {
		fmt.Fprintf(os.Stderr, "    %-*s  %-16s %-11s parent %s\n", w, c.ID, c.Name, c.Status, c.Parent)
	}
	os.Exit(1)
	return ""
}

type ambiguous struct{ cands []candidate }

func (e *ambiguous) Error() string { return fmt.Sprintf("%d workers", len(e.cands)) }

type noWorker struct{ names []string }

func (e *noWorker) Error() string { return "no such worker" }

// resolveWorkerID is resolveWorker without dying.
func resolveWorkerID(arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if strings.HasPrefix(arg, "%") {
		return arg, nil
	}
	if m, p, ok := splitRemote(arg); ok {
		mm, err := resolveMachine(m)
		if err != nil {
			// Not signed in, or a slug the account list does not have
			// yet: as it was given, for the call to say what is wrong.
			return arg, nil
		}
		return mm.Slug + "/" + p, nil
	}
	machine, name, ok := splitName(arg)
	if !ok {
		return "", fmt.Errorf("%q is not a worker id (%s)", arg, idForms)
	}
	onMachine := machine != ""
	all := knownWorkers(orchPanes(), orch.Runs())
	if onMachine {
		mm, err := resolveMachine(machine)
		if err != nil {
			return "", fmt.Errorf("%s: %v", machine, err)
		}
		var on []candidate
		for _, c := range all {
			if c.Machine == mm.Slug {
				on = append(on, c)
			}
		}
		all = on
		if len(all) == 0 {
			// This machine knows only the workers its runs started or
			// adopted there.
			return "", fmt.Errorf("no worker on %s is in this machine's runs: give its id, %s/%%N", machine, machine)
		}
	}
	got, _ := matchName(name, selfPane(), all)
	switch len(got) {
	case 1:
		return got[0].ID, nil
	case 0:
		return "", &noWorker{names: names(all)}
	}
	return "", &ambiguous{cands: got}
}

// splitName is a name, or "<machine>/<name>", taken apart; ok false when
// it is neither. A name is what orch.Name makes: letters, digits, dashes.
func splitName(arg string) (machine, name string, ok bool) {
	machine, name, found := strings.Cut(arg, "/")
	if !found {
		machine, name = "", arg
	}
	if name == "" || orch.Name(name) != strings.ToLower(name) || (found && machine == "") {
		return "", "", false
	}
	return machine, name, true
}

// selfPane is the pane this runs in, "" outside tmux.
func selfPane() string {
	if os.Getenv("TMUX") == "" {
		return ""
	}
	return os.Getenv("TMUX_PANE")
}

// names is the distinct names of cs, sorted.
func names(cs []candidate) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range cs {
		if c.Name != "" && !seen[c.Name] {
			seen[c.Name] = true
			out = append(out, c.Name)
		}
	}
	sort.Strings(out)
	return out
}

// statuses fills in each candidate's status, for choosing between them.
func statuses(cs []candidate) {
	var states map[string]agentPane
	ps := map[string]*orchPane{}
	for _, p := range orchPanes() {
		p := p
		ps[p.ID] = &p
	}
	for i := range cs {
		c := &cs[i]
		if c.Machine == "" {
			if states == nil {
				states = map[string]agentPane{}
				if s := state.Running(); s != nil {
					states = agentPanes(s)
				}
			}
			c.Status = workerStatus(ps[c.ID], states[c.ID])
			continue
		}
		slug, _, _ := splitRemote(c.ID)
		if _, err := tryRemoteClient(slug); err != nil {
			c.Status = "unreachable"
			continue
		}
		p, a, err := remoteWorker(c.ID, true)
		if err != nil {
			c.Status = "unreachable"
		} else {
			c.Status = workerStatus(p, a)
		}
	}
}

// resolveArgs is the worker ids among a verb's words (its flags and their
// values left out), each resolved.
func resolveArgs(words []string, asJSON bool) []string {
	var out []string
	for i := 0; i < len(words); i++ {
		w := words[i]
		if strings.HasPrefix(w, "-") {
			if valued[w] {
				i++
			}
			continue
		}
		out = append(out, resolveWorker(w, asJSON))
	}
	return out
}

// named is a worker as people read it: "desktop (%15)", or its id when
// it has no name here.
func named(id string) string {
	if strings.HasPrefix(id, "%") {
		if n := paneOpt(id, "@stui-name"); n != "" {
			return n + " (" + id + ")"
		}
	}
	return id
}
