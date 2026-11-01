package cli

// Is the pane stui worker send (or stop, or read) was given still the worker
// that was meant?
//
// A pane id means something only in its tmux server. After a restore or a
// tmux restart the panes are numbered again, and an old id can be given to
// some other pane -- one that may run another agent. Work sent to a
// worker's old id would go to whatever had that id then, and send would
// say delivered: true, since an agent there did take it. So the pane must belong
// to one of the sender's runs; when it does not, the send is refused (or,
// with --force, sent with a warning), and the record says where the worker
// it meant is now.

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/sophonz-labs/stui/internal/orch"
)

type targetPane struct {
	ID, Run, InRun, Role, Key, Name string
}

const targetFormat = "#{pane_id}\t#{@stui-run}\t#{" + orch.OptInRun + "}\t#{@stui-role}\t#{" + orch.OptKey + "}\t#{@stui-name}"

func parseTargets(out string) map[string]targetPane {
	ps := map[string]targetPane{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Split(l, "\t")
		if len(f) < 6 || !strings.HasPrefix(f[0], "%") {
			continue
		}
		if _, seen := ps[f[0]]; !seen {
			ps[f[0]] = targetPane{ID: f[0], Run: f[1], InRun: f[2], Role: f[3], Key: f[4], Name: f[5]}
		}
	}
	return ps
}

// judgeTarget says why pane is not one meant by a sender working in runs
// (its @stui-run and @stui-in-run), "" when it is; now is where the record of
// runs says that worker is now, mine the sender's workers as "%8 mobile".
func judgeTarget(runs []string, recs []*orch.Run, panes map[string]targetPane, pane string) (why, now string, mine []string) {
	in := func(q targetPane) bool {
		for _, r := range runs {
			if r != "" && (q.Run == r || q.InRun == r) {
				return true
			}
		}
		return false
	}
	p, alive := panes[pane]
	if alive && in(p) {
		return "", "", nil
	}
	anyRun := false
	for _, r := range runs {
		anyRun = anyRun || r != ""
	}
	if !alive {
		why = "there is no pane " + pane
	} else if !anyRun {
		// Nothing to judge by: this pane directs no one.
		return "", "", nil
	} else if p.Run != "" || p.InRun != "" {
		why = fmt.Sprintf("%s is in another run now, not yours", pane)
	} else {
		why = fmt.Sprintf("%s is not one of yours any more: its id now belongs to another pane (a restore or a tmux restart numbers panes again)", pane)
	}
	for _, r := range recs {
		if w := r.Worker(pane); w != nil && w.Key != "" {
			for id, q := range panes {
				if q.Key == w.Key && id != pane {
					now = id
				}
			}
		}
	}
	for id, q := range panes {
		if in(q) && q.Role == orch.RoleWorkerName {
			mine = append(mine, strings.TrimSpace(id+" "+q.Name))
		}
	}
	sort.Strings(mine)
	return why, now, mine
}

// checkTarget is judgeTarget for the pane this runs in.
func checkTarget(pane string) (why, now string, mine []string) {
	me := os.Getenv("TMUX_PANE")
	runs := []string{paneOpt(me, "@stui-run"), paneOpt(me, orch.OptInRun)}
	if runs[0] == "" && runs[1] == "" {
		if r := orch.RunOf(me, paneOpt(me, orch.OptKey)); r != nil {
			runs[0] = r.ID
		}
	}
	var recs []*orch.Run
	for _, id := range runs {
		if r, err := orch.Load(id); err == nil {
			recs = append(recs, r)
		}
	}
	out, err := tx("list-panes", "-a", "-F", targetFormat)
	if err != nil {
		return "", "", nil
	}
	return judgeTarget(runs, recs, parseTargets(out), pane)
}

// targetMessage is why, with where to send instead.
func targetMessage(why, now string, mine []string) string {
	msg := why
	if now != "" {
		msg += "; that worker is " + now + " now"
	}
	if len(mine) > 0 {
		msg += ". Your workers: " + strings.Join(mine, ", ")
	}
	return msg
}
