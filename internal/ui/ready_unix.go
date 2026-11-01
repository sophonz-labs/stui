//go:build !windows

package ui

import "golang.org/x/sys/unix"

// ready is whether fd has input within ms. select, not poll: macOS's poll
// does not take a terminal.
func ready(fd uintptr, ms int) bool {
	var set unix.FdSet
	set.Set(int(fd))
	tv := unix.NsecToTimeval(int64(ms) * 1e6)
	n, err := unix.Select(int(fd)+1, &set, nil, nil, &tv)
	return err == nil && n > 0
}
