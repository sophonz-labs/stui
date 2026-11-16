package sysstat

import (
	"os"
	"runtime"
	"testing"
	"time"
)

type cpuFake struct {
	fakeWalk
	cpu map[int]time.Duration
}

func (f cpuFake) CPU(pid int) (time.Duration, bool) {
	t, ok := f.cpu[pid]
	return t, ok
}

// stui's own share: its server (10) and its child (11), and the tmux server (20)
// alone -- not its shells (21, 22) or the agent running in one (23, 24).
func TestMeasureLeaves(t *testing.T) {
	w := cpuFake{fakeWalk{
		kids: map[int][]int{10: {11}, 20: {21, 22}, 22: {23}, 23: {24}},
		rss:  map[int]uint64{10: 30, 11: 5, 20: 4, 21: 2, 22: 2, 23: 400, 24: 50},
	}, map[int]time.Duration{10: 3 * time.Second, 11: time.Second, 20: 2 * time.Second, 23: time.Hour}}
	u := Measure(w, []int{10}, []int{20})
	if u.RSS != 39 || u.Procs != 3 {
		t.Fatalf("self = %d bytes in %d, want 39 in 3", u.RSS, u.Procs)
	}
	if len(u.CPU) != 3 || u.CPU[10] != 3*time.Second || u.CPU[20] != 2*time.Second {
		t.Errorf("cpu %v", u.CPU)
	}
	// A leaf a root's tree reaches is still not walked into (psmux, a child
	// of stui on Windows).
	w.kids[11] = []int{20}
	if u := Measure(w, []int{10}, []int{20}); u.RSS != 39 || u.Procs != 3 {
		t.Errorf("leaf under a root = %d in %d, want 39 in 3", u.RSS, u.Procs)
	}
	// A Walk without CPU gives memory only.
	if u := Measure(w.fakeWalk, []int{10}, nil); u.CPU != nil || u.RSS == 0 {
		t.Errorf("no CPU walk: %+v", u)
	}
}

func TestParseStatCPU(t *testing.T) {
	stat := "4242 (tmux: server (x)) S 1 4242 4242 0 -1 4194624 391 0 0 0 1234 567 0 0 20 0 1 0 100 1000 200"
	if n, ok := ParseStatCPU(stat); !ok || n != 1801 {
		t.Errorf("ParseStatCPU = %d, %v; want 1801", n, ok)
	}
	if _, ok := ParseStatCPU("4242 (x) S 1"); ok {
		t.Error("short stat parsed")
	}
}

// The real reader gives this test process the CPU time it is spending.
func TestCPUHere(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("nothing is read on", runtime.GOOS)
	}
	cw, ok := NewWalk().(CPUWalk)
	if !ok {
		t.Fatal("no CPU on this platform's walk")
	}
	before, ok := cw.CPU(os.Getpid())
	if !ok {
		t.Fatal("CPU failed")
	}
	for end := time.Now().Add(300 * time.Millisecond); time.Now().Before(end); {
	}
	after, _ := NewWalk().(CPUWalk).CPU(os.Getpid())
	if d := after - before; d < 100*time.Millisecond || d > 5*time.Second {
		t.Errorf("300 ms of spinning read as %v", d)
	}
}
