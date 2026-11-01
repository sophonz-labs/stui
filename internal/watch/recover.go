package watch

// Subagents that started before stui did (section 1, "Across a restart").
//
// A subagent stui has no record of announces itself with its own hooks
// (agent_id set) as soon as it uses a tool. Claude Code keeps a small
// record of every Agent tool run next to the conversation:
//
//	<claude config dir>/projects/<project>/<session id>/subagents/agent-<agent id>.meta.json
//	{"agentType": "...", "description": "...", "toolUseId": "...", ...}
//
// (Claude Code 2.1.284 to 2.1.288; written when the run starts.) That file
// existing is what makes such a hook a subagent worth listing, not a helper
// Claude never announced; the hook itself is what makes it running. Its
// type and description come from the file, its start from the file's
// time. Nothing else of it is read: the transcript beside it is never
// opened. A subagent that sends no hook (stuck in one long tool call) is
// listed once it does; one that ended while stui was down is never listed,
// which is the point: only hooks say "running".

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/sophonz-labs/stui/internal/proc"
)

type foundSubagent struct {
	typ, desc string
	started   time.Time
}

// safeID is what a session or agent id may be to go into a path.
var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// unknownSubLocked reports whether subagent aid of pane is one stui has no
// record of at all, and has not looked up already.
func (w *Watcher) unknownSubLocked(pane, aid string) bool {
	if w.adoptTried[aid] {
		return false
	}
	if s := w.restore[pane]; s != nil {
		for _, it := range s.Items {
			if it.ID == aid {
				return false
			}
		}
	}
	p := w.panes[pane]
	return p == nil || p.running[aid] == nil && !p.lost[aid] && p.subItems[aid] == nil
}

// findSubagent reads Claude's record of subagent aid of conversation sid,
// or nil when there is none.
func findSubagent(dirs []string, sid, aid string) *foundSubagent {
	if !safeID.MatchString(sid) || !safeID.MatchString(aid) {
		return nil
	}
	for _, d := range dirs {
		matches, _ := filepath.Glob(filepath.Join(d, "projects", "*", sid, "subagents", "agent-"+aid+".meta.json"))
		for _, m := range matches {
			if f := readSubagentMeta(m); f != nil {
				return f
			}
		}
	}
	return nil
}

func readSubagentMeta(path string) *foundSubagent {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil {
		return nil
	}
	var m struct {
		AgentType   string `json:"agentType"`
		Description string `json:"description"`
	}
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return &foundSubagent{
		typ:     clipRunes(m.AgentType, 100),
		desc:    clipRunes(strings.Join(strings.Fields(m.Description), " "), 80),
		started: fi.ModTime(),
	}
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// descTries is how many of a subagent's hooks look for its description in
// Claude's record: the file is written just after SubagentStart, so the
// first may come too soon.
const descTries = 3

// blankDescLocked reports whether subagent aid of pane is listed, or about
// to be from the saved record, without a description, and has not been
// looked up descTries times.
func (w *Watcher) blankDescLocked(pane, aid string) bool {
	if w.descTried[aid] >= descTries {
		return false
	}
	if p := w.panes[pane]; p != nil {
		if it := p.subItems[aid]; it != nil {
			return it.Description == ""
		}
	}
	if s := w.restore[pane]; s != nil {
		for _, it := range s.Items {
			if it.ID == aid {
				return it.Description == ""
			}
		}
	}
	return false
}

// descFill gives listed subagent id the description in Claude's record of
// it, when it has none. Its description comes from the main agent's Agent
// call (subagents.go); a call whose hook never reached stui -- not delivered
// in time, spooled, and not played back (spool.go) -- left it empty, and
// Claude's record is where it is for certain.
func descFill(p *paneState, id string, f *foundSubagent) {
	it := p.subItems[id]
	if it == nil || it.Description != "" || f.desc == "" {
		return
	}
	it.Description = f.desc
	if it.Type == "" {
		it.Type = f.typ
	}
}

// adoptSubagentLocked lists subagent id, found in Claude's records, as
// running.
func (w *Watcher) adoptSubagentLocked(p *paneState, id, hookType string, f *foundSubagent, now time.Time) {
	if p.running == nil {
		p.running = map[string]*subRun{}
	}
	// Adopted because one of its own hooks came: heard from.
	p.running[id] = &subRun{seen: now.UnixMilli(), heard: true}
	if p.subItems == nil {
		p.subItems = map[string]*SubagentItem{}
	}
	typ := f.typ
	if typ == "" {
		typ = hookType
	}
	started := f.started.UnixMilli()
	if started <= 0 || started > now.UnixMilli() {
		started = now.UnixMilli()
	}
	p.subItems[id] = &SubagentItem{ID: id, Type: typ, Description: f.desc, State: SubRunning, StartedAt: started}
	p.pruneSubItems()
	p.Subagents.Running = len(p.running)
}

// ---- process start

// foregroundStart is when the foreground process on tty started, from ps.
func foregroundStart(tty string) (time.Time, bool) {
	procs := foregroundProcs(tty)
	if len(procs) == 0 {
		return time.Time{}, false
	}
	return procs[0].Start, true
}

// foregroundProcs is the foreground process group on tty, its leader first,
// from ps; nil on Windows or when ps does not answer.
func foregroundProcs(tty string) []Proc {
	if runtime.GOOS == "windows" || tty == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ps", "-o", "pid=,pgid=,tpgid=,etime=", "-t", strings.TrimPrefix(tty, "/dev/"))
	proc.Hide(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	now := time.Now()
	var procs []Proc
	for _, f := range parseForeground(string(out)) {
		procs = append(procs, Proc{PID: f.pid, Start: now.Add(-f.elapsed)})
	}
	return procs
}

type fgProc struct {
	pid     int
	elapsed time.Duration
}

// parseForeground picks the foreground process group from ps output (pid,
// pgid, tpgid, etime): its leader first, then the other members.
func parseForeground(out string) []fgProc {
	var leader, members []fgProc
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[1] != f[2] {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		el, ok := parseEtime(f[3])
		if err != nil || pid <= 0 || !ok {
			continue
		}
		if f[0] == f[2] {
			leader = append(leader, fgProc{pid, el})
		} else {
			members = append(members, fgProc{pid, el})
		}
	}
	return append(leader, members...)
}

// foregroundElapsed is how long the foreground process group's leader (or,
// without one, any member) has run, from ps output (pid, pgid, tpgid,
// etime).
func foregroundElapsed(out string) (time.Duration, bool) {
	procs := parseForeground(out)
	if len(procs) == 0 {
		return 0, false
	}
	return procs[0].elapsed, true
}

// parseEtime reads ps's etime: [[dd-]hh:]mm:ss.
func parseEtime(s string) (time.Duration, bool) {
	var days int
	if d, rest, ok := strings.Cut(s, "-"); ok {
		n, err := strconv.Atoi(d)
		if err != nil {
			return 0, false
		}
		days, s = n, rest
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	total := 0
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return 0, false
		}
		total = total*60 + n
	}
	return time.Duration(days)*24*time.Hour + time.Duration(total)*time.Second, true
}
