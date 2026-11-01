//go:build darwin || linux || freebsd || netbsd || openbsd

package restore

import "golang.org/x/sys/unix"

func ttyEditing(path string) (ready, ok bool) {
	if path == "" {
		return false, false
	}
	// O_NOCTTY: looking at a pane's terminal must never make it ours.
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		return false, false
	}
	defer unix.Close(fd)
	tio, err := unix.IoctlGetTermios(fd, getTermios)
	if err != nil {
		return false, false
	}
	return tio.Lflag&unix.ICANON == 0 && tio.Lflag&unix.ECHO == 0, true
}
