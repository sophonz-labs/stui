package mux

import (
	"regexp"
	"strings"
)

// Pane ids. tmux's are "%3", unique on the server. psmux runs a server per
// session and each numbers its panes from %1, so there stui names a pane
// "<session>:%3": unique on the machine, the same name agent watch gives it
// (internal/watch/psmux.go), and a target psmux routes to the right server.
// Every pane id stui hands out on psmux has that form; every one it takes may
// have either.

var paneIDRE = regexp.MustCompile(`^(?:[A-Za-z0-9_-]{1,64}:)?%[0-9]{1,9}$`)

// IsPaneID is whether s is a pane id: %3, or psmux's work:%3.
func IsPaneID(s string) bool { return paneIDRE.MatchString(s) }

// SplitPaneID is a pane id's session ("" for a bare one) and its %n.
func SplitPaneID(s string) (session, bare string) {
	if i := strings.LastIndex(s, ":"); i >= 0 {
		return s[:i], s[i+1:]
	}
	return "", s
}

// QualifyPane is the psmux name of pane id (%3) of session.
func QualifyPane(session, id string) string {
	if session == "" || !strings.HasPrefix(id, "%") {
		return id
	}
	return session + ":" + id
}

// PsmuxTarget is how a -t names pane "<session>:%N" to psmux:
// "<session>:.%N". psmux 3.4.0 does not find "<session>:%N" -- send-keys,
// capture-pane and display-message all answer "can't find window: %N", the
// first window's %1 too -- and finds ":.%N", a pane id in the pane slot
// (measured on a Windows runner). Any other
// target is as it was.
func PsmuxTarget(t string) string {
	s, bare := SplitPaneID(t)
	if s == "" || !strings.HasPrefix(bare, "%") || strings.HasSuffix(s, ".") || !paneIDRE.MatchString(t) {
		return t
	}
	return s + ":." + bare
}

// paneOut is the id stui reports for pane id of session: psmux's names it by
// its session, tmux's is as tmux said.
func (t *Tmux) paneOut(session, id string) string {
	if t.id != "psmux" {
		return id
	}
	return QualifyPane(session, id)
}

// paneTarget is what -t takes for a pane id that came in. tmux: the %n (a
// qualified one names the same pane, ids being the server's). psmux: the
// qualified name, which reaches its session's server; a bare one in session
// when that is known.
func (t *Tmux) paneTarget(session, id string) string {
	s, bare := SplitPaneID(id)
	if t.id != "psmux" {
		return bare
	}
	if s == "" {
		s = session
	}
	return PsmuxTarget(QualifyPane(s, bare))
}
