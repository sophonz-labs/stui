//go:build !windows

package restore

// Control clients left behind, and a server that cannot finish exiting
// because of them.
//
// stui's watch keeps a control client (tmux -C attach-session -f ignore-size)
// on every session. When the stui that started one is gone, the client should
// go too, and it usually does -- its stdin closes and it detaches -- but not
// when the server is not answering at the time: the detach never finishes,
// and the client stays, parented to init, for weeks. runControl now has it
// end with its stui (pdeathsig, or the guard); this is for the ones before.
// Harmless while the server runs. When the server is told to stop, though,
// it waits for its clients, and those never answer: the server closes every
// session yet keeps its socket, and drops each new connection ("server
// exited unexpectedly"). Nothing can then start a server there, so no
// restore can work, until those clients are gone.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// controlSig is how stui's watch starts its control clients
// (internal/watch, runControl).
const controlSig = "-C attach-session -f ignore-size"

// Stuck says whether err is what a client gets from a server that took the
// connection and closed it at once: one that is exiting and cannot finish.
func Stuck(err error) bool {
	return err != nil && strings.Contains(err.Error(), "server exited unexpectedly")
}

func parentOf(pid int) int {
	out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return -1
	}
	return n
}

// stop asks each pid to end, then makes it.
func stop(pids []int) {
	for _, p := range pids {
		syscall.Kill(p, syscall.SIGTERM)
	}
	if len(pids) == 0 {
		return
	}
	time.Sleep(500 * time.Millisecond)
	for _, p := range pids {
		if syscall.Kill(p, 0) == nil {
			syscall.Kill(p, syscall.SIGKILL)
		}
	}
}

// ReapOrphans ends the control clients attached to t's server whose stui is
// gone (their parent is init): nobody reads them, and they are what would
// keep this server from ever finishing an exit. It says how many.
func ReapOrphans(ctx context.Context, t Tmux) int {
	out, err := t.Run(ctx, "list-clients", "-F", "#{client_pid}"+sep+"#{client_control_mode}")
	if err != nil {
		return 0
	}
	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		pidS, ctl, ok := strings.Cut(line, sep)
		if !ok || ctl != "1" {
			continue
		}
		pid, err := strconv.Atoi(pidS)
		if err != nil || pid <= 1 {
			continue
		}
		if parentOf(pid) == 1 {
			pids = append(pids, pid)
		}
	}
	stop(pids)
	return len(pids)
}

// DefaultSocket is the socket a tmux run with env (nil: this process's)
// and no -L or -S talks to. tmux 3.7 tries $TMUX_TMPDIR and, when that
// directory does not exist, falls back to /tmp without a word -- which is
// how a command meant for a test server reached the real one.
func DefaultSocket(env []string) string {
	get := os.Getenv
	if env != nil {
		get = func(k string) string {
			for _, kv := range env {
				if strings.HasPrefix(kv, k+"=") {
					return kv[len(k)+1:]
				}
			}
			return ""
		}
	}
	return socketIn(get("TMUX_TMPDIR"), "default")
}

// SocketFor is the socket tmux -L label (label "default" when there is no
// -L) talks to with TMUX_TMPDIR set to tmpdir ("" when unset).
func SocketFor(tmpdir, label string) string { return socketIn(tmpdir, label) }

// Canonical is path with its directory's symlinks resolved.
func Canonical(path string) string { return canonical(path) }

func socketIn(tmpdir, label string) string {
	base := "/tmp"
	if tmpdir != "" {
		if fi, err := os.Stat(tmpdir); err == nil && fi.IsDir() {
			base = tmpdir
		}
	}
	return canonical(filepath.Join(base, fmt.Sprintf("tmux-%d", os.Getuid()), label))
}

// canonical resolves symlinks in the part of path that exists (/tmp is
// /private/tmp on macOS), so two spellings of one socket compare equal
// whether or not its directory has been made yet.
func canonical(path string) string {
	path = filepath.Clean(path)
	rest := ""
	for dir := path; ; {
		if d, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(d, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return path
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}

// socketOf is the socket a running tmux client talks to, read from its
// command line and environment; "" when that cannot be told.
func socketOf(pid int, args string) string {
	f := strings.Fields(args)
	for i := 1; i+1 < len(f); i++ {
		switch f[i] {
		case "-S":
			return canonical(f[i+1])
		case "-L":
			return socketIn(envOf(pid, "TMUX_TMPDIR"), f[i+1])
		}
	}
	return socketIn(envOf(pid, "TMUX_TMPDIR"), "default")
}

// EnvOf is one variable of another process of this user, "" when it has
// none or it cannot be read.
func EnvOf(pid int, key string) string { return envOf(pid, key) }

// envOf reads one variable of another process of this user.
func envOf(pid int, key string) string {
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid)); err == nil {
		for _, kv := range strings.Split(string(b), "\x00") {
			if strings.HasPrefix(kv, key+"=") {
				return kv[len(key)+1:]
			}
		}
		return ""
	}
	// macOS: ps prints the environment after the command line.
	out, err := exec.Command("ps", "eww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	for _, w := range strings.Fields(string(out)) {
		if strings.HasPrefix(w, key+"=") {
			return w[len(key)+1:]
		}
	}
	return ""
}

// Unstick ends the orphaned control clients of the server at socket, when
// that server is stuck on its way out (Stuck), so it can finish and a new
// one start in its place. Only stui's own control clients whose stui is gone;
// the server itself is never touched. It says how many.
func Unstick(ctx context.Context, socket string) int {
	socket = canonical(socket)
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,command=").Output()
	if err != nil {
		return 0
	}
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || f[1] != "1" {
			continue
		}
		args := strings.Join(f[2:], " ")
		if filepath.Base(f[2]) != "tmux" || !strings.Contains(args, controlSig) {
			continue
		}
		pid, _ := strconv.Atoi(f[0])
		if pid > 1 && socketOf(pid, args) == socket {
			pids = append(pids, pid)
		}
	}
	stop(pids)
	// The server exits once its last client has.
	for i := 0; i < 20 && len(pids) > 0; i++ {
		if _, err := os.Stat(socket); err != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	return len(pids)
}
