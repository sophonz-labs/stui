package metrics

import (
	"testing"
	"time"

	"github.com/sophonz-labs/stui/internal/sysstat"
)

func TestProcMeter(t *testing.T) {
	var m procMeter
	t0 := time.Unix(1000, 0)
	cores := float64(sysstat.CPUs())
	if m.next(t0, map[int]time.Duration{1: time.Second}, []int{1}) != nil {
		t.Error("the first sample has a value")
	}
	// 5 s later: pid 1 used 1 s more, pid 2 is new and used 0.5 s, pid 3
	// (gone) is not missed.
	got := m.next(t0.Add(5*time.Second), map[int]time.Duration{1: 2 * time.Second, 2: 500 * time.Millisecond}, []int{1})
	want := round1(1.5 / (5 * cores) * 100)
	if got == nil || *got != want {
		t.Errorf("share = %v, want %v", got, want)
	}
	// The roots changed (the tmux server found): a new start, not a spike.
	if m.next(t0.Add(10*time.Second), map[int]time.Duration{1: 3 * time.Second, 9: time.Hour}, []int{1, 9}) != nil {
		t.Error("a sample after the roots changed has a value")
	}
	// A walk without CPU: null.
	if m.next(t0.Add(15*time.Second), nil, []int{1, 9}) != nil {
		t.Error("no CPU, a value")
	}
}
