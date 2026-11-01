package watch

// Which conversation a Claude pane is in when no hook has said: a Claude
// that started before stui did and has sent no hook since (an idle one that
// has run for days) is known from its screen only, and the chat view had no
// session id to find its transcript by.
//
// Claude Code (2.1.284 to 2.1.291) keeps a small record of every running
// interactive process in its config directory:
//
//	<claude config dir>/sessions/<pid>.json
//	{"pid": 19712, "sessionId": "...", "cwd": "...", "startedAt": <ms>,
//	 "procStart": "Wed Oct  7 01:59:18 2026", "updatedAt": <ms>, ...}
//
// stui finds the pane's Claude among the foreground processes on its tty and
// reads that file, for sessionId and cwd only. The other files in that
// directory (<pid>.<hash>.key) are never opened. Hooks win: this is asked
// only when they have not named the conversation.

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// Proc is a process on a pane's tty and when it started (zero: unknown).
type Proc struct {
	PID   int
	Start time.Time
}

type claudeSessionEntry struct {
	mtime time.Time
	size  int64
	file  claudeSessionFile
	ok    bool
}

type claudeSessionFile struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	StartedAt int64  `json:"startedAt"`
	ProcStart string `json:"procStart"`
}

type fgProcsEntry struct {
	procs []Proc
	at    time.Time
}

// How long a tty's foreground processes, once read, are taken as they were:
// the chat view asks on every request.
const fgProcsTTL = 5 * time.Second

// How much earlier than the process a session file may say it started and
// still be that process's (ps's start time is to the second; a file of a
// process that had the same pid before is older than that).
const claudeStartSlack = 30 * time.Second

// lstart is ps's (and Claude's procStart's) way of writing a start time.
const lstart = "Mon Jan _2 15:04:05 2006"

// claudeSession is the conversation the Claude in the foreground on tty is
// in, from its session file in one of dirs: its id and directory; "" when
// there is none to trust.
func (w *Watcher) claudeSession(tty string, dirs []string) (sid, cwd string) {
	if tty == "" || len(dirs) == 0 {
		return "", ""
	}
	procs := w.ttyProcs(tty)
	sort.Strings(dirs)
	for _, pr := range procs {
		var best claudeSessionFile
		var bestAt time.Time
		for _, d := range dirs {
			f, at, ok := w.readClaudeSession(filepath.Join(d, "sessions", strconv.Itoa(pr.PID)+".json"))
			if ok && f.PID == pr.PID && plausibleStart(f, pr.Start) && (best.SessionID == "" || at.After(bestAt)) {
				best, bestAt = f, at
			}
		}
		if best.SessionID != "" {
			if !filepath.IsAbs(best.Cwd) {
				best.Cwd = ""
			}
			return best.SessionID, best.Cwd
		}
	}
	return "", ""
}

// ttyProcs is opts.Procs(tty), kept for a moment.
func (w *Watcher) ttyProcs(tty string) []Proc {
	now := time.Now()
	w.claudeSessMu.Lock()
	if e, ok := w.fgProcs[tty]; ok && now.Sub(e.at) < fgProcsTTL {
		w.claudeSessMu.Unlock()
		return e.procs
	}
	w.claudeSessMu.Unlock()
	procs := w.opts.Procs(tty)
	w.claudeSessMu.Lock()
	if w.fgProcs == nil || len(w.fgProcs) > 256 {
		w.fgProcs = map[string]fgProcsEntry{}
	}
	w.fgProcs[tty] = fgProcsEntry{procs: procs, at: now}
	w.claudeSessMu.Unlock()
	return procs
}

// readClaudeSession reads one session file, again only when it changed.
func (w *Watcher) readClaudeSession(path string) (claudeSessionFile, time.Time, bool) {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return claudeSessionFile{}, time.Time{}, false
	}
	w.claudeSessMu.Lock()
	if e, ok := w.claudeSess[path]; ok && e.mtime.Equal(st.ModTime()) && e.size == st.Size() {
		w.claudeSessMu.Unlock()
		return e.file, e.mtime, e.ok
	}
	w.claudeSessMu.Unlock()
	f, ok := parseClaudeSession(path)
	w.claudeSessMu.Lock()
	if w.claudeSess == nil || len(w.claudeSess) > 256 {
		w.claudeSess = map[string]claudeSessionEntry{}
	}
	w.claudeSess[path] = claudeSessionEntry{mtime: st.ModTime(), size: st.Size(), file: f, ok: ok}
	w.claudeSessMu.Unlock()
	return f, st.ModTime(), ok
}

func parseClaudeSession(path string) (claudeSessionFile, bool) {
	fh, err := os.Open(path)
	if err != nil {
		return claudeSessionFile{}, false
	}
	defer fh.Close()
	b, err := io.ReadAll(io.LimitReader(fh, 64<<10))
	if err != nil {
		return claudeSessionFile{}, false
	}
	var f claudeSessionFile
	if json.Unmarshal(b, &f) != nil || f.PID <= 0 || !safeID.MatchString(f.SessionID) {
		return claudeSessionFile{}, false
	}
	return f, true
}

// plausibleStart says whether f can be the record of a process that started
// at start: not one written by an earlier process that had the same pid.
func plausibleStart(f claudeSessionFile, start time.Time) bool {
	if start.IsZero() {
		return true
	}
	var began time.Time
	switch {
	case f.StartedAt > 0:
		began = time.UnixMilli(f.StartedAt)
	case f.ProcStart != "":
		t, err := time.ParseInLocation(lstart, f.ProcStart, time.Local)
		if err != nil {
			return true
		}
		began = t
	default:
		return true
	}
	return !began.Before(start.Add(-claudeStartSlack))
}
