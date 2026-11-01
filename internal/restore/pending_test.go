package restore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// fakeTmux answers list-sessions with out, or err.
type fakeTmux struct {
	out string
	err error
}

func (f fakeTmux) Run(ctx context.Context, args ...string) (string, error) { return f.out, f.err }

func TestProbeKnowsWhatItCannotTell(t *testing.T) {
	cases := []struct {
		f    fakeTmux
		want ServerState
	}{
		{fakeTmux{out: "123|&|1792500000\n"}, Known},
		// Under load: no start time, or killed for taking too long.
		{fakeTmux{out: "123|&|0\n"}, Unknown},
		{fakeTmux{out: "123|&|\n"}, Unknown},
		{fakeTmux{err: errors.New("signal: killed")}, Unknown},
		{fakeTmux{err: errors.New("list-sessions -F: no server running on /tmp/tmux-501/default")}, NoServer},
	}
	for _, c := range cases {
		if _, got := Probe(context.Background(), c.f); got != c.want {
			t.Errorf("%+v: %v, want %v", c.f, got, c.want)
		}
		if _, ok := ServerOf(context.Background(), c.f); ok != (c.want == Known) {
			t.Errorf("%+v: ServerOf ok %v", c.f, ok)
		}
	}
	// A record from before start times were required matches by pid.
	if !(Server{PID: 5}).Same(Server{PID: 5, Start: 9}) || (Server{PID: 5, Start: 8}).Same(Server{PID: 5, Start: 9}) {
		t.Error("Same")
	}
}

// The hooks lost the conversation ids with the server; the runs' records
// have them by the panes' lasting keys.
func TestResumeByRunRecord(t *testing.T) {
	snap := &Snapshot{Sessions: []Session{{Name: "app", Windows: []Window{{Panes: []Pane{
		{ID: "%1", Key: "k-coord", Command: "claude", Agent: &Agent{Kind: "claude"}},
		{ID: "%2", Key: "k-w", Command: "node"},   // Claude Code under node: no kind from the command
		{ID: "%3", Key: "k-idle", Command: "zsh"}, // its agent had exited
		{ID: "%4", Key: "k-hook", Command: "claude", Agent: &Agent{Kind: "claude", ID: "from-hook"}},
	}}}}}}
	keys := map[string]Agent{
		"k-coord": {ID: "c-1"}, "k-w": {ID: "w-1"}, "k-idle": {Kind: "claude", ID: "i-1"}, "k-hook": {Kind: "claude", ID: "older"},
	}
	has := func(a *Agent) bool { return a.Kind == "codex" && a.ID == "w-1" || a.Kind == "claude" && a.ID != "w-1" }
	o := Options{ResumeAgents: true, HasRecord: has}
	got := snap.WithKeys(keys, has)
	if snap.Sessions[0].Windows[0].Panes[0].Agent.ID != "" {
		t.Fatal("the snapshot itself was changed")
	}
	want := []string{"claude --resume c-1", "codex resume w-1", "", "claude --resume from-hook"}
	for i, p := range got.Sessions[0].Windows[0].Panes {
		d := Decide(p, o)
		if d.Line != want[i] || (want[i] != "" && d.Action != Run) {
			t.Errorf("pane %s: %+v, want %q", p.ID, d, want[i])
		}
	}
	info := Describe(snap, Options{ResumeAgents: true, HasRecord: has, Keys: keys}, map[string][]string{"k-coord": {"r-1"}, "k-w": {"r-1"}})
	if info.Resumes != 3 || info.Agents["claude"] != 2 || info.Agents["codex"] != 1 || len(info.Runs) != 1 {
		t.Fatalf("%+v", info)
	}
}

func TestPendingAndDecline(t *testing.T) {
	st := Store{Dir: filepath.Join(t.TempDir(), "s")}
	now := time.Now()
	dead := fakeTmux{err: errors.New("no server running on /tmp/x")}
	if p, sure := FindPending(context.Background(), dead, st, now); p != nil || !sure {
		t.Fatal("pending with no snapshot")
	}
	snap := &Snapshot{V: 1, Server: Server{PID: 7, Start: 70}, Sessions: []Session{{Name: "a", Windows: []Window{{Panes: []Pane{{ID: "%0"}}}}}}}
	if _, _, err := st.Save(snap, now); err != nil {
		t.Fatal(err)
	}
	if p, _ := FindPending(context.Background(), dead, st, now); p == nil || p.ID != snap.ID {
		t.Fatal("not pending after a death")
	}
	// The same server: not a death.
	if p, _ := FindPending(context.Background(), fakeTmux{out: "7|&|70\n"}, st, now); p != nil {
		t.Fatal("pending on its own server")
	}
	// Not known which: nothing decided.
	if p, sure := FindPending(context.Background(), fakeTmux{out: "7|&|0\n"}, st, now); p != nil || sure {
		t.Fatal("decided while unsure")
	}
	// Too old.
	if p, _ := FindPending(context.Background(), dead, st, now.Add(RestoreWithin+time.Hour)); p != nil {
		t.Fatal("pending after days")
	}
	if err := st.Decline(snap.ID, now); err != nil {
		t.Fatal(err)
	}
	if p, _ := FindPending(context.Background(), dead, st, now); p != nil {
		t.Fatal("pending after a decline")
	}
	if m := st.Marker(); m == nil || !m.Declined || m.From != snap.ID {
		t.Fatalf("marker %+v", m)
	}
	st.SetMarker(&Marker{From: snap.ID, Auto: true})
	if err := st.Decline(snap.ID, now); err == nil {
		t.Fatal("declined one already restored")
	}
}
