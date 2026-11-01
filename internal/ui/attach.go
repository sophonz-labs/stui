package ui

// Attaching is tmui's, for stui's server: the
// default tmux server, or the one $TMUX_TMPDIR points at.

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// parseVersion is tmux -V as major*10+minor: "tmux 3.2a" is 32. A build that
// does not say a number ("tmux next-3.6", "tmux openbsd-7.6") is taken as new.
func parseVersion(v string) int {
	v = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "tmux "))
	if i := strings.LastIndexAny(v, "-"); i >= 0 {
		v = v[i+1:]
	}
	major, rest, ok := strings.Cut(v, ".")
	ma, err := strconv.Atoi(major)
	if !ok || err != nil {
		return 99
	}
	mi := 0
	for _, r := range rest {
		if r < '0' || r > '9' {
			break
		}
		mi = mi*10 + int(r-'0')
	}
	return ma*10 + min(mi, 9)
}

// where is where stui ui runs.
type where struct {
	tmuxEnv    string // $TMUX: set when stui ui runs inside a tmux pane
	ownSession string // the session stui ui's pane is in, inside tmux
	sameServer bool   // the server stui shows is the one stui ui runs in
	version    int    // parseVersion of tmux -V
}

// attachPlan is how to put this terminal on target and get back to stui.
type attachPlan struct {
	kind string   // "attach" (stui hands its terminal over), "popup", "switch"
	argv []string // the tmux command line, argv[0] is "tmux"
	back string   // how to get back, said before going
}

// planAttach decides how to attach to target (a "=session", "@window" or
// "%pane", in session). prefix is the server's prefix key.
//
//   - outside tmux: tmux attach; detaching ends it and stui comes back.
//   - inside tmux 3.2+: a full-size popup on the outer client running a
//     second client of the target with $TMUX cleared, so "prefix d"
//     detaches that inner client, which closes the popup.
//   - inside an older tmux: switch-client when it is the same server;
//     else a nested attach in stui's own pane.
//   - stui's own session: a popup of it would show stui inside itself, so
//     it switches to the window instead.
func planAttach(w where, session, target, prefix string) attachPlan {
	attach := []string{"tmux", "attach-session", "-t", target}
	if w.tmuxEnv == "" {
		return attachPlan{kind: "attach", argv: attach, back: prefix + " d comes back to stui"}
	}
	if w.sameServer && session == w.ownSession {
		return attachPlan{kind: "switch", argv: []string{"tmux", "switch-client", "-t", target}, back: prefix + " l (last window) comes back to stui"}
	}
	if w.sameServer && w.version < 32 {
		return attachPlan{kind: "switch", argv: []string{"tmux", "switch-client", "-t", target},
			back: prefix + " L (last session) or " + prefix + " s comes back to stui"}
	}
	if w.version < 32 {
		return attachPlan{kind: "attach", argv: attach, back: prefix + " " + prefix + " d comes back to stui"}
	}
	// $TMUX is cleared for the inner client, so name the server outright.
	inner := []string{}
	if !w.sameServer {
		// The popup's shell inherits the outer server's environment, not
		// stui's: name stui's server by its socket.
		if s := serverSocket(); s != "" {
			inner = []string{"-S", s}
		}
	} else {
		sock, _, _ := strings.Cut(w.tmuxEnv, ",")
		inner = []string{"-S", sock}
	}
	sh := "TMUX= exec tmux"
	for _, f := range append(inner, "attach-session", "-t", target) {
		sh += " " + shellQuote(f)
	}
	argv := []string{"tmux", "display-popup", "-E", "-w", "100%", "-h", "100%"}
	if w.version >= 33 {
		argv = append(argv, "-B") // no border: the full terminal
	}
	return attachPlan{kind: "popup", argv: append(argv, sh), back: prefix + " d comes back to stui"}
}

// shellQuote quotes s for the popup's shell, which may be zsh: there a
// leading = expands, so "=web" must be quoted too.
func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// serverSocket is the socket of the server stui shows (without $TMUX).
func serverSocket() string {
	c := exec.Command("tmux", "display-message", "-p", "#{socket_path}")
	c.Env = withoutTMUX(os.Environ())
	out, err := c.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func withoutTMUX(env []string) []string {
	out := env[:0:0]
	for _, e := range env {
		if !strings.HasPrefix(e, "TMUX=") {
			out = append(out, e)
		}
	}
	return out
}

// locate fills in where stui ui runs. A failure leaves the safe answers:
// not the same server, no session of its own.
func locate() where {
	w := where{tmuxEnv: os.Getenv("TMUX")}
	if out, err := exec.Command("tmux", "-V").Output(); err == nil {
		w.version = parseVersion(string(out))
	}
	if w.tmuxEnv == "" {
		return w
	}
	sock, _, _ := strings.Cut(w.tmuxEnv, ",")
	w.sameServer = serverSocket() == sock
	if p := os.Getenv("TMUX_PANE"); w.sameServer && p != "" {
		if out, err := exec.Command("tmux", "display-message", "-p", "-t", p, "#{session_name}").Output(); err == nil {
			w.ownSession = strings.TrimSpace(string(out))
		}
	}
	return w
}

// attachTo puts this terminal on target (in session; label is how to say
// it) and comes back to stui ui when it detaches.
func (a *app) attachTo(session, target, label string) {
	prefix := "C-b"
	if out, err := exec.Command("tmux", "show-options", "-gv", "prefix").Output(); err == nil && strings.TrimSpace(string(out)) != "" {
		prefix = strings.TrimSpace(string(out))
	}
	plan := planAttach(a.at, session, target, prefix)
	line := "attaching to " + label + " — " + plan.back
	c := exec.Command(plan.argv[0], plan.argv[1:]...)
	switch plan.kind {
	case "attach":
		if a.at.tmuxEnv != "" {
			c.Env = withoutTMUX(os.Environ()) // nested in this pane on purpose
		}
		a.handOverCmd(false, c, line)
	case "popup":
		a.say(line, false)
		a.scr.draw(a.render())
		// The popup covers stui and takes every key until it closes.
		a.scr.pause()
		out, err := c.CombinedOutput()
		a.scr.resume()
		a.scr.w.WriteString("\x1b[2J")
		if err != nil {
			a.say("display-popup: "+strings.TrimSpace(string(out)+" "+err.Error()), true)
		}
		a.refreshNow()
	case "switch":
		if out, err := c.CombinedOutput(); err != nil {
			a.say(strings.TrimSpace(string(out)+" "+err.Error()), true)
			return
		}
		a.say("switched to "+label+" — "+plan.back, false)
	}
}
