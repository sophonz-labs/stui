// Package ui is stui ui: this machine's tmux sessions, windows, panes,
// coding agents and their runs, their repositories' GitHub Actions and the
// agents' plan limits, in the terminal, as k9s shows a cluster. Every view
// reads stui's local server; ":" switches views or runs any stui command.
package ui

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/term"

	"github.com/sophonz-labs/stui/internal/config"
	"github.com/sophonz-labs/stui/internal/plugin"
	"github.com/sophonz-labs/stui/internal/state"
)

type mode int

const (
	normal mode = iota
	command
	filtering
	asking
	confirming
)

// page is one step of the way down (sessions > windows > panes): a view,
// or a pager of text, and where the cursor was on it.
type page struct {
	v      view
	pg     *pager
	cursor int
	top    int
	sel    string // the row the cursor is on, kept across refreshes
	filter string
}

// pager is text to scroll: a pane's output, an stui command's.
type pager struct {
	title, scope string
	lines        []string
	top          int
	follow       bool // at the bottom, staying there as it grows
	reload       func() ([]string, error)
}

type app struct {
	scr    *screen
	c      *client
	at     where  // where this terminal is, for attaching
	ver    string // this stui's version, under the logo
	shared string // the session stui ui runs in, "" outside tmux
	exe    string // this stui, to run its commands
	snap   *snap
	stack  []*page
	folded map[string]bool
	w, h   int

	mode    mode
	input   string
	label   string
	submit  func(string)
	flash   string
	bad     bool
	flashAt time.Time

	plugins []*plugin.Plugin
	pset    *plugin.Set

	cfg      *config.Config
	logoOn   bool         // ui.logo, until :logo says otherwise
	headless bool         // ui.headless, likewise
	rate     atomic.Int64 // refreshRate, read by fetchLoop
	ticks    int

	snaps   chan *snap
	refresh chan struct{}
	later   chan func()
	quit    bool
}

// Run is stui ui. Everything it shows comes from stui's local server, s.
func Run(s *state.Agent, version string, args []string) int {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	at := locate()
	a := &app{c: newClient(s), at: at, ver: version, shared: at.ownSession, exe: exe, folded: map[string]bool{},
		snaps: make(chan *snap, 1), refresh: make(chan struct{}, 1), later: make(chan func(), 16)}
	a.applyConfig(config.Load(config.Path("stui"), startViews))
	a.snap = a.c.fetch(nil, false)
	if a.snap.err != nil {
		fmt.Fprintf(os.Stderr, "stui ui: stui's server does not answer: %v\n", a.snap.err)
		return 1
	}
	start := firstWord(args)
	if start == "" {
		start = a.cfg.StartView
	}
	a.stack = []*page{{v: rootView(start)}}
	scr, err := openScreen()
	if err != nil {
		fmt.Fprintln(os.Stderr, "stui ui:", err)
		return 1
	}
	a.scr = scr
	scr.mono = !a.cfg.Color
	if n := len(a.cfg.Warnings); n > 0 {
		a.say(fmt.Sprintf("config.yaml: %s (and %d more; :config)", a.cfg.Warnings[0], n-1), true)
	}
	defer scr.close()
	go a.fetchLoop()
	a.loop()
	return 0
}

func firstWord(args []string) string {
	for _, x := range args {
		if !strings.HasPrefix(x, "-") {
			return x
		}
	}
	return ""
}

func (a *app) fetchLoop() {
	prev := a.snap
	for n := 1; ; n++ {
		select {
		case <-a.refresh:
		case <-time.After(time.Duration(a.rate.Load())):
		}
		s := a.c.fetch(prev, n%5 == 1) // CI every 5th time, first after one
		prev = s
		select {
		case <-a.snaps: // drop one not drawn yet
		default:
		}
		a.snaps <- s
	}
}

func (a *app) refreshNow() {
	select {
	case a.refresh <- struct{}{}:
	default:
	}
}

func (a *app) loop() {
	a.w, a.h = a.scr.size()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for !a.quit {
		a.scr.draw(a.render())
		select {
		case k, ok := <-a.scr.keys:
			if !ok {
				return
			}
			a.onKey(k)
		case s := <-a.snaps:
			a.snap = s
			if p := a.top(); p.pg != nil && p.pg.follow && p.pg.reload != nil {
				a.reloadPager(p.pg)
			}
		case f := <-a.later:
			f()
		case <-tick.C:
			w, h := a.scr.size()
			expired := a.flash != "" && time.Since(a.flashAt) > 5*time.Second
			if expired {
				a.flash = ""
			}
			if v, ok := a.top().v.(*pluginView); ok {
				v.tick(a)
			}
			if a.ticks++; a.ticks%4 == 0 && a.cfg.Changed() {
				a.reloadConfig()
			}
			if w == a.w && h == a.h && !expired {
				continue
			}
			if w != a.w || h != a.h {
				a.scr.w.WriteString("\x1b[2J")
			}
			a.w, a.h = w, h
		}
	}
}

// -- what the keys do --

func (a *app) top() *page { return a.stack[len(a.stack)-1] }

func (a *app) push(v view) { a.stack = append(a.stack, &page{v: v}) }

func (a *app) pushPager(p *pager) { a.stack = append(a.stack, &page{pg: p}) }

func (a *app) pop() {
	if len(a.stack) > 1 {
		a.stack = a.stack[:len(a.stack)-1]
	}
}

func (a *app) setRoot(v view) { a.stack = []*page{{v: v}} }

func (a *app) say(msg string, bad bool) { a.flash, a.bad, a.flashAt = msg, bad, time.Now() }

// views by the names ":" takes, k9s style: the name, or a short one.
var viewNames = map[string]func() view{
	"sessions": func() view { return sessionsView{} }, "session": func() view { return sessionsView{} }, "se": func() view { return sessionsView{} }, "ss": func() view { return sessionsView{} },
	"agents": func() view { return agentsView{} }, "agent": func() view { return agentsView{} }, "ag": func() view { return agentsView{} },
	"orch": func() view { return orchView{} }, "tree": func() view { return orchView{} }, "workers": func() view { return orchView{} }, "runs": func() view { return orchView{} }, "or": func() view { return orchView{} },
	"ci": func() view { return ciView{} }, "actions": func() view { return ciView{} },
	"usage": func() view { return usageView{} }, "us": func() view { return usageView{} },
}

// hotkeys are the digits that switch views, as k9s's favourites.
var hotkeys = []struct{ key, name string }{{"1", "sessions"}, {"2", "agents"}, {"3", "orch"}, {"4", "ci"}, {"5", "usage"}}

func rootView(name string) view {
	if f, ok := viewNames[name]; ok {
		return f()
	}
	return sessionsView{}
}

func (a *app) onKey(k string) {
	switch a.mode {
	case normal:
		a.normalKey(k)
	case confirming:
		f := a.submit
		a.mode, a.submit = normal, nil
		if k == "y" || k == "Y" {
			f("")
		} else {
			a.say("cancelled", false)
		}
	default:
		a.editKey(k)
	}
}

func (a *app) normalKey(k string) {
	p := a.top()
	switch k {
	case "ctrl-c":
		a.quit = true
		return
	case "q":
		if len(a.stack) == 1 {
			a.quit = true
		}
		a.pop()
		return
	case "esc":
		if p.filter != "" {
			p.filter = ""
			return
		}
		a.pop()
		return
	case ":":
		a.mode, a.input, a.label = command, "", ""
		return
	case "/":
		if p.pg == nil {
			a.mode, a.input = filtering, p.filter
		}
		return
	case "?":
		a.pushPager(&pager{title: "Help", scope: "keys", lines: a.helpText()})
		return
	case "ctrl-e":
		a.headless = !a.headless
		a.clear()
		return
	case "ctrl-r":
		a.reloadConfig()
		a.refreshNow()
		if p.pg != nil && p.pg.reload != nil {
			a.reloadPager(p.pg)
		}
		if v, ok := p.v.(*pluginView); ok {
			v.err = ""
			a.runView(v)
		}
		return
	}
	for _, h := range hotkeys {
		if k == h.key {
			a.setRoot(viewNames[h.name]())
			return
		}
	}
	if p.pg != nil {
		a.pagerKey(p.pg, k)
		return
	}
	rows := a.visibleRows(p)
	switch k {
	case "up", "k":
		p.cursor--
	case "down", "j":
		p.cursor++
	case "home", "g":
		p.cursor = 0
	case "end", "G":
		p.cursor = len(rows) - 1
	case "pgup", "ctrl-b":
		p.cursor -= a.bodyRows()
	case "pgdn", "ctrl-f":
		p.cursor += a.bodyRows()
	default:
		var r *row
		if p.cursor >= 0 && p.cursor < len(rows) {
			r = &rows[p.cursor]
		}
		if !p.v.key(a, k, r) && !a.pluginKey(p, k, r) && k == "enter" && r == nil {
			a.say("nothing here", false)
		}
		return
	}
	p.cursor = max(0, min(p.cursor, len(rows)-1))
	if p.cursor < len(rows) && p.cursor >= 0 {
		p.sel = rows[p.cursor].id
	}
}

func (a *app) pagerKey(pg *pager, k string) {
	n := a.bodyRows()
	switch k {
	case "up", "k":
		pg.top--
	case "down", "j", "enter":
		pg.top++
	case "pgup", "ctrl-b", "b":
		pg.top -= n
	case "pgdn", "ctrl-f", " ":
		pg.top += n
	case "home", "g":
		pg.top = 0
	case "end", "G":
		pg.top = len(pg.lines)
	case "r":
		if pg.reload != nil {
			a.reloadPager(pg)
		}
	}
	end := max(0, len(pg.lines)-n)
	pg.top = max(0, min(pg.top, end))
	pg.follow = pg.top == end
}

func (a *app) editKey(k string) {
	switch k {
	case "esc", "ctrl-c":
		if a.mode == filtering {
			a.top().filter = ""
		}
		a.mode = normal
		return
	case "enter":
		m, text := a.mode, a.input
		a.mode = normal
		switch m {
		case command:
			a.runCommand(text)
		case asking:
			a.submit(text)
		}
		return
	case "backspace":
		if r := []rune(a.input); len(r) > 0 {
			a.input = string(r[:len(r)-1])
		}
	case "ctrl-u":
		a.input = ""
	case "ctrl-w":
		t := strings.TrimRight(a.input, " ")
		if i := strings.LastIndexByte(t, ' '); i >= 0 {
			a.input = t[:i+1]
		} else {
			a.input = ""
		}
	case "tab":
		if a.mode == command {
			a.input = complete(a.input, a.pluginNames()...)
		}
	default:
		if len([]rune(k)) == 1 {
			a.input += k
		}
	}
	if a.mode == filtering {
		p := a.top()
		p.filter, p.cursor = a.input, 0
	}
}

// ask reads a line on the prompt line, then calls f with it.
func (a *app) ask(label, initial string, f func(string)) {
	a.mode, a.label, a.input, a.submit = asking, label, initial, f
}

// confirm asks y/N before f.
func (a *app) confirm(question string, f func()) {
	a.mode, a.label, a.submit = confirming, question, func(string) { f() }
}

func (a *app) cursorTo(id string) {
	p := a.top()
	for i, r := range a.visibleRows(p) {
		if r.id == id {
			p.cursor, p.sel = i, id
			return
		}
	}
}

// -- running things --

// call sends a request to stui's server in the background and says how it went.
func (a *app) call(method, path string, body any, ok string) {
	go func() {
		err := a.c.do(method, path, body, nil)
		a.later <- func() {
			if err != nil {
				a.say(err.Error(), true)
				return
			}
			a.say(ok, false)
			a.refreshNow()
		}
	}()
}

// stCmd is this stui with args, as a child that knows it was started by stui ui.
func (a *app) stCmd(ctx context.Context, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, a.exe, args...)
	c.Env = append(os.Environ(), "STUI_UI=1")
	return c
}

// runQuiet runs an stui command in the background and says what it said.
func (a *app) runQuiet(ok string, args ...string) {
	a.say("stui "+strings.Join(args, " ")+" …", false)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		out, err := a.stCmd(ctx, args...).CombinedOutput()
		a.later <- func() {
			msg := lastLine(string(out))
			if err != nil {
				if msg == "" {
					msg = err.Error()
				}
				a.say(msg, true)
				return
			}
			if msg == "" {
				msg = ok
			}
			a.say(msg, false)
			a.refreshNow()
		}
	}()
}

// output runs an stui command and shows what it printed in a pager.
func (a *app) output(title string, args ...string) {
	pg := &pager{title: "Output", scope: title, lines: []string{"running stui " + strings.Join(args, " ") + " …"}}
	pg.reload = func() ([]string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		out, err := a.stCmd(ctx, args...).CombinedOutput()
		lines := strings.Split(strings.TrimRight(plain(string(out)), "\n"), "\n")
		if err != nil {
			lines = append(lines, "", "("+err.Error()+")")
		}
		return lines, nil
	}
	a.pushPager(pg)
	a.reloadPager(pg)
}

func (a *app) reloadPager(pg *pager) {
	if pg.reload == nil {
		return
	}
	go func() {
		lines, err := pg.reload()
		a.later <- func() {
			if err != nil {
				a.say(err.Error(), true)
				return
			}
			pg.lines = lines
			if pg.follow {
				pg.top = max(0, len(lines)-a.bodyRows())
			}
		}
	}()
}

// showPane is a pane's output, its history and screen, following it.
func (a *app) showPane(session, id string) {
	pg := &pager{title: "Output", scope: session + " " + id, follow: true}
	pg.reload = func() ([]string, error) {
		var h struct {
			Text string `json:"text"`
		}
		path := "/api/panes/history?lines=2000&session=" + url.QueryEscape(session) + "&pane=" + url.QueryEscape(id)
		if err := a.c.get(path, &h); err != nil {
			return nil, err
		}
		return strings.Split(strings.TrimRight(plain(h.Text), "\n"), "\n"), nil
	}
	a.pushPager(pg)
	a.reloadPager(pg)
}

// handOver gives the terminal to an stui command and takes it back once it
// ends. wait keeps its last words on screen until a key.
func (a *app) handOver(wait bool, args ...string) {
	a.handOverCmd(wait, a.stCmd(context.Background(), args...), "")
}

// handOverCmd gives the terminal to c, saying before first.
func (a *app) handOverCmd(wait bool, c *exec.Cmd, before string) {
	a.scr.pause()
	a.scr.leave()
	if before != "" {
		fmt.Println(before)
	}
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := c.Run()
	if wait || err != nil {
		if err != nil {
			fmt.Printf("\n(%v)", err)
		}
		fmt.Print("\npress any key to go back to stui ui ")
		if old, e := term.MakeRaw(a.scr.in); e == nil {
			os.Stdin.Read(make([]byte, 16))
			term.Restore(a.scr.in, old)
		}
	}
	a.scr.enter()
	a.scr.resume()
	a.refreshNow()
}

// attach puts this terminal on a session.
func (a *app) attach(session string) { a.attachTo(session, "="+session, "session "+session) }

// attachPane attaches with that pane picked: its window, then it.
func (a *app) attachPane(session, id string) {
	if err := a.c.do("POST", "/api/action", map[string]string{"session": session, "action": "select-pane", "target": id}, nil); err != nil {
		a.say(err.Error(), true)
		return
	}
	target := "=" + session
	if a.at.sameServer && session == a.at.ownSession {
		target = id
	}
	a.attachTo(session, target, session+" "+id)
}

// runCommand is what ":" was given: a view, quit, or an stui command.
func (a *app) runCommand(text string) {
	words := splitWords(strings.TrimSpace(text))
	if len(words) == 0 {
		return
	}
	if words[0] == "stui" {
		words = words[1:]
		if len(words) == 0 {
			return
		}
	}
	switch {
	case words[0] == "q" || words[0] == "quit" || words[0] == "q!":
		a.quit = true
		return
	case words[0] == "help" || words[0] == "?":
		a.pushPager(&pager{title: "Help", scope: "keys", lines: a.helpText()})
		return
	case viewNames[words[0]] != nil:
		a.setRoot(viewNames[words[0]]())
		return
	case words[0] == "windows" || words[0] == "panes":
		s := a.shared
		if s == "" && len(a.snap.sessions) > 0 {
			s = a.snap.sessions[0].Name
		}
		if len(words) > 1 {
			s = words[1]
		}
		a.setRoot(sessionsView{})
		if words[0] == "windows" {
			a.push(windowsView{session: s})
		} else {
			a.push(panesView{session: s})
		}
		return
	}
	switch {
	case words[0] == "ext":
		a.push(pluginsView{})
		return
	case words[0] == "config" && len(words) == 1:
		a.push(configView{})
		return
	case words[0] == "logo":
		a.logoOn = !a.logoOn
		a.clear()
		a.say(fmt.Sprintf("logo %s for now; ui.logo in config.yaml keeps it", onOff(a.logoOn)), false)
		return
	case words[0] == "headless":
		a.headless = !a.headless
		a.clear()
		a.say(fmt.Sprintf("header %s for now (ctrl-e); ui.headless in config.yaml keeps it", onOff(!a.headless)), false)
		return
	case a.ps().Views[words[0]] != nil:
		a.openView(a.ps().Views[words[0]], a.context(a.top(), a.cursorRow(a.top())))
		return
	case a.ps().Commands[words[0]] != nil:
		a.runExec(":"+words[0], a.ps().Commands[words[0]].C.Exec, a.context(a.top(), a.cursorRow(a.top())), words[1:])
		return
	}
	if words[0] == "ui" {
		return
	}
	a.handOver(words[0] != "attach", words...)
}

// stCommands is what ":" completes besides the views: stui's own commands.
var stCommands = []string{"ci", "gate", "goal", "hooks", "orch", "plugin", "skills", "status", "stop", "task", "usage", "version", "worker"}

func complete(in string, extra ...string) string {
	if strings.Contains(in, " ") {
		return in
	}
	var names []string
	seen := map[string]bool{}
	for n := range viewNames {
		names = append(names, n)
	}
	names = append(names, "windows", "panes", "help", "quit", "ext", "config", "logo", "headless")
	names = append(names, extra...)
	names = append(names, stCommands...)
	var hits []string
	for _, n := range names {
		if strings.HasPrefix(n, in) && !seen[n] {
			seen[n] = true
			hits = append(hits, n)
		}
	}
	sort.Strings(hits)
	if len(hits) == 0 {
		return in
	}
	// The longest prefix they share; a single hit gets a space after it.
	p := hits[0]
	for _, h := range hits[1:] {
		for !strings.HasPrefix(h, p) {
			p = p[:len(p)-1]
		}
	}
	if len(hits) == 1 {
		return p + " "
	}
	return p
}

// splitWords splits on spaces, keeping "quoted words" whole.
func splitWords(s string) []string {
	var out []string
	var b strings.Builder
	quote := rune(0)
	has := false
	for _, r := range s {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && (r == '"' || r == '\''):
			quote, has = r, true
		case quote == 0 && r == ' ':
			if has {
				out = append(out, b.String())
				b.Reset()
				has = false
			}
		default:
			b.WriteRune(r)
			has = true
		}
	}
	if has {
		out = append(out, b.String())
	}
	return out
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(plain(s)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// -- drawing --

func (a *app) visibleRows(p *page) []row {
	rows := p.v.rows(a)
	if p.filter == "" {
		return rows
	}
	f := strings.ToLower(p.filter)
	var out []row
	for _, r := range rows {
		if strings.Contains(strings.ToLower(strings.Join(r.cells, " ")), f) {
			out = append(out, r)
		}
	}
	return out
}

// headerRows is 6, or 9 for the whole logo and the version under it when
// the terminal is tall enough and the keys leave it room; 0 headless.
func (a *app) headerRows() int {
	if a.h < 16 || a.headless {
		return 0
	}
	if a.logoOn && a.h >= logoTall {
		c1, c2 := headerCols(a.w)
		if _, _, room := a.keys(a.w, 9, c1, c2); room >= Cells(logoRows[0]) {
			return 9
		}
	}
	return 6
}

// headerCols is the widths of the machine's column and the limits' (0
// when there is no room for them).
func headerCols(w int) (c1, c2 int) {
	c1 = min(38, w)
	c2 = min(46, max(0, w-c1-2))
	if c2 < 30 {
		c2 = 0
	}
	return c1, c2
}

const keyColW = 18

// keys is the header's keys -- this view's, then the views' digits -- in
// columns of n rows, and the columns left beside them for the logo: the
// keys come first, and a header without a key column has no logo.
func (a *app) keys(w, n, c1, c2 int) (hints []hint, cols, room int) {
	if p := a.top(); p.v != nil {
		hints = append(hints, p.v.hints()...)
		hints = append(hints, a.pluginKeyHints(p.v)...)
	} else {
		hints = append(hints, hint{"j/k", "scroll"}, hint{"r", "reload"})
	}
	for _, h := range hotkeys {
		hints = append(hints, hint{h.key, h.name})
	}
	hints = append(hints, hint{":", "command"}, hint{"/", "filter"}, hint{"?", "help"})
	c3 := max(0, w-c1-c2-6)
	cols = min(c3/keyColW, (len(hints)+n-1)/max(1, n))
	if cols == 0 {
		return hints, 0, -1
	}
	return hints, cols, c3 - cols*keyColW - 2
}

// bodyRows is how many table rows fit.
func (a *app) bodyRows() int {
	n := a.h - a.headerRows() - 1 - 2 - 1 // footer, frame, column names
	if a.mode != normal {
		n--
	}
	return max(1, n)
}

func (a *app) render() []string {
	lines := a.header(a.w, a.headerRows())
	if a.mode != normal {
		lines = append(lines, a.promptLine())
	}
	body := a.h - len(lines) - 1
	lines = append(lines, a.body(body)...)
	return append(lines, a.footer())
}

func (a *app) promptLine() string {
	switch a.mode {
	case command:
		return cOrange + bold + " :" + reset + cWhite + fit(a.input+"█", a.w-2) + reset
	case filtering:
		return cOrange + bold + " /" + reset + cWhite + fit(a.input+"█", a.w-2) + reset
	case confirming:
		return cRed + bold + fit(" "+a.label+" (y/N)", a.w) + reset
	}
	l := " " + a.label + ": "
	return cOrange + bold + l + reset + cWhite + fit(a.input+"█", a.w-Cells(l)) + reset
}

// header is k9s's top: the machine on the left, the agents' limits in the
// middle, this view's keys on the right.
func (a *app) header(w, n int) []string {
	if n == 0 {
		return nil
	}
	s := a.snap
	label := func(k, v string, width int) string {
		return cOrange + fit(k, 7) + reset + cWhite + fit(v, width-7) + reset
	}
	c1, c2 := headerCols(w)
	left := []string{
		label("OS:", s.version.OS, c1),
		label("Mux:", strings.TrimSpace(s.version.Mux+" "+s.version.MuxVersion), c1),
		label("Panes:", fmt.Sprintf("%d in %d sessions", a.paneCount(), len(s.sessions)), c1),
		label("CI:", a.ciSummary(), c1),
	}
	// The limits go when there is no room for them, then the keys.
	var mid []string
	for _, ag := range s.usage.Agents {
		for _, uw := range ag.Windows {
			name := fit(ag.Agent, 7) + fit(limitName(uw), 8)
			mid = append(mid, cOrange+name+reset+bar(uw.UsedPercent, 10)+cWhite+fit(fmt.Sprintf(" %3.0f%% %s", uw.UsedPercent, resets(uw.ResetsAt)), c2-25)+reset)
		}
	}
	working, waiting, idle := 0, 0, 0
	for _, x := range s.agents.Sessions {
		working, waiting, idle = working+x.Working, waiting+x.Waiting, idle+x.Idle+x.Done
	}
	counts := fmt.Sprintf("%d working %d asking ", working, waiting)
	mid = append(mid, cOrange+fit("Agents:", 8)+reset+cTeal+fmt.Sprintf("%d working ", working)+reset+cOrange+fmt.Sprintf("%d asking ", waiting)+reset+cWhite+fit(fmt.Sprintf("%d idle", idle), max(0, c2-8-Cells(counts)))+reset)
	guard := "off"
	if s.usage.Guard.On {
		guard = "on"
	}
	mid = append(mid, cOrange+fit("Guard:", 8)+reset+cWhite+fit(guard, c2-8)+reset)

	hints, cols, room := a.keys(w, n, c1, c2)
	colW := keyColW
	var logo []string
	logoW := 0
	if a.logoOn {
		logo, logoW = logoLines(a.ver, room, n)
	}
	out := make([]string, n)
	for i := range n {
		line := ""
		if i < len(left) {
			line = left[i]
		} else {
			line = fit("", c1)
		}
		used := c1
		if c2 > 0 {
			if i < len(mid) {
				line += "  " + mid[i]
			} else {
				line += "  " + fit("", c2)
			}
			used += 2 + c2
		}
		if cols > 0 {
			line += "  "
			used += 2
		}
		for c := range cols {
			j := c*n + i
			if j >= len(hints) {
				break
			}
			h := hints[j]
			k := "<" + h.key + ">"
			line += cBlue + k + reset + " " + cGray + fit(h.desc, colW-Cells(k)-2) + " " + reset
			used += colW
		}
		if i < len(logo) {
			line += strings.Repeat(" ", max(0, w-used-logoW)) + logo[i]
			used = w
		}
		out[i] = line + strings.Repeat(" ", max(0, w-used))
	}
	return out
}

func (a *app) paneCount() int {
	n := 0
	for _, ps := range a.snap.panes {
		n += len(ps)
	}
	return n
}

// ciSummary is how many repositories the panes are in, by CI state.
func (a *app) ciSummary() string {
	if len(a.snap.ci) == 0 {
		return "reading…"
	}
	n := map[string]int{}
	repos, _ := ciView{}.repos(a)
	for _, c := range repos {
		if c.Root != "" {
			n[ciState(c.latest())]++
		}
	}
	var parts []string
	for _, k := range []string{"failed", "running", "passed"} {
		if n[k] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n[k], k))
		}
	}
	if len(parts) == 0 {
		return "no runs"
	}
	return strings.Join(parts, ", ")
}

// body is the framed table (or pager) in h lines.
func (a *app) body(h int) []string {
	p := a.top()
	inner := a.w - 2
	frame := func(title, scope string, count int, rows []string) []string {
		t := " " + title + "(" + scope + ")"
		styled := cAqua + bold + " " + title + reset + cAqua + "(" + cPink + scope + cAqua + ")"
		if count >= 0 {
			t += fmt.Sprintf("[%d]", count)
			styled += "[" + cWhite + fmt.Sprint(count) + cAqua + "]"
		}
		if p.filter != "" {
			t += " </" + p.filter + ">"
			styled += cOrange + " </" + p.filter + ">"
		}
		t += " "
		styled += " " + reset
		dash := max(0, inner-Cells(t))
		out := []string{cBlue + "┌" + strings.Repeat("─", dash/2) + reset + styled + cBlue + strings.Repeat("─", dash-dash/2) + "┐" + reset}
		for i := range max(0, h-2) {
			r := strings.Repeat(" ", inner)
			if i < len(rows) {
				r = rows[i]
			}
			out = append(out, cBlue+"│"+reset+r+cBlue+"│"+reset)
		}
		return append(out, cBlue+"└"+strings.Repeat("─", inner)+"┘"+reset)
	}
	if p.pg != nil {
		pg := p.pg
		var rows []string
		for i := pg.top; i < len(pg.lines) && len(rows) < h-2; i++ {
			rows = append(rows, cWhite+fit(pg.lines[i], inner)+reset)
		}
		return frame(pg.title, pg.scope, -1, rows)
	}
	all := a.visibleRows(p)
	// The cursor stays on its row when rows come and go above it.
	if p.sel != "" {
		for i, r := range all {
			if r.id == p.sel {
				p.cursor = i
				break
			}
		}
	}
	p.cursor = max(0, min(p.cursor, len(all)-1))
	if p.cursor < len(all) {
		p.sel = all[p.cursor].id
	}
	n := max(1, h-3)
	if p.cursor < p.top {
		p.top = p.cursor
	}
	if p.cursor >= p.top+n {
		p.top = p.cursor - n + 1
	}
	p.top = max(0, min(p.top, max(0, len(all)-n)))
	hdr := p.v.header()
	ws := widths(hdr, all, inner-2)
	cellLine := func(cells []string) string {
		var b strings.Builder
		b.WriteString(" ")
		for i, w := range ws {
			if i > 0 {
				b.WriteString(" ")
			}
			c := ""
			if i < len(cells) {
				c = cells[i]
			}
			b.WriteString(fit(c, w))
		}
		b.WriteString(" ")
		return b.String()
	}
	rows := []string{cWhite + bold + cellLine(hdr) + reset}
	if len(all) == 0 {
		lines := []string{"nothing to show"}
		if e, ok := p.v.(interface{ empty(*app) []string }); ok && p.filter == "" {
			lines = e.empty(a)
		}
		for _, l := range lines {
			rows = append(rows, cGray+fit("  "+l, inner)+reset)
		}
	}
	for i := p.top; i < len(all) && i < p.top+n; i++ {
		color := all[i].color
		if i == p.cursor {
			color = selected
		}
		rows = append(rows, color+cellLine(all[i].cells)+reset)
	}
	return frame(p.v.title(), p.v.scope(), len(all), rows)
}

// footer is the way here as k9s's crumbs, and the last thing said.
func (a *app) footer() string {
	var b strings.Builder
	used := 0
	for i, p := range a.stack {
		name := ""
		if p.v != nil {
			name = strings.ToLower(p.v.title())
		} else {
			name = strings.ToLower(p.pg.title)
		}
		style := crumbOff
		if i == len(a.stack)-1 {
			style = crumbOn
		}
		b.WriteString(style + " <" + name + "> " + reset + " ")
		used += Cells(name) + 5
	}
	rest := max(0, a.w-used)
	color := cGreen
	if a.bad {
		color = cRed
	}
	msg := a.flash
	if msg == "" && a.snap.err != nil {
		msg, color = "stui's server does not answer: "+a.snap.err.Error(), cRed
	}
	b.WriteString(color + fit(msg, rest) + reset)
	return b.String()
}

func (a *app) helpText() []string {
	out := []string{
		"stui ui: this machine's tmux sessions, windows, panes and agents, their GitHub",
		fmt.Sprintf("Actions and the agents' limits. Everything refreshes every %s, CI every fifth time.", a.conf().RefreshRate),
		"",
		"VIEWS (:name, or the digit)",
	}
	for _, h := range hotkeys {
		out = append(out, fmt.Sprintf("  %-4s :%s", h.key, h.name))
	}
	out = append(out,
		"       :windows [session]   :panes [session]",
		"",
		"MOVING",
		"  j k ↑ ↓     up and down        g G     top and bottom",
		"  ctrl-b ctrl-f  a page          enter   into the row (sessions > windows > panes > output)",
		"  esc         back (or clear the filter)       q   back, quit at the top",
		"  /           filter the rows    ctrl-r  read everything again now, config.yaml too",
		"  ctrl-e      the header on and off",
		"",
		"COMMANDS",
		"  :<view>     switch to a view (tab completes)",
		"  :<stui command>  run it with this terminal, then come back: :worker list,",
		"              :orch ls, :hooks status — whatever stui does. :stui <command> works too",
		"  :ext        the plugins: other CLIs' views and keys (stui ext --help)",
		"  :config     the settings in effect and where each comes from (stui config edit)",
		"  :logo  :headless   the logo, the whole header, on or off for now (ctrl-e: the header)",
		"  :q          quit (or ctrl-c)",
		"",
		"ATTACHING",
		"  a           attach. Outside tmux, prefix d comes back. Inside tmux 3.2+ it opens",
		"              a full-size popup: prefix d closes it and stui ui is where it was.",
		"",
		"STATES",
		"  working (teal), asking (orange: the agent waits for a person), idle, done.",
		"  CI: ● running, ✓ passed, ✗ failed: each workflow's latest run on the branch.",
		"",
	)
	if p := a.top(); p.v != nil {
		out = append(out, strings.ToUpper(p.v.title())+" KEYS")
		for _, h := range p.v.hints() {
			out = append(out, fmt.Sprintf("  %-10s %s", h.key, h.desc))
		}
		out = append(out, "")
	}
	return append(append(out, a.pluginHelp()...), a.configHelp()...)
}
