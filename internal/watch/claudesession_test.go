package watch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sophonz-labs/stui/internal/chat"
)

const screenSession = "7a2b3c4d-0000-4000-8000-000000000002"

func writeClaudeSession(t *testing.T, dir string, pid int, rec map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(dir, "sessions", strconv.Itoa(pid)+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A Claude that started before stui did and has sent no hook since is known
// from its screen only: the chat view finds its conversation through
// Claude's session file for the pane's process, and nothing of it is kept.
func TestTranscriptFromClaudeSessionFile(t *testing.T) {
	r := newPersistRig(t)
	dir := t.TempDir()
	start := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	var asked atomic.Int32
	r.procs = func(tty string) []Proc {
		asked.Add(1)
		if tty != "/dev/ttys001" {
			return nil
		}
		// A helper first, the Claude after it.
		return []Proc{{PID: 4100, Start: start}, {PID: 4242, Start: start}}
	}
	writeClaudeSession(t, dir, 4242, map[string]any{
		"pid": 4242, "sessionId": screenSession, "cwd": "/home/u/p",
		"startedAt": start.Add(800 * time.Millisecond).UnixMilli(), "procStart": start.Format(lstart),
		"updatedAt": time.Now().UnixMilli(), "status": "idle",
	})
	// Never opened.
	key := filepath.Join(dir, "sessions", "4242.0123abcd.key")
	if err := os.WriteFile(key, []byte("{}"), 0o000); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(dir, "projects", "-home-u-p")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(project, screenSession+".jsonl")
	if err := os.WriteFile(file, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	w := r.start(t, dir)
	w.poll(context.Background())
	tr, ok := w.Transcript("%1")
	if !ok || tr.Agent != "claude" || tr.SessionID != screenSession || tr.Cwd != "/home/u/p" || tr.Path != "" || tr.MovedTo != "" {
		t.Fatalf("%+v %v", tr, ok)
	}
	got := chat.Locate(chat.Source{Agent: tr.Agent, SessionID: tr.SessionID, Path: tr.Path, Cwd: tr.Cwd, ClaudeDirs: tr.ClaudeDirs})
	if got != file {
		t.Fatalf("located %q", got)
	}

	// Kept for a moment: no ps on every request.
	n := asked.Load()
	w.Transcript("%1")
	if asked.Load() != n {
		t.Fatal("ps asked again")
	}

	// For this answer only: not the pane's conversation to resume, not in
	// the state file, the snapshot or an event.
	w.mu.Lock()
	resume := w.panes["%1"].resumeID
	w.mu.Unlock()
	if resume != "" {
		t.Fatalf("resumeID %q", resume)
	}
	w.Flush()
	b, _ := os.ReadFile(r.path)
	snap, _ := json.Marshal(w.Snapshot())
	r.sink.mu.Lock()
	ev, _ := json.Marshal(r.sink.events)
	r.sink.mu.Unlock()
	for _, s := range []string{string(b), string(snap), string(ev)} {
		if strings.Contains(s, screenSession) || strings.Contains(s, "/home/u/p") {
			t.Fatalf("leaked: %s", s)
		}
	}

	// /clear in that Claude: its file names the new conversation.
	second := "8b3c4d5e-0000-4000-8000-000000000003"
	writeClaudeSession(t, dir, 4242, map[string]any{
		"pid": 4242, "sessionId": second, "cwd": "/home/u/p", "startedAt": start.UnixMilli(), "status": "idle",
		"updatedAt": time.Now().UnixMilli() + 1,
	})
	if tr, _ := w.Transcript("%1"); tr.SessionID != second {
		t.Fatalf("after /clear: %+v", tr)
	}

	// Hooks win once they speak.
	shook(w, "UserPromptSubmit", map[string]any{"cwd": "/home/u/q"})
	if tr, _ := w.Transcript("%1"); tr.SessionID != testSession || tr.Cwd != "/home/u/q" {
		t.Fatalf("hooks: %+v", tr)
	}
}

// A session file that is not this process's is not trusted: a pid it does
// not name, a process that had the pid before, an id that is not one.
func TestClaudeSessionFileChecks(t *testing.T) {
	start := time.Now().Add(-time.Hour).Truncate(time.Second)
	for name, rec := range map[string]map[string]any{
		"other pid":    {"pid": 4243, "sessionId": screenSession, "startedAt": start.UnixMilli()},
		"older":        {"pid": 4242, "sessionId": screenSession, "startedAt": start.Add(-2 * time.Hour).UnixMilli()},
		"older lstart": {"pid": 4242, "sessionId": screenSession, "procStart": start.Add(-2 * time.Hour).Format(lstart)},
		"bad id":       {"pid": 4242, "sessionId": "../../etc/passwd", "startedAt": start.UnixMilli()},
		"no id":        {"pid": 4242, "startedAt": start.UnixMilli()},
	} {
		t.Run(name, func(t *testing.T) {
			r := newPersistRig(t)
			dir := t.TempDir()
			r.procs = func(string) []Proc { return []Proc{{PID: 4242, Start: start}} }
			writeClaudeSession(t, dir, 4242, rec)
			w := r.start(t, dir)
			w.poll(context.Background())
			if tr, ok := w.Transcript("%1"); !ok || tr.SessionID != "" || tr.Cwd != "" {
				t.Fatalf("%+v %v", tr, ok)
			}
		})
	}
	// A start stui could not read: the pid alone.
	r := newPersistRig(t)
	dir := t.TempDir()
	r.procs = func(string) []Proc { return []Proc{{PID: 4242}} }
	writeClaudeSession(t, dir, 4242, map[string]any{"pid": 4242, "sessionId": screenSession, "cwd": "rel/dir"})
	w := r.start(t, dir)
	w.poll(context.Background())
	if tr, _ := w.Transcript("%1"); tr.SessionID != screenSession || tr.Cwd != "" {
		t.Fatalf("%+v", tr)
	}
	// Not a Claude pane: never looked for.
	if _, ok := w.Transcript("%2"); ok {
		t.Fatal("shell pane")
	}
}

func TestParseForeground(t *testing.T) {
	out := " 100 100 200 01:00:00\n 201 200 200 00:29\n 200 200 200 00:30\n 202 200 200 bad\n"
	got := parseForeground(out)
	if len(got) != 2 || got[0].pid != 200 || got[0].elapsed != 30*time.Second || got[1].pid != 201 {
		t.Fatalf("%+v", got)
	}
}
