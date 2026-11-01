package hookclient

// What stui hook did last time, per agent.
//
// stui hook never prints and always exits 0, so that a hook of stui's never
// shows up in somebody's session. The price was that nobody could tell why
// a hook did nothing: run outside tmux, it returned without a word, and a
// hook the agent could not even start left no trace at all. So each run
// leaves one line of what it did, and stui hooks status reads it back: a hook
// that ran outside tmux says so, and one that has never run since it was
// installed says that.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// LastDir is where the records are, under stui's home.
const LastDir = "hook-last"

// What a run did.
const (
	Delivered   = "delivered"    // stui took the event
	Spooled     = "spooled"      // stui was not running: kept for when it is
	Dropped     = "dropped"      // stui did not take it in time, and a tool hook is not kept: counted (DroppedFile)
	OutsideTmux = "outside-tmux" // no pane to say it happened in: nothing sent
	Unreadable  = "unreadable"   // the agent's input was not JSON stui reads
)

// LastRun is one agent's record.
type LastRun struct {
	// Unix milliseconds.
	At      int64  `json:"at"`
	Event   string `json:"event,omitempty"`
	Outcome string `json:"outcome"`
	Pane    string `json:"pane,omitempty"`
}

// LastFile is kind's record under home.
func LastFile(home, kind string) string {
	return filepath.Join(home, LastDir, kind+".json")
}

// ReadLast is kind's record; ok false when it has none.
func ReadLast(home, kind string) (LastRun, bool) {
	var r LastRun
	b, err := os.ReadFile(LastFile(home, kind))
	if err != nil || json.Unmarshal(b, &r) != nil || r.Outcome == "" {
		return LastRun{}, false
	}
	return r, true
}

// noteRun records what this run did. A hook runs on every tool call, so
// the same outcome is written again only once a while has passed.
func noteRun(home, kind string, r LastRun) {
	if kind == "" || len(kind) > 32 || filepath.Base(kind) != kind {
		return
	}
	path := LastFile(home, kind)
	if old, ok := ReadLast(home, kind); ok && old.Outcome == r.Outcome && old.Pane == r.Pane &&
		r.At-old.At < (30*time.Second).Milliseconds() {
		return
	}
	b, err := json.Marshal(r)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	tmp := path + ".tmp" + itoa(os.Getpid())
	if os.WriteFile(tmp, b, 0o600) != nil {
		return
	}
	if os.Rename(tmp, path) != nil {
		os.Remove(tmp)
	}
}
