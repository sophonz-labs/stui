//go:build !windows

package watch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise the production control-mode reader, not Output or controlLine:
// printf -> pane PTY -> tmux %output -> scanner -> watcher snapshot.
func TestOSC7501RealTmux(t *testing.T) {
	bin, err := exec.LookPath("tmux")
	if err != nil {
		if os.Getenv("ST_REQUIRE_TMUX") != "" || os.Getenv("STUI_REQUIRE_TMUX") != "" {
			t.Fatal("tmux required:", err)
		}
		t.Skip("no tmux")
	}
	dir, err := os.MkdirTemp("/tmp", "osc7501-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	// A short explicit socket and a wrapper pin EVERY command, including the
	// watcher control client and cleanup, to this test's server.
	sock := filepath.Join(dir, "sock")
	t.Setenv("OSC7501_TMUX", bin)
	t.Setenv("OSC7501_SOCKET", sock)
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("TMUX_TMPDIR", dir)
	t.Setenv("SOPHONZ_HOME", filepath.Join(dir, "st"))
	t.Setenv("STUI_HOME", filepath.Join(dir, "stui"))
	shim := filepath.Join(dir, "tmux")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nexec \"$OSC7501_TMUX\" -S \"$OSC7501_SOCKET\" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	tm := ExecTmux{Bin: shim}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	run := func(args ...string) string {
		t.Helper()
		out, err := tm.Run(ctx, args...)
		if err != nil {
			t.Fatalf("tmux %v: %v (%s)", args, err, func() []byte {
				if e, ok := err.(*exec.ExitError); ok {
					return e.Stderr
				}
				return nil
			}())
		}
		return strings.TrimSpace(out)
	}
	defer func() {
		cleanup, c := context.WithTimeout(context.Background(), 2*time.Second)
		defer c()
		tm.Run(cleanup, "kill-server")
	}()
	driver := filepath.Join(dir, "driver.sh")
	if err := os.WriteFile(driver, []byte(`stty -echo
printf 'OSC7501_READY\n'
while IFS= read -r command; do eval "$command"; done
`), 0600); err != nil {
		t.Fatal(err)
	}
	id := run("-f", "/dev/null", "new-session", "-d", "-P", "-F", "#{pane_id}", "-s", "osc7501", "-x", "100", "-y", "30", "/bin/sh "+driver)
	w := New(Options{Tmux: tm, ClaudeDir: filepath.Join(dir, "claude")})
	stopped := make(chan struct{})
	go func() { defer close(stopped); w.Run(ctx, shim) }()
	defer func() { cancel(); <-stopped }()
	wait := func(label string, check func() bool) {
		t.Helper()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			if check() {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatalf("%s: %v; snapshot=%+v", label, ctx.Err(), w.Snapshot())
			case <-ticker.C:
			}
		}
	}
	wait("pane ready", func() bool { return strings.Contains(run("capture-pane", "-p", "-t", id), "OSC7501_READY") })
	wait("control client", func() bool { return strings.Contains(run("list-clients", "-F", "#{client_flags}"), "control-mode") })
	send := func(command string) {
		t.Helper()
		run("send-keys", "-t", id, "-l", command)
		run("send-keys", "-t", id, "Enter")
	}
	current := func() *Pane {
		for _, p := range w.Snapshot().Panes {
			if p.ID == id {
				return &p
			}
		}
		return nil
	}
	report := func(label, command, state string, progress *int) {
		t.Helper()
		send(command)
		wait(label, func() bool {
			p := current()
			return p != nil && p.Reported && p.Source == "report" && p.State == state && p.Report != nil && (progress == nil || (p.Report.Progress != nil && *p.Report.Progress == *progress))
		})
		t.Logf("%s: %+v", label, current())
	}
	progress := 40
	report("working ST", `printf '\033]7501;state=working:app=sandbox:progress=40\033\\'`, Working, &progress)
	if current().Report.App != "sandbox" {
		t.Fatal("app lost")
	}
	report("blocked BEL", `printf '\033]7501;state=blocked:kind=permission:app=sandbox\007'`, Waiting, nil)
	if current().Report.Kind != "permission" {
		t.Fatal("permission kind lost")
	}
	report("done", `printf '\033]7501;state=done\007'`, Done, nil)
	report("error", `printf '\033]7501;state=error\007'`, Error, nil)
	report("malformed pair skipped", `printf '\033]7501;broken:state=working:app=sandbox\007'`, Working, nil)
	// Do not echo the commands into an unfinished OSC: the pane's driver
	// has echo disabled. Wait for the production scanner to retain the prefix
	// before allowing a second write, without injecting bytes into the engine.
	send(`printf '\033]7501;state=blo'`)
	wait("partial report", func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		p := w.panes[id]
		return p != nil && p.osc.state == 2 && string(p.osc.buf) == "7501;state=blo"
	})
	report("split report", `printf 'cked:kind=permission\033\\'`, Waiting, nil)
	send(`printf '\033]7501;state=clear\007'; printf '\033]9;needs input\007Proceed? [y/n] '`)
	wait("clear restores screen", func() bool {
		p := current()
		return p != nil && !p.Reported && p.Report == nil && p.Source == "screen" && p.Prompt != nil
	})
}
