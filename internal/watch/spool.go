package watch

// Playing back the hook events no agent took (hookclient/spool.go).
//
// Only what ends or settles something is played back: a turn's end, an
// interrupt, a session's end, a subagent's start and end. A prompt or a
// permission asked for while stui was away is either still on screen, where
// stui reads it, or long answered. What is played back changes state and
// never tells anybody: a turn-done for a turn that ended a minute ago is
// not news, and the phones already lost the moment.
//
// An event from before the pane's last live hook, or from before the turn
// running now began, is history the pane has moved past and is dropped.

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sophonz-labs/stui/internal/hookclient"
)

func replayable(event string) bool {
	switch event {
	case "Stop", "StopFailure", "agent-turn-complete", "AfterAgent", "Interrupt",
		"SessionEnd", "SubagentStart", "SubagentStop":
		return true
	}
	return false
}

// ReplaySpool plays the spool back and empties it. Each file is claimed by
// renaming it first, so a hook writing at the same moment starts a new one.
// It returns how many events it played.
func (w *Watcher) ReplaySpool() int {
	dir := w.opts.SpoolDir
	if dir == "" {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	oldest := w.now().Add(-hookclient.SpoolMaxAge).UnixMilli()
	claim := ".replay-" + strconv.Itoa(os.Getpid())
	var events []hookclient.Event
	// What the spool held that is not played back: by event, and too old.
	skipped := map[string]int{}
	for _, e := range entries {
		name := e.Name()
		path := filepath.Join(dir, name)
		switch {
		case strings.HasSuffix(name, ".jsonl"):
			// Claimed below.
		case strings.Contains(name, ".jsonl.replay-"):
			// Claimed by an agent that died before it was through: its now.
			claimed := path
			events = append(events, readSpool(claimed, oldest, skipped)...)
			os.Remove(claimed)
			continue
		default:
			continue
		}
		claimed := path + claim
		if os.Rename(path, claimed) != nil {
			continue
		}
		events = append(events, readSpool(claimed, oldest, skipped)...)
		os.Remove(claimed)
	}
	if line := skippedLine(len(events), skipped); line != "" && w.opts.Logf != nil {
		w.opts.Logf("%s", line)
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].At < events[j].At })
	for _, ev := range events {
		ev.Replay = true
		w.Hook(ev)
	}
	return len(events)
}

// droppedEvery is how often the watch counts the tool hooks stui hook could
// not deliver (TakeDropped).
const droppedEvery = time.Minute

// TakeDropped counts the tool hooks stui hook could not deliver in time and
// did not keep (hookclient.DroppedFile), empties the file, and logs one line
// when there were any. It returns how many. A lost Agent PreToolUse and
// PostToolUse is a subagent listed with no description.
func (w *Watcher) TakeDropped() int {
	path := w.opts.DroppedFile
	if path == "" {
		return 0
	}
	claimed := path + ".take-" + strconv.Itoa(os.Getpid())
	if os.Rename(path, claimed) != nil {
		return 0
	}
	defer os.Remove(claimed)
	b, err := os.ReadFile(claimed)
	if err != nil {
		return 0
	}
	by := map[string]int{}
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" && len(l) <= 64 {
			by[l]++
		}
	}
	n, line := countLine(by)
	if n > 0 && w.opts.Logf != nil {
		w.opts.Logf("hook: %d tool hooks did not reach stui in time and were not kept (%s)", n, line)
	}
	return n
}

// countLine is "A 1, B 2" in name order, and the total.
func countLine(by map[string]int) (int, string) {
	n := 0
	names := make([]string, 0, len(by))
	for k, v := range by {
		n += v
		names = append(names, k)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, k := range names {
		parts[i] = k + " " + strconv.Itoa(by[k])
	}
	return n, strings.Join(parts, ", ")
}

// skippedLine is the log line for a replay that left events out, "" when
// it left none: spooled events that are not played back, and ones too old
// (stui hook does not spool tool hooks at all: those are TakeDropped's).
func skippedLine(played int, skipped map[string]int) string {
	if len(skipped) == 0 {
		return ""
	}
	n, line := countLine(skipped)
	return "hook spool: played back " + strconv.Itoa(played) + ", dropped " + strconv.Itoa(n) + " that are not played back (" + line + ")"
}

// readSpool reads one spool file; what it leaves out is counted in skipped
// by event name, "too old" for those past SpoolMaxAge.
func readSpool(path string, oldest int64, skipped map[string]int) []hookclient.Event {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []hookclient.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var ev hookclient.Event
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Pane == "" {
			continue
		}
		if ev.At < oldest {
			skipped["too old"]++
			continue
		}
		if !replayable(ev.Event) {
			name := ev.Event
			if name == "" {
				name = "(no event)"
			}
			skipped[name]++
			continue
		}
		if ev.Data == nil {
			ev.Data = map[string]any{}
		}
		out = append(out, ev)
	}
	return out
}

// spoolSettle is how long after its first look at the panes the watch
// plays the spool back: long enough for a pane's first screen to be read.
var spoolSettle = 2 * time.Second
