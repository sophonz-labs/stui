package proc

import (
	"syscall"
	"time"
)

// waitEither blocks until parent or child exits, and says whether it was the
// parent. kqueue's NOTE_EXIT, so it costs nothing while both are there.
func waitEither(parent, child int) (parentWent bool) {
	kq, err := syscall.Kqueue()
	if err != nil {
		return pollEither(parent, child)
	}
	defer syscall.Close(kq)
	watch := func(pid int) error {
		ev := syscall.Kevent_t{
			Ident:  uint64(pid),
			Filter: syscall.EVFILT_PROC,
			Flags:  syscall.EV_ADD | syscall.EV_ONESHOT,
			Fflags: syscall.NOTE_EXIT,
		}
		_, err := syscall.Kevent(kq, []syscall.Kevent_t{ev}, nil, nil)
		return err
	}
	// Either one already gone before it could be watched counts as gone.
	if watch(child) != nil {
		return false
	}
	if watch(parent) != nil || !Present(parent) {
		return true
	}
	out := make([]syscall.Kevent_t, 2)
	for {
		n, err := syscall.Kevent(kq, nil, out, nil)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return pollEither(parent, child)
		}
		for i := 0; i < n; i++ {
			if int(out[i].Ident) == child {
				return false
			}
		}
		if n > 0 {
			return true
		}
	}
}

func pollEither(parent, child int) bool {
	for {
		if !Alive(child) {
			return false
		}
		if !Present(parent) {
			return true
		}
		time.Sleep(time.Second)
	}
}
