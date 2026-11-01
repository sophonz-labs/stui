//go:build !windows

package restore

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sandbox is a tmux server of the test's own: its own socket, no tmux.conf,
// nothing inherited that could point it at anybody else's server.
func sandbox(t *testing.T) Exec {
	t.Helper()
	bin, err := exec.LookPath("tmux")
	if err != nil {
		if b := os.Getenv("STUI_TMUX_BIN"); b != "" {
			bin = b
		} else if os.Getenv("STUI_REQUIRE_TMUX") != "" {
			t.Fatal("no tmux, and STUI_REQUIRE_TMUX says these must run")
		} else {
			t.Skip("no tmux")
		}
	}
	dir, err := os.MkdirTemp("/tmp", "strt")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "s")
	// No zsh new-user wizard (Debian's zsh): it reads in cooked mode and
	// fills the screen, over a restored pane's note.
	os.WriteFile(filepath.Join(dir, ".zshrc"), nil, 0o644)
	x := Exec{
		Bin:    bin,
		Prefix: []string{"-f", "/dev/null", "-S", sock},
		Env:    []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "SHELL=" + shellForTest(), "TERM=xterm-256color", "LANG=C"},
	}
	t.Cleanup(func() {
		x.Run(context.Background(), "kill-server")
		os.RemoveAll(dir)
	})
	return x
}

// killServer ends the sandbox's server and waits until it is gone: tmux
// answers kill-server before the server lets go of its socket, and a
// new-session in between gets "server exited unexpectedly" (Linux, tmux 3.4).
func killServer(t *testing.T, x Exec) {
	t.Helper()
	must(t, x, "kill-server")
	sock := x.Prefix[len(x.Prefix)-1]
	waitFor(t, "the old server to go", func() bool {
		if _, err := os.Stat(sock); os.IsNotExist(err) {
			return true
		}
		_, err := x.Run(context.Background(), "list-sessions")
		return err != nil && !strings.Contains(err.Error(), "exited unexpectedly")
	})
}

func must(t *testing.T, x Exec, args ...string) string {
	t.Helper()
	out, err := x.Run(context.Background(), args...)
	if err != nil {
		t.Fatalf("tmux %v: %v", args, err)
	}
	return strings.TrimSpace(out)
}

func realDir(t *testing.T) string {
	d, _ := filepath.EvalSymlinks(t.TempDir())
	return d
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 50; i++ {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func build(t *testing.T, x Exec) (d1, d2, d3, agentPane string) {
	d1, d2, d3 = realDir(t), realDir(t), realDir(t)
	must(t, x, "new-session", "-d", "-s", "alpha", "-n", "code", "-c", d1, "-x", "160", "-y", "48")
	agentPane = must(t, x, "split-window", "-h", "-t", "=alpha:0", "-c", d2, "-P", "-F", "#{pane_id}")
	must(t, x, "new-window", "-d", "-t", "=alpha:", "-n", "logs", "-c", d3)
	// A pane running something: it must not run again after a restore.
	must(t, x, "new-session", "-d", "-s", "beta", "-c", d2, "sleep 1000")
	return
}

func windowsOf(t *testing.T, x Exec, session string) []string {
	return strings.Split(must(t, x, "list-windows", "-t", "="+session, "-F", "#{window_name}:#{window_panes}"), "\n")
}

func TestSnapshotKillRestore(t *testing.T) {
	x := sandbox(t)
	ctx := context.Background()
	d1, d2, d3, agentPane := build(t, x)

	snap, ok := Capture(ctx, x, nil)
	if !ok {
		t.Fatal("capture failed")
	}
	snap.Attach(map[string]Agent{agentPane: {Kind: "claude", ID: "be3612de-d629"}})
	st := Store{Dir: filepath.Join(t.TempDir(), "snapshots")}
	saved, fresh, err := st.Save(snap, time.Now())
	if err != nil || !fresh {
		t.Fatalf("save: %v fresh=%v", err, fresh)
	}
	if s, w, p := saved.Counts(); s != 2 || w != 3 || p != 4 {
		t.Fatalf("captured %d sessions, %d windows, %d panes", s, w, p)
	}
	// Saved again unchanged: confirmed in place, not a new generation.
	if _, fresh, _ := st.Save(snap, time.Now().Add(time.Second)); fresh || len(st.List()) != 1 {
		t.Fatalf("an unchanged snapshot made a new generation")
	}

	// The server dies -- this sandbox's own, and nothing else.
	killServer(t, x)
	if _, alive := ServerOf(ctx, x); alive {
		t.Fatal("server still up")
	}

	res, err := Apply(ctx, x, st.Latest(), Options{StoppedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Restored(); strings.Join(got, ",") != "alpha,beta" {
		t.Fatalf("restored %v", got)
	}
	if got := windowsOf(t, x, "alpha"); strings.Join(got, " ") != "code:2 logs:1" {
		t.Errorf("alpha windows: %v", got)
	}
	cwds := must(t, x, "list-panes", "-a", "-F", "#{session_name}:#{window_name}.#{pane_index} #{pane_current_path}")
	for _, want := range []string{"alpha:code.0 " + d1, "alpha:code.1 " + d2, "alpha:logs.0 " + d3, "beta:"} {
		if !strings.Contains(cwds, want) {
			t.Errorf("no %q in\n%s", want, cwds)
		}
	}
	if !strings.Contains(cwds, d2) {
		t.Errorf("beta not in %s", d2)
	}

	// Nothing runs by itself: beta was running sleep, and gets a shell and a
	// note; the agent pane gets its resume line typed, not entered.
	waitFor(t, "shells", func() bool {
		cmds := must(t, x, "list-panes", "-a", "-F", "#{pane_current_command}")
		return !strings.Contains(cmds, "sleep") && strings.Count(cmds, "sh") >= 4
	})
	beta := must(t, x, "list-panes", "-t", "=beta", "-F", "#{pane_current_command}")
	if beta == "sleep" {
		t.Fatal("the command beta was running was started again")
	}
	noted := false
	for i := 0; i < 50 && !noted; i++ {
		noted = strings.Contains(must(t, x, "capture-pane", "-p", "-t", "=beta:"), "was running: sleep 1000")
		time.Sleep(100 * time.Millisecond)
	}
	if !noted {
		var was []string
		for _, se := range snap.Sessions {
			for _, w := range se.Windows {
				for _, p := range w.Panes {
					was = append(was, se.Name+":"+p.Command+":"+p.Args)
				}
			}
		}
		t.Fatalf("no note in beta; snapshot %q; screen:\n%s", was, must(t, x, "capture-pane", "-p", "-t", "=beta:"))
	}
	var agentID string
	for _, l := range strings.Split(must(t, x, "list-panes", "-t", "=alpha:code", "-F", "#{pane_index} #{pane_id}"), "\n") {
		if strings.HasPrefix(l, "1 ") {
			agentID = strings.TrimPrefix(l, "1 ")
		}
	}
	waitFor(t, "the typed resume line", func() bool {
		return strings.Contains(must(t, x, "capture-pane", "-p", "-t", agentID), "claude --resume be3612de-d629")
	})
	// Once: typed when the shell's editor is up, not echoed by the
	// terminal first and drawn again by the editor.
	time.Sleep(time.Second)
	if screen := must(t, x, "capture-pane", "-p", "-t", agentID); strings.Count(screen, "claude --resume be3612de-d629") != 1 {
		t.Errorf("the resume line is on screen %d times, not once:\n%s", strings.Count(screen, "claude --resume be3612de-d629"), screen)
	}
	if cmd := must(t, x, "display-message", "-p", "-t", agentID, "#{pane_current_command}"); cmd == "claude" {
		t.Fatal("the resume line was run")
	}
	if v := must(t, x, "show-options", "-wv", "-t", "=alpha:code", "@stui_restored"); v == "" {
		t.Error("restored window not marked")
	}
	if v := must(t, x, "show-options", "-wv", "-t", "=alpha:code", "@stui_restored_agent"); v != "claude" {
		t.Errorf("the agent window's restored agent is %q, not claude", v)
	}
	if v, _ := x.Run(ctx, "show-options", "-wv", "-t", "=alpha:logs", "@stui_restored_agent"); strings.TrimSpace(v) != "" {
		t.Errorf("a window with no agent says %q", v)
	}

	// The marks last until the window has been dealt with, not for a time:
	// long after the restore, with the resume line still waiting, both stay.
	later := time.Now().Add(settleAfter + time.Hour)
	Settle(ctx, x, later)
	Settle(ctx, x, later)
	if v := must(t, x, "show-options", "-wv", "-t", "=alpha:code", "@stui_restored_agent"); v != "claude" {
		t.Errorf("the restored agent went before its resume line was entered: %q", v)
	}
	// Entered (claude is not installed here, so it fails -- but it ran).
	must(t, x, "send-keys", "-t", agentID, "Enter")
	waitFor(t, "the agent window's marks to go", func() bool {
		Settle(ctx, x, later)
		a, _ := x.Run(ctx, "show-options", "-wv", "-t", "=alpha:code", "@stui_restored_agent")
		r, _ := x.Run(ctx, "show-options", "-wv", "-t", "=alpha:code", "@stui_restored")
		return strings.TrimSpace(a) == "" && strings.TrimSpace(r) == ""
	})
	if v := must(t, x, "show-options", "-wv", "-t", "=alpha:logs", "@stui_restored"); v == "" {
		t.Error("a window nobody touched lost its mark")
	}

	// Twice is once: everything is there now, so nothing is touched.
	again, err := Apply(ctx, x, st.Latest(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Restored()) != 0 || again.Windows != 0 {
		t.Fatalf("second restore did something: %+v", again)
	}
	if got := windowsOf(t, x, "alpha"); strings.Join(got, " ") != "code:2 logs:1" {
		t.Errorf("alpha changed on the second restore: %v", got)
	}
}

// After a death a reconnecting client often gets there first and makes an
// empty session of the same name. Its windows are added to that one; a
// session somebody is using is never touched.
func TestRestoreFillsFreshSessionAndSkipsLiveOne(t *testing.T) {
	x := sandbox(t)
	ctx := context.Background()
	build(t, x)
	snap, _ := Capture(ctx, x, nil)
	killServer(t, x)

	// New server: alpha as a reconnecting client leaves it (one fresh shell),
	// beta as somebody's own (two windows).
	must(t, x, "new-session", "-d", "-s", "alpha")
	must(t, x, "new-session", "-d", "-s", "beta")
	must(t, x, "new-window", "-d", "-t", "=beta:", "-n", "mine")

	plan := Plan(ctx, x, snap, time.Now())
	acts := map[string]string{}
	for _, s := range plan {
		acts[s.Session] = s.Action
	}
	if acts["alpha"] != Fill || acts["beta"] != Skip {
		t.Fatalf("plan %v", acts)
	}
	if _, err := Apply(ctx, x, snap, Options{}); err != nil {
		t.Fatal(err)
	}
	if got := windowsOf(t, x, "alpha"); len(got) != 3 {
		t.Errorf("alpha: %v", got)
	}
	if got := windowsOf(t, x, "beta"); len(got) != 2 || !strings.HasPrefix(got[1], "mine") {
		t.Errorf("beta was touched: %v", got)
	}
}

// The same server, sessions gone: that was somebody closing them, and a
// fill must not happen there.
func TestPlanSameServerNeverFills(t *testing.T) {
	x := sandbox(t)
	ctx := context.Background()
	build(t, x)
	snap, _ := Capture(ctx, x, nil)
	must(t, x, "kill-session", "-t", "=alpha")
	must(t, x, "new-session", "-d", "-s", "alpha")
	for _, s := range Plan(ctx, x, snap, time.Now()) {
		if s.Session == "alpha" && s.Action != Skip {
			t.Fatalf("alpha: %s on the same server", s.Action)
		}
	}
}

func TestStoreKeepsGenerationsAndSkipsEmpty(t *testing.T) {
	st := Store{Dir: t.TempDir(), Keep: 3}
	now := time.Now()
	for i := 0; i < 5; i++ {
		s := &Snapshot{V: 1, Server: Server{PID: 1, Start: 1}, Sessions: []Session{{Name: "s", Windows: []Window{{Index: i}}}}}
		if _, fresh, err := st.Save(s, now.Add(time.Duration(i)*time.Second)); err != nil || !fresh {
			t.Fatal(err)
		}
	}
	if n := len(st.List()); n != 3 {
		t.Errorf("%d generations kept", n)
	}
	if _, _, err := st.Save(&Snapshot{Server: Server{PID: 2}}, now); err == nil {
		t.Error("an empty server was saved")
	}
	st.SetMarker(&Marker{From: "x"})
	if n := len(st.List()); n != 3 || st.Marker() == nil {
		t.Errorf("marker counted as a snapshot, or lost (%d)", n)
	}
}

func TestLayoutSizeAndResumeLine(t *testing.T) {
	if w, h := layoutSize("b25d,190x50,0,0{95x50,0,0,1,94x50,96,0,2}"); w != 190 || h != 50 {
		t.Errorf("%d x %d", w, h)
	}
	if w, h := layoutSize(`{"V":2,"L":{"t":"h","w":80,"h":24,"x":0,"y":0,"c":[{"t":"p","w":40,"h":24,"x":0,"y":0,"I":"%0"},{"t":"p","w":39,"h":24,"x":41,"y":0,"I":"%1"}]}}`); w != 80 || h != 24 {
		t.Errorf("tmux 3.8's JSON: %d, %d", w, h)
	}
	if w, h := layoutSize("0000,80x24,0,0{40x24,0,0,0,39x24,41,0,1}"); w != 80 || h != 24 {
		t.Errorf("the older form: %d, %d", w, h)
	}
	for _, c := range []struct {
		a    *Agent
		want string
	}{
		{&Agent{Kind: "claude", ID: "abc"}, "claude --resume abc"},
		{&Agent{Kind: "claude"}, "claude --continue"},
		{&Agent{Kind: "codex", ID: "t1"}, "codex resume t1"},
		{&Agent{Kind: "gemini"}, ""},
		{nil, ""},
	} {
		if got := resumeLine(c.a); got != c.want {
			t.Errorf("%+v: %q", c.a, got)
		}
	}
}

// shellForTest is zsh where there is one: its line editor is the one that
// drew a resume line typed too early a second time. RT_SHELL picks another.
func shellForTest() string {
	if s := os.Getenv("RT_SHELL"); s != "" {
		return s
	}
	if _, err := os.Stat("/bin/zsh"); err == nil {
		return "/bin/zsh"
	}
	return "/bin/sh"
}
