package mux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/sophonz-labs/stui/internal/proc"
)

// A multiplexer that never answers must not hold a request forever. The
// original had no bound here; this one is generous enough never to matter
// to a command that works.
const commandTimeout = 30 * time.Second

// run executes bin with args, returning stdout and whether it exited 0.
// Nothing reaches a shell: arguments are passed as they are.
func run(ctx context.Context, timeout time.Duration, bin string, args ...string) (string, bool) {
	return runEnv(ctx, timeout, nil, bin, args...)
}

// runEnv is run with an environment of its own; nil inherits this process's.
//
// Every command this package starts goes through here, and that is the point.
// The agent has no console, and on Windows a console program started from a
// process without one gets a window of its own: every query the page makes --
// the window list, the session list, each few seconds -- flashed one open and
// shut.
//
// It has come back once already, through a caller that needed to set the
// environment and so built its own exec.Command instead. That is why the
// environment is a parameter here rather than a reason to go around.
func runEnv(ctx context.Context, timeout time.Duration, env []string, bin string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	proc.Hide(cmd)
	out, err := cmd.Output()
	return string(out), err == nil
}

func sh(ctx context.Context, bin string, args ...string) (string, bool) {
	return run(ctx, commandTimeout, bin, args...)
}

// shErr is sh with what the command said on stderr when it failed.
func shErr(ctx context.Context, bin string, args ...string) (string, string, bool) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	proc.Hide(cmd)
	out, err := cmd.Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return string(out), string(ee.Stderr), false
	}
	return string(out), "", err == nil
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// installed answers for a bare name on PATH and for a path alike.
func installed(bin string) bool {
	if bin == "" {
		return false
	}
	if strings.ContainsAny(bin, `/\`) {
		_, err := os.Stat(bin)
		return !errors.Is(err, os.ErrNotExist)
	}
	_, err := exec.LookPath(bin)
	return err == nil
}
