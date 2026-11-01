package watch

// Watching psmux, tmux for Windows (github.com/psmux/psmux).
//
// psmux speaks tmux's commands but runs one server per session, and each
// server numbers its panes from %1: two sessions both have a %1. So here a
// pane is "<session>:%<n>", which is unique on the machine. As a target it
// goes as `-t work:.%1` (PsmuxArgs): psmux 3.4.0 does not find `work:%1`. `list-panes -a`
// asks every server in turn; #{server_pid} says which one a line came from,
// and is what a pane's $TMUX carries (/tmp/psmux-<pid>/<socket>,<port>,0),
// so a hook's %1 is put in its session by the server that sent it.
//
// What psmux does not give, the watch does without:
//   - no control clients: a psmux control client is a client attached to
//     the session (the session list would count it), and its -f
//     ignore-size has not been seen working. Screens are read on the poll
//     instead, and turns come from the hooks.
//   - no tty: #{pane_tty} is a made-up /dev/ptyN and Windows has no ps. A
//     pane's conversation is the one its hooks name (transcript_path), and
//     a teammate is not told from its command line.

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/sophonz-labs/stui/internal/mux"
	"github.com/sophonz-labs/stui/internal/proc"
)

// ExecPsmux runs psmux as the watch's tmux, its arguments put the way psmux
// reads them (PsmuxArgs).
type ExecPsmux struct{ Bin string }

func (t ExecPsmux) Run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, t.Bin, PsmuxArgs(args)...)
	proc.Hide(cmd)
	out, err := cmd.Output()
	return string(out), err
}

// psmuxFlags are the arguments psmux's send-keys takes as flags wherever
// they are (v3.3.8 src/main.rs): a key that is one of these is never typed.
var psmuxFlags = map[string]bool{"-l": true, "-R": true, "-X": true, "-H": true, "-t": true, "-N": true}

// PsmuxArgs is a tmux command line as psmux needs it. A -t of a pane
// "<session>:%N" goes as "<session>:.%N", which psmux finds
// (mux.PsmuxTarget). send-keys differs more: psmux has no "--" (it would
// type it), and reads -l and the others as flags even after the keys. Text
// that is exactly one of those goes a character at a time: "-" and "l" are
// keys.
func PsmuxArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}
	if args[0] != "send-keys" && args[0] != "send" {
		var out []string
		for i := 0; i < len(args); i++ {
			out = append(out, args[i])
			if args[i] == "-t" && i+1 < len(args) {
				out = append(out, mux.PsmuxTarget(args[i+1]))
				i++
			}
		}
		return out
	}
	out := []string{args[0]}
	keys := false
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch {
		case keys:
			if psmuxFlags[a] {
				for _, r := range a {
					out = append(out, string(r))
				}
				continue
			}
			out = append(out, a)
		case a == "--":
			keys = true
		case a == "-t" || a == "-N":
			out = append(out, a)
			if i+1 < len(args) {
				v := args[i+1]
				if a == "-t" {
					v = mux.PsmuxTarget(v)
				}
				out = append(out, v)
				i++
			}
		default:
			out = append(out, a)
		}
	}
	return out
}

// psmuxPaneID is the watch's name for pane id (%1) of session.
func psmuxPaneID(session, id string) string { return session + ":" + id }

// psmuxServer is the server pid in a psmux pane's $TMUX socket part
// (/tmp/psmux-<pid>/<socket>), or "".
func psmuxServer(socket string) string {
	i := strings.Index(socket, "/psmux-")
	if i < 0 {
		return ""
	}
	rest := socket[i+len("/psmux-"):]
	pid, _, _ := strings.Cut(rest, "/")
	if _, err := strconv.Atoi(pid); err != nil {
		return ""
	}
	return pid
}

var psmuxPaneFields = "#{server_pid}" + fsep + paneFields

// parsePsmuxPanes reads `list-panes -a -F psmuxPaneFields`: the panes, with
// ids made unique, and each server's session.
func parsePsmuxPanes(out string) ([]paneRow, map[string]string) {
	byServer := map[string][]string{}
	var order []string
	for _, line := range strings.Split(out, "\n") {
		pid, rest, ok := strings.Cut(strings.TrimRight(line, "\r"), fsep)
		if !ok || pid == "" {
			continue
		}
		if _, seen := byServer[pid]; !seen {
			order = append(order, pid)
		}
		byServer[pid] = append(byServer[pid], rest)
	}
	var rows []paneRow
	servers := map[string]string{}
	for _, pid := range order {
		for _, r := range parsePanes(strings.Join(byServer[pid], "\n")) {
			servers[pid] = r.session
			r.id = psmuxPaneID(r.session, r.id)
			r.command = psmuxCommand(r.command)
			r.tty = ""
			r.group = ""
			rows = append(rows, r)
		}
	}
	return rows, servers
}

// psmuxCommand is what a pane runs as tmux would say it: psmux gives the
// command line a shell announced (OSC 133) when there is one, else the
// process's image name -- "claude --resume x", "C:\...\pwsh.exe".
func psmuxCommand(c string) string {
	c = strings.Trim(strings.TrimSpace(c), `"`)
	if i := strings.Index(strings.ToLower(c), ".exe"); i >= 0 {
		// The image, however many spaces its path has.
		c = c[:i]
	} else if f := strings.Fields(c); len(f) > 0 {
		c = f[0]
	}
	if i := strings.LastIndexAny(c, `\/`); i >= 0 {
		c = c[i+1:]
	}
	return strings.Trim(c, `"`)
}

// psmuxPane is the watch's id for a hook's pane: the %n of the server its
// socket names, in that server's session. Asks psmux when the server is not
// known yet (a session made since the last poll). "" when nobody knows it.
func (w *Watcher) psmuxPane(socket, pane string) string {
	if strings.Contains(pane, ":") {
		return pane
	}
	pid := psmuxServer(socket)
	if pid == "" {
		return ""
	}
	w.mu.Lock()
	s := w.psmuxServers[pid]
	w.mu.Unlock()
	if s == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		out, err := w.opts.Tmux.Run(ctx, "list-panes", "-a", "-F", "#{server_pid}"+fsep+"#{session_name}")
		if err != nil {
			return ""
		}
		w.mu.Lock()
		for _, line := range strings.Split(out, "\n") {
			p, name, ok := strings.Cut(strings.TrimSpace(line), fsep)
			if ok && p != "" && name != "" {
				if w.psmuxServers == nil {
					w.psmuxServers = map[string]string{}
				}
				w.psmuxServers[p] = name
			}
		}
		s = w.psmuxServers[pid]
		w.mu.Unlock()
		if s == "" {
			return ""
		}
	}
	return psmuxPaneID(s, pane)
}
