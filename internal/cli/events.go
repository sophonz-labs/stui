package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sophonz-labs/stui/internal/orch"
)

// The run's events (orch/events.go): what stui worker
// start, send, stop and done write, and stui orch history reads.

// logEvent writes ev to its run's events; without a run there is nowhere
// to keep it, and a record that cannot be written does not stop the work.
func logEvent(ev orch.Event) {
	if ev.Run == "" {
		return
	}
	if _, err := orch.AppendEvent(ev); err != nil {
		fmt.Fprintf(os.Stderr, "  (not written to the run's history: %v)\n", err)
	}
}

// paneName is what a pane is called in a run: its worker name, else
// "coordinator" for one.
func paneName(pane string) string {
	if !strings.HasPrefix(pane, "%") {
		return ""
	}
	if n := paneOpt(pane, "@stui-name"); n != "" {
		return n
	}
	if paneOpt(pane, "@stui-role") == orch.RoleCoordinatorName {
		return "coordinator"
	}
	return ""
}

// firstRun is the run a message between two panes belongs to: the
// receiver's (it is a worker in it), else the sender's.
func firstRun(to, from string) string {
	if strings.HasPrefix(to, "%") {
		if r := workRun(to); r != "" {
			return r
		}
	}
	if strings.HasPrefix(from, "%") {
		if r := workRun(from); r != "" {
			return r
		}
		return paneOpt(from, "@stui-run")
	}
	return ""
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

const historyHelp = `stui orch history [--run r-…] [--all] [--limit N] [--json]
  what was handed to whom, and when; whether it went in; who finished and who was told`

// orchHistory is stui orch history: this pane's run (else every recent
// run), oldest first, the last --limit (50).
func orchHistory(a args, asJSON bool) {
	limit := 50
	if v, ok := a.flag("--limit"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			die(asJSON, map[string]any{}, "%s", historyHelp)
		}
		limit = n
	}
	var runs []string
	if r, ok := a.flag("--run"); ok {
		runs = []string{r}
	} else if p := os.Getenv("TMUX_PANE"); p != "" && !a.has("--all") {
		if r := firstRun("", p); r != "" {
			runs = []string{r}
		}
	}
	if len(runs) == 0 {
		runs = orch.RecentRuns(30 * 24 * time.Hour)
	}
	if asJSON {
		// The same JSON as GET /api/orch/history (orch/history.go), with the
		// copies' paths, which this machine's own readers open.
		printJSON(orch.HistoryOf(runs, limit))
		return
	}
	var evs []orch.Event
	for _, r := range runs {
		evs = append(evs, orch.ReadEvents(r)...)
	}
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].TS < evs[j].TS })
	if len(evs) > limit {
		evs = evs[len(evs)-limit:]
	}
	if len(evs) == 0 {
		fmt.Println("  nothing yet")
		return
	}
	names := map[string]string{}
	for _, ev := range evs {
		if ev.FromName != "" {
			names[ev.From] = ev.FromName
		}
		if ev.ToName != "" {
			names[ev.To] = ev.ToName
		}
	}
	who := func(id, name string) string {
		if name == "" {
			name = names[id]
		}
		if name == "" {
			return id
		}
		return name + " " + id
	}
	run := ""
	for _, ev := range evs {
		if ev.Run != run {
			run = ev.Run
			obj := ""
			if r, err := orch.Load(run); err == nil {
				obj = r.Objective
			}
			fmt.Printf("  %s  %s\n", run, obj)
		}
		kind := ev.Kind
		if ev.OK != nil && !*ev.OK {
			kind += " ✗"
		}
		what := ev.Title
		switch ev.Kind {
		case orch.EvDelivered:
			kind, what = "told", ev.Text
		case orch.EvIdle:
			what = idleWhat(ev)
		}
		if ev.Why != "" && ev.Kind != orch.EvIdle {
			what = ev.Why + "  " + what
		}
		from := who(ev.From, ev.FromName)
		if from == "" && ev.By != "" {
			// A change of the tree no pane asked for: an app's, or another
			// machine's stui.
			from = ev.By
		}
		fmt.Printf("  %s  %-9s %s → %s  %s\n", time.UnixMilli(ev.TS).Format("01-02 15:04:05"), kind, from, who(ev.To, ev.ToName), what)
	}
}

// idleWhat is an idle event in stui orch events: why the turn ended.
func idleWhat(ev orch.Event) string {
	what := "ended its turn without stui worker done"
	switch ev.StopReason {
	case orch.StopLimit:
		what = "stopped at its usage limit"
		if when := orch.ResetsLine(ev.ResetsAt, time.Now()); when != "" {
			what += " until " + when
		}
	case orch.StopError:
		what = "stopped on an error"
	case orch.StopCancelled:
		what = "was interrupted at its pane"
	}
	if ev.Why != "" {
		what += ": " + ev.Why
	}
	return what
}
