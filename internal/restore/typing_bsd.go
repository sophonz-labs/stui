//go:build darwin || freebsd || netbsd || openbsd

package restore

import "golang.org/x/sys/unix"

const getTermios = unix.TIOCGETA
