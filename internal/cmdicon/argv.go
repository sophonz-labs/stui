package cmdicon

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ForegroundArgs reads, with ps, the argument vector of the foreground
// process on tty (a pane's #{pane_tty}). It is for Resolve's eyes only:
// callers pass it straight in and never print, log or send it. nil on
// Windows, or when ps does not answer.
func ForegroundArgs(tty string) []string {
	if runtime.GOOS == "windows" || tty == "" {
		return nil
	}
	argvMu.Lock()
	if c, ok := argvCache[tty]; ok && time.Since(c.at) < argvTTL {
		argvMu.Unlock()
		return c.argv
	}
	argvMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "pid=,pgid=,tpgid=,args=", "-t", strings.TrimPrefix(tty, "/dev/")).Output()
	var argv []string
	if err == nil {
		argv = foreground(string(out))
	}
	argvMu.Lock()
	if len(argvCache) > 256 {
		argvCache = map[string]argvEntry{}
	}
	argvCache[tty] = argvEntry{argv: argv, at: time.Now()}
	argvMu.Unlock()
	return argv
}

type argvEntry struct {
	argv []string
	at   time.Time
}

const argvTTL = 3 * time.Second

var (
	argvMu    sync.Mutex
	argvCache = map[string]argvEntry{}
)

// foreground picks the foreground process group's leader from ps output:
// pid, pgid, tpgid, args. Any member of the group will do without one.
func foreground(out string) []string {
	var member []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		pid, pgid, tpgid := f[0], f[1], f[2]
		if pgid != tpgid {
			continue
		}
		if pid == tpgid {
			return f[3:]
		}
		if member == nil {
			member = f[3:]
		}
	}
	return member
}
