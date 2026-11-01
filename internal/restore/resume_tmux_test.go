//go:build !windows

package restore

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// keys records what is typed into panes, and sends it on.
type keys struct {
	Tmux
	mu   sync.Mutex
	sent []string
}

func (k *keys) Run(ctx context.Context, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "send-keys" {
		k.mu.Lock()
		k.sent = append(k.sent, strings.Join(args[1:], " "))
		k.mu.Unlock()
	}
	return k.Tmux.Run(ctx, args...)
}

// On a real server: the agent whose conversation is on record gets its line
// entered, the one without gets it typed only, and the command is shown and
// not started. The counts say so.
func TestApplyEntersOnlyRecordedAgents(t *testing.T) {
	x := sandbox(t)
	ctx := context.Background()
	must(t, x, "new-session", "-d", "-s", "keep")
	dir := t.TempDir()
	snap := &Snapshot{V: 1, ID: "1", Taken: time.Now().UnixMilli(), Sessions: []Session{{
		Name: "work",
		Windows: []Window{{Index: 0, Name: "agents", Active: true, Panes: []Pane{
			{Index: 0, Cwd: dir, Command: "claude", Agent: &Agent{Kind: "claude", ID: "kept-1"}, Active: true},
			{Index: 1, Cwd: dir, Command: "claude", Agent: &Agent{Kind: "claude", ID: "gone-2"}},
			{Index: 2, Cwd: dir, Command: "sleep", Args: "sleep 1000"},
		}}},
	}}}
	k := &keys{Tmux: x}
	res, err := Apply(ctx, k, snap, Options{
		ResumeAgents: true, StoppedAt: time.Now(),
		HasRecord: func(a *Agent) bool { return a.ID == "kept-1" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Resume != (Counts{Resumed: 1, Typed: 1, NoRecord: 1, Commands: 1}) {
		t.Fatalf("%+v", res.Resume)
	}
	pane := map[string]string{}
	entered := map[string]bool{}
	for _, s := range k.sent {
		f := strings.Fields(s)
		// "-t %N -l <line…>" or "-t %N Enter"
		if len(f) >= 3 && f[2] == "-l" {
			pane[strings.Join(f[3:], " ")] = f[1]
		}
		if len(f) == 3 && f[2] == "Enter" {
			entered[f[1]] = true
		}
	}
	kept, gone := pane["claude --resume kept-1"], pane["claude --resume gone-2"]
	if kept == "" || gone == "" {
		t.Fatalf("typed %v", k.sent)
	}
	if !entered[kept] {
		t.Error("the recorded agent's line was not entered")
	}
	if entered[gone] {
		t.Error("the line of an agent with no record was entered")
	}
	if len(entered) != 1 {
		t.Errorf("Enter went to %v", entered)
	}
	// The command was not started.
	waitFor(t, "shells", func() bool {
		return !strings.Contains(must(t, x, "list-panes", "-a", "-F", "#{pane_current_command}"), "sleep")
	})
	// The pane without a record says why its line waits.
	waitFor(t, "the note", func() bool {
		return strings.Contains(must(t, x, "capture-pane", "-pJ", "-t", gone), "no record of conversation gone-2")
	})
}
