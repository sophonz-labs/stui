package restore

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// When "restored" stops being news.
//
// A restore marks each window it makes (@stui_restored, and @stui_restored_agent
// on a window whose agent's resume line is waiting), and the window list
// says so on the web and the phone. Not for a while: a server that died in
// the night still needs saying in the morning. Until it has been dealt with:
//
//   - the agent's mark goes once something runs in the agent's pane -- the
//     resume line was entered;
//   - the window's mark goes once something has run in it after the restore
//     and no agent of it is still waiting.
//
// "Something ran" is a program other than the shell in the foreground, or
// the pane's lines having grown at the same size: a command that ended
// before anyone looked still moved the prompt down. Not window_activity: a
// shell redraws its prompt when the window is resized, which is what a
// browser or phone opening the window does, so looking would count as
// dealing with it.

// settleAfter is how long after a restore its own doing -- the note, the
// login shell starting, the resume line typed (up to typeWait) -- is not
// taken for somebody's.
var settleAfter = typeWait + 15*time.Second

// The pane options: the pane whose agent's resume line waits (set by the
// restore), and the pane's width and line count when Settle first looked, to
// tell a command from a redraw. Not named like the window's options: a pane
// reads a window option it does not have itself.
const (
	paneAgent = "@stui_resume_pane"
	paneBase  = "@stui_restored_base"
)

var settleFields = strings.Join([]string{
	"#{window_id}", "#{@stui_restored}", "#{@stui_restored_agent}",
	"#{pane_id}", "#{pane_current_command}", "#{pane_width}x#{pane_height}", "#{history_size}", "#{cursor_y}",
}, sep)

type settlePane struct {
	id, cmd, base, agent string
	size                 string
	lines                int
}

type settleWindow struct {
	id, stamp, agent string
	panes            []settlePane
}

// Settle clears the restored marks of the windows that have been dealt
// with. It reports how many windows lost theirs.
func Settle(ctx context.Context, t Tmux, now time.Time) int {
	out, err := t.Run(ctx, "list-panes", "-a", "-F", settleFields+sep+"#{"+paneBase+"}"+sep+"#{"+paneAgent+"}")
	if err != nil {
		return 0
	}
	var order []string
	wins := map[string]*settleWindow{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, sep)
		if len(f) != 10 || f[1] == "" {
			continue
		}
		w := wins[f[0]]
		if w == nil {
			w = &settleWindow{id: f[0], stamp: f[1], agent: f[2]}
			wins[f[0]] = w
			order = append(order, f[0])
		}
		hist, _ := strconv.Atoi(f[6])
		cy, _ := strconv.Atoi(f[7])
		w.panes = append(w.panes, settlePane{id: f[3], cmd: f[4], size: f[5], lines: hist + cy, base: f[8], agent: f[9]})
	}
	n := 0
	for _, id := range order {
		if settleWindowMarks(ctx, t, wins[id], now) {
			n++
		}
	}
	return n
}

func settleWindowMarks(ctx context.Context, t Tmux, w *settleWindow, now time.Time) bool {
	if at, err := time.Parse(time.RFC3339, w.stamp); err == nil && now.Sub(at) < settleAfter {
		return false
	}
	// A restore before stui marked the agent's pane: any pane may be it.
	marked := false
	for _, p := range w.panes {
		marked = marked || p.agent != ""
	}
	ran, waiting := false, false
	for _, p := range w.panes {
		did := paneRan(ctx, t, p)
		ran = ran || did
		isAgent := p.agent != "" || (!marked && w.agent != "")
		switch {
		case isAgent && did:
			if p.agent != "" {
				t.Run(ctx, "set-option", "-pu", "-t", p.id, paneAgent)
			}
		case isAgent:
			waiting = true
		}
	}
	if w.agent != "" && !waiting {
		t.Run(ctx, "set-option", "-wu", "-t", w.id, "@stui_restored_agent")
	}
	if !ran || waiting {
		return false
	}
	t.Run(ctx, "set-option", "-wu", "-t", w.id, "@stui_restored")
	for _, p := range w.panes {
		if p.base != "" {
			t.Run(ctx, "set-option", "-pu", "-t", p.id, paneBase)
		}
	}
	return true
}

// paneRan says whether something has run in the pane since its baseline,
// and takes the baseline when there is none or the pane changed size: a
// reflow, or the shell redrawing its prompt for the new size, moves the line
// count without anything running.
func paneRan(ctx context.Context, t Tmux, p settlePane) bool {
	if p.cmd != "" && !IsShell(p.cmd) {
		return true
	}
	size, lines, ok := strings.Cut(p.base, " ")
	if l, err := strconv.Atoi(lines); ok && err == nil && size == p.size {
		return p.lines > l
	}
	t.Run(ctx, "set-option", "-p", "-t", p.id, paneBase, p.size+" "+strconv.Itoa(p.lines))
	return false
}
