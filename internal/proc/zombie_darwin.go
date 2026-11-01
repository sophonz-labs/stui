package proc

import "golang.org/x/sys/unix"

// zombie is whether pid has ended and waits to be reaped (SZOMB), asked of
// the kernel in-process: Probe is called in loops.
func zombie(pid int) bool {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	return err == nil && k.Proc.P_stat == 5 // SZOMB
}
