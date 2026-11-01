package hookclient

// The spool: hook events no agent was there to take.
//
// An upgrade, stui restart or a crash leaves a few seconds with no agent, and
// a turn or a subagent that ended in them never ended as far as stui knows --
// the pane stays "working". So an event that could not be delivered is
// appended to ~/.stui/hook-spool/<pane>.jsonl, and the next agent plays
// the spool back once it has seen the panes (watch.Watcher.ReplaySpool).
//
// Tool hooks are not kept: there are many of them, and by the time they
// could be read they say nothing. A pane's file stops growing at SpoolMax;
// the agent reads nothing older than SpoolMaxAge. The events hold what the
// local API serves anyway (states, ids, a command's one-line summary), never
// a prompt or a transcript's path, and the files are the user's alone.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// SpoolDir is the spool's directory under ~/.stui.
	SpoolDir = "hook-spool"
	// SpoolMax is how large one pane's spool grows.
	SpoolMax = 5 << 20
	// SpoolMaxAge is how old an event can be and still be played back.
	SpoolMaxAge = 7 * 24 * time.Hour
)

// spooled says whether an event is worth keeping for later.
func spooled(event string) bool {
	switch event {
	case "PreToolUse", "PostToolUse", "PostToolUseFailure", "BeforeTool", "AfterTool", "":
		return false
	}
	return true
}

// SpoolFile is the spool of pane, under home (~/.stui).
func SpoolFile(home, pane string) string {
	name := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		return '_'
	}, pane)
	return filepath.Join(home, SpoolDir, "pane-"+name+".jsonl")
}

// DroppedName is the file, under LastDir, a hook that reached no agent and
// is not spooled (a tool hook) leaves its event name in, one line each, up
// to DroppedMax: the running agent counts and empties it (watch
// TakeDropped). A lost Agent PreToolUse/PostToolUse is a subagent with no
// description, so how often this happens is worth knowing.
const (
	DroppedName = "dropped.log"
	DroppedMax  = 64 << 10
)

// DroppedFile is the dropped-events file under home.
func DroppedFile(home string) string {
	return filepath.Join(home, LastDir, DroppedName)
}

// noteDropped adds event to the dropped-events file.
func noteDropped(home, event string) {
	if event == "" || len(event) > 64 || strings.ContainsAny(event, "\n\r") {
		return
	}
	path := DroppedFile(home)
	if fi, err := os.Stat(path); err == nil && fi.Size() >= DroppedMax {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	f.WriteString(event + "\n")
	f.Close()
}

// Spool appends ev to its pane's spool. Failing is not an error anyone
// hears of: the hook must never get in the agent's way.
func Spool(home string, ev Event) {
	if !spooled(ev.Event) {
		return
	}
	path := SpoolFile(home, ev.Pane)
	if fi, err := os.Stat(path); err == nil && fi.Size() >= SpoolMax {
		return
	}
	// The transcript's path and the working directory are for the agent's
	// memory only: never written down.
	if _, ok := ev.Data["transcript_path"]; ok || ev.Data["cwd"] != nil {
		data := make(map[string]any, len(ev.Data))
		for k, v := range ev.Data {
			if k != "transcript_path" && k != "cwd" {
				data[k] = v
			}
		}
		ev.Data = data
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	// One write, so lines from hooks running at once do not interleave.
	f.Write(append(line, '\n'))
	f.Close()
}
