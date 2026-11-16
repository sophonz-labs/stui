// Package sysstat reads the machine stui is running on -- CPU time, load,
// memory, swap and the process table -- from the operating system itself,
// without starting a process to ask. It is read every few seconds by the
// metrics ring buffer , which exists to watch a
// machine under load and must not add to it.
//
// Every reader says whether it could answer. A platform or a call that
// cannot is false, never a made-up zero.
package sysstat

import (
	"runtime"
	"time"
)

// Memory is the machine's, in bytes. Available is what a new process could
// get without swapping -- free plus what the system would reclaim -- and
// Used is Total less that, the same on every platform.
type Memory struct {
	Total     uint64
	Available uint64
	SwapTotal uint64
	SwapUsed  uint64
}

func (m Memory) Used() uint64 {
	if m.Available > m.Total {
		return 0
	}
	return m.Total - m.Available
}

// CPUTimes is time spent since boot, as the platform's own counters in the
// platform's own unit, split into the ones that count busy time and the
// ones that count idle time. Only the difference between two readings means
// anything. Wrap32 says each counter is 32 bits wide and wraps: macOS's
// host-wide ticks do, every seven weeks on a ten-core machine.
type CPUTimes struct {
	Busy, Idle []uint64
	Wrap32     bool
}

// Percent is how busy the machine was between two readings, 0 to 100, and
// false when nothing measurable passed or a counter went backwards.
func Percent(prev, now CPUTimes) (float64, bool) {
	if len(prev.Busy) != len(now.Busy) || len(prev.Idle) != len(now.Idle) || len(now.Busy) == 0 {
		return 0, false
	}
	delta := func(a, b []uint64) (uint64, bool) {
		var sum uint64
		for i := range a {
			switch {
			case now.Wrap32:
				sum += uint64(uint32(b[i]) - uint32(a[i]))
			case b[i] < a[i]:
				return 0, false
			default:
				sum += b[i] - a[i]
			}
		}
		return sum, true
	}
	busy, ok1 := delta(prev.Busy, now.Busy)
	idle, ok2 := delta(prev.Idle, now.Idle)
	if !ok1 || !ok2 || busy+idle == 0 {
		return 0, false
	}
	return float64(busy) / float64(busy+idle) * 100, true
}

// Walk answers questions about processes for one sample: whose children
// are whose, and how much memory each holds. A platform that can only list
// the whole table reads it once per Walk, not once per question.
type Walk interface {
	Children(pid int) []int
	RSS(pid int) (uint64, bool)
}

// CPUWalk is a Walk that can also say how much CPU time a process has
// used since it started, user and system together. Every platform stui reads
// processes on has it; a Walk without it gives memory only.
type CPUWalk interface {
	Walk
	CPU(pid int) (time.Duration, bool)
}

// Usage is what Measure found: memory and processes, and each measured
// process's CPU time so far (nil when the Walk cannot say), by pid.
type Usage struct {
	RSS   uint64
	Procs int
	CPU   map[int]time.Duration
}

// Tree is the processes under roots, the roots included, each counted once
// however the roots relate. RSS is their sum; a process that could not be
// measured (gone between the two questions, or not ours) is counted in
// Procs and not in RSS.
func Tree(w Walk, roots ...int) (rss uint64, procs int) {
	u := Measure(w, roots, nil)
	return u.RSS, u.Procs
}

// Measure is Tree of roots, and leaves besides: each leaf counted by
// itself, never what is under it, also when a root's tree reaches it. That
// is stui's own share: its server and what it starts, and the multiplexer
// server, but not the shells and coding agents the server runs.
func Measure(w Walk, roots, leaves []int) Usage {
	var u Usage
	cw, hasCPU := w.(CPUWalk)
	if hasCPU {
		u.CPU = map[int]time.Duration{}
	}
	seen := map[int]bool{}
	root := map[int]bool{}
	leaf := map[int]bool{}
	for _, l := range leaves {
		if l > 0 {
			leaf[l] = true
		}
	}
	queue := make([]int, 0, 16)
	add := func(pid int, isRoot bool) {
		if pid > 0 && !seen[pid] {
			seen[pid] = true
			if isRoot {
				root[pid] = true
			}
			queue = append(queue, pid)
		}
	}
	for _, r := range roots {
		add(r, true)
	}
	for _, l := range leaves {
		add(l, true)
	}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		n, ok := w.RSS(pid)
		switch {
		case ok:
			u.RSS += n
			u.Procs++
			if hasCPU {
				if t, ok := cw.CPU(pid); ok {
					u.CPU[pid] = t
				}
			}
		case root[pid]:
			// A root that is not there -- a multiplexer server that has
			// not started, or has stopped -- is nobody's process.
			continue
		default:
			// There but not measurable: still one of stui's.
			u.Procs++
		}
		if leaf[pid] {
			continue
		}
		for _, c := range w.Children(pid) {
			add(c, false)
		}
	}
	return u
}

// CPUs is the logical processor count.
func CPUs() int { return runtime.NumCPU() }

// The per-platform readers, each in its own file:
//
//	ReadCPU() (CPUTimes, bool)
//	ReadLoad() (float64, bool)
//	ReadMemory() (Memory, bool)
//	NewWalk() Walk   (a CPUWalk where the platform can say)
