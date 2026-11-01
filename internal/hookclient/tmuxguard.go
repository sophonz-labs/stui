package hookclient

// A coding agent must not stop the tmux server it runs in -- the one that
// holds every session on the machine.
//
// An agent cleaning up after a test can run
// `TMUX_TMPDIR=/tmp/orch-test tmux kill-server`, meaning the test's own
// server. If that directory was never made, tmux 3.7 falls back to /tmp
// without a word: the command reaches the real server, and every session
// on the machine goes, the agent's own among them.
//
// So Claude Code's PreToolUse for a shell command that would do that is
// answered with a denial and the way to do it safely: a kill-server that
// does not name its server (-S <socket> or -L <name>), or names this one,
// and pkill/killall of tmux. Anything stui cannot read is let through, as
// with owned paths: stui never stops an agent by accident.

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"

	"github.com/sophonz-labs/stui/internal/restore"
)

var heredocAt = regexp.MustCompile(`<<-?\s*['"]?([A-Za-z_][A-Za-z0-9_]*)['"]?`)

// segments splits command into simple commands and their words, the way the
// shell would: quotes taken off, and a word with a quoted space in it -- a
// message or a pattern ("tmux kill-server is refused now") -- read as data,
// "_".
func segments(command string) [][]string {
	var segs [][]string
	var cur []string
	var w strings.Builder
	inWord, data := false, false
	word := func() {
		if inWord {
			s := w.String()
			if data {
				s = "_"
			}
			cur = append(cur, s)
		}
		w.Reset()
		inWord, data = false, false
	}
	seg := func() {
		word()
		if len(cur) > 0 {
			segs = append(segs, cur)
		}
		cur = nil
	}
	for i := 0; i < len(command); i++ {
		c := command[i]
		switch {
		case c == '\'' || c == '"':
			j := strings.IndexByte(command[i+1:], c)
			if j < 0 {
				j = len(command) - i - 1
			}
			q := command[i+1 : i+1+j]
			if strings.ContainsAny(q, " \t\n") {
				data = true
			}
			w.WriteString(q)
			inWord = true
			i += j + 1
		case c == '\\' && i+1 < len(command):
			w.WriteByte(command[i+1])
			inWord = true
			i++
		case c == ' ' || c == '\t':
			word()
		case c == '\n' || c == ';' || c == '|' || c == '&' || c == '(' || c == ')':
			seg()
		default:
			w.WriteByte(c)
			inWord = true
		}
	}
	seg()
	return segs
}

// commandWords is command without heredoc bodies: what the shell runs, not
// what it hands on as data.
func commandWords(command string) string {
	var keep []string
	end := ""
	for _, line := range strings.Split(command, "\n") {
		if end != "" {
			if strings.TrimSpace(line) == end {
				end = ""
			}
			continue
		}
		if m := heredocAt.FindStringSubmatch(line); m != nil {
			end = m[1]
		}
		keep = append(keep, line)
	}
	return strings.Join(keep, "\n")
}

// tmuxish is whether a word runs tmux: a path ending in tmux, or a
// variable holding one.
func tmuxish(w string) bool {
	w = strings.Trim(w, `"'`)
	return strings.HasSuffix(w, "tmux") || strings.HasPrefix(w, "$")
}

// tmuxKillReason is why command would stop the tmux server at socket (the
// pane's own, from $TMUX), or "" when it would not.
func tmuxKillReason(command, socket string) string {
	socket = restore.Canonical(socket)
	tmpdir := os.Getenv("TMUX_TMPDIR")
	for _, f := range segments(commandWords(command)) {
		// TMUX_TMPDIR=… set for the rest of the command, or for this one.
		for _, w := range f {
			if v, ok := strings.CutPrefix(w, "TMUX_TMPDIR="); ok {
				tmpdir = strings.Trim(v, `"'`)
			}
		}
		if len(f) == 0 {
			continue
		}
		first := 0
		for first < len(f) && (strings.Contains(f[first], "=") || f[first] == "env" || f[first] == "sudo" || f[first] == "command" || f[first] == "exec") {
			first++
		}
		if first < len(f) {
			switch strings.Trim(f[first], `"'`) {
			case "pkill", "killall":
				for _, w := range f[first+1:] {
					if strings.Contains(strings.Trim(w, `"'`), "tmux") {
						return "pkill/killall of tmux stops every tmux server of this user, the one holding all of this machine's sessions included. Stop a test server by its socket instead: tmux -S <socket> kill-server."
					}
				}
			}
		}
		at := -1
		for i, w := range f {
			if w == "kill-server" {
				at = i
			}
		}
		if at < 0 {
			continue
		}
		runsTmux := false
		for _, w := range f[:at] {
			if tmuxish(w) {
				runsTmux = true
			}
		}
		if !runsTmux {
			continue
		}
		named := ""
		for i := 0; i+1 < at; i++ {
			switch f[i] {
			case "-S":
				named = strings.Trim(f[i+1], `"'`)
				if strings.ContainsAny(named, "$`") {
					// Named, by a variable stui cannot read: taken as meant.
					named = "?"
					break
				}
				named = restore.Canonical(named)
			case "-L":
				label := strings.Trim(f[i+1], `"'`)
				if strings.ContainsAny(label, "$`") || strings.ContainsAny(tmpdir, "$`") {
					named = "?"
					break
				}
				named = restore.SocketFor(tmpdir, label)
			}
		}
		switch {
		case named == "":
			return "tmux kill-server without -S or -L stops the default server -- when TMUX_TMPDIR names a directory that does not exist, tmux falls back to /tmp, which is the server holding all of this machine's sessions, this agent's included. Name the server to stop: tmux -S <socket> kill-server (or -L <name>, with its TMUX_TMPDIR made first)."
		case named == "?":
		case socket != "" && named == socket:
			return "that is the tmux server this agent runs in, holding all of this machine's sessions: it is not stopped from here."
		}
	}
	return ""
}

// tmuxGuard is the denial to print for a Claude PreToolUse, or nil.
func tmuxGuard(kind, event string, raw []byte, socket string) []byte {
	if kind != "claude" || event != "PreToolUse" {
		return nil
	}
	var in struct {
		ToolName  string `json:"tool_name"`
		ToolInput struct {
			Command string `json:"command"`
		} `json:"tool_input"`
	}
	if json.Unmarshal(raw, &in) != nil || in.ToolName != "Bash" || in.ToolInput.Command == "" {
		return nil
	}
	why := tmuxKillReason(in.ToolInput.Command, socket)
	if why == "" {
		return nil
	}
	b, _ := json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": "stui: " + why,
		},
	})
	return b
}
