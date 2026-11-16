//go:build linux

package sysstat

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Linux says all of it in /proc. Each reader is a read of one small file
// and a parse; the parsers are in proc.go, apart, so they are tested on
// fixtures.

var procRoot = "/proc"

func read(path string) string {
	b, err := os.ReadFile(filepath.Join(procRoot, path))
	if err != nil {
		return ""
	}
	return string(b)
}

func ReadCPU() (CPUTimes, bool)  { return ParseStat(read("stat")) }
func ReadLoad() (float64, bool)  { return ParseLoadAvg(read("loadavg")) }
func ReadMemory() (Memory, bool) { return ParseMemInfo(read("meminfo")) }
func NewWalk() Walk              { return &linuxWalk{page: uint64(os.Getpagesize())} }

// linuxWalk reads a process's children from /proc/<pid>/task/*/children,
// which the kernel keeps when it is built with checkpoint/restore (every
// mainstream distribution). Without those files it reads every process's
// parent once, for the whole Walk.
type linuxWalk struct {
	page  uint64
	table map[int][]int
}

func (w *linuxWalk) Children(pid int) []int {
	if w.table == nil {
		if kids, ok := childrenFiles(pid); ok {
			return kids
		}
		w.table = scanParents()
	}
	return w.table[pid]
}

func childrenFiles(pid int) ([]int, bool) {
	tasks, err := filepath.Glob(filepath.Join(procRoot, strconv.Itoa(pid), "task", "*", "children"))
	if err != nil || len(tasks) == 0 {
		// A process that is gone has no task directory either; only a
		// live one without the file says the kernel does not keep it.
		if _, err := os.Stat(filepath.Join(procRoot, strconv.Itoa(pid), "task")); err != nil {
			return nil, true
		}
		return nil, false
	}
	var out []int
	for _, t := range tasks {
		b, err := os.ReadFile(t)
		if err != nil {
			continue
		}
		for _, f := range strings.Fields(string(b)) {
			if n, err := strconv.Atoi(f); err == nil {
				out = append(out, n)
			}
		}
	}
	return out, true
}

func scanParents() map[int][]int {
	table := map[int][]int{}
	ents, _ := os.ReadDir(procRoot)
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if ppid, ok := ParsePPID(read(filepath.Join(e.Name(), "stat"))); ok {
			table[ppid] = append(table[ppid], pid)
		}
	}
	return table
}

// RSS is the second field of /proc/<pid>/statm, in pages.
func (w *linuxWalk) RSS(pid int) (uint64, bool) {
	pages, ok := ParseStatm(read(filepath.Join(strconv.Itoa(pid), "statm")))
	if !ok {
		return 0, false
	}
	return pages * w.page, true
}

// userHZ is the unit of /proc/<pid>/stat's times: USER_HZ, which the
// kernel fixes at 100 for user space on every architecture stui builds for.
const userHZ = 100

// CPU is utime + stime of /proc/<pid>/stat.
func (w *linuxWalk) CPU(pid int) (time.Duration, bool) {
	ticks, ok := ParseStatCPU(read(filepath.Join(strconv.Itoa(pid), "stat")))
	if !ok {
		return 0, false
	}
	return time.Duration(ticks) * time.Second / userHZ, true
}
