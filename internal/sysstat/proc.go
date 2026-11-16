package sysstat

import (
	"strconv"
	"strings"
)

// Parsers for Linux's /proc files. Built everywhere, so they are tested
// everywhere; only linux.go reads the real files.

// ParseStat reads the "cpu" line of /proc/stat: user nice system idle
// iowait irq softirq steal, in clock ticks. Guest time is already inside
// user and nice, so it is not counted again. Waiting on I/O is idle.
func ParseStat(out string) (CPUTimes, bool) {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || f[0] != "cpu" {
			continue
		}
		n := make([]uint64, 0, 8)
		for _, s := range f[1:] {
			v, err := strconv.ParseUint(s, 10, 64)
			if err != nil {
				return CPUTimes{}, false
			}
			n = append(n, v)
		}
		for len(n) < 8 {
			n = append(n, 0)
		}
		return CPUTimes{
			Busy: []uint64{n[0], n[1], n[2], n[5], n[6], n[7]},
			Idle: []uint64{n[3], n[4]},
		}, true
	}
	return CPUTimes{}, false
}

// ParseLoadAvg reads the first field of /proc/loadavg.
func ParseLoadAvg(out string) (float64, bool) {
	f := strings.Fields(out)
	if len(f) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(f[0], 64)
	return v, err == nil
}

// ParseMemInfo reads /proc/meminfo, whose values are in kibibytes.
//
// MemAvailable is the kernel's own estimate and is what to use; the older
// sum of free + buffers + cached overstates it on a machine with a large
// page cache that is not actually reclaimable. internal/health reads its
// memory warnings from this too.
func ParseMemInfo(out string) (Memory, bool) {
	var m Memory
	var swapFree uint64
	var total, avail bool
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			continue
		}
		kb, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			m.Total, total = kb*1024, true
		case "MemAvailable":
			m.Available, avail = kb*1024, true
		case "SwapTotal":
			m.SwapTotal = kb * 1024
		case "SwapFree":
			swapFree = kb * 1024
		}
	}
	if swapFree < m.SwapTotal {
		m.SwapUsed = m.SwapTotal - swapFree
	}
	return m, total && avail && m.Total > 0
}

// ParsePPID reads the parent out of /proc/<pid>/stat. The command name in
// parentheses may hold spaces and parentheses of its own, so the fields
// are counted from the last ')'.
func ParsePPID(out string) (int, bool) {
	i := strings.LastIndexByte(out, ')')
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(out[i+1:])
	if len(f) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(f[1])
	return n, err == nil
}

// ParseStatm reads the resident pages, the second field of /proc/<pid>/statm.
func ParseStatm(out string) (uint64, bool) {
	f := strings.Fields(out)
	if len(f) < 2 {
		return 0, false
	}
	n, err := strconv.ParseUint(f[1], 10, 64)
	return n, err == nil
}

// ParseStatCPU is utime + stime, the 14th and 15th fields of
// /proc/<pid>/stat, in clock ticks; counted from the last ')' as ParsePPID.
func ParseStatCPU(out string) (uint64, bool) {
	i := strings.LastIndexByte(out, ')')
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(out[i+1:])
	if len(f) < 13 {
		return 0, false
	}
	u, err1 := strconv.ParseUint(f[11], 10, 64)
	s, err2 := strconv.ParseUint(f[12], 10, 64)
	return u + s, err1 == nil && err2 == nil
}
