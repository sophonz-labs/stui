package mux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ClientSep separates a base session from a per-client view.
//
// Every browser gets its OWN session in the base session's group, not one
// shared clone. Two clients on a single tmux session are clamped to the
// smaller of the two; group members keep an independent current window.
const ClientSep = "__"

// ViewNameFor is the per-client view of base.
func ViewNameFor(base, client string) string {
	if !ValidClient(client) {
		client = "anon"
	}
	return base + ClientSep + client
}

// IsInternalView is a clone this tool made for one client rather than a
// session to open: <base>__<client>, where base is a session of the same
// group -- the group's own name, or any member of it, since tmux keeps a
// group's name when its first session is renamed, and a view made after the
// rename is named for the new one. Matched against the group only, so a
// session legitimately named "foo__bar" or "foo-mobile" is not hidden;
// `-mobile` is recognised because that is what earlier versions called them.
//
// peers are the names of every session in name's group; name itself among
// them is ignored.
func IsInternalView(name, group string, peers []string) bool {
	if group == "" {
		return false
	}
	isViewOf := func(base string) bool {
		if base == "" || base == name {
			return false
		}
		if name == base+"-mobile" {
			return true
		}
		client, ok := strings.CutPrefix(name, base+ClientSep)
		return ok && ValidClient(client)
	}
	if isViewOf(group) {
		return true
	}
	for _, p := range peers {
		if isViewOf(p) {
			return true
		}
	}
	return false
}

// sep is the field separator for every -F format here. Not a tab: tmux 3.4
// rewrites tabs in format output to underscores. A pipe survives both, and
// can appear in a window name, so that field comes last and the split is
// bounded.
const sep = "|"

// tmux does not always answer. While a command-prompt is open on the client
// -- the (repeat) one copy-mode-vi opens on a digit -- display-message
// blocks indefinitely, and every scroll queued behind it stalls.
var queryTimeout = 1500 * time.Millisecond

// Tmux drives tmux, or anything that speaks its command language.
//
// grouped is tmux's session groups: each browser gets its own session in the
// base session's group, sharing its windows but not its current window.
// psmux runs one server per session and so has no such groups; there every
// client attaches to the session itself, as with zellij.
type Tmux struct {
	bin      string
	id       string
	grouped  bool
	startDir string // STUI_START_DIR: where `stui` was run from, unix tmux only
}

// unixPaneCommand sends a pane that would open in a directory that is gone
// to $HOME instead. Two ways to get there: a tmux server whose own cwd was
// deleted puts every pane in it, -c or not; and a session whose start
// directory was deleted gets "/" from tmux for each new window. An external
// pwd fails in the first (the shell's builtin does not) and prints "/" in the
// second, and either leaves grep nothing to match, so `cd` goes home.
// Plain enough for zsh, bash, sh and fish 3 to all run it unmodified.
const unixPaneCommand = `env pwd -P 2>/dev/null | grep -qvx / || cd; exec "$SHELL" -l`

// NewTmux drives the machine's own tmux first. A client and server of
// different versions refuse each other, so a carried one could not attach to
// the session someone's own tmux is already running. A compiled stui names its
// carried copy for the machine that has none.
//
// Never on Windows: any tmux there is Cygwin's or MSYS2's, which cannot
// drive a Windows console program and hangs waiting on a pty it never gets.
func NewTmux() *Tmux {
	t := &Tmux{id: "tmux", grouped: true}
	if runtime.GOOS == "windows" {
		return t
	}
	t.bin = "tmux"
	p, skipped := lookTmux(os.Getenv("PATH"), os.Getenv(orcaShimEnv))
	if p != "" {
		t.bin = p
	} else if p := os.Getenv("STUI_TMUX_BIN"); p != "" {
		t.bin = p
	} else if skipped {
		// Only a stand-in is on PATH: no tmux, so auto goes on to zellij.
		t.bin = ""
	}
	t.startDir = os.Getenv("STUI_START_DIR")
	ensureTmpdir()
	return t
}

// orcaShimEnv names the folder Orca puts first on PATH for Claude Code's
// agent teams. Its `tmux` is not tmux: it answers `-V` with "tmux 3.4", does
// eleven commands itself, says 0 to fourteen more without doing them
// (has-session among them) and refuses the rest, new-session included. An stui
// started under it took it for tmux, and the session it said it shared was
// never made.
const orcaShimEnv = "ORCA_AGENT_TEAMS_SHIM_DIR"

// lookTmux is exec.LookPath("tmux") that passes over Orca's stand-in: the
// folder in $ORCA_AGENT_TEAMS_SHIM_DIR, or Orca's default one
// (~/.orca/claude-agent-teams-bin) when the variable is gone but the PATH
// kept it. skipped says a stand-in was passed over.
func lookTmux(path, shimDir string) (bin string, skipped bool) {
	shim := func(dir string) bool {
		d := filepath.Clean(dir)
		if shimDir != "" && d == filepath.Clean(shimDir) {
			return true
		}
		return filepath.Base(d) == "claude-agent-teams-bin" && filepath.Base(filepath.Dir(d)) == ".orca"
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, "tmux")
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
			continue
		}
		if shim(dir) {
			skipped = true
			continue
		}
		return p, skipped
	}
	return "", skipped
}

// notACommand is tmux (or something on PATH under its name) refusing the
// command itself, as opposed to refusing to do it: "unknown command: x" from
// tmux, "unsupported command: x" from Orca's stand-in.
func notACommand(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "unknown command") || strings.Contains(s, "unsupported command")
}

// ensureTmpdir makes $TMUX_TMPDIR when it is set and missing. tmux 3.7
// does not stop at a TMUX_TMPDIR that does not exist: it uses /tmp, the
// default server, holding the user's sessions. An stui started for a test with
// TMUX_TMPDIR pointing at a directory nobody made shared those sessions,
// and that test's cleanup would stop them all. Made here, the
// directory is where everything this stui starts in tmux goes.
func ensureTmpdir() {
	if d := os.Getenv("TMUX_TMPDIR"); d != "" {
		if _, err := os.Stat(d); os.IsNotExist(err) {
			os.MkdirAll(d, 0o700)
		}
	}
}

// NewPsmux is tmux for Windows (github.com/psmux/psmux): native, on ConPTY,
// speaking tmux's command language. The machine's own comes first, for the
// same reason as tmux's -- a client and server of different versions -- and
// the copy stui carries is for the machine that has none.
func NewPsmux() *Tmux {
	t := &Tmux{id: "psmux", bin: "psmux"}
	if runtime.GOOS != "windows" {
		t.bin = ""
		return t
	}
	if p, err := exec.LookPath("psmux"); err == nil {
		t.bin = p
	} else if p := os.Getenv("STUI_PSMUX_BIN"); p != "" {
		t.bin = p
	}
	return t
}

func (t *Tmux) ID() string    { return t.id }
func (t *Tmux) Label() string { return t.id }

// Actions is everything, except that without a view of its own a client
// cannot detach alone: detach-client -s would take every browser with it.
func (t *Tmux) Actions() []Action {
	if t.grouped {
		return allActions
	}
	var out []Action
	for _, a := range allActions {
		if a != Detach {
			out = append(out, a)
		}
	}
	return out
}
func (t *Tmux) Persistent() bool            { return true }
func (t *Tmux) IndependentClientSize() bool { return t.grouped }
func (t *Tmux) Command() string             { return t.bin }
func (t *Tmux) Available() bool             { return installed(t.bin) }

func (t *Tmux) sh(ctx context.Context, args ...string) (string, bool) {
	return sh(ctx, t.bin, args...)
}

func (t *Tmux) ask(ctx context.Context, target, format string) (string, bool) {
	out, ok := run(ctx, queryTimeout, t.bin, "display-message", "-p", "-t", target, format)
	out = strings.TrimSpace(out)
	if !ok || out == "" {
		return "", false
	}
	return out, true
}

// reapViews drops per-client views nothing is attached to any more. Every
// private window and every cleared localStorage leaves another behind. The
// windows belong to the base session, so a view holds nothing but which
// window its client was on; the delay keeps a phone's brief disconnects from
// losing that.
func (t *Tmux) reapViews(ctx context.Context, idle time.Duration) {
	out, ok := t.sh(ctx, "list-sessions", "-F", "#{session_name}"+sep+"#{session_attached}"+sep+"#{session_activity}")
	if !ok {
		return
	}
	now := time.Now().Unix()
	for _, line := range lines(out) {
		f := strings.Split(line, sep)
		if len(f) < 3 || !strings.Contains(f[0], ClientSep) {
			continue
		}
		if n, _ := strconv.Atoi(f[1]); n > 0 {
			continue
		}
		if act, _ := strconv.ParseInt(f[2], 10, 64); now-act < int64(idle.Seconds()) {
			continue
		}
		t.sh(ctx, "kill-session", "-t", "="+f[0])
	}
}

var sessionFields = strings.Join([]string{
	"#{session_name}", "#{session_group}", "#{session_windows}",
	"#{session_attached}", "#{session_created}",
	"#{pane_current_command}", "#{pane_id}", "#{pane_tty}", "#{window_name}",
}, sep)

// PanesBySession is every live pane's id, by the session it belongs to.
//
// One call for the whole server rather than one per session: a list is drawn
// often and `list-panes -a` costs the same whether there is one session or
// twenty.
func (t *Tmux) PanesBySession(ctx context.Context) map[string][]string {
	out, ok := t.sh(ctx, "list-panes", "-a", "-F", "#{session_name}"+sep+"#{pane_id}")
	if !ok {
		return nil
	}
	by := map[string][]string{}
	for _, line := range lines(out) {
		f := strings.Split(strings.TrimSpace(line), sep)
		if len(f) < 2 || f[0] == "" || f[1] == "" {
			continue
		}
		by[f[0]] = append(by[f[0]], t.paneOut(f[0], f[1]))
	}
	return by
}

func (t *Tmux) ListSessions(ctx context.Context) []SessionInfo {
	// Only views are reaped, and only grouped sessions have them -- psmux's
	// session_activity is also the creation time, which would reap anything.
	if t.grouped {
		t.reapViews(ctx, 600*time.Second)
	}
	out, ok := t.sh(ctx, "list-sessions", "-F", sessionFields)
	if !ok {
		return []SessionInfo{} // no server running
	}
	list := parseSessions(out)
	for i := range list {
		list[i].Pane = t.paneOut(list[i].Name, list[i].Pane)
	}
	// Agent watch's control clients are not anybody looking at a session.
	if clients, ok := t.sh(ctx, "list-clients", "-F", "#{client_flags}"+sep+"#{session_name}"); ok {
		watchers := WatcherClients(clients)
		for i := range list {
			list[i].Attached = max(0, list[i].Attached-watchers[list[i].Name])
		}
	}
	return list
}

// WatcherClients counts, per session, the clients agent watch attached:
// control mode, ignoring size. iTerm2's tmux integration is control mode
// too, but a person, and does not ignore size.
func WatcherClients(out string) map[string]int {
	n := map[string]int{}
	for _, line := range lines(out) {
		flags, session, ok := strings.Cut(line, sep)
		if !ok {
			continue
		}
		f := "," + flags + ","
		if strings.Contains(f, ",control-mode,") && strings.Contains(f, ",ignore-size,") {
			n[session]++
		}
	}
	return n
}

func parseSessions(out string) []SessionInfo {
	var rows [][]string
	groups := map[string][]string{}
	for _, line := range lines(out) {
		// The window name may contain the separator, and it is last, so
		// everything past the eighth split belongs to it.
		f := strings.SplitN(line, sep, 9)
		for len(f) < 9 {
			f = append(f, "")
		}
		rows = append(rows, f)
		if f[1] != "" {
			groups[f[1]] = append(groups[f[1]], f[0])
		}
	}
	list := []SessionInfo{}
	for _, f := range rows {
		if IsInternalView(f[0], f[1], groups[f[1]]) {
			continue
		}
		windows, _ := strconv.Atoi(f[2])
		attached, _ := strconv.Atoi(f[3])
		created, _ := strconv.ParseInt(f[4], 10, 64)
		si := SessionInfo{
			Name: f[0], Windows: windows, Attached: attached, Created: created, CurrentWindow: f[8],
			Pane: f[6], proc: f[5],
		}
		m := resolve(f[5], f[7])
		si.Icon, si.Command = m.Icon, m.Command
		list = append(list, si)
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := strings.ToLower(list[i].Name), strings.ToLower(list[j].Name)
		if a != b {
			return a < b
		}
		return list[i].Name < list[j].Name
	})
	return list
}

func (t *Tmux) HasSession(ctx context.Context, name string) bool {
	_, ok := t.sh(ctx, "has-session", "-t", "="+name)
	return ok
}

// CreateSession makes sure it exists. Not has-session first: an answer of
// non-zero for any reason at all sent a following new-session into a
// duplicate-name error, so new-session is simply tried and a failure is read
// as "already there" -- which is also why this cannot use -A: -A attaches to
// whatever pane is already running rather than making a fresh one, and the
// guard below (see unixPaneCommand) only matters for a pane this call is the
// one starting.
func (t *Tmux) CreateSession(ctx context.Context, name string) error {
	if !ValidName(name) {
		return fmt.Errorf("invalid session name: %s", name)
	}
	args := []string{"new-session", "-d", "-s", name}
	// Not psmux: the guard is a POSIX shell line, and Windows panes are not
	// stranded this way.
	unix := runtime.GOOS != "windows"
	if unix {
		if t.startDir != "" {
			if fi, err := os.Stat(t.startDir); err == nil && fi.IsDir() {
				args = append(args, "-c", t.startDir)
			}
		}
		args = append(args, unixPaneCommand)
	}
	_, stderr, created := shErr(ctx, t.bin, args...)
	if !created && notACommand(stderr) {
		// Not "already there": this program does not make sessions at all.
		return fmt.Errorf("%s cannot make a session (%s) -- try --mux zellij", t.bin, strings.TrimSpace(stderr))
	}
	if created && unix {
		// Only when this call is the one that made the session, and only
		// when nothing (a tmux.conf) already customises default-command --
		// so later windows and splits, prefix+c included, get the same
		// guard, without overriding a user's own choice.
		if v, _ := t.sh(ctx, "show-options", "-gv", "default-command"); strings.TrimSpace(v) == "" {
			t.sh(ctx, "set-option", "-t", name, "default-command", unixPaneCommand)
		}
	}
	// Every time, not only on the ones created here: a session that was
	// already there would otherwise keep whatever sizing this tmux defaults to.
	t.sizing(ctx)
	t.hyperlinks(ctx)
	t.clipboard(ctx)
	return nil
}

// sizing pins how tmux sizes windows. tmux 3.4 (Ubuntu's) defaults to
// `latest`, 3.7 to `manual`. A window in a session group has one size for
// everyone; aggressive-resize is what lets clients on *different* windows
// get their own. Two on the same window cannot, and `latest` is then the
// kinder answer -- everyone sees whole lines.
func (t *Tmux) sizing(ctx context.Context) {
	t.sh(ctx, "set-option", "-g", "window-size", "latest")
	t.sh(ctx, "set-window-option", "-g", "aggressive-resize", "on")
}

// hyperlinks turns on OSC 8 forwarding for stui's own clients. tmux 3.4+ only
// forwards a hyperlink to a client whose terminal has the "hyperlinks"
// feature, and ttyd reports xterm-256color (its own default, unset by
// spawnTtyd) for every client it spawns.
//
// `-a` appends to the array rather than replacing it, so a pattern the
// user's own tmux.conf already set for xterm* (colour, clipboard, whatever)
// keeps everything it had; this only adds one feature to it. Skipped once
// "hyperlinks" already appears anywhere in the list -- by us on an earlier
// call (CreateSession runs this every time, not only when it makes the
// session) or by the user's own config -- so it is never applied twice.
func (t *Tmux) hyperlinks(ctx context.Context) {
	if v, _ := t.sh(ctx, "show-options", "-s", "terminal-features"); strings.Contains(v, "hyperlinks") {
		return
	}
	t.sh(ctx, "set-option", "-as", "terminal-features", ",xterm*:hyperlinks")
}

// clipboard lets a program inside tmux put something on the clipboard of
// whoever is watching it, with OSC 52.
//
// set-clipboard decides what tmux does with the sequence an application
// writes, and its default -- `external` -- throws it away: external means
// tmux sends its *own* copies out to the terminal and ignores the
// application's. Only `on` accepts the application's and passes it on. So a
// program that copies (Claude Code, vim's clipboard register, anything using
// OSC 52) said it had copied, tmux swallowed it, and the browser's clipboard
// kept whatever was in it from before. That is worse than not offering to
// copy at all, because it looks as though it worked.
//
// The terminal feature is not the missing piece: `clipboard` is already in
// what tmux works out for every terminal stui serves. Measured, not assumed --
// with `on` the sequence comes out of the client's pty, with `external` and
// `off` it does not.
//
// Left alone when it is not still at the default: `off` is a thing somebody
// chooses on purpose, and stui has no business undoing it.
func (t *Tmux) clipboard(ctx context.Context) {
	if v, _ := t.sh(ctx, "show-options", "-sv", "set-clipboard"); strings.TrimSpace(v) != "external" {
		return
	}
	t.sh(ctx, "set-option", "-s", "set-clipboard", "on")
}

// KillSession takes every client's view with it, or they survive as orphans
// in an empty group and keep showing up as attached.
func (t *Tmux) KillSession(ctx context.Context, name string) error {
	if !ValidName(name) {
		return fmt.Errorf("invalid session name: %s", name)
	}
	out, _ := t.sh(ctx, "list-sessions", "-F", "#{session_name}")
	for _, s := range lines(out) {
		s = strings.TrimSpace(s)
		if s == name || strings.HasPrefix(s, name+ClientSep) {
			t.sh(ctx, "kill-session", "-t", "="+s)
		}
	}
	return nil
}

// AttachArgs gives the browser its own session inside the base session's
// group: a shared window list, an independent current window.
func (t *Tmux) AttachArgs(session, client string) []string {
	if !t.grouped {
		return []string{"new-session", "-A", "-s", session}
	}
	return []string{"new-session", "-A", "-s", ViewNameFor(session, client), "-t", session}
}

// SwitchArgs is AttachArgs for a terminal that is already a client of this
// server: tmux refuses to nest one client inside another, so that client is
// moved instead. Every command but the last may fail -- the view it makes
// can already be there.
func (t *Tmux) SwitchArgs(session, client string) [][]string {
	if !t.grouped {
		return [][]string{{"switch-client", "-t", "=" + session}}
	}
	view := ViewNameFor(session, client)
	return [][]string{
		{"new-session", "-d", "-s", view, "-t", session},
		{"switch-client", "-t", "=" + view},
	}
}

// ServerSocket is the socket of the server this process's tmux commands
// reach: $TMUX's when it has one, as tmux itself decides. OutsideSocket is
// the one reached from outside any tmux. Either is empty when no server
// answers.
func (t *Tmux) ServerSocket(ctx context.Context) string {
	return t.socket(ctx, os.Environ())
}

func (t *Tmux) OutsideSocket(ctx context.Context) string {
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "TMUX=") && !strings.HasPrefix(e, "TMUX_PANE=") {
			env = append(env, e)
		}
	}
	return t.socket(ctx, env)
}

func (t *Tmux) socket(ctx context.Context, env []string) string {
	if t.bin == "" {
		return ""
	}
	// list-sessions, not display-message: it needs no current pane, and the
	// pane an inherited $TMUX_PANE names may be long gone.
	out, ok := runEnv(ctx, queryTimeout, env, t.bin, "list-sessions", "-F", "#{socket_path}")
	if !ok {
		return ""
	}
	if l := lines(out); len(l) > 0 {
		return strings.TrimSpace(l[0])
	}
	return ""
}

// inCopyMode is (false, false) when tmux would not answer. Reading "no
// answer" as "not in copy mode" is what once left scrolling stuck for good.
func (t *Tmux) inCopyMode(ctx context.Context, target string) (in, known bool) {
	out, ok := t.ask(ctx, target, "#{pane_in_mode}")
	return out == "1", ok
}

func (t *Tmux) scrollPosition(ctx context.Context, target string) (int, bool) {
	out, ok := t.ask(ctx, target, "#{scroll_position}")
	if !ok {
		return 0, false
	}
	n, _ := strconv.Atoi(out)
	return n, true
}

// settledPosition waits for send-keys, which queues and returns, to stop
// moving the position before anyone decides the bottom has been reached.
func (t *Tmux) settledPosition(ctx context.Context, target string) (int, bool) {
	last, lastOK := t.scrollPosition(ctx, target)
	for i := 0; i < 8; i++ {
		time.Sleep(40 * time.Millisecond)
		now, ok := t.scrollPosition(ctx, target)
		if now == last && ok == lastOK {
			return now, ok
		}
		last, lastOK = now, ok
	}
	return last, lastOK
}

// leaveCopyMode self-corrects when something holds copy mode open.
//
// copy-mode-vi binds every digit to a (repeat) prompt, and the digits in a
// terminal reply can open one with nobody typing. While it is up, cancel goes
// to the prompt. So Escape is sent only after a cancel has demonstrably
// failed -- sent unconditionally it could land in a shell or an editor.
func (t *Tmux) leaveCopyMode(ctx context.Context, target string) {
	if in, known := t.inCopyMode(ctx, target); known && !in {
		return
	}
	t.sh(ctx, "send-keys", "-t", target, "-X", "cancel")
	if in, known := t.inCopyMode(ctx, target); known && !in {
		return
	}
	t.sh(ctx, "send-keys", "-t", target, "Escape")
	t.sh(ctx, "send-keys", "-t", target, "-X", "cancel")
}

// scroll works the scrollback through copy mode -- there is nothing else to
// scroll, since tmux repaints the whole screen. Reaching the bottom leaves
// copy mode, which otherwise swallows everything typed next.
func (t *Tmux) scroll(ctx context.Context, target, dir string, n int) {
	if dir == "up" {
		if in, known := t.inCopyMode(ctx, target); known && !in {
			t.sh(ctx, "copy-mode", "-t", target)
		}
	}
	// Unknown means a prompt is in the way; clear it before deciding anything.
	if _, known := t.inCopyMode(ctx, target); !known {
		t.leaveCopyMode(ctx, target)
	}
	if in, known := t.inCopyMode(ctx, target); !known || !in {
		return // nothing to scroll
	}
	// Already at the bottom: scrolling further would leave -N's count
	// unconsumed, which is what puts a stray (repeat) prompt on screen.
	if before, ok := t.scrollPosition(ctx, target); dir == "down" && ok && before <= 0 {
		t.leaveCopyMode(ctx, target)
		return
	}
	// -XN, not -X -N: the same to tmux, but psmux's client drops a lone -N
	// and its count before the server sees them (v3.3.8 src/main.rs
	// send-keys), so every scroll there moved one line. Its server reads
	// the count from any flag cluster ending in N.
	t.sh(ctx, "send-keys", "-t", target, "-XN", strconv.Itoa(n), "scroll-"+dir)
	if dir == "down" {
		if after, ok := t.settledPosition(ctx, target); !ok || after <= 0 {
			t.leaveCopyMode(ctx, target)
		}
	}
}

// The pane fields are the window's active pane's.
var windowFields = strings.Join([]string{
	"#{window_index}", "#{window_width}", "#{window_active}",
	"#{pane_current_command}", "#{pane_id}", "#{pane_tty}", "#{@stui_restored}", "#{@stui_restored_agent}",
	"#{window_id}", "#{window_panes}", "#{window_zoomed_flag}", "#{pane_current_path}", "#{window_name}",
}, sep)

func parseWindows(out string) []WindowInfo {
	list := []WindowInfo{}
	for _, line := range lines(out) {
		// The name can contain the separator and nothing else can, so it is
		// whatever is left after the twelve fixed fields.
		f := strings.SplitN(line, sep, 13)
		for len(f) < 13 {
			f = append(f, "")
		}
		index, _ := strconv.Atoi(f[0])
		width, _ := strconv.Atoi(f[1])
		panes, _ := strconv.Atoi(f[9])
		w := WindowInfo{Index: index, ID: f[8], Width: width, Active: f[2] == "1", Name: f[12], Pane: f[4], proc: f[3], Restored: f[6],
			Panes: panes, Zoomed: f[10] == "1", path: f[11]}
		// Only on a window that is still marked restored.
		if w.Restored != "" {
			w.RestoredAgent = f[7]
		}
		m := resolve(f[3], f[5])
		w.Icon, w.Command = m.Icon, m.Command
		list = append(list, w)
	}
	return list
}

// ListWindows reads the view, which knows which window *this* client is on.
// If it does not exist yet the base session has the same window list.
func (t *Tmux) ListWindows(ctx context.Context, session, client string) []WindowInfo {
	read := func(target string) []WindowInfo {
		out, ok := t.sh(ctx, "list-windows", "-t", target+":", "-F", windowFields)
		if !ok {
			return []WindowInfo{}
		}
		list := parseWindows(out)
		for i := range list {
			list[i].Pane = t.paneOut(session, list[i].Pane)
			list[i].Repo, list[i].Forge = RepoOf(list[i].path), ForgeOf(list[i].path)
		}
		return list
	}
	if !t.grouped {
		return read(session)
	}
	if own := read(ViewNameFor(session, client)); len(own) > 0 {
		return own
	}
	return read(session)
}

var leadingNumber = regexp.MustCompile(`^-?\d+`)

// num reads arg the way the frontend's Number() calls did, near enough:
// the proxy has already made sure it is a number.
func num(arg string) int {
	n, err := strconv.Atoi(arg)
	if err != nil {
		f, err := strconv.ParseFloat(arg, 64)
		if err != nil {
			n, _ = strconv.Atoi(leadingNumber.FindString(arg))
		} else {
			n = int(f)
		}
	}
	return n
}

func (t *Tmux) Run(ctx context.Context, session string, action Action, arg, client string) error {
	view := session
	if t.grouped {
		view = ViewNameFor(session, client)
	}
	target := view + ":"

	switch action {
	case ScrollUp, ScrollDown:
		dir := "up"
		if action == ScrollDown {
			dir = "down"
		}
		t.scroll(ctx, target, dir, max(1, num(arg)))
		return nil
	case ScrollEnd:
		t.leaveCopyMode(ctx, target)
		return nil
	case SelectPane:
		// A client not attached yet has no view: the window it will see
		// is the base session's, and so is its active pane.
		if _, ok := t.sh(ctx, "select-pane", "-t", fmt.Sprintf("%s.%d", target, num(arg))); !ok && view != session {
			t.sh(ctx, "select-pane", "-t", fmt.Sprintf("%s:.%d", session, num(arg)))
		}
		return nil
	case ZoomPane:
		// Selecting another pane unzooms a zoomed window; zoom it again on
		// this one. resize-pane -Z toggles, so only when it is not zoomed.
		// A client not attached yet zooms the base session's window, as
		// select-pane does: the window is the same one.
		pane := fmt.Sprintf("%s.%d", target, num(arg))
		if _, ok := t.sh(ctx, "select-pane", "-t", pane); !ok {
			if view == session {
				return nil
			}
			pane = fmt.Sprintf("%s:.%d", session, num(arg))
			if _, ok := t.sh(ctx, "select-pane", "-t", pane); !ok {
				return nil
			}
		}
		// Unread is not unzoomed: toggling a window that was zoomed on
		// this pane already would undo it.
		if z, ok := t.ask(ctx, pane, "#{window_zoomed_flag}"); ok && z == "0" {
			t.sh(ctx, "resize-pane", "-Z", "-t", pane)
		}
		return nil
	case Unzoom:
		// Window <arg> of the base session (the same window every view
		// has), or the one the client is looking at; only when zoomed,
		// since resize-pane -Z toggles.
		win := target
		if arg != "" {
			win = fmt.Sprintf("%s:%d", session, num(arg))
		}
		if z, _ := t.ask(ctx, win, "#{window_zoomed_flag}"); z == "1" {
			t.sh(ctx, "resize-pane", "-Z", "-t", win)
		}
		return nil
	}

	var cmd []string
	switch action {
	case NextWindow:
		cmd = []string{"next-window", "-t", target}
	case PrevWindow:
		cmd = []string{"previous-window", "-t", target}
	case NewWindow:
		cmd = []string{"new-window", "-t", target, "-c", herePath}
	case CloseWindow:
		cmd = []string{"kill-window", "-t", target}
	case GotoWindow:
		cmd = []string{"select-window", "-t", fmt.Sprintf("%s:%d", view, num(arg))}
	case RenameWindow:
		cmd = []string{"rename-window", "-t", target, arg}
	case NextPane:
		// `target` is "<view>:", and ".+" on the end of it is the next pane in
		// that window. Not `-n`, which select-pane has never had: tmux
		// answered "unknown flag", the error went where errors from here go,
		// and the button did nothing at all.
		cmd = []string{"select-pane", "-t", target + ".+"}
	case Zoom:
		cmd = []string{"resize-pane", "-t", target, "-Z"}
	case SplitRight:
		cmd = []string{"split-window", "-t", target, "-h", "-c", herePath}
	case SplitDown:
		cmd = []string{"split-window", "-t", target, "-v", "-c", herePath}
	case ClosePane:
		cmd = []string{"kill-pane", "-t", target}
	case Detach:
		cmd = []string{"detach-client", "-s", view}
	}
	if cmd == nil {
		return nil
	}

	// Said, not swallowed. For as long as this discarded tmux's answer the
	// route replied {"ok":true} whatever happened: a browser asked for a
	// window, was told it had one, and no window appeared.
	if _, ok := t.sh(ctx, cmd...); ok {
		return nil
	}

	// The view may simply not be there. A client's view appears when its
	// websocket attaches and is reaped again when nothing is attached, so a
	// page that has its id but has not attached yet -- or whose view was
	// reaped while it sat idle -- names a session that does not exist. The
	// windows belong to the base session and every view shares them, so the
	// base session is the same window list by another name.
	//
	// NewWindowPane has always fallen back this way, and so does zoom-pane
	// for a client that is not attached yet. The generic commands did not, which is why
	// a run of twenty new-window calls could leave five windows.
	//
	// Not detach: `detach-client -s <base>` would detach every client of the
	// session, which is the opposite of what one client asked for.
	if view != session && action != Detach {
		if _, ok := t.sh(ctx, retarget(cmd, view, session)...); ok {
			return nil
		}
	}
	// A new pane in the folder of the one being looked at, or -- should a
	// multiplexer refuse the format (psmux has not been seen with it) -- where
	// it opened before, rather than not at all.
	if bare := withoutDir(cmd); len(bare) != len(cmd) {
		if _, ok := t.sh(ctx, bare...); ok {
			return nil
		}
		if view != session {
			if _, ok := t.sh(ctx, retarget(bare, view, session)...); ok {
				return nil
			}
		}
	}
	return fmt.Errorf("tmux %s on %s failed", cmd[0], target)
}

// herePath is the start directory of a new window or pane: the folder of
// the pane the client is looking at (the target's active pane). tmux expands
// formats in -c (3.7c, the one stui carries, and 3.8 measured): one call, and a
// folder that has gone opens in the home directory as before, not in a
// directory of that name.
const herePath = "#{pane_current_path}"

// withoutDir is cmd without its -c.
func withoutDir(cmd []string) []string {
	for i, a := range cmd {
		if a == "-c" && i+1 < len(cmd) {
			return append(append([]string(nil), cmd[:i]...), cmd[i+2:]...)
		}
	}
	return cmd
}

// retarget rewrites a command's `-t` argument from one session to another,
// keeping whatever follows the session name ("", ":", ":3", ":.+").
func retarget(cmd []string, from, to string) []string {
	out := append([]string(nil), cmd...)
	for i, a := range out {
		if a != "-t" || i+1 >= len(out) {
			continue
		}
		if v := out[i+1]; strings.HasPrefix(v, from) {
			out[i+1] = to + strings.TrimPrefix(v, from)
		}
	}
	return out
}
