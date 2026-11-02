package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/sophonz-labs/stui/internal/proc"
	"github.com/sophonz-labs/tmui/pkg/plugin"
)

// Builtins is what a plugin cannot take: stui ui's own : commands (its
// views, and every stui command, which : runs) and keys.
func Builtins() plugin.Builtins {
	cmds := []string{"q", "quit", "q!", "help", "?", "ext", "stui", "ui", "windows", "panes", "exit", "task", "gate", "config", "logo", "headless"}
	cmds = append(cmds, stCommands...)
	for n := range viewNames {
		cmds = append(cmds, n)
	}
	panes := []string{"a", "c", "o"}
	return plugin.Builtins{Commands: cmds, Scopes: append(append([]string(nil), plugin.TmuxScopes...), "agents", "orch", "ci", "usage"), Keys: map[string][]string{
		"":         plugin.Reserved,
		"sessions": {"p", "a", "n", "ctrl-d"},
		"windows":  {"a", "n", "ctrl-d"},
		"panes":    panes,
		"agents":   panes,
		"orch":     {"left", "right", " ", "r", "d", "a", "ctrl-d"},
		"ci":       {"o"},
		"usage":    {"g"},
	}}
}

func (a *app) loadPlugins(dirs []string) {
	a.plugins = a.keepPlugins(plugin.Load(dirs))
	a.pset = plugin.Resolve(a.plugins, Builtins())
}

// ps is the plugins, resolved; none until loadPlugins.
func (a *app) ps() *plugin.Set {
	if a.pset == nil {
		a.pset = plugin.Resolve(nil, Builtins())
	}
	return a.pset
}

// scopeOf is a view's name as plugin actions scope it.
func scopeOf(v view) string {
	switch v := v.(type) {
	case sessionsView:
		return "sessions"
	case windowsView:
		return "windows"
	case panesView:
		return "panes"
	case agentsView:
		return "agents"
	case orchView:
		return "orch"
	case ciView:
		return "ci"
	case usageView:
		return "usage"
	case *pluginView:
		return v.ref.V.Name
	}
	return ""
}

// tmuxVars are the tmux placeholders for a session, a window in it or a
// pane: a session or window stands for its active pane.
func (a *app) tmuxVars(session, win, paneID string) map[string]string {
	vars := map[string]string{"session": session, "window": win}
	if win == "" && paneID == "" {
		for _, w := range a.snap.windows[session] {
			if w.Active {
				win = w.ID
			}
		}
	}
	for _, p := range a.snap.panes[session] {
		if p.ID == paneID || (paneID == "" && p.WindowID == win && p.Active) {
			vars["window"], vars["pane"], vars["pane_cwd"], vars["pane_command"] = p.WindowID, p.ID, paneCwd(p.ID), p.Command
			break
		}
	}
	for _, n := range plugin.TmuxVars {
		if _, ok := vars[n]; !ok {
			vars[n] = ""
		}
	}
	return vars
}

// context is the placeholders for row r of page p: tmux's for tmux's
// views, and on a plugin view the row's fields over the tmux ones it was
// opened with. Without a row it is stui ui's own session, else the first.
func (a *app) context(p *page, r *row) map[string]string {
	if r != nil {
		switch v := p.v.(type) {
		case sessionsView:
			return a.tmuxVars(r.id, "", "")
		case windowsView:
			return a.tmuxVars(v.session, r.id, "")
		case panesView:
			return a.tmuxVars(v.session, "", r.id)
		case agentsView:
			if p := a.snap.agentOf(r.id); p != nil {
				return a.tmuxVars(p.Session, "", p.ID)
			}
		}
	}
	if v, ok := p.v.(*pluginView); ok {
		vars := map[string]string{}
		for k, x := range v.ctx {
			vars[k] = x
		}
		if r != nil {
			for k, x := range r.vars {
				vars[k] = x
			}
		}
		return vars
	}
	s := a.shared
	if s == "" && len(a.snap.sessions) > 0 {
		s = a.snap.sessions[0].Name
	}
	return a.tmuxVars(s, "", "")
}

func (a *app) cursorRow(p *page) *row {
	if p.v == nil {
		return nil
	}
	rows := a.visibleRows(p)
	if p.cursor >= 0 && p.cursor < len(rows) {
		return &rows[p.cursor]
	}
	return nil
}

// dir is where cwd says to run: the pane's directory, home, or stui's own.
func (a *app) dir(cwd string, vars map[string]string) (string, error) {
	switch cwd {
	case "pane":
		if vars["pane_cwd"] == "" {
			return "", errors.New("no pane to run in")
		}
		return vars["pane_cwd"], nil
	case "home":
		return os.UserHomeDir()
	}
	return "", nil
}

// paneCwd is a pane's directory, from tmux: stui's server lists panes
// without it, and stui ui talks to the same tmux server as it (attach.go).
var paneCwd = func(id string) string {
	out, err := exec.Command("tmux", "display-message", "-p", "-t", id, "#{pane_current_path}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// tilde shortens a path under $HOME to ~.
func tilde(p string) string {
	if h, err := os.UserHomeDir(); err == nil && h != "" && (p == h || strings.HasPrefix(p, h+"/")) {
		return "~" + p[len(h):]
	}
	return p
}

// -- a plugin's view: a command's output as a table --

type pluginView struct {
	ref     *plugin.ViewRef
	ctx     map[string]string // the tmux placeholders it was opened with
	where   string            // its scope line: the directory, or the plugin
	dir     string
	heads   []string
	data    []row
	err     string
	running bool
	ran     time.Time
}

func (v *pluginView) title() string {
	if v.ref.V.Title != "" {
		return v.ref.V.Title
	}
	return v.ref.V.Name
}
func (v *pluginView) scope() string { return v.where }
func (v *pluginView) header() []string {
	if v.err != "" {
		return []string{"ERROR"}
	}
	return v.heads
}
func (v *pluginView) rows(a *app) []row {
	if v.err == "" {
		return v.data
	}
	var out []row
	for i, l := range strings.Split(v.err, "\n") {
		out = append(out, row{id: fmt.Sprint(i), cells: []string{l}, color: cRed})
	}
	return out
}
func (v *pluginView) hints() []hint { return []hint{{"ctrl-r", "rerun"}} }
func (v *pluginView) empty(*app) []string {
	if v.running {
		return []string{"running " + v.ref.V.Run[0] + "…"}
	}
	return []string{"nothing to show"}
}
func (v *pluginView) key(a *app, k string, r *row) bool { return false }

// openView opens a plugin's view with the placeholders of where it was
// opened from. One plugin view replaces another instead of piling up.
func (a *app) openView(ref *plugin.ViewRef, vars map[string]string) {
	v := &pluginView{ref: ref, ctx: vars, where: ref.P.Name}
	dir, err := a.dir(ref.V.Cwd, vars)
	if err != nil {
		v.err = err.Error()
	}
	v.dir = dir
	if dir != "" {
		v.where = tilde(dir)
	}
	if _, ok := a.top().v.(*pluginView); ok {
		a.pop()
	}
	a.push(v)
	if v.err == "" {
		a.runView(v)
	}
}

// runView runs the view's command once more, unless it is running.
func (a *app) runView(v *pluginView) {
	if v.running {
		return
	}
	argv, err := plugin.Expand(v.ref.V.Run, v.ctx, nil)
	if err != nil {
		v.err = err.Error()
		return
	}
	v.running = true
	timeout := v.ref.V.TimeoutAfter()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		c := exec.CommandContext(ctx, argv[0], argv[1:]...)
		c.Dir = v.dir
		proc.Hide(c)
		c.WaitDelay = time.Second // a child that keeps the pipes open does not hold the view
		out, errOut := &plugin.Capped{N: 4 << 20}, &plugin.Capped{N: 64 << 10}
		c.Stdout, c.Stderr = out, errOut
		err := c.Run()
		if ctx.Err() == context.DeadlineExceeded {
			err = fmt.Errorf("%s: no answer in %v", argv[0], timeout)
		}
		var recs []plugin.Record
		var fields []string
		if err == nil {
			recs, fields, err = plugin.ParseOutput(v.ref.V.Format, out.Bytes())
			if err != nil {
				err = fmt.Errorf("%s output: %w", v.ref.V.Format, err)
			}
		} else if msg := strings.TrimSpace(string(errOut.Bytes())); msg != "" {
			err = fmt.Errorf("%s\n%v", msg, err)
		}
		a.later <- func() {
			v.running, v.ran = false, time.Now()
			if err != nil {
				v.err = strings.Join(argv, " ") + "\n" + err.Error()
				return
			}
			v.err = ""
			v.fill(recs, fields)
		}
	}()
}

func (v *pluginView) fill(recs []plugin.Record, fields []string) {
	cols := v.ref.V.Columns
	if len(cols) == 0 {
		for _, f := range fields {
			cols = append(cols, plugin.Column{Field: f})
		}
	}
	v.heads = v.heads[:0]
	for _, c := range cols {
		v.heads = append(v.heads, c.Heading())
	}
	v.data = v.data[:0]
	for i, r := range recs {
		cells := make([]string, len(cols))
		for j, c := range cols {
			cells[j] = r[c.Field]
		}
		id := fmt.Sprint(i)
		if f := v.ref.V.ID; f != "" {
			id = r[f]
		} else if len(cells) > 0 {
			id = cells[0]
		}
		v.data = append(v.data, row{id: id, cells: cells, color: cAqua, vars: r})
	}
}

// tick runs the view again when its refresh is due.
func (v *pluginView) tick(a *app) {
	if d := v.ref.V.RefreshEvery(); d > 0 && v.err == "" && !v.running && time.Since(v.ran) >= d {
		a.runView(v)
	} else if d > 0 && v.err != "" && !v.running && time.Since(v.ran) >= max(d, 10*time.Second) {
		a.runView(v) // a failing view tries again, but not too often
	}
}

// -- actions and commands --

// pluginKey runs a plugin action bound to k on this view, if any.
func (a *app) pluginKey(p *page, k string, r *row) bool {
	sc := scopeOf(p.v)
	ref := a.ps().Actions[sc][k]
	if ref == nil && sc == "agents" { // its rows are panes
		ref = a.ps().Actions["panes"][k]
	}
	if ref == nil {
		return false
	}
	vars := a.context(p, r)
	if ref.A.Open != "" {
		a.openView(a.ps().Views[ref.A.Open], vars)
		return true
	}
	a.runExec(ref.A.Title, ref.A.Exec, vars, nil)
	return true
}

// runExec runs an action or command: in the background with its last
// line in the status line, in the foreground with the terminal, or in a
// tmux popup over stui ui.
func (a *app) runExec(title string, e plugin.Exec, vars map[string]string, args []string) {
	argv, err := plugin.Expand(e.Run, vars, args)
	if err == nil && len(argv) == 0 {
		err = errors.New("nothing to run")
	}
	dir, derr := a.dir(e.Cwd, vars)
	if err = errors.Join(err, derr); err != nil {
		a.say(title+": "+err.Error(), true)
		return
	}
	run := func() {
		switch {
		case e.Mode == "popup" && a.at.tmuxEnv != "" && a.at.version >= 32:
			a.popup(title, argv, dir, e.Wait)
		case e.Mode == "popup" || e.Mode == "foreground":
			c := exec.Command(argv[0], argv[1:]...)
			c.Dir = dir
			a.handOverCmd(e.Wait, c, "")
			a.rerun()
		default:
			a.background(title, argv, dir)
		}
	}
	if e.Confirm {
		a.confirm(title+": run "+shellLine(argv)+"?", run)
		return
	}
	run()
}

func (a *app) background(title string, argv []string, dir string) {
	a.say(title+": running "+argv[0]+"…", false)
	go func() {
		c := exec.Command(argv[0], argv[1:]...)
		c.Dir = dir // stdin stays empty: a command that asks for input fails instead of taking stui's keys
		proc.Hide(c)
		out := &plugin.Capped{N: 64 << 10}
		c.Stdout, c.Stderr = out, out
		err := c.Run()
		last := lastLine(string(out.Bytes()))
		a.later <- func() {
			switch {
			case err != nil && last != "":
				a.say(title+": "+last+" ("+err.Error()+")", true)
			case err != nil:
				a.say(title+": "+err.Error(), true)
			case last != "":
				a.say(title+": "+last, false)
			default:
				a.say(title+": done", false)
			}
			a.rerun()
		}
	}()
}

// popup runs argv in a tmux popup over stui ui: the outer tmux, the one it
// runs in.
func (a *app) popup(title string, argv []string, dir string, wait bool) {
	script := shellLine(argv)
	if wait {
		script += `; printf '\n[exit %s] press enter to close ' "$?"; read -r _`
	}
	cmd := []string{"tmux", "display-popup", "-E", "-w", "90%", "-h", "90%"}
	if dir != "" {
		cmd = append(cmd, "-d", dir)
	}
	if a.at.version >= 33 {
		cmd = append(cmd, "-T", " "+title+" ")
	}
	cmd = append(cmd, "exec sh -c "+shellQuote(script))
	a.scr.pause()
	out, err := exec.Command(cmd[0], cmd[1:]...).CombinedOutput()
	a.scr.resume()
	a.scr.w.WriteString("\x1b[2J")
	if err != nil && strings.TrimSpace(string(out)) != "" {
		a.say("display-popup: "+strings.TrimSpace(string(out)), true)
	}
	a.rerun()
}

// rerun reads tmux and the plugin view on top again after an action.
func (a *app) rerun() {
	a.refreshNow()
	if v, ok := a.top().v.(*pluginView); ok && v.err == "" {
		a.runView(v)
	}
}

func shellLine(argv []string) string {
	q := make([]string, len(argv))
	for i, s := range argv {
		q[i] = shellQuote(s)
	}
	return strings.Join(q, " ")
}

// pluginNames are the : names plugins add, for tab.
func (a *app) pluginNames() []string { return a.ps().Order }

// pluginKeyHints are the plugin keys on view v, for the header; the
// agents view has the panes' too.
func (a *app) pluginKeyHints(v view) []hint {
	var out []hint
	seen := map[string]bool{}
	sc := scopeOf(v)
	for _, s := range []string{sc, map[string]string{"agents": "panes"}[sc]} {
		for _, ar := range a.ps().Keys(s) {
			if !seen[ar.A.Key] {
				seen[ar.A.Key] = true
				out = append(out, hint{ar.A.Key, ar.A.Title})
			}
		}
	}
	return out
}

// -- :ext, the plugins --

type pluginsView struct{}

func (pluginsView) title() string { return "Plugins" }
func (pluginsView) scope() string { return "all" }
func (pluginsView) header() []string {
	return []string{"NAME", "FROM", "VIEWS", "KEYS", "COMMANDS", "STATUS"}
}
func (pluginsView) rows(a *app) []row {
	var out []row
	for _, p := range a.plugins {
		from, status, color := "stui", "ok", cGreen
		if p.Shared {
			from = "shared"
		}
		var views, keys, cmds []string
		if p.M != nil {
			for _, v := range p.M.Views {
				views = append(views, ":"+v.Name)
			}
			for _, x := range p.M.Actions {
				keys = append(keys, x.Key)
			}
			for _, c := range p.M.Commands {
				cmds = append(cmds, ":"+c.Name)
			}
		}
		switch {
		case p.Err != nil:
			status, color = "error: "+strings.SplitN(p.Err.Error(), "\n", 2)[0], cRed
		case len(p.Problems) == 1:
			status, color = p.Problems[0], cOrange
		case len(p.Problems) > 1:
			status, color = fmt.Sprintf("%d problems: %s, …", len(p.Problems), p.Problems[0]), cOrange
		}
		out = append(out, row{id: p.Path, color: color, cells: []string{p.Name, from, strings.Join(views, " "), strings.Join(keys, " "), strings.Join(cmds, " "), status}})
	}
	return out
}
func (pluginsView) hints() []hint { return []hint{{"enter", "details"}} }
func (pluginsView) empty(*app) []string {
	return []string{"no plugins yet: stui ext install <name> (see stui ext)"}
}
func (pluginsView) key(a *app, k string, r *row) bool {
	if r == nil || (k != "enter" && k != "l") {
		return false
	}
	for _, p := range a.plugins {
		if p.Path != r.id {
			continue
		}
		lines := []string{p.Path, ""}
		if p.M != nil {
			if p.M.Description != "" {
				lines = append(lines, p.M.Description, "")
			}
			if len(p.M.Requires) > 0 {
				lines = append(lines, "needs: "+strings.Join(p.M.Requires, " "), "")
			}
			lines = append(lines, plugin.Describe(p.M)...)
		}
		if p.Err != nil {
			lines = append(lines, "", "ERROR")
			lines = append(lines, strings.Split(p.Err.Error(), "\n")...)
		}
		if len(p.Problems) > 0 {
			lines = append(lines, "", "LEFT OUT OR MISSING")
			for _, x := range p.Problems {
				lines = append(lines, "  "+x)
			}
		}
		a.pushPager(&pager{title: "Plugin", scope: p.Name, lines: lines})
	}
	return true
}

func (a *app) pluginHelp() []string {
	out := []string{"PLUGINS (:ext lists them; stui ext install <name> adds one)"}
	if len(a.plugins) == 0 {
		return append(out, "  none yet", "")
	}
	for _, n := range a.ps().Order {
		if v := a.ps().Views[n]; v != nil {
			out = append(out, fmt.Sprintf("  :%-16s %s (%s)", n, v.V.Title, v.P.Name))
		} else if c := a.ps().Commands[n]; c != nil {
			out = append(out, fmt.Sprintf("  :%-16s %s (%s)", n+" [args]", c.C.Title, c.P.Name))
		}
	}
	for _, sc := range append(append([]string(nil), Builtins().Scopes...), a.viewOrder()...) {
		for _, ar := range a.ps().Keys(sc) {
			out = append(out, fmt.Sprintf("  %-6s on %-10s %s (%s)", ar.A.Key, sc, ar.A.Title, ar.P.Name))
		}
	}
	return append(out, "")
}

func (a *app) viewOrder() []string {
	var out []string
	for _, n := range a.ps().Order {
		if a.ps().Views[n] != nil {
			out = append(out, n)
		}
	}
	return out
}

// y on a plugin is its name.
func (pluginsView) copyValue(r *row) string { return r.cells[0] }
