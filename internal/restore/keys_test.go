//go:build !windows

package restore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A pane's lasting key (@stui-key) is in the snapshot and on the pane that
// takes its place: a run's records find their agents by it (internal/orch).
func TestRestoreKeepsKeys(t *testing.T) {
	x := sandbox(t)
	ctx := context.Background()
	d := realDir(t)
	must(t, x, "new-session", "-d", "-s", "team", "-n", "lead", "-c", d, "-x", "120", "-y", "40")
	worker := must(t, x, "split-window", "-h", "-t", "=team:0", "-c", d, "-P", "-F", "#{pane_id}")
	must(t, x, "set-option", "-p", "-t", worker, "@stui-key", "k-0123456789ab")
	snap, ok := Capture(ctx, x, nil)
	if !ok {
		t.Fatal("capture failed")
	}
	if p := snap.Sessions[0].Windows[0].Panes[1]; p.Key != "k-0123456789ab" || p.Cwd != d {
		t.Fatalf("captured %+v", p)
	}
	killServer(t, x)
	if _, err := Apply(ctx, x, snap, Options{}); err != nil {
		t.Fatal(err)
	}
	got := must(t, x, "list-panes", "-t", "=team:0", "-F", "#{pane_index} #{@stui-key}")
	if !strings.Contains(got, "1 k-0123456789ab") || strings.Contains(got, "0 k-") {
		t.Fatalf("keys after restore:\n%s", got)
	}
}

// failTmux answers every command the way a server stuck exiting does.
type failTmux struct{}

func (failTmux) Run(_ context.Context, args ...string) (string, error) {
	return "", errors.New(args[0] + ": server exited unexpectedly")
}

// A restore that made nothing says so: no session counts as restored.
func TestApplyReportsWhatFailed(t *testing.T) {
	snap := &Snapshot{Sessions: []Session{{Name: "a", Windows: []Window{{Name: "w", Panes: []Pane{{Cwd: "/"}}}}}, {Name: "b", Windows: []Window{{Name: "w", Panes: []Pane{{Cwd: "/"}}}}}}}
	res, err := Apply(context.Background(), failTmux{}, snap, Options{})
	if err == nil || !Stuck(err) {
		t.Fatalf("err %v", err)
	}
	if len(res.Restored()) != 0 || strings.Join(res.Failed(), ",") != "a,b" || res.Windows != 0 {
		t.Fatalf("result %+v", res)
	}
}

// The last snapshot of the server before -- what a death took away -- and
// the one the last restore came from outlive the generations after it.
func TestPruneKeepsWhatARestoreNeeds(t *testing.T) {
	st := Store{Dir: t.TempDir(), Keep: 3}
	now := time.Now()
	save := func(pid, i int) *Snapshot {
		s := &Snapshot{V: 1, Server: Server{PID: pid, Start: int64(pid)}, Sessions: []Session{{Name: "s", Windows: []Window{{Index: i}}}}}
		saved, _, err := st.Save(s, now.Add(time.Duration(pid*100+i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		return saved
	}
	save(1, 0)
	dead := save(1, 1)
	for i := 0; i < 5; i++ {
		save(2, i)
	}
	ids := map[string]bool{}
	for _, s := range st.List() {
		ids[s.ID] = true
	}
	if len(ids) != 4 || !ids[dead.ID] {
		t.Fatalf("kept %v, want the newest 3 and %s", ids, dead.ID)
	}
}

func TestDefaultSocketFallsBackLikeTmux(t *testing.T) {
	tmp := Canonical(filepath.Join("/tmp", "x"))
	tmp = filepath.Dir(tmp)
	missing := filepath.Join(t.TempDir(), "never-made")
	if got := DefaultSocket([]string{"TMUX_TMPDIR=" + missing}); !strings.HasPrefix(got, tmp+"/tmux-") {
		t.Errorf("missing TMUX_TMPDIR: %s, want under %s (tmux falls back)", got, tmp)
	}
	have := t.TempDir()
	if got := DefaultSocket([]string{"TMUX_TMPDIR=" + have}); !strings.HasPrefix(got, Canonical(have + "/x")[:len(Canonical(have+"/x"))-1]) {
		t.Errorf("existing TMUX_TMPDIR: %s", got)
	}
	if got := socketOf(os.Getpid(), "tmux -S /tmp/stui-test/s -C attach-session"); got != Canonical("/tmp/stui-test/s") {
		t.Errorf("-S: %s", got)
	}
}
