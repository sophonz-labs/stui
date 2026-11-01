//go:build !windows

package orch

import (
	"os"
	"syscall"
)

func tryMailLock(f *os.File) bool {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil
}
