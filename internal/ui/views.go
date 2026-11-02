package ui

import (
	"fmt"
	"net/url"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sophonz-labs/stui/internal/home"
	stusage "github.com/sophonz-labs/stui/internal/usage"
)

// row is one line of a view's table. id is what it stands for (a session
// name, a pane id, a machine slug); a tree row also has its depth and,
// when it has children, whether they show.
type row struct {
	id     string
	cells  []string
	color  string
	parent string // the tree row above it, "" at the top
	kids   bool
	open   bool
	vars   map[string]string // a plugin view's record, for its actions' placeholders
}

type hint struct{ key, desc string }

// view is one screen: a table and the keys that act on its rows.
type view interface {
	title() string                     // "Sessions"
	scope() string                     // what it is of: "default", "%9", "all"
	header() []string                  // column names
	rows(a *app) []row                 // from a.snap
	hints() []hint                     // its own keys, for the header and ?
	key(a *app, k string, r *row) bool // a key on the row under the cursor (nil when none)
}

func ago(ms int64) string {
	if ms <= 0 {
		return ""
	}
	d := time.Since(time.UnixMilli(ms))
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(0, int(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// stateColor is how an agent's state reads: waiting needs a person, so it
// stands out most.
func stateColor(state string) string {
	switch state {
	case "working":
		return cTeal
	case "waiting":
		return cOrange
	case "error":
		return cRed
	case "delegated":
		return cPink
	case "done":
		return cGray
	}
	return cWhite
}

// stateLabel is the state as stui says it: an agent the server calls
// waiting is asking a person something.
func stateLabel(state string) string {
	if state == "waiting" {
		return "asking"
	}
	return state
}

// agentSummary is "2 working, 1 waiting", the counts that are not zero.
func agentSummary(a *agentSession) (string, string) {
	if a == nil {
		return "", cWhite
	}
	var parts []string
	color := cWhite
	for _, c := range []struct {
		n     int
		state string
	}{{a.Waiting, "waiting"}, {a.Error, "error"}, {a.Working, "working"}, {a.Delegated, "delegated"}, {a.Idle, "idle"}} {
		if c.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c.n, stateLabel(c.state)))
			if color == cWhite {
				color = stateColor(c.state)
			}
		}
	}
	return strings.Join(parts, ", "), color
}

func turnAge(p *agentPane) string {
	if p == nil {
		return ""
	}
	if p.Turn != nil && p.Turn.StartedAt > 0 {
		return ago(p.Turn.StartedAt)
	}
	return ago(p.Since)
}

func orchLabel(o *orchInfo) string {
	if o == nil {
		return ""
	}
	if o.Role == "coordinator" {
		return "coordinator " + o.Run
	}
	label := o.Name
	if label == "" {
		label = o.Role
	}
	if o.Done {
		label += " ✓"
	}
	return label
}

// -- sessions --

type sessionsView struct{}

func (sessionsView) title() string { return "Sessions" }
func (sessionsView) scope() string { return "this machine" }
func (sessionsView) header() []string {
	return []string{"NAME", "WINDOWS", "AGENTS", "STATE", "ATTACHED", "AGE", "CURRENT WINDOW"}
}
func (sessionsView) rows(a *app) []row {
	var out []row
	for _, x := range a.snap.sessions {
		state, color := agentSummary(a.snap.agentSession(x.Name))
		name := x.Name
		if x.Name == a.shared && a.shared != "" { // where stui ui itself runs
			name += " *"
		}
		agents := ""
		if x.Agents > 0 {
			agents = fmt.Sprint(x.Agents)
		}
		age := ""
		if x.Created > 0 {
			age = ago(x.Created * 1000)
		}
		out = append(out, row{id: x.Name, color: color, cells: []string{name, fmt.Sprint(x.Windows), agents, state, fmt.Sprint(x.Attached), age, x.CurrentWindow}})
	}
	return out
}
func (sessionsView) hints() []hint {
	return []hint{{"enter", "windows"}, {"p", "panes"}, {"a", "attach"}, {"n", "new"}, {"ctrl-d", "kill"}}
}
func (sessionsView) key(a *app, k string, r *row) bool {
	switch k {
	case "n":
		a.ask("New session name", "", func(name string) {
			if strings.TrimSpace(name) == "" {
				return
			}
			a.call("POST", "/api/sessions", map[string]string{"name": name}, "made session "+name)
		})
		return true
	}
	if r == nil {
		return false
	}
	switch k {
	case "enter":
		a.push(windowsView{session: r.id})
	case "p":
		a.push(panesView{session: r.id})
	case "a":
		a.attach(r.id)
	case "ctrl-d":
		a.confirm("Kill session "+r.id+" and everything running in it?", func() {
			a.call("DELETE", "/api/sessions/"+url.PathEscape(r.id), nil, "killed session "+r.id)
		})
	default:
		return false
	}
	return true
}

// -- windows --

type windowsView struct{ session string }

func (v windowsView) title() string { return "Windows" }
func (v windowsView) scope() string { return v.session }
func (windowsView) header() []string {
	return []string{"#", "NAME", "PANES", "COMMAND", "AGENT", "STATE", "TURN", "REPO"}
}
func (v windowsView) rows(a *app) []row {
	var out []row
	for _, w := range a.snap.windows[v.session] {
		// The window's most urgent agent speaks for it.
		var top *agentPane
		for i := range a.snap.agents.Panes {
			p := &a.snap.agents.Panes[i]
			if p.Session == v.session && p.Window == w.Index && (top == nil || urgency(p.State) > urgency(top.State)) {
				top = p
			}
		}
		agent, state, color := "", "", cWhite
		if top != nil {
			agent, state, color = top.Agent, stateLabel(top.State), stateColor(top.State)
		}
		name := w.Name
		if w.Active {
			name += " *"
		}
		out = append(out, row{id: w.ID, color: color, cells: []string{fmt.Sprint(w.Index), name, fmt.Sprint(w.Panes), w.Command, agent, state, turnAge(top), w.Repo}})
	}
	return out
}
func urgency(state string) int {
	return map[string]int{"waiting": 5, "error": 4, "working": 3, "delegated": 2, "idle": 1}[state]
}
func (windowsView) hints() []hint {
	return []hint{{"enter", "panes"}, {"a", "attach"}, {"n", "new"}, {"ctrl-d", "close"}}
}
func (v windowsView) key(a *app, k string, r *row) bool {
	switch k {
	case "n":
		a.ask("New window: agent (blank for a shell, or claude, codex, opencode, pi)", "", func(agent string) {
			a.call("POST", "/api/windows", map[string]string{"session": v.session, "agent": strings.TrimSpace(agent)}, "opened a window in "+v.session)
		})
		return true
	case "a":
		a.attach(v.session)
		return true
	}
	if r == nil {
		return false
	}
	switch k {
	case "enter":
		a.push(panesView{session: v.session, window: r.id, windowName: r.cells[1]})
	case "ctrl-d":
		a.confirm("Close window "+r.cells[1]+" in "+v.session+"?", func() {
			a.call("DELETE", "/api/windows?session="+url.QueryEscape(v.session)+"&window="+url.QueryEscape(r.id), nil, "closed "+r.cells[1])
		})
	default:
		return false
	}
	return true
}

// -- panes --

type panesView struct{ session, window, windowName string }

func (v panesView) title() string { return "Panes" }
func (v panesView) scope() string {
	if v.window != "" {
		return v.session + ":" + v.windowName
	}
	return v.session
}
func (panesView) header() []string {
	return []string{"PANE", "WINDOW", "COMMAND", "AGENT", "STATE", "TURN", "CI", "ORCH", "TITLE"}
}
func (v panesView) rows(a *app) []row {
	var out []row
	for _, p := range a.snap.panes[v.session] {
		if v.window != "" && p.WindowID != v.window {
			continue
		}
		ag := a.snap.agentOf(p.ID)
		agent, state, title, color := "", "", "", cWhite
		var o *orchInfo
		if ag != nil {
			agent, state, title, color, o = ag.Agent, stateLabel(ag.State), ag.Title, stateColor(ag.State), ag.Orch
		}
		out = append(out, row{id: p.ID, color: color, cells: []string{p.ID, fmt.Sprintf("%d %s", p.Window, p.WindowName), p.Command, agent, state, turnAge(ag), ciMark(a.snap.ci[p.ID]), orchLabel(o), title}})
	}
	return out
}
func (panesView) hints() []hint {
	return []hint{{"enter", "output"}, {"a", "attach to it"}, {"c", "its CI"}, {"o", "open CI run"}}
}
func (v panesView) key(a *app, k string, r *row) bool {
	if r == nil {
		return false
	}
	switch k {
	case "enter", "l":
		a.showPane(v.session, r.id)
	case "a":
		a.attachPane(v.session, r.id)
	case "c":
		a.push(ciView{pane: r.id})
	case "o":
		a.openCI(r.id)
	default:
		return false
	}
	return true
}

// -- agents --

type agentsView struct{}

func (agentsView) title() string { return "Agents" }
func (agentsView) scope() string { return "this machine" }
func (agentsView) header() []string {
	return []string{"PANE", "SESSION", "WINDOW", "AGENT", "STATE", "TURN", "SUB", "CI", "ORCH", "TITLE"}
}
func (agentsView) rows(a *app) []row {
	ps := append([]agentPane(nil), a.snap.agents.Panes...)
	sort.SliceStable(ps, func(i, j int) bool { return urgency(ps[i].State) > urgency(ps[j].State) })
	var out []row
	for i := range ps {
		p := &ps[i]
		sub := ""
		if p.Subagents.Running > 0 {
			sub = fmt.Sprint(p.Subagents.Running)
		}
		out = append(out, row{id: p.ID, color: stateColor(p.State), cells: []string{p.ID, p.Session, fmt.Sprintf("%d %s", p.Window, p.WindowName), p.Agent, stateLabel(p.State), turnAge(p), sub, ciMark(a.snap.ci[p.ID]), orchLabel(p.Orch), p.Title}})
	}
	return out
}
func (agentsView) hints() []hint {
	return []hint{{"enter", "output"}, {"a", "attach to it"}, {"c", "its CI"}, {"o", "open CI run"}}
}
func (agentsView) key(a *app, k string, r *row) bool {
	if r == nil {
		return false
	}
	p := a.snap.agentOf(r.id)
	if p == nil {
		return false
	}
	switch k {
	case "enter", "l":
		a.showPane(p.Session, p.ID)
	case "a":
		a.attachPane(p.Session, p.ID)
	case "c":
		a.push(ciView{pane: p.ID})
	case "o":
		a.openCI(p.ID)
	default:
		return false
	}
	return true
}

// -- the orchestration tree --

// orchView is stui's runs as a tree, as a file browser shows folders: a run,
// its coordinator, the coordinator's workers and theirs. ← and → fold and
// unfold, as in k9s's xray view and most file trees.
type orchView struct{}

func (orchView) title() string { return "Orchestration" }
func (orchView) scope() string { return "runs" }
func (orchView) header() []string {
	return []string{"NAME", "PANE", "AGENT", "STATE", "TURN", "WHERE", "SUMMARY"}
}

func (orchView) rows(a *app) []row {
	byRun := map[string][]*agentPane{}
	var runs []string
	for i := range a.snap.agents.Panes {
		p := &a.snap.agents.Panes[i]
		if p.Orch == nil || p.Orch.Run == "" {
			continue
		}
		if byRun[p.Orch.Run] == nil {
			runs = append(runs, p.Orch.Run)
		}
		byRun[p.Orch.Run] = append(byRun[p.Orch.Run], p)
	}
	sort.Strings(runs)
	var out []row
	for _, run := range runs {
		ps := byRun[run]
		here := map[string]bool{}
		kids := map[string][]*agentPane{}
		for _, p := range ps {
			here[p.ID] = true
		}
		var tops []*agentPane
		for _, p := range ps {
			// A worker whose parent is not here (a coordinator on another
			// machine) starts its own branch.
			if p.Orch.Parent == "" || !here[p.Orch.Parent] || p.Orch.Role == "coordinator" {
				tops = append(tops, p)
			} else {
				kids[p.Orch.Parent] = append(kids[p.Orch.Parent], p)
			}
		}
		key := "run:" + run
		open := !a.folded[key]
		working := 0
		for _, p := range ps {
			if p.State == "working" {
				working++
			}
		}
		out = append(out, row{id: key, kids: true, open: open, color: cPink,
			cells: []string{fold(open) + run, "", "", fmt.Sprintf("%d working", working), "", "", fmt.Sprintf("%d panes", len(ps))}})
		if !open {
			continue
		}
		var walk func(list []*agentPane, prefix, parent string)
		walk = func(list []*agentPane, prefix, parent string) {
			for i, p := range list {
				last := i == len(list)-1
				branch, next := "├─ ", "│  "
				if last {
					branch, next = "└─ ", "   "
				}
				ch := kids[p.ID]
				o := !a.folded[p.ID]
				mark := "● "
				if len(ch) > 0 {
					mark = fold(o)
				}
				name := p.Orch.Name
				if p.Orch.Role == "coordinator" {
					name = "coordinator"
				}
				if p.Orch.Done {
					name += " ✓"
				}
				summary := p.Orch.Summary
				if summary == "" {
					summary = p.Orch.Brief
				}
				if summary == "" {
					summary = p.Title
				}
				color := stateColor(p.State)
				if p.Orch.Done && p.State == "idle" {
					color = cGray
				}
				out = append(out, row{id: p.ID, parent: parent, kids: len(ch) > 0, open: o, color: color,
					cells: []string{prefix + branch + mark + name, p.ID, p.Agent, stateLabel(p.State), turnAge(p), fmt.Sprintf("%s:%d", p.Session, p.Window), summary}})
				if len(ch) > 0 && o {
					walk(ch, prefix+next, p.ID)
				}
			}
		}
		walk(tops, " ", key)
	}
	return out
}

// empty is what the view says when it has no runs: how to start one, and
// where st's are when st is on this machine (stui does not read them).
func (orchView) empty(*app) []string {
	out := []string{
		"No runs yet. In an agent's pane:",
		`  stui orch start --objective "<goal>"`,
		"  stui worker start --agent claude --task task.md",
		"Or let the agent do it: stui plugin install, then ask it to split the work.",
	}
	if stOnPath() {
		out = append(out, "", "st is installed here: its runs are in st ui.")
	}
	return out
}

// shortcut: any "st" on PATH counts (another program could have the name);
// running it to ask would be worse.
var stOnPath = sync.OnceValue(func() bool {
	_, err := exec.LookPath("st")
	return err == nil
})

func fold(open bool) string {
	if open {
		return "▾ "
	}
	return "▸ "
}

func (orchView) hints() []hint {
	return []hint{{"←/→", "fold"}, {"enter", "output"}, {"a", "attach to it"}, {"r", "read"}, {"d", "diff"}, {"ctrl-d", "stop"}}
}

func (orchView) key(a *app, k string, r *row) bool {
	if r == nil {
		return false
	}
	switch k {
	case "left", "h":
		if r.kids && r.open {
			a.folded[r.id] = true
		} else if r.parent != "" {
			a.cursorTo(r.parent)
		}
		return true
	case "right", "l":
		if r.kids {
			delete(a.folded, r.id)
		}
		return true
	case " ":
		if r.kids {
			a.folded[r.id] = r.open
		}
		return true
	}
	if strings.HasPrefix(r.id, "run:") {
		if k == "enter" {
			a.folded[r.id] = r.open
			return true
		}
		return false
	}
	p := a.snap.agentOf(r.id)
	if p == nil {
		return false
	}
	switch k {
	case "enter":
		a.showPane(p.Session, p.ID)
	case "r":
		a.output("stui worker read "+p.ID, "worker", "read", p.ID)
	case "d":
		a.output("stui worker diff "+p.ID, "worker", "diff", p.ID, "--stat")
	case "a":
		a.attachPane(p.Session, p.ID)
	case "ctrl-d":
		a.confirm("Stop worker "+orchLabel(p.Orch)+" ("+p.ID+") and close its pane?", func() {
			a.runQuiet("stopped "+p.ID, "worker", "stop", p.ID, "--yes")
		})
	default:
		return false
	}
	return true
}

// -- usage --

type usageView struct{}

func (usageView) title() string { return "Usage" }
func (usageView) scope() string { return "plan limits" }
func (usageView) header() []string {
	return []string{"AGENT", "PLAN", "ACCOUNT", "LIMIT", "USED", "GAUGE", "RESETS", "GUARD"}
}
func (usageView) rows(a *app) []row {
	// The guard's verdict only acts while it is on.
	guard := map[string]string{}
	for _, g := range a.snap.usage.Guard.Agents {
		guard[g.Agent] = g.State
		if !a.snap.usage.Guard.On {
			guard[g.Agent] = "off"
		}
	}
	var out []row
	for _, ag := range a.snap.usage.Agents {
		account := ""
		if ag.Account != nil {
			account = ag.Account.Label
		}
		if len(ag.Windows) == 0 {
			out = append(out, row{id: ag.Agent, color: cGray, cells: []string{ag.Agent, ag.Plan, account, noWindows(ag), "", "", "", guard[ag.Agent]}})
			continue
		}
		for _, w := range ag.Windows {
			color := cWhite
			switch {
			case w.UsedPercent >= 95:
				color = cRed
			case w.UsedPercent >= 80:
				color = cOrange
			}
			out = append(out, row{id: ag.Agent + "/" + w.ID, color: color, cells: []string{
				ag.Agent, ag.Plan, account, limitName(w), fmt.Sprintf("%.0f%%", w.UsedPercent),
				// The gauge is drawn into the cell as text; the row's colour stays.
				gaugeText(w.UsedPercent, 20), resets(w.ResetsAt), guard[ag.Agent]}})
		}
	}
	return out
}

// noWindows says what to do about an agent with no limits to show.
func noWindows(ag usageAgent) string {
	if stusage.PollOff(stusage.DefaultEnv(home.Dir())) {
		if ag.Agent == "claude" {
			return "none: stui usage poll on, or stui hooks install --usage"
		}
		return "none: stui usage poll on"
	}
	if ag.Agent == "claude" {
		return "none yet: needs a Claude login here, or stui hooks install --usage"
	}
	return "none yet: needs a ChatGPT login in codex"
}

func limitName(w usageWindow) string {
	switch {
	case w.Kind == "weekly" && w.Label != "":
		// The per-model weekly limit beside the all-model one.
		return "weekly " + strings.ToLower(w.Label)
	case w.Kind == "weekly":
		return "weekly"
	case w.Minutes == 300:
		return "5 hours"
	case w.Minutes > 0 && w.Minutes%1440 == 0:
		return fmt.Sprintf("%d days", w.Minutes/1440)
	case w.Minutes > 0:
		return fmt.Sprintf("%dh", w.Minutes/60)
	}
	return w.ID
}

func gaugeText(p float64, width int) string {
	full := int(max(0, min(100, p))/100*float64(width) + 0.5)
	return strings.Repeat("█", full) + strings.Repeat("░", width-full)
}

func resets(ms int64) string {
	if ms <= 0 {
		return ""
	}
	t := time.UnixMilli(ms)
	if d := time.Until(t); d < 24*time.Hour {
		return fmt.Sprintf("in %dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return t.Format("Mon 15:04")
}

func (usageView) hints() []hint { return []hint{{"g", "guard on/off"}} }
func (usageView) key(a *app, k string, _ *row) bool {
	if k != "g" {
		return false
	}
	to := "on"
	if a.snap.usage.Guard.On {
		to = "off"
	}
	a.runQuiet("usage guard "+to, "usage", "guard", to)
	return true
}
