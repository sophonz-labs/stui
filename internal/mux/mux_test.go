package mux

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestValidators(t *testing.T) {
	for name, want := range map[string]bool{
		"stui": true, "a_b-C9": true, "": false, "a.b": false, "a:b": false, "x y": false,
		"0123456789012345678901234567890123456789012345678901234567890123":  true,
		"01234567890123456789012345678901234567890123456789012345678901234": false,
	} {
		if ValidName(name) != want {
			t.Errorf("ValidName(%q) != %v", name, want)
		}
	}
	for id, want := range map[string]bool{"abc123": true, "ABC": false, "": false, "12345678901234567": false} {
		if ValidClient(id) != want {
			t.Errorf("ValidClient(%q) != %v", id, want)
		}
	}
	for name, want := range map[string]bool{"my window": true, "한글 창": true, "-flag": false, "a\tb": false, "": false, "a\x7f": false} {
		if ValidWindowName(name) != want {
			t.Errorf("ValidWindowName(%q) != %v", name, want)
		}
	}
}

func TestViewNames(t *testing.T) {
	if ViewNameFor("s", "abc") != "s__abc" || ViewNameFor("s", "") != "s__anon" || ViewNameFor("s", "BAD!") != "s__anon" {
		t.Error("ViewNameFor")
	}
	if !IsInternalView("s__abc", "s", nil) || !IsInternalView("s-mobile", "s", nil) || IsInternalView("s-mobile", "", nil) || IsInternalView("other", "s", nil) {
		t.Error("IsInternalView")
	}
	// Only a client id stui could have made: not a user's own "s__My-Notes".
	if IsInternalView("s__My-Notes", "s", nil) || IsInternalView("s__", "s", nil) {
		t.Error("IsInternalView took a name that is not a view")
	}
	// Ungrouped, a double underscore is just a name.
	if IsInternalView("st__abc", "", []string{"stui"}) {
		t.Error("IsInternalView outside a group")
	}
}

// The base session was renamed from old to new; tmux kept the group's
// name, so the view stui then made for a phone is named for neither the group
// nor anything IsInternalView used to compare it with.
func TestViewsOfARenamedSession(t *testing.T) {
	out := "new|old|3|1|100|zsh\n" +
		"old|old|3|0|100|zsh\n" +
		"old__local|old|3|0|100|zsh\n" +
		"new__b1s6lxmh|old|3|1|100|zsh\n"
	var names []string
	for _, s := range parseSessions(out) {
		names = append(names, s.Name)
	}
	if !reflect.DeepEqual(names, []string{"new", "old"}) {
		t.Errorf("listed %v, want [new old]", names)
	}
	peers := []string{"new", "old", "old__local", "new__b1s6lxmh"}
	for name, want := range map[string]bool{"new": false, "old": false, "old__local": true, "new__b1s6lxmh": true} {
		if got := IsInternalView(name, "old", peers); got != want {
			t.Errorf("IsInternalView(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestParseSessions(t *testing.T) {
	out := "zeta||2|0|100|nvim|%3||vim\nalpha|alpha|1|1|200|-zsh|%1||a|b|c\nalpha__abc|alpha|1|1|200|zsh|%1||a\nBeta||3|0|300||||\n"
	got := parseSessions(out)
	want := []SessionInfo{
		{Name: "alpha", Windows: 1, Attached: 1, Created: 200, CurrentWindow: "a|b|c", Icon: "zsh", Command: "zsh", Pane: "%1", proc: "-zsh"},
		{Name: "Beta", Windows: 3, Created: 300},
		{Name: "zeta", Windows: 2, Created: 100, CurrentWindow: "vim", Icon: "neovim", Command: "nvim", Pane: "%3", proc: "nvim"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
}

func TestParseWindows(t *testing.T) {
	got := parseWindows("0|120|1|zsh|%0||||@0|1|0|/src/st|zsh\n1|80|0|python3.12|%4||||@4|3|1||a|b\n2|80|0|node|%5||2026-09-29T14:02:00+09:00|claude|@5|1|0||agent\n3|80|0|zsh|%6|||claude|@6|1|0||stale\n")
	want := []WindowInfo{
		{Index: 0, ID: "@0", Width: 120, Active: true, Panes: 1, Name: "zsh", Icon: "zsh", Command: "zsh", Pane: "%0", proc: "zsh", path: "/src/st"},
		{Index: 1, ID: "@4", Width: 80, Panes: 3, Zoomed: true, Name: "a|b", Icon: "python", Command: "python3.12", Pane: "%4", proc: "python3.12"},
		{Index: 2, ID: "@5", Width: 80, Panes: 1, Name: "agent", Icon: "nodedotjs", Command: "node", Pane: "%5", proc: "node", Restored: "2026-09-29T14:02:00+09:00", RestoredAgent: "claude"},
		// An agent mark on a window no longer marked restored says nothing.
		{Index: 3, ID: "@6", Width: 80, Panes: 1, Name: "stale", Icon: "zsh", Command: "zsh", Pane: "%6", proc: "zsh"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
	// Agent watch knows the node in %5 is Claude Code.
	AgentIcons(got, func(p string) string { return map[string]string{"%5": "claude"}[p] })
	if got[2].Icon != "claude" || got[2].Command != "claude" || got[1].Icon != "python" {
		t.Errorf("agent override: %+v", got)
	}
}

func TestParseTabs(t *testing.T) {
	got := parseTabs("TAB_ID POSITION NAME\n0 0 Tab #1\n3 1 logs\n")
	want := []WindowInfo{{Index: 1, ID: "0", Name: "Tab #1"}, {Index: 2, ID: "3", Name: "logs"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
}

func TestNum(t *testing.T) {
	for in, want := range map[string]int{"3": 3, "": 0, "2.7": 2, "-1": -1} {
		if num(in) != want {
			t.Errorf("num(%q)=%d", in, num(in))
		}
	}
}

func TestResolveNone(t *testing.T) {
	m, err := Resolve(context.Background(), NoneChoice, "sh")
	if err != nil || m.ID() != "none" || m.Persistent() || len(m.Actions()) != 0 {
		t.Fatalf("%v %v", m, err)
	}
	if _, err := Resolve(context.Background(), "bogus", "sh"); err == nil {
		t.Error("unknown choice accepted")
	}
}

// The one shell is listed by a name /ws and a link accept, with the command
// where the list shows what is running.
func TestNoneListSessions(t *testing.T) {
	n := NewNone("/bin/zsh")
	got := n.ListSessions(context.Background())
	if len(got) != 1 || got[0].Name != DefaultSession || got[0].CurrentWindow != "/bin/zsh" || got[0].Windows != 1 || got[0].Icon != "zsh" {
		t.Fatalf("unnamed: %+v", got)
	}
	n.SetSession("sttest-n")
	got = n.ListSessions(context.Background())
	if len(got) != 1 || got[0].Name != "sttest-n" || got[0].CurrentWindow != "/bin/zsh" {
		t.Fatalf("named: %+v", got)
	}
	if !ValidName(got[0].Name) {
		t.Errorf("%q is not a name a link can carry", got[0].Name)
	}
}

// Against a real tmux, on a server of its own so nothing of the user's is touched.
func TestTmuxLifecycle(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux")
	}
	// Not t.TempDir(): a unix socket path has a length limit (104 bytes on
	// macOS) that the test's own directory name alone can use up.
	dir, err := os.MkdirTemp("/tmp", "stgo")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("TMUX_TMPDIR", dir)
	t.Setenv("TMUX", "")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tm := NewTmux()
	defer exec.Command(tm.bin, "kill-server").Run()

	if err := tm.CreateSession(ctx, "gotest"); err != nil {
		t.Fatal(err)
	}
	if err := tm.CreateSession(ctx, "gotest"); err != nil {
		t.Fatal("second create should be a no-op:", err)
	}
	if !tm.HasSession(ctx, "gotest") {
		t.Fatal("session missing")
	}
	// A per-client view, as ttyd would make it.
	if out, err := exec.Command(tm.bin, append([]string{"new-session", "-d"}, tm.AttachArgs("gotest", "abc")[1:]...)...).CombinedOutput(); err != nil {
		t.Fatalf("view: %v %s", err, out)
	}
	sessions := tm.ListSessions(ctx)
	if len(sessions) != 1 || sessions[0].Name != "gotest" {
		t.Fatalf("views must be hidden: %+v", sessions)
	}

	tm.Run(ctx, "gotest", NewWindow, "", "abc")
	tm.Run(ctx, "gotest", RenameWindow, "renamed", "abc")
	wins := tm.ListWindows(ctx, "gotest", "abc")
	if len(wins) != 2 {
		t.Fatalf("windows: %+v", wins)
	}
	var active WindowInfo
	for _, w := range wins {
		if w.Active {
			active = w
		}
	}
	if active.Name != "renamed" {
		t.Errorf("active window: %+v", wins)
	}

	tm.KillSession(ctx, "gotest")
	if tm.HasSession(ctx, "gotest") || tm.HasSession(ctx, "gotest__abc") {
		t.Error("kill left sessions behind")
	}
}

// psmux's mode -- the tmux language without session groups -- against a real
// tmux, which speaks the same commands.
func TestUngroupedLifecycle(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux")
	}
	dir, err := os.MkdirTemp("/tmp", "stgo")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("TMUX_TMPDIR", dir)
	t.Setenv("TMUX", "")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	grouped := NewTmux()
	m := &Tmux{bin: grouped.bin, id: "psmux"}
	defer exec.Command(m.bin, "kill-server").Run()

	for _, a := range m.Actions() {
		if a == Detach {
			t.Error("detach offered without views: it would detach every client")
		}
	}
	if m.IndependentClientSize() || m.ID() != "psmux" {
		t.Error("ungrouped identity")
	}
	if got := m.AttachArgs("work", "abc"); strings.Join(got, " ") != "new-session -A -s work" {
		t.Errorf("attach args: %v", got)
	}

	if err := m.CreateSession(ctx, "work"); err != nil {
		t.Fatal(err)
	}
	m.Run(ctx, "work", NewWindow, "", "abc")
	m.Run(ctx, "work", RenameWindow, "second", "abc")
	wins := m.ListWindows(ctx, "work", "abc")
	if len(wins) != 2 {
		t.Fatalf("windows: %+v", wins)
	}
	m.Run(ctx, "work", GotoWindow, strconv.Itoa(wins[0].Index), "abc")
	for _, w := range m.ListWindows(ctx, "work", "") {
		if w.Active != (w.Index == wins[0].Index) {
			t.Errorf("goto did not act on the session itself: %+v", w)
		}
	}
	if s := m.ListSessions(ctx); len(s) != 1 || s[0].Name != "work" || s[0].Windows != 2 {
		t.Errorf("sessions: %+v", s)
	}
	// zoom-pane: that pane, zoomed, whichever was zoomed before.
	m.Run(ctx, "work", SplitRight, "", "abc")
	for _, n := range []int{0, 1, 1} {
		m.Run(ctx, "work", ZoomPane, strconv.Itoa(n), "abc")
		got, _ := m.ask(ctx, "work:", "#{pane_index} #{window_zoomed_flag}")
		if got != strconv.Itoa(n)+" 1" {
			t.Errorf("zoom-pane %d: %q", n, got)
		}
	}
	if _, exists := run(ctx, time.Second, m.bin, "has-session", "-t", "=work__abc"); exists {
		t.Error("a view was made")
	}
}

// Regression for a tmux server left running in a directory that was since
// deleted: every pane used to inherit that vanished directory verbatim --
// even `new-session -c` could not override it -- leaving shells that could
// not even print a prompt. The pane command guard (unixPaneCommand) is what
// routes a fresh pane to $HOME instead.
func TestCreateSessionAfterStartDirDeleted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-only guard")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux")
	}
	sockDir, err := os.MkdirTemp("/tmp", "stgo")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockDir)
	t.Setenv("TMUX_TMPDIR", sockDir)
	t.Setenv("TMUX", "")

	tm := NewTmux()
	defer exec.Command(tm.bin, "kill-server").Run()

	gone, err := os.MkdirTemp("/tmp", "stgo-gone")
	if err != nil {
		t.Fatal(err)
	}
	// Starts the server with `gone` as its own process cwd, and gives it a
	// session so exit-empty does not tear the server down again before
	// CreateSession below gets to use it.
	probe := exec.Command(tm.bin, "new-session", "-d", "-s", "probe")
	probe.Dir = gone
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("probe: %v %s", err, out)
	}
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := tm.CreateSession(ctx, "gotest3"); err != nil {
		t.Fatal(err)
	}

	pid, ok := tm.ask(ctx, "gotest3", "#{pane_pid}")
	if !ok {
		t.Fatal("no pane_pid")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir to compare against")
	}
	// The guard runs in the pane after new-session returns; give its cd a
	// moment rather than read the directory it is about to leave.
	homeReal, _ := filepath.EvalSymlinks(home)
	var cwd string
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if cwd, ok = paneCwd(pid); !ok {
			t.Skip("could not read the pane's cwd on this platform")
		}
		if real, _ := filepath.EvalSymlinks(cwd); real == homeReal || time.Now().After(deadline) {
			break
		}
	}
	if real, _ := filepath.EvalSymlinks(cwd); real != homeReal {
		t.Errorf("pane cwd = %s, want home (%s)", cwd, home)
	}
}

// paneCwd reads a process's current directory: /proc on Linux, lsof on macOS.
func paneCwd(pid string) (string, bool) {
	if dir, err := os.Readlink("/proc/" + pid + "/cwd"); err == nil {
		return dir, true
	}
	out, err := exec.Command("lsof", "-a", "-p", pid, "-d", "cwd", "-Fn").Output()
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") {
			return line[1:], true
		}
	}
	return "", false
}

func TestResolveWhereMultiplexersRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("covers the other platforms")
	}
	if _, err := Resolve(context.Background(), PsmuxMux, "sh"); err == nil {
		t.Error("psmux resolved off Windows")
	}
}

// The socket stui attach compares $TMUX with, on a server of the test's own.
func TestTmuxServerSocket(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil || runtime.GOOS == "windows" {
		t.Skip("no tmux")
	}
	dir, err := os.MkdirTemp("/tmp", "stgo")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("TMUX_TMPDIR", dir)
	t.Setenv("TMUX", "")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tm := NewTmux()
	defer exec.Command(tm.bin, "kill-server").Run()

	if got := tm.OutsideSocket(ctx); got != "" {
		t.Fatalf("a socket with no server: %q", got)
	}
	if err := tm.CreateSession(ctx, "gotest"); err != nil {
		t.Fatal(err)
	}
	sock := tm.OutsideSocket(ctx)
	real, _ := filepath.EvalSymlinks(dir)
	if !strings.HasPrefix(sock, real+"/") || tm.ServerSocket(ctx) != sock {
		t.Fatalf("socket %q (server %q), want one under %s", sock, tm.ServerSocket(ctx), real)
	}
	// From inside a server, tmux's commands go to that one; from outside,
	// to this one, whatever $TMUX says.
	t.Setenv("TMUX", filepath.Join(dir, "elsewhere")+",1,0")
	if got := tm.ServerSocket(ctx); got != "" {
		t.Errorf("ServerSocket ignored $TMUX: %q", got)
	}
	if got := tm.OutsideSocket(ctx); got != sock {
		t.Errorf("OutsideSocket followed $TMUX: %q", got)
	}
	t.Setenv("TMUX", "")

	// The view SwitchArgs switches to is the one it makes, in the group.
	cmds := tm.SwitchArgs("gotest", "local")
	for _, args := range cmds[:len(cmds)-1] {
		if out, err := exec.Command(tm.bin, args...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
	}
	if !tm.HasSession(ctx, "gotest__local") {
		t.Fatal("no view to switch to")
	}
	if got := tm.ListSessions(ctx); len(got) != 1 || got[0].Name != "gotest" {
		t.Errorf("the view is listed: %+v", got)
	}
}

func TestWatcherClients(t *testing.T) {
	out := "attached,focused,control-mode,ignore-size,UTF-8|work\n" +
		"attached,focused,control-mode,UTF-8|work\n" + // iTerm2's -CC: a person
		"attached,focused,UTF-8|work\n" +
		"attached,control-mode,ignore-size|other\n"
	n := WatcherClients(out)
	if n["work"] != 1 || n["other"] != 1 {
		t.Fatalf("%v", n)
	}
}

func TestParseAllWindows(t *testing.T) {
	got := parseAllWindows("work|0|120|1|zsh|%0||||@0|1|0||zsh\nwork|1|80|0|claude|%4||||@4|2|0||a|b\nwork-ab12|0|120|1|zsh|%0||||@0|1|0||zsh\nlogs|0|80|1|tail|%7||||@9|1|0||tail\n")
	if len(got) != 3 || len(got["work"]) != 2 || got["work"][1].Name != "a|b" || got["work"][1].Panes != 2 || got["logs"][0].ID != "@9" {
		t.Fatalf("got %+v", got)
	}
}

// retarget keeps whatever follows the session name, because the generic
// actions build targets four different shapes: "<s>:", "<s>", "<s>:3" and
// "<s>:.+" (select-pane's next pane).
func TestRetargetKeepsWhatFollowsTheSession(t *testing.T) {
	for _, c := range []struct {
		name string
		in   []string
		want []string
	}{
		{"a trailing colon", []string{"new-window", "-t", "work__a1:"}, []string{"new-window", "-t", "work:"}},
		{"a window index", []string{"select-window", "-t", "work__a1:3"}, []string{"select-window", "-t", "work:3"}},
		{"the next pane", []string{"select-pane", "-t", "work__a1:.+"}, []string{"select-pane", "-t", "work:.+"}},
		{"a flag after the target", []string{"resize-pane", "-t", "work__a1:", "-Z"}, []string{"resize-pane", "-t", "work:", "-Z"}},
		{"an argument that is not a target", []string{"rename-window", "-t", "work__a1:", "work__a1"}, []string{"rename-window", "-t", "work:", "work__a1"}},
		{"nothing to rewrite", []string{"detach-client", "-s", "work__a1"}, []string{"detach-client", "-s", "work__a1"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := strings.Join(c.in, " ")
			got := retarget(c.in, "work__a1", "work")
			if strings.Join(got, " ") != strings.Join(c.want, " ") {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			// Run keeps the original for its error message, so the rewrite
			// must not write through to it.
			if strings.Join(c.in, " ") != before {
				t.Fatalf("retarget changed its input to %v", c.in)
			}
		})
	}
}
