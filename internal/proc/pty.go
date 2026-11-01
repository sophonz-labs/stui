package proc

import "errors"

// ErrNoPty is a machine with every pseudo-terminal taken: nothing on it --
// ttyd, tmux, a terminal app -- can open another until some are closed. On
// macOS that is kern.tty.ptmx_max (511 by default), and what tmux and ttyd
// print is "Device not configured" (ENXIO); on Linux it is
// /proc/sys/kernel/pty/max, and ENOSPC.
var ErrNoPty = errors.New("no free pty")
