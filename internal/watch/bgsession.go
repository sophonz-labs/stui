package watch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// Claude Code's background sessions (a session sent to the background, or
// `claude --bg`) run under Claude's own daemon, outside tmux: their hooks
// name no pane. The pane that shows one is a Claude in tmux whose title is
// the session's name, as Claude keeps it in <claude dir>/jobs/<id>/state.json
// (the id is the session id's first eight characters). Events from such a
// session go to that pane, so its turns, questions and subagents show where
// the conversation is on screen.

// bgName is how long a background session's name is kept between reads of
// its state file: hooks come many a second.
const bgNameTTL = 10 * time.Second

type bgEntry struct {
	name string
	at   time.Time
}

// bgPane is the pane showing background session sid, or "" when no single
// pane does. dir is the Claude directory the hook named, or "".
func (w *Watcher) bgPane(sid, dir string) string {
	if len(sid) < 8 || strings.ContainsAny(sid, `/\.`) {
		return ""
	}
	name := w.bgSessionName(sid, dir)
	if name == "" {
		return ""
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	found := ""
	for id, p := range w.panes {
		if p.Agent != "claude" || titleText(p.Title) != name {
			continue
		}
		if found != "" {
			// Two panes with the same name: which one is not for stui to guess.
			return ""
		}
		found = id
	}
	return found
}

func (w *Watcher) bgSessionName(sid, dir string) string {
	w.mu.Lock()
	if e, ok := w.bgNames[sid]; ok && w.now().Sub(e.at) < bgNameTTL {
		w.mu.Unlock()
		return e.name
	}
	dirs := []string{}
	if dir != "" {
		dirs = append(dirs, dir)
	}
	for d := range w.claudeDirs {
		dirs = append(dirs, d)
	}
	w.mu.Unlock()
	name := ""
	for _, d := range dirs {
		if name = readBgName(filepath.Join(d, "jobs", sid[:8], "state.json"), sid); name != "" {
			break
		}
	}
	w.mu.Lock()
	if len(w.bgNames) > 256 {
		w.bgNames = map[string]bgEntry{}
	}
	w.bgNames[sid] = bgEntry{name: name, at: w.now()}
	w.mu.Unlock()
	return name
}

func readBgName(path, sid string) string {
	b, err := os.ReadFile(path)
	if err != nil || len(b) > 4<<20 {
		return ""
	}
	var s struct {
		Name      string `json:"name"`
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(b, &s) != nil || s.SessionID != sid {
		return ""
	}
	return strings.TrimSpace(s.Name)
}

// titleText is a pane title without the glyph Claude puts before it (its
// spinner while it works, ✳ when idle).
func titleText(t string) string {
	t = strings.TrimSpace(t)
	r := []rune(t)
	if len(r) > 2 && !unicode.IsLetter(r[0]) && !unicode.IsDigit(r[0]) && unicode.IsSpace(r[1]) {
		t = string(r[2:])
	}
	return strings.TrimSpace(t)
}
