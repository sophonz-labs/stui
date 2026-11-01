//go:build !windows

package proc

import (
	"errors"
	"syscall"
)

// PtyFree asks for a pseudo-terminal and gives it straight back: ErrNoPty
// when the machine has none left, nil when it had one a moment ago.
func PtyFree() error {
	fd, err := syscall.Open("/dev/ptmx", syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		if NoPty(err) {
			return ErrNoPty
		}
		return err
	}
	syscall.Close(fd)
	return nil
}

// NoPty is whether err is the one a full pty table gives.
func NoPty(err error) bool {
	return errors.Is(err, ErrNoPty) || errors.Is(err, syscall.ENXIO) || errors.Is(err, syscall.ENOSPC)
}
