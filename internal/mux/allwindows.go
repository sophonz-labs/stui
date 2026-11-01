package mux

import (
	"context"
	"strings"
)

// AllWindows is the windows of each of these sessions, by session name.
//
// tmux answers for the whole server at once (`list-windows -a`), so a
// listing costs one call however many sessions there are. Every other
// backend -- psmux has a server per session, zellij lists tabs per session --
// is asked once per session, and only for the first max of them. A session
// that does not answer is left out rather than reported with no windows.
func AllWindows(ctx context.Context, m Multiplexer, sessions []SessionInfo, max int) map[string][]WindowInfo {
	out := map[string][]WindowInfo{}
	if len(sessions) == 0 {
		return out
	}
	want := map[string]bool{}
	for _, s := range sessions {
		want[s.Name] = true
	}
	if t, ok := m.(*Tmux); ok && t.grouped {
		res, ok := t.sh(ctx, "list-windows", "-a", "-F", "#{session_name}"+sep+windowFields)
		if !ok {
			return out
		}
		for name, list := range parseAllWindows(res) {
			if want[name] {
				out[name] = list
			}
		}
		return out
	}
	for i, s := range sessions {
		if i >= max {
			break
		}
		if list := m.ListWindows(ctx, s.Name, ""); len(list) > 0 {
			out[s.Name] = list
		}
	}
	return out
}

// parseAllWindows reads `list-windows -a` with the session name in front of
// windowFields. Session names never hold the separator (ValidName, and tmux's
// own views are made from valid names), so the first field is the name and
// the rest is one window line.
func parseAllWindows(out string) map[string][]WindowInfo {
	by := map[string][]string{}
	var order []string
	for _, line := range lines(out) {
		name, rest, ok := strings.Cut(line, sep)
		if !ok || name == "" {
			continue
		}
		if _, seen := by[name]; !seen {
			order = append(order, name)
		}
		by[name] = append(by[name], rest)
	}
	res := map[string][]WindowInfo{}
	for _, name := range order {
		res[name] = parseWindows(strings.Join(by[name], "\n"))
	}
	return res
}
