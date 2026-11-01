package proc

import (
	"os/exec"
	"syscall"
)

// DieWithParent has the kernel send cmd SIGTERM when the agent that started
// it goes, however it goes -- kill -9 included, when no way out of ours runs.
// Set before Start. (Strictly, when the thread that started it exits; the Go
// runtime keeps its threads for the life of the process.)
func DieWithParent(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGTERM
}

// NeedsGuard is whether DieWithParent is not enough here and a guard process
// (Guard) has to watch the parent instead. Linux has the kernel do it.
func NeedsGuard() bool { return false }
