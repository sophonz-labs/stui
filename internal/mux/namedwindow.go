package mux

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// keyOption marks a window stui opened for one thing (a CI job's log), so
// two windows of the same name are told apart.
const keyOption = "@stui-window-key"

// NamedWindow is the window called name that stui opened for key in pane's
// session: found when it is there, opened running argv in dir when not. A
// window of the same name for another key (the same job of another branch,
// or an earlier attempt) is left alone and a new one opened beside it: it
// may be somebody's log. The window opens behind (-d); the client goes to
// it. It returns the session -- the base one when pane is in a per-client
// view -- and the window's index.
func (t *Tmux) NamedWindow(ctx context.Context, pane, name, key, dir string, argv []string) (string, int, error) {
	if t.bin == "" {
		return "", 0, errors.New("no tmux")
	}
	if !paneIDRE.MatchString(pane) || name == "" || len(argv) == 0 {
		return "", 0, errors.New("bad window")
	}
	info, ok := t.ask(ctx, t.paneTarget("", pane), "#{session_name}"+sep+"#{session_group}")
	if !ok {
		return "", 0, errors.New("no such pane")
	}
	session, group, _ := strings.Cut(info, sep)
	if IsInternalView(session, group, nil) {
		session = group
	}
	out, _ := t.sh(ctx, "list-windows", "-t", "="+session+":", "-F",
		"#{window_id}"+sep+"#{window_index}"+sep+"#{"+keyOption+"}"+sep+"#{window_name}")
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.SplitN(line, sep, 4)
		if len(f) != 4 || f[3] != name || f[2] != key {
			continue
		}
		index, _ := strconv.Atoi(f[1])
		return session, index, nil
	}
	args := append([]string{"new-window", "-d", "-t", "=" + session + ":", "-n", name, "-c", dir,
		"-P", "-F", "#{window_id}" + sep + "#{window_index}"}, argv...)
	made, ok := t.sh(ctx, args...)
	if !ok {
		return "", 0, errors.New("could not open a window in " + session)
	}
	id, idx, _ := strings.Cut(strings.TrimSpace(made), sep)
	t.sh(ctx, "set-option", "-w", "-t", id, keyOption, key)
	index, _ := strconv.Atoi(idx)
	return session, index, nil
}
