package mux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestIsShell(t *testing.T) {
	for _, c := range []string{"zsh", "-zsh", "/bin/bash", "fish", "sh", "pwsh", "pwsh.exe", `C:\\Windows\\System32\\cmd.exe`, "powershell.exe", "nu", "login", ""} {
		if !IsShell(c) {
			t.Errorf("%q should read as a shell", c)
		}
	}
	for _, c := range []string{"claude", "node", "npm", "vim", "python3.12", "tmux", "ssh", "zshx"} {
		if IsShell(c) {
			t.Errorf("%q is not a shell", c)
		}
	}
}

func TestParsePanes(t *testing.T) {
	got := parsePanes("@1|0|%0|zsh|||main\n@1|0|%1|vim|||main\n@2|1|%2|npm||0|a|b\n@3|2|%3|python3||1|dead\n")
	if len(got) != 4 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Command != "" || got[0].Window != "@1" || got[0].ID != "%0" {
		t.Errorf("a shell runs nothing: %+v", got[0])
	}
	if got[1].Command != "vim" || got[1].Icon == "" {
		t.Errorf("vim: %+v", got[1])
	}
	if got[2].Command != "npm" || got[2].WindowName != "a|b" || got[2].WindowIndex != 1 {
		t.Errorf("npm: %+v", got[2])
	}
	if !got[3].Dead || got[3].Command != "" {
		t.Errorf("a dead pane runs nothing: %+v", got[3])
	}
}

func TestValidWindowID(t *testing.T) {
	for _, id := range []string{"@0", "@123", "3"} {
		if !ValidWindowID(id) {
			t.Errorf("%q", id)
		}
	}
	for _, id := range []string{"", "@", "@1;ls", "main:1", "-t", "@1 @2", "%3"} {
		if ValidWindowID(id) {
			t.Errorf("%q passed", id)
		}
	}
}

func TestCanClose(t *testing.T) {
	if CanClose(NewNone("")) {
		t.Error("none has no windows to close")
	}
	if !CanClose(&Tmux{id: "tmux", bin: "tmux", grouped: true}) || !CanClose(NewZellij()) {
		t.Error("tmux and zellij close windows")
	}
	if CanClose(&Tmux{id: "tmux"}) {
		t.Error("a tmux with no binary cannot")
	}
}

// Against a real tmux, on a server of its own.
func TestTmuxCloseWindow(t *testing.T) {
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
	// A shell that starts at once, whatever the user's own does.
	t.Setenv("SHELL", "/bin/sh")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tm := NewTmux()
	defer exec.Command(tm.bin, "kill-server").Run()

	if err := tm.CreateSession(ctx, "closing"); err != nil {
		t.Fatal(err)
	}
	// Another session whose name starts the same, which must not be reached.
	if err := tm.CreateSession(ctx, "closing2"); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(tm.bin, append([]string{"new-session", "-d"}, tm.AttachArgs("closing", "abc")[1:]...)...).CombinedOutput(); err != nil {
		t.Fatalf("view: %v %s", err, out)
	}
	// A window running something, the way a shell would after a command.
	// exec: dash (Ubuntu's /bin/sh) forks rather than execs the last command
	// of `sh -c`, and tmux would then name the pane's process "sh".
	if out, err := exec.Command(tm.bin, "new-window", "-d", "-t", "=closing:", "exec sleep 30").CombinedOutput(); err != nil {
		t.Fatalf("new-window: %v %s", err, out)
	}
	wins := tm.ListWindows(ctx, "closing", "abc")
	if len(wins) != 2 || wins[0].ID == "" || wins[1].ID == "" {
		t.Fatalf("windows: %+v", wins)
	}
	var running []string
	for i := 0; i < 100; i++ {
		panes, processes, ok := tm.Panes(ctx, "closing")
		if !ok || !processes || len(panes) != 2 {
			t.Fatalf("panes: %+v %v %v", panes, processes, ok)
		}
		running = running[:0]
		for _, p := range panes {
			if p.Command != "" {
				running = append(running, p.Window+" "+p.Command)
			}
		}
		if len(running) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(running) != 1 || running[0] != wins[1].ID+" sleep" {
		t.Fatalf("running: %v (windows %+v)", running, wins)
	}

	other := tm.ListWindows(ctx, "closing2", "")
	if err := tm.KillWindow(ctx, "closing", other[0].ID); !errors.Is(err, ErrNoWindow) {
		t.Errorf("another session's window: %v", err)
	}
	if err := tm.KillWindow(ctx, "closing", wins[1].ID); err != nil {
		t.Fatal(err)
	}
	if left := tm.ListWindows(ctx, "closing", "abc"); len(left) != 1 || left[0].ID != wins[0].ID {
		t.Errorf("left: %+v", left)
	}
	// The last window takes the session, and the view with it.
	if err := tm.KillWindow(ctx, "closing", wins[0].ID); err != nil {
		t.Fatal(err)
	}
	if tm.HasSession(ctx, "closing") || tm.HasSession(ctx, "closing__abc") || !tm.HasSession(ctx, "closing2") {
		t.Error("closing the last window should end that session only")
	}
}

func TestPreview(t *testing.T) {
	panes := parsePanes("@1|0|%0|zsh|||main\n@2|1|%1|npm|||api\n@2|1|%2|node|||api\n@3|2|%3|zsh|||idle\n")
	p := Preview("work", panes, true, map[string]AgentState{"%2": {Agent: "claude", State: "working"}, "%3": {Agent: "codex", State: "done"}})
	if p.Session != "work" || !p.Processes || len(p.Windows) != 3 {
		t.Fatalf("%+v", p)
	}
	if w := p.Windows[0]; w.ID != "@1" || w.Panes != 1 || len(w.Running) != 0 || w.Running == nil {
		t.Errorf("an idle window: %+v", w)
	}
	w := p.Windows[1]
	if w.Name != "api" || w.Index != 1 || w.Panes != 2 || len(w.Running) != 2 {
		t.Fatalf("api: %+v", w)
	}
	if w.Running[0].Command != "npm" || w.Running[0].Agent != "" {
		t.Errorf("npm: %+v", w.Running[0])
	}
	if r := w.Running[1]; r.Command != "claude" || r.Icon != "claude" || r.Agent != "claude" || r.State != "working" {
		t.Errorf("agent: %+v", r)
	}
	// An agent watch still remembers in a pane back at its shell is gone.
	if len(p.Windows[2].Running) != 0 {
		t.Errorf("idle: %+v", p.Windows[2])
	}
	// zellij: tabs, nothing known about their panes.
	z := Preview("z", []PaneInfo{{Window: "0", WindowIndex: 1, WindowName: "Tab #1"}}, false, nil)
	if len(z.Windows) != 1 || z.Windows[0].Panes != 0 || z.Processes {
		t.Errorf("zellij: %+v", z)
	}
}
