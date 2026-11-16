package sysstat

import (
	"math"
	"os"
	"runtime"
	"testing"
)

func TestPercent(t *testing.T) {
	prev := CPUTimes{Busy: []uint64{100, 50}, Idle: []uint64{850}}
	now := CPUTimes{Busy: []uint64{130, 60}, Idle: []uint64{910}}
	p, ok := Percent(prev, now)
	if !ok || math.Abs(p-40) > 1e-9 {
		t.Fatalf("Percent = %v %v, want 40", p, ok)
	}
	if _, ok := Percent(now, now); ok {
		t.Error("no time passing gave a percentage")
	}
	if _, ok := Percent(now, prev); ok {
		t.Error("counters going backwards gave a percentage")
	}
	if _, ok := Percent(CPUTimes{}, now); ok {
		t.Error("a missing reading gave a percentage")
	}
}

// macOS's host-wide ticks are 32 bits and wrap; the delta must too.
func TestPercentWrap32(t *testing.T) {
	prev := CPUTimes{Busy: []uint64{math.MaxUint32 - 9}, Idle: []uint64{math.MaxUint32 - 29}, Wrap32: true}
	now := CPUTimes{Busy: []uint64{20}, Idle: []uint64{60}, Wrap32: true}
	// busy 30, idle 90.
	p, ok := Percent(prev, now)
	if !ok || math.Abs(p-25) > 1e-9 {
		t.Fatalf("Percent across a wrap = %v %v, want 25", p, ok)
	}
}

type fakeWalk struct {
	kids map[int][]int
	rss  map[int]uint64
}

func (f fakeWalk) Children(pid int) []int { return f.kids[pid] }
func (f fakeWalk) RSS(pid int) (uint64, bool) {
	n, ok := f.rss[pid]
	return n, ok
}

// stui (10) runs a child (11); the tmux server (20) is not stui's child, it
// daemonized, and holds two shells, one running an agent that runs a tool.
// A process belonging to nobody (99) is not counted.
func TestTree(t *testing.T) {
	w := fakeWalk{
		kids: map[int][]int{
			1:  {10, 20, 99},
			10: {11},
			20: {21, 22},
			22: {23},
			23: {24},
		},
		rss: map[int]uint64{10: 30, 11: 5, 20: 4, 21: 2, 22: 2, 23: 400, 24: 50, 99: 1 << 30},
	}
	rss, n := Tree(w, 10, 20)
	if rss != 493 || n != 7 {
		t.Fatalf("Tree = %d bytes in %d, want 493 in 7", rss, n)
	}
	// The same process named twice, or reached twice, counts once.
	if rss, n := Tree(w, 10, 20, 10, 22); rss != 493 || n != 7 {
		t.Fatalf("overlapping roots = %d in %d, want 493 in 7", rss, n)
	}
	// A multiplexer that is not running is no process at all.
	if rss, n := Tree(w, 10, 555); rss != 35 || n != 2 {
		t.Fatalf("missing root = %d in %d, want 35 in 2", rss, n)
	}
	// One that cannot be measured (another user's, under sudo) is still
	// stui's, and so are its children.
	delete(w.rss, 23)
	if rss, n := Tree(w, 10, 20); rss != 93 || n != 7 {
		t.Fatalf("unmeasurable = %d in %d, want 93 in 7", rss, n)
	}
	// A table that loops does not loop the walk.
	w.kids[24] = []int{20}
	if _, n := Tree(w, 20); n != 5 {
		t.Fatalf("a cycle gave %d processes, want 5", n)
	}
}

// The real readers, on the machine running the test.
func TestReadHere(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("nothing is read on", runtime.GOOS)
	}
	m, ok := ReadMemory()
	if !ok || m.Total == 0 || m.Available > m.Total || m.SwapUsed > m.SwapTotal {
		t.Errorf("ReadMemory = %+v, %v", m, ok)
	}
	if _, ok := ReadCPU(); !ok {
		t.Error("ReadCPU failed")
	}
	if _, ok := ReadLoad(); !ok && runtime.GOOS != "windows" {
		t.Error("ReadLoad failed")
	}
	rss, n := Tree(NewWalk(), os.Getpid())
	if n < 1 || rss < 1<<20 {
		t.Errorf("this test's own tree = %d bytes in %d", rss, n)
	}
}
