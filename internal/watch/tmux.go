package watch

// Watching the tmux server: a poll of every pane for what runs in it, and a
// control-mode client per session group for what the panes print.
//
// The control client attaches with -f ignore-size, so it never takes part in
// sizing a window: whatever the user's clients and the phones' views have,
// they keep. It is not read-only, on purpose: a read-only client makes tmux
// refuse send-keys from other clients aimed at that session ("client is
// read-only"), stui's own answers included. It never sends input itself.
//
// %output carries a pane's bytes as the program wrote them, before tmux
// interprets them, with bytes below 0x20 and the backslash written as \ooo
// octal: a BEL is \007 and an OSC 9 is \033]9;text\007. tmux does not pass
// OSC 9/777 on to its own clients, so this is the only place they are seen.

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sophonz-labs/stui/internal/mux"
	"github.com/sophonz-labs/stui/internal/orch"
	"github.com/sophonz-labs/stui/internal/proc"
)

// ExecTmux runs the tmux binary as stui's multiplexer does.
type ExecTmux struct{ Bin string }

func (t ExecTmux) Run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, t.Bin, args...)
	proc.Hide(cmd)
	out, err := cmd.Output()
	return string(out), err
}

const fsep = "|~|"

var paneFields = strings.Join([]string{
	"#{pane_id}", "#{session_name}", "#{session_group}", "#{window_index}", "#{pane_index}",
	"#{pane_current_command}", "#{window_name}", "#{pane_tty}",
	// stui orchestration's pane options, packed into one field (osep).
	strings.Join([]string{"#{@stui-run}", "#{@stui-role}", "#{@stui-parent}", "#{@stui-name}", "#{@stui-done}", "#{@stui-task}", "#{@stui-worktree}", "#{@stui-fan}", "#{@stui-in-run}", "#{@stui-adopted}", "#{@stui-about}", "#{@stui-brief-state}", "#{@stui-brief-at}", "#{@stui-brief}", "#{@stui-summary}"}, osep),
	"#{pane_title}",
}, fsep)

// osep separates the orchestration options inside their field.
const osep = "|^|"

// parseOrch reads the packed orchestration field, or nil when the pane has
// no run.
func parseOrch(s string) *Orch {
	f := strings.SplitN(s, osep, 15)
	if len(f) < 15 || f[0] == "" {
		return nil
	}
	at, _ := strconv.ParseInt(f[12], 10, 64)
	return &Orch{Run: f[0], Role: f[1], Parent: f[2], Name: f[3], Done: f[4] == "1", Task: f[5], Worktree: f[6], Fan: f[7], InRun: f[8],
		Adopted: f[9] == "1", About: f[10], BriefState: f[11], BriefAt: at, Brief: orch.BriefText(f[13]), Summary: f[14]}
}

var versionName = regexp.MustCompile(`^\d+\.\d+\.\d+`)

// AgentFor is the agent a pane's current command is, or "".
func AgentFor(command string) string {
	c := strings.ToLower(strings.TrimSuffix(command, ".exe"))
	switch {
	case c == "claude":
		return "claude"
	// Claude Code's native build names its process for its version.
	case versionName.MatchString(c):
		return "claude"
	case c == "codex" || strings.HasPrefix(c, "codex-"):
		return "codex"
	case c == "gemini":
		return "gemini"
	case c == "opencode":
		return "opencode"
	// Kimi Code sets its process title to kimi-code (where tmux reads the
	// title; elsewhere a Node CLI shows as node, and its hooks say which).
	case c == "kimi" || c == "kimi-code":
		return "kimi"
	// Qwen Code sets its process title only on Windows: on macOS and Linux
	// tmux may show a Qwen pane as node, which is no agent here. Its hooks
	// still name it (SessionStart at launch), and adoption goes by them; stui
	// worker start knows what it started. A Qwen pane "stui cannot adopt" is a
	// pane whose hooks are not installed, not this switch.
	case c == "qwen":
		return "qwen"
	// Pi is a Node CLI too: tmux showed its pane as node on macOS (Pi
	// 1.4.0), where its SessionStart names it.
	case c == "pi":
		return "pi"
	}
	return ""
}

var shells = map[string]bool{
	"zsh": true, "bash": true, "fish": true, "sh": true, "dash": true, "ksh": true,
	"tcsh": true, "csh": true, "nu": true, "pwsh": true, "powershell": true, "login": true, "xonsh": true, "elvish": true,
}

func isShell(command string) bool { return shells[strings.TrimPrefix(strings.ToLower(command), "-")] }

type paneRow struct {
	id, session, group string
	window, pane       int
	command, wname     string
	tty, title         string
	orch               string
}

func parsePanes(out string) []paneRow {
	var rows []paneRow
	groups := map[string][]string{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(line, fsep, 10)
		if len(f) < 8 || !strings.HasPrefix(f[0], "%") {
			continue
		}
		w, _ := strconv.Atoi(f[3])
		p, _ := strconv.Atoi(f[4])
		r := paneRow{id: f[0], session: f[1], group: f[2], window: w, pane: p, command: f[5], wname: f[6], title: f[7]}
		switch len(f) {
		case 9:
			r.tty, r.title = f[7], f[8]
		case 10:
			r.tty, r.orch, r.title = f[7], f[8], f[9]
		}
		rows = append(rows, r)
		if f[2] != "" {
			groups[f[2]] = append(groups[f[2]], f[1])
		}
	}
	// A window linked into several sessions (a group, a phone's view) is
	// listed once per session: keep the pane once, under a real session.
	best := map[string]paneRow{}
	var order []string
	for _, r := range rows {
		view := mux.IsInternalView(r.session, r.group, groups[r.group])
		cur, seen := best[r.id]
		if !seen {
			order = append(order, r.id)
		}
		if !seen || (mux.IsInternalView(cur.session, cur.group, groups[cur.group]) && !view) {
			best[r.id] = r
		}
	}
	out2 := make([]paneRow, 0, len(order))
	for _, id := range order {
		out2 = append(out2, best[id])
	}
	return out2
}

// watchTargets is one session per session group to attach a control client
// to: the group's windows are all its members', so one sees them all.
// Phones' views are never picked.
func watchTargets(out string) []string {
	type row struct{ name, group string }
	var rows []row
	groups := map[string][]string{}
	for _, line := range strings.Split(out, "\n") {
		name, group, ok := strings.Cut(line, fsep)
		if !ok || name == "" {
			continue
		}
		rows = append(rows, row{name, group})
		if group != "" {
			groups[group] = append(groups[group], name)
		}
	}
	picked := map[string]string{}
	for _, r := range rows {
		if mux.IsInternalView(r.name, r.group, groups[r.group]) {
			continue
		}
		key := r.group
		if key == "" {
			key = "=" + r.name
		}
		if cur, ok := picked[key]; !ok || r.name < cur {
			picked[key] = r.name
		}
	}
	var targets []string
	for _, n := range picked {
		targets = append(targets, n)
	}
	sort.Strings(targets)
	return targets
}

// poll reads every pane and brings the model up to date.
func (w *Watcher) poll(ctx context.Context) (targets []string) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var rows []paneRow
	var listErr error
	if w.opts.Psmux {
		// No control clients on psmux (psmux.go): no targets.
		var out string
		out, listErr = w.opts.Tmux.Run(ctx, "list-panes", "-a", "-F", psmuxPaneFields)
		if listErr == nil {
			var servers map[string]string
			rows, servers = parsePsmuxPanes(out)
			w.mu.Lock()
			w.psmuxServers = servers
			w.mu.Unlock()
		}
	} else {
		out, err := w.opts.Tmux.Run(ctx, "list-panes", "-a", "-F", paneFields)
		if listErr = err; err != nil {
			out = ""
		}
		rows = parsePanes(out)
		if s, err := w.opts.Tmux.Run(ctx, "list-sessions", "-F", "#{session_name}"+fsep+"#{session_group}"); err == nil {
			targets = watchTargets(s)
		}
	}

	// Teammates' command lines and the team configs, read before taking
	// the lock.
	w.mu.Lock()
	now := w.now()
	var dirs []string
	for d := range w.claudeDirs {
		dirs = append(dirs, d)
	}
	var askArgs, askStart []paneRow
	for _, r := range rows {
		if needArgs(w.panes[r.id], r.command, r.tty, now) {
			askArgs = append(askArgs, r)
		}
		if w.restore[r.id] != nil && r.tty != "" && !isShell(r.command) {
			askStart = append(askStart, r)
		}
	}
	w.mu.Unlock()
	gotArgs := map[string]*teamArgs{}
	for _, r := range askArgs {
		gotArgs[r.id] = parseTeamArgs(w.opts.Args(r.tty))
	}
	// When each pane with a saved record started its foreground process.
	starts := map[string]time.Time{}
	for _, r := range askStart {
		if t, ok := w.opts.ProcStart(r.tty); ok {
			starts[r.id] = t
		}
	}
	teams := loadTeams(dirs)

	var captures []string
	w.mu.Lock()
	now = w.now()
	seen := map[string]bool{}
	// Who directs how many, for the teams' leads (markOrchTeams), before
	// any pane is published.
	orchs := make([]*Pane, len(rows))
	for i, r := range rows {
		orchs[i] = &Pane{ID: r.id, Orch: parseOrch(r.orch)}
	}
	markOrchTeams(orchs)
	for i, r := range rows {
		seen[r.id] = true
		p := w.panes[r.id]
		fresh := p == nil
		if fresh {
			p = &paneState{Pane: Pane{ID: r.id, State: Unknown, Since: now.UnixMilli(), Source: "screen"}}
			w.panes[r.id] = p
			// An open gate from before stui restarted is its prompt again.
			p.gate = w.openGateLocked(r.id)
		}
		before := p.view(now)
		p.Session, p.Window, p.WindowName, p.Pane.Pane = r.session, r.window, r.wname, r.pane
		p.Command, p.Title, p.tty = r.command, r.title, r.tty
		if p.reported != nil && p.reportCmd != r.command {
			// The program that reported is gone: hooks and the screen
			// decide again.
			p.reported = nil
		}
		p.Orch = orchs[i].Orch
		p.cmdAgent = AgentFor(r.command)
		restored := false
		if s := w.restore[r.id]; s != nil {
			delete(w.restore, r.id)
			t, ok := starts[r.id]
			if w.keepRestore(s, r, t, ok) {
				w.applyRestoreLocked(p, s)
				restored = true
			}
		}
		if a, ok := gotArgs[r.id]; ok {
			p.teamArgs, p.argsKey, p.argsAt = a, r.command+"|"+r.tty, now
		} else if p.cmdAgent != "claude" {
			p.teamArgs, p.argsKey = nil, ""
		}
		if p.agentFromHook && isShell(r.command) {
			// The agent exited without saying so. Its subagents went with
			// it: those never heard from past their start do not age out.
			p.agentFromHook = false
			p.Subagents.Lost += len(p.running)
			for id := range p.running {
				p.subEndLocked(id, SubLost, now)
			}
			p.running, p.spawns, p.Subagents.Running = nil, nil, 0
			p.delegated, p.delegatedSent, p.turnLost, p.heldDel = false, false, 0, nil
			w.endPromptLocked(p, Expired)
			p.turnStart = time.Time{}
			p.setState(Unknown, "screen", now)
		}
		if !p.agentFromHook {
			if p.Agent != p.cmdAgent {
				if p.cmdAgent == "" {
					w.endPromptLocked(p, Expired)
					p.turnStart = time.Time{}
					p.setState(Unknown, "screen", now)
				} else if p.State == Unknown {
					p.setState(Idle, "screen", now)
				}
			}
			p.Agent = p.cmdAgent
		}
		if isShell(r.command) {
			p.notified = false
		}
		after := p.view(now)
		if fresh || restored || !samePane(before, after) {
			w.publishLocked(p)
		}
		// Screens are read when a pane prints; one that went quiet
		// mid-turn or mid-question is read here now and then too.
		if p.Agent != "" && (p.State == Working || p.State == Waiting || p.State == Unknown) && now.Sub(p.lastCapture) > 3*time.Second {
			captures = append(captures, p.ID)
		}
	}
	for id, p := range w.panes {
		if seen[id] {
			continue
		}
		// A pane only a hook knows of, not listed yet: give it a moment.
		if p.Session == "" && now.Sub(time.UnixMilli(p.Since)) < 10*time.Second {
			continue
		}
		w.endPromptLocked(p, Expired)
		p.Agent, p.notified, p.prompt = "", false, nil
		w.publishLocked(p)
		if p.timer != nil {
			p.timer.Stop()
		}
		delete(w.panes, id)
	}
	if listErr == nil {
		// Every saved record's pane is accounted for: one not listed is gone.
		if len(w.restore) > 0 {
			w.restore = nil
			w.markDirtyLocked()
		}
	}
	for _, p := range w.assignTeamsLocked(teams) {
		w.publishLocked(p)
	}
	w.mu.Unlock()
	for _, id := range captures {
		w.captureNow(id)
	}
	return targets
}

func samePane(a, b Pane) bool {
	return a.Session == b.Session && a.Window == b.Window && a.WindowName == b.WindowName && a.Pane == b.Pane &&
		a.Command == b.Command && a.Title == b.Title && a.Agent == b.Agent && a.State == b.State &&
		(a.Prompt == nil) == (b.Prompt == nil)
}

// ---- control clients

type ctlClient struct {
	session string
	stop    context.CancelFunc
	done    chan struct{}
}

// unescape reverses %output's escaping: \ooo octal, and \\.
func unescape(s string) []byte {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && isOct(s, i+1) {
			n, _ := strconv.ParseUint(s[i+1:i+4], 8, 8)
			out = append(out, byte(n))
			i += 3
			continue
		}
		if c == '\\' && i+1 < len(s) && s[i+1] == '\\' {
			out = append(out, '\\')
			i++
			continue
		}
		out = append(out, c)
	}
	return out
}

func isOct(s string, i int) bool {
	if i+3 > len(s) {
		return false
	}
	for j := i; j < i+3; j++ {
		if s[j] < '0' || s[j] > '7' {
			return false
		}
	}
	return true
}

// controlLine handles one line from a control client.
func (w *Watcher) controlLine(line string) {
	switch {
	case strings.HasPrefix(line, "%output "):
		rest := line[len("%output "):]
		id, data, ok := strings.Cut(rest, " ")
		if ok {
			w.Output(id, unescape(data))
		}
	case strings.HasPrefix(line, "%extended-output "):
		// %extended-output %id age ... : data
		rest := line[len("%extended-output "):]
		id, rest, _ := strings.Cut(rest, " ")
		if _, data, ok := strings.Cut(rest, " : "); ok {
			w.Output(id, unescape(data))
		}
	case strings.HasPrefix(line, "%sessions-changed"), strings.HasPrefix(line, "%session-renamed"),
		strings.HasPrefix(line, "%window-add"), strings.HasPrefix(line, "%window-close"),
		strings.HasPrefix(line, "%unlinked-window-close"), strings.HasPrefix(line, "%layout-change"):
		w.askPoll()
	}
}

// runControl keeps one control client attached to session until ctx ends
// or the session goes. It reports false if tmux refused the client itself
// (a tmux before 3.2 has no -f), which is not worth retrying.
func (w *Watcher) runControl(ctx context.Context, bin, session string) bool {
	cmd := exec.CommandContext(ctx, bin, "-C", "attach-session", "-f", "ignore-size", "-t", "="+session)
	proc.Hide(cmd)
	// Gone when the agent is, however it goes. A control client left
	// behind holds its server: told to stop, the server waits for its
	// clients, which never answer, and keeps its socket and every pane.
	// Agents killed in tests and in use leave such clients behind, with
	// init as their parent.
	proc.DieWithParent(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return true
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return true
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return true
	}
	proc.GuardChild(cmd.Process.Pid)
	// Closing stdin detaches the client; that is the way out.
	go func() {
		<-ctx.Done()
		stdin.Close()
	}()
	r := bufio.NewReaderSize(stdout, 64<<10)
	lines := 0
	for {
		line, err := r.ReadString('\n')
		if len(line) > 0 {
			lines++
			w.controlLine(strings.TrimRight(line, "\r\n"))
		}
		if err != nil {
			break
		}
	}
	cmd.Wait()
	stdin.Close()
	if lines == 0 && strings.Contains(stderr.String(), "unknown flag") {
		return false
	}
	return true
}

// Run watches until ctx ends: bin is the tmux binary.
func (w *Watcher) Run(ctx context.Context, bin string) {
	clients := map[string]*ctlClient{}
	var cmu sync.Mutex
	supported := bin != ""
	var warned sync.Once
	reconcile := func(targets []string) {
		if !supported {
			return
		}
		cmu.Lock()
		defer cmu.Unlock()
		want := map[string]bool{}
		for _, t := range targets {
			want[t] = true
			if c := clients[t]; c != nil {
				select {
				case <-c.done:
				default:
					continue
				}
			}
			cctx, cancel := context.WithCancel(ctx)
			c := &ctlClient{session: t, stop: cancel, done: make(chan struct{})}
			clients[t] = c
			go func() {
				defer close(c.done)
				start := time.Now()
				if !w.runControl(cctx, bin, t) {
					warned.Do(func() {
						fmt.Fprintln(os.Stderr, "  agent watch: this tmux has no control-mode -f ignore-size (tmux 3.2+); reading screens only")
					})
					cmu.Lock()
					supported = false
					cmu.Unlock()
				}
				// One that ends at once is not restarted in a tight loop.
				if time.Since(start) < 2*time.Second {
					select {
					case <-cctx.Done():
					case <-time.After(2 * time.Second):
					}
				}
			}()
		}
		for t, c := range clients {
			if !want[t] {
				c.stop()
				delete(clients, t)
			}
		}
	}

	go w.saveLoop(ctx.Done())
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	replay := time.After(spoolSettle)
	var dropAt time.Time
	for {
		reconcile(w.poll(ctx))
		w.tick()
		select {
		case <-replay:
			// The panes are known: what no agent heard can go to them now.
			replay = nil
			w.ReplaySpool()
		default:
		}
		if time.Since(dropAt) >= droppedEvery {
			dropAt = time.Now()
			w.TakeDropped()
		}
		select {
		case <-ctx.Done():
			cmu.Lock()
			for _, c := range clients {
				c.stop()
			}
			cmu.Unlock()
			return
		case <-tick.C:
		case <-w.pollNow:
			// Let a burst of changes settle.
			time.Sleep(100 * time.Millisecond)
		}
	}
}
