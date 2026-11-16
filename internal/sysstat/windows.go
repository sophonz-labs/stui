//go:build windows

package sysstat

import (
	"encoding/binary"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows: kernel32 for memory, CPU time and each process's working set,
// ntdll for the page file, and a Toolhelp snapshot for who is whose parent.
// There is no load average on Windows; ReadLoad says so.

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetSystemTimes       = kernel32.NewProc("GetSystemTimes")
	procGetProcessMemoryInfo = kernel32.NewProc("K32GetProcessMemoryInfo")
)

// GetSystemTimes: kernel time includes idle time, so busy is kernel less
// idle, plus user. In 100 ns units, 64 bits wide.
func ReadCPU() (CPUTimes, bool) {
	var idle, kernel, user windows.Filetime
	r, _, _ := procGetSystemTimes.Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user)))
	if r == 0 {
		return CPUTimes{}, false
	}
	ft := func(f windows.Filetime) uint64 { return uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime) }
	i, k, u := ft(idle), ft(kernel), ft(user)
	if k < i {
		return CPUTimes{}, false
	}
	return CPUTimes{Busy: []uint64{k - i, u}, Idle: []uint64{i}}, true
}

func ReadLoad() (float64, bool) { return 0, false }

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func ReadMemory() (Memory, bool) {
	s := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&s)))
	if r == 0 || s.TotalPhys == 0 {
		return Memory{}, false
	}
	m := Memory{Total: s.TotalPhys, Available: s.AvailPhys}
	m.SwapTotal, m.SwapUsed = pageFiles()
	return m, true
}

// pageFiles is the page files' size and use, which is what swap is here.
// GlobalMemoryStatusEx's "page file" is the commit limit, physical memory
// included, and says nothing about how much of the file is in use; SYSTEM_
// PAGEFILE_INFORMATION does, in pages, one entry per file.
func pageFiles() (total, used uint64) {
	buf := make([]byte, 4096)
	for {
		var n uint32
		err := windows.NtQuerySystemInformation(windows.SystemPageFileInformation, unsafe.Pointer(&buf[0]), uint32(len(buf)), &n)
		if err == nil {
			buf = buf[:n]
			break
		}
		if len(buf) >= 1<<16 {
			return 0, 0
		}
		buf = make([]byte, len(buf)*4)
	}
	page := uint64(os.Getpagesize())
	// NextEntryOffset, TotalSize, TotalInUse, PeakUsage (ULONG), then the
	// file's name.
	for off := 0; off+16 <= len(buf); {
		next := binary.LittleEndian.Uint32(buf[off:])
		total += uint64(binary.LittleEndian.Uint32(buf[off+4:])) * page
		used += uint64(binary.LittleEndian.Uint32(buf[off+8:])) * page
		if next == 0 {
			break
		}
		off += int(next)
	}
	return total, used
}

// NewWalk takes one Toolhelp snapshot of the process table, lazily.
func NewWalk() Walk { return &windowsWalk{} }

type windowsWalk struct{ table map[int][]int }

func (w *windowsWalk) Children(pid int) []int {
	if w.table == nil {
		w.table = map[int][]int{}
		snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
		if err != nil {
			return nil
		}
		defer windows.CloseHandle(snap)
		e := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
		for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
			// Pid 0 names itself as its own parent.
			if e.ProcessID != 0 && e.ProcessID != e.ParentProcessID {
				w.table[int(e.ParentProcessID)] = append(w.table[int(e.ParentProcessID)], int(e.ProcessID))
			}
		}
	}
	return w.table[pid]
}

type processMemoryCounters struct {
	Cb                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

// RSS is the working set, Windows' resident size.
func (w *windowsWalk) RSS(pid int) (uint64, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return 0, false
	}
	defer windows.CloseHandle(h)
	c := processMemoryCounters{Cb: uint32(unsafe.Sizeof(processMemoryCounters{}))}
	r, _, _ := procGetProcessMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&c)), uintptr(c.Cb))
	if r == 0 {
		return 0, false
	}
	return uint64(c.WorkingSetSize), true
}

// CPU is GetProcessTimes' kernel and user time, in 100 ns units.
func (w *windowsWalk) CPU(pid int) (time.Duration, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return 0, false
	}
	defer windows.CloseHandle(h)
	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(h, &created, &exited, &kernel, &user) != nil {
		return 0, false
	}
	ft := func(f windows.Filetime) uint64 { return uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime) }
	return time.Duration(ft(kernel)+ft(user)) * 100, true
}
