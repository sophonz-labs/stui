//go:build !windows

package proc

import (
	"errors"
	"os/exec"
	"syscall"
)

func Probe(pid int) State {
	err := syscall.Kill(pid, 0)
	switch {
	case err == nil:
		// A process that has ended but not been reaped yet still answers
		// signal 0. It holds nothing and will run nothing: it is gone. In a
		// container whose init reaps slowly, a killed agent stayed like this
		// past stui exit's wait, and was reported as one it could not stop.
		if zombie(pid) {
			return Gone
		}
		return Running
	case errors.Is(err, syscall.EPERM):
		return Denied
	default:
		return Gone
	}
}

func Terminate(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }
func Kill(pid int) error      { return syscall.Kill(pid, syscall.SIGKILL) }

// Detach makes cmd a session leader, so closing the terminal that started it
// does not take it with it.
func Detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// Hide is a Windows concern; there is no console window to keep away here.
func Hide(cmd *exec.Cmd) {}

// Contain is a Windows concern: a Unix child is already signalled with its
// parent's process group, and stui's agent terminates its children itself.
func Contain(cmd *exec.Cmd) {}

// EnableVT is a Windows concern; Unix terminals interpret escapes already.
func EnableVT() {}
