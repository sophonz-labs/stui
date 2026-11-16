package sysstat

import "testing"

func TestParseStat(t *testing.T) {
	c, ok := ParseStat("cpu  4705 356 584 3699176 23060 0 277 12 0 0\ncpu0 1 2 3 4 5 6 7 8 0 0\nintr 1\n")
	if !ok {
		t.Fatal("did not parse")
	}
	// user nice system irq softirq steal / idle iowait
	want := CPUTimes{Busy: []uint64{4705, 356, 584, 0, 277, 12}, Idle: []uint64{3699176, 23060}}
	for i := range want.Busy {
		if c.Busy[i] != want.Busy[i] {
			t.Fatalf("busy = %v, want %v", c.Busy, want.Busy)
		}
	}
	for i := range want.Idle {
		if c.Idle[i] != want.Idle[i] {
			t.Fatalf("idle = %v, want %v", c.Idle, want.Idle)
		}
	}
	// An old kernel with four fields.
	if c, ok := ParseStat("cpu 1 2 3 4\n"); !ok || len(c.Busy) != 6 || c.Idle[0] != 4 {
		t.Errorf("short line = %+v %v", c, ok)
	}
	if _, ok := ParseStat("nonsense"); ok {
		t.Error("nonsense parsed")
	}
}

func TestParseLoadAvg(t *testing.T) {
	if l, ok := ParseLoadAvg("4.20 3.10 2.00 3/712 12345\n"); !ok || l != 4.2 {
		t.Errorf("ParseLoadAvg = %v %v", l, ok)
	}
	if _, ok := ParseLoadAvg(""); ok {
		t.Error("empty parsed")
	}
}

func TestParseMemInfo(t *testing.T) {
	m, ok := ParseMemInfo(`MemTotal:       65856124 kB
MemFree:          214512 kB
MemAvailable:    1048576 kB
SwapTotal:       2097152 kB
SwapFree:         104856 kB
`)
	if !ok || m.Total != 65856124*1024 || m.Available != 1<<30 {
		t.Fatalf("memory = %+v %v", m, ok)
	}
	if m.SwapTotal != 2<<30 || m.SwapUsed != (2097152-104856)*1024 {
		t.Errorf("swap = %+v", m)
	}
	if m.Used() != m.Total-m.Available {
		t.Errorf("used = %d", m.Used())
	}
	// A kernel too old for MemAvailable is not a machine with none.
	if _, ok := ParseMemInfo("MemTotal: 1000 kB\n"); ok {
		t.Error("no MemAvailable parsed as ok")
	}
}

func TestParsePPID(t *testing.T) {
	cases := map[string]int{
		"1234 (bash) S 1000 1234 1234 34816 ...":          1000,
		"77 (tmux: server) S 1 77 77 0 -1 4194624 ...":    1,
		"88 (a) b (c)) R 42 88 88 0 -1 4194304 ...":       42,
		"99 (claude) S 23 99 23 34817 99 4194304 1 2 3 4": 23,
	}
	for in, want := range cases {
		if got, ok := ParsePPID(in); !ok || got != want {
			t.Errorf("ParsePPID(%q) = %d %v, want %d", in, got, ok, want)
		}
	}
	if _, ok := ParsePPID("gone"); ok {
		t.Error("nonsense parsed")
	}
}

func TestParseStatm(t *testing.T) {
	if n, ok := ParseStatm("262144 12800 3000 100 0 20000 0\n"); !ok || n != 12800 {
		t.Errorf("ParseStatm = %d %v", n, ok)
	}
	if _, ok := ParseStatm(""); ok {
		t.Error("empty parsed")
	}
}
