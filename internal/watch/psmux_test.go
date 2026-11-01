package watch

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/sophonz-labs/stui/internal/hookclient"
)

// fakePsmux is psmux with a server per session, each numbering its panes
// from %1, answering list-panes -a for all of them as psmux v3.3.8 does.
type fakePsmux struct {
	mu      sync.Mutex
	servers map[string][]string // pid -> lines of paneFields (no pid)
	screens map[string]string
	asked   [][]string
}

func (f *fakePsmux) Run(ctx context.Context, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, args)
	switch args[0] {
	case "list-panes":
		format := args[len(args)-1]
		var b strings.Builder
		for pid, lines := range f.servers {
			for _, l := range lines {
				if format == psmuxPaneFields {
					b.WriteString(pid + fsep + l + "\r\n")
				} else {
					session := strings.Split(l, fsep)[1]
					b.WriteString(pid + fsep + session + "\r\n")
				}
			}
		}
		return b.String(), nil
	case "capture-pane":
		return f.screens[args[len(args)-1]], nil
	}
	return "", nil
}

func psmuxRow(id, session string, window int, command, title string) string {
	return strings.Join([]string{id, session, "", string(rune('0' + window)), "0", command, "win", "/dev/pty" + id[1:], "", title}, fsep)
}

func TestPsmuxPanesAreNamedBySession(t *testing.T) {
	f := &fakePsmux{
		servers: map[string][]string{
			"111": {psmuxRow("%1", "work", 0, `C:\Users\me\.local\bin\claude.exe`, "t")},
			"222": {psmuxRow("%1", "play", 0, "pwsh", "t"), psmuxRow("%2", "play", 1, "claude --resume abc", "t")},
		},
		screens: map[string]string{"work:%1": screen(t, "claude-idle.txt")},
	}
	w := New(Options{Tmux: f, ClaudeDir: t.TempDir(), Psmux: true})
	w.poll(context.Background())
	got := map[string]string{}
	for _, p := range w.Snapshot().Panes {
		got[p.ID] = p.Session + "/" + p.Agent
	}
	want := map[string]string{"work:%1": "work/claude", "play:%2": "play/claude"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("agent panes %v, want %v", got, want)
	}
	// Both servers' %1 are known, apart; no ps is asked of a tty.
	w.mu.Lock()
	if len(w.panes) != 3 || w.panes["play:%1"] == nil {
		t.Errorf("panes %v", w.panes)
	}
	for id, p := range w.panes {
		if p.tty != "" {
			t.Errorf("%s has tty %q", id, p.tty)
		}
	}
	w.mu.Unlock()

	// A hook from server 222's %2 is play's pane, and says where its
	// conversation is.
	w.Hook(hookclient.Event{Kind: "claude", Pane: "%2", Socket: "/tmp/psmux-222/default", Event: "UserPromptSubmit",
		Data: map[string]any{"session_id": "s-1", "transcript_path": `C:\Users\me\.claude\projects\x\s-1.jsonl`}})
	if p := pane(t, w, "play:%2"); p.State != Working {
		t.Fatalf("play:%%2 %+v", p)
	}
	if p := pane(t, w, "work:%1"); p.State == Working {
		t.Fatalf("work:%%1 took play's hook: %+v", p)
	}
	tr, ok := w.Transcript("play:%2")
	if !ok || tr.SessionID != "s-1" || !strings.HasSuffix(tr.Path, "s-1.jsonl") {
		t.Fatalf("transcript %+v %v", tr, ok)
	}
}

// A session made since the last poll: its server is asked for.
func TestPsmuxHookFromNewServer(t *testing.T) {
	f := &fakePsmux{servers: map[string][]string{"111": {psmuxRow("%1", "work", 0, "pwsh", "t")}}, screens: map[string]string{}}
	w := New(Options{Tmux: f, ClaudeDir: t.TempDir(), Psmux: true})
	w.poll(context.Background())
	f.mu.Lock()
	f.servers["333"] = []string{psmuxRow("%1", "new", 0, "claude", "t")}
	f.mu.Unlock()
	w.Hook(hookclient.Event{Kind: "claude", Pane: "%1", Socket: "/tmp/psmux-333/default", Event: "SessionStart", Data: map[string]any{"session_id": "z"}})
	if got := w.PaneResume()["new:%1"]; got.ID != "z" {
		t.Fatalf("%+v", w.PaneResume())
	}
	// One nobody knows is dropped, not guessed at.
	w.Hook(hookclient.Event{Kind: "claude", Pane: "%1", Socket: "/tmp/psmux-999/default", Event: "SessionStart", Data: map[string]any{"session_id": "q"}})
	for id := range w.PaneResume() {
		if !strings.Contains(id, ":") {
			t.Fatalf("pane %q", id)
		}
	}
}

func TestPsmuxArgs(t *testing.T) {
	for _, c := range []struct{ in, want []string }{
		{[]string{"send-keys", "-t", "w:%1", "-l", "--", "hello"}, []string{"send-keys", "-t", "w:.%1", "-l", "hello"}},
		{[]string{"send-keys", "-t", "w:%1", "-l", "--", "-l"}, []string{"send-keys", "-t", "w:.%1", "-l", "-", "l"}},
		{[]string{"send-keys", "-t", "w:%1", "-l", "--", "-x"}, []string{"send-keys", "-t", "w:.%1", "-l", "-x"}},
		{[]string{"send-keys", "-t", "w:%1", "C-u", "Enter"}, []string{"send-keys", "-t", "w:.%1", "C-u", "Enter"}},
		{[]string{"send-keys", "-t", "w:%1", "-X", "cancel"}, []string{"send-keys", "-t", "w:.%1", "-X", "cancel"}},
		{[]string{"capture-pane", "-p", "-t", "w:%1"}, []string{"capture-pane", "-p", "-t", "w:.%1"}},
		// Every command's -t, as psmux 3.4.0 finds a pane (mux.PsmuxTarget).
		{[]string{"display-message", "-p", "-t", "w:%2", "#{pane_id}"}, []string{"display-message", "-p", "-t", "w:.%2", "#{pane_id}"}},
		{[]string{"set-option", "-p", "-t", "w:%2", "@stui-run", "r"}, []string{"set-option", "-p", "-t", "w:.%2", "@stui-run", "r"}},
		{[]string{"list-panes", "-s", "-t", "w", "-F", "#{pane_id}"}, []string{"list-panes", "-s", "-t", "w", "-F", "#{pane_id}"}},
		{[]string{"new-window", "-t", "=w:", "-P"}, []string{"new-window", "-t", "=w:", "-P"}},
	} {
		if got := PsmuxArgs(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPsmuxCommand(t *testing.T) {
	for in, want := range map[string]string{
		"claude":                                  "claude",
		"claude.exe":                              "claude",
		"claude --resume abc":                     "claude",
		`C:\Program Files\PowerShell\7\pwsh.exe`:  "pwsh",
		`"C:\Program Files\nodejs\node.EXE" x.js`: "node",
		"codex --cd C:\\src":                      "codex",
		"2.1.288":                                 "2.1.288",
	} {
		if got := psmuxCommand(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestPsmuxServer(t *testing.T) {
	for in, want := range map[string]string{
		"/tmp/psmux-4242/default": "4242",
		"/tmp/psmux-4242/work":    "4242",
		"/tmp/tmux-501/default":   "",
		"/tmp/psmux-x/default":    "",
		"":                        "",
	} {
		if got := psmuxServer(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
