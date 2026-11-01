//go:build !linux

package proc

import (
	"os/exec"
	"runtime"
)

// DieWithParent is Linux's; see Guard for macOS. Windows already has the
// job object Contain puts children in, closed with the agent.
func DieWithParent(cmd *exec.Cmd) {}

// NeedsGuard is whether a guard process (Guard) has to watch the parent:
// everywhere but Linux, which has pdeathsig, and Windows, which has jobs.
func NeedsGuard() bool { return runtime.GOOS != "windows" }
