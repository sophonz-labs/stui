//go:build darwin

package sysstat

import (
	"encoding/binary"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// macOS has the numbers in Mach and libproc, not in sysctl, and stui is built
// with cgo off. So the few functions needed are called in libSystem the way
// golang.org/x/sys calls everything else there: a trampoline per function
// (darwin_trampolines.s) and the runtime's own libc call, syscall.syscall6.
// No new dependency, and nothing is started.

//go:linkname syscall_syscall6 syscall.syscall6
func syscall_syscall6(fn, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2 uintptr, err syscall.Errno)

var (
	libc_mach_host_self_trampoline_addr     uintptr
	libc_host_statistics_trampoline_addr    uintptr
	libc_host_statistics64_trampoline_addr  uintptr
	libc_proc_pidinfo_trampoline_addr       uintptr
	libc_proc_listchildpids_trampoline_addr uintptr
	libc_mach_timebase_info_trampoline_addr uintptr
)

//go:cgo_import_dynamic libc_mach_host_self mach_host_self "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_host_statistics host_statistics "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_host_statistics64 host_statistics64 "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_proc_pidinfo proc_pidinfo "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_proc_listchildpids proc_listchildpids "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_mach_timebase_info mach_timebase_info "/usr/lib/libSystem.B.dylib"

const (
	hostCPULoadInfo = 3 // HOST_CPU_LOAD_INFO: user, system, idle, nice ticks
	hostVMInfo64    = 4 // HOST_VM_INFO64
	vmInfo64Count   = 38
	procPIDTaskInfo = 4 // PROC_PIDTASKINFO
	taskInfoSize    = 96
)

// host is this machine's host port. mach_host_self adds a send right on
// every call, so it is asked for once.
var host = sync.OnceValue(func() uintptr {
	r, _, _ := syscall_syscall6(libc_mach_host_self_trampoline_addr, 0, 0, 0, 0, 0, 0)
	return r
})

func ReadCPU() (CPUTimes, bool) {
	var ticks [4]uint32
	count := uint32(len(ticks))
	r, _, _ := syscall_syscall6(libc_host_statistics_trampoline_addr, host(), hostCPULoadInfo,
		uintptr(unsafe.Pointer(&ticks[0])), uintptr(unsafe.Pointer(&count)), 0, 0)
	if r != 0 {
		return CPUTimes{}, false
	}
	// user, system, idle, nice; each a 32-bit count that wraps.
	return CPUTimes{
		Busy:   []uint64{uint64(ticks[0]), uint64(ticks[1]), uint64(ticks[3])},
		Idle:   []uint64{uint64(ticks[2])},
		Wrap32: true,
	}, true
}

// ReadLoad is vm.loadavg: three fixed-point averages and their scale.
func ReadLoad() (float64, bool) {
	b, err := unix.SysctlRaw("vm.loadavg")
	if err != nil || len(b) < 24 {
		return 0, false
	}
	load := binary.LittleEndian.Uint32(b[0:4])
	scale := binary.LittleEndian.Uint64(b[16:24])
	if scale == 0 {
		return 0, false
	}
	return float64(load) / float64(scale), true
}

// ReadMemory counts available the way vm_stat's reader in internal/health
// always has: free + inactive + speculative + purgeable pages.
func ReadMemory() (Memory, bool) {
	total, err := unix.SysctlUint64("hw.memsize")
	if err != nil || total == 0 {
		return Memory{}, false
	}
	page, err := unix.SysctlUint32("hw.pagesize")
	if err != nil || page == 0 {
		return Memory{}, false
	}
	var vm [vmInfo64Count]uint32
	count := uint32(vmInfo64Count)
	r, _, _ := syscall_syscall6(libc_host_statistics64_trampoline_addr, host(), hostVMInfo64,
		uintptr(unsafe.Pointer(&vm[0])), uintptr(unsafe.Pointer(&count)), 0, 0)
	if r != 0 {
		return Memory{}, false
	}
	// vm_statistics64: free_count at 0, inactive_count at 8, purgeable_count
	// at 88, speculative_count at 92 (bytes), all natural_t.
	pages := uint64(vm[0]) + uint64(vm[2]) + uint64(vm[22]) + uint64(vm[23])
	m := Memory{Total: total, Available: pages * uint64(page)}
	if m.Available > m.Total {
		m.Available = m.Total
	}
	// xsw_usage: total, avail, used (uint64), then the page size.
	if b, err := unix.SysctlRaw("vm.swapusage"); err == nil && len(b) >= 24 {
		m.SwapTotal = binary.LittleEndian.Uint64(b[0:8])
		m.SwapUsed = binary.LittleEndian.Uint64(b[16:24])
	}
	return m, true
}

// NewWalk asks libproc about each process as it is reached, so a sample
// costs a few calls for stui's own processes rather than a copy of the whole
// process table (kern.proc.all is over a megabyte on a busy Mac).
func NewWalk() Walk { return &darwinWalk{} }

// darwinWalk keeps each process's proc_taskinfo for the one sample, so its
// memory and its CPU time are one call.
type darwinWalk struct{ info map[int]*[taskInfoSize]byte }

func (w *darwinWalk) task(pid int) (*[taskInfoSize]byte, bool) {
	if b, ok := w.info[pid]; ok {
		return b, b != nil
	}
	if w.info == nil {
		w.info = map[int]*[taskInfoSize]byte{}
	}
	var info [taskInfoSize]byte
	n, _, _ := syscall_syscall6(libc_proc_pidinfo_trampoline_addr, uintptr(pid), procPIDTaskInfo, 0,
		uintptr(unsafe.Pointer(&info[0])), taskInfoSize, 0)
	if int32(n) != taskInfoSize {
		w.info[pid] = nil
		return nil, false
	}
	w.info[pid] = &info
	return &info, true
}

func (*darwinWalk) Children(pid int) []int {
	var buf [512]int32
	n, _, _ := syscall_syscall6(libc_proc_listchildpids_trampoline_addr, uintptr(pid),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*4), 0, 0, 0)
	count := int(int32(n))
	if count <= 0 {
		return nil
	}
	if count > len(buf) {
		count = len(buf)
	}
	out := make([]int, 0, count)
	for _, c := range buf[:count] {
		if c > 0 {
			out = append(out, int(c))
		}
	}
	return out
}

// RSS is proc_taskinfo's pti_resident_size. Any process of the same user
// answers, which is every process stui starts.
func (w *darwinWalk) RSS(pid int) (uint64, bool) {
	info, ok := w.task(pid)
	if !ok {
		return 0, false
	}
	return binary.LittleEndian.Uint64(info[8:16]), true
}

// CPU is pti_total_user + pti_total_system, which are Mach absolute time:
// nanoseconds on Intel, 125/3 of one on Apple silicon (mach_timebase_info).
func (w *darwinWalk) CPU(pid int) (time.Duration, bool) {
	info, ok := w.task(pid)
	tb := timebase()
	if !ok || tb[1] == 0 {
		return 0, false
	}
	ticks := binary.LittleEndian.Uint64(info[16:24]) + binary.LittleEndian.Uint64(info[24:32])
	return time.Duration(ticks / uint64(tb[1]) * uint64(tb[0])), true
}

// timebase is mach_timebase_info's numer and denom, asked for once.
var timebase = sync.OnceValue(func() [2]uint32 {
	var tb [2]uint32
	if r, _, _ := syscall_syscall6(libc_mach_timebase_info_trampoline_addr, uintptr(unsafe.Pointer(&tb[0])), 0, 0, 0, 0, 0); r != 0 {
		return [2]uint32{}
	}
	return tb
})
