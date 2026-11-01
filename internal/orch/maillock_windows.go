//go:build windows

package orch

import (
	"os"

	"golang.org/x/sys/windows"
)

// tryMailLock takes the first byte of the lock file, failing at once when
// another handle has it (as internal/account does).
func tryMailLock(f *os.File) bool {
	var ol windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ol) == nil
}
