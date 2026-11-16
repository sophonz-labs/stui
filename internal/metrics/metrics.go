// Package metrics keeps the last ten minutes of the machine stui is on --
// CPU, load, memory, swap, and how much of it is stui and tmux -- in a ring
// buffer the top view reads (GET /api/metrics).
//
// It samples whether or not anyone is looking. The graph is wanted after
// something has gone wrong, and then the minutes before it are the point.
package metrics

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/sophonz-labs/stui/internal/sysstat"
)

// Every and Window are the defaults: 120 samples. They are sent with every
// snapshot so the apps never assume them.
const (
	Every  = 5 * time.Second
	Window = 10 * time.Minute
)

// Sample is one moment. A number the platform cannot give is null, never
// zero: cpu on the first sample (it is the difference between two), load1
// on Windows.
type Sample struct {
	At       int64    `json:"at"`
	CPU      *float64 `json:"cpu"`
	Load1    *float64 `json:"load1"`
	MemUsed  *uint64  `json:"memUsed"`
	SwapUsed *uint64  `json:"swapUsed"`
	// On macOS swap grows and shrinks a file at a time, so its total is
	// not a constant; the one in machine is the latest.
	SwapTotal *uint64 `json:"swapTotal,omitempty"`
	StRSS     *uint64 `json:"stRss"`
	StProcs   *int    `json:"stProcs"`
	// The CPU stui's tree used since the sample before, as a share of the
	// whole machine like cpu (0-100): null on the first sample, and on one
	// after the tree's roots changed.
	StCPU *float64 `json:"stCpu"`
	// stui itself: its server and what it starts, and the multiplexer
	// server alone, without the shells and coding agents the server runs.
	StSelfRSS   *uint64  `json:"stSelfRss"`
	StSelfProcs *int     `json:"stSelfProcs"`
	StSelfCPU   *float64 `json:"stSelfCpu"`
	// The disk the home is on: bytes this user may still write, and its
	// size (Options.Disk). Absent when it could not be read, and from an
	// older build.
	DiskFree  *uint64 `json:"diskFree,omitempty"`
	DiskTotal *uint64 `json:"diskTotal,omitempty"`
}

// Machine is what does not change from one sample to the next.
type Machine struct {
	OS        string  `json:"os"`
	CPUs      int     `json:"cpus"`
	MemTotal  *uint64 `json:"memTotal"`
	SwapTotal *uint64 `json:"swapTotal"`
	// The latest sample's disk, for a reader that wants it once.
	DiskFree  *uint64 `json:"diskFree,omitempty"`
	DiskTotal *uint64 `json:"diskTotal,omitempty"`
}

// Snapshot is GET /api/metrics and the stream's first event: the envelope
// of /api/agents (v, seq, at) around the buffer, oldest sample first.
type Snapshot struct {
	V       int      `json:"v"`
	Seq     int64    `json:"seq"`
	At      int64    `json:"at"`
	Every   int64    `json:"every"`
	Window  int64    `json:"window"`
	Machine Machine  `json:"machine"`
	Samples []Sample `json:"samples"`
}

// Event is one new sample, with the seq the stream sends as its id.
type Event struct {
	Seq    int64
	Sample Sample
}

// Source is where samples come from. The real one reads the machine; tests
// hand in their own.
type Source interface {
	Machine() Machine
	Sample(now time.Time) Sample
}

type Options struct {
	Source Source
	// Disk is the disk the home is on (diskfree.Space); nil: not reported.
	Disk   func() (free, total uint64, err error)
	Every  time.Duration
	Window time.Duration
	Now    func() time.Time
}

// Buffer is the ring and the people reading it.
type Buffer struct {
	every, window time.Duration
	now           func() time.Time
	src           Source
	disk          func() (free, total uint64, err error)

	mu      sync.Mutex
	ring    []Sample
	next    int // where the next sample goes
	full    bool
	seq     int64
	machine Machine
	subs    map[chan Event]struct{}
}

func New(o Options) *Buffer {
	if o.Every <= 0 {
		o.Every = Every
	}
	if o.Window <= 0 {
		o.Window = Window
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	n := int(o.Window / o.Every)
	if n < 1 {
		n = 1
	}
	return &Buffer{
		every: o.Every, window: o.Window, now: o.Now, src: o.Source, disk: o.Disk,
		ring: make([]Sample, n), subs: map[chan Event]struct{}{},
	}
}

// Run samples now and then every Every until ctx ends.
func (b *Buffer) Run(ctx context.Context) {
	b.Tick()
	t := time.NewTicker(b.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.Tick()
		}
	}
}

// Tick takes one sample and tells whoever is streaming.
func (b *Buffer) Tick() {
	s := b.src.Sample(b.now())
	s.DiskFree, s.DiskTotal = b.readDisk()
	b.Add(b.src.Machine(), s)
}

// Add puts s in the ring, over the oldest once it is full.
func (b *Buffer) Add(m Machine, s Sample) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.machine = m
	b.ring[b.next] = s
	b.next = (b.next + 1) % len(b.ring)
	if b.next == 0 {
		b.full = true
	}
	b.seq++
	ev := Event{Seq: b.seq, Sample: s}
	for ch := range b.subs {
		select {
		case ch <- ev:
		default:
			// Fell this far behind: it gets a fresh snapshot when it
			// reconnects rather than being waited on.
			close(ch)
			delete(b.subs, ch)
		}
	}
}

// Snapshot is the buffer now.
func (b *Buffer) Snapshot() Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.snapshotLocked()
}

// readDisk is one statfs (GetDiskFreeSpaceEx on Windows) a sample: cheap,
// and a page left open follows the disk filling. Nil for both when it fails.
func (b *Buffer) readDisk() (free, total *uint64) {
	if b.disk == nil {
		return nil, nil
	}
	f, t, err := b.disk()
	if err != nil || t == 0 {
		return nil, nil
	}
	return ptr(f), ptr(t)
}

// snapshotLocked leaves out anything older than the window: a laptop that
// slept for an hour wakes with samples from before it, which are not the
// last ten minutes.
func (b *Buffer) snapshotLocked() Snapshot {
	now := b.now()
	oldest := now.Add(-b.window).UnixMilli()
	out := make([]Sample, 0, len(b.ring))
	start, n := 0, b.next
	if b.full {
		start, n = b.next, len(b.ring)
	}
	for i := 0; i < n; i++ {
		s := b.ring[(start+i)%len(b.ring)]
		if s.At >= oldest {
			out = append(out, s)
		}
	}
	m := b.machine
	if len(out) > 0 {
		m.DiskFree, m.DiskTotal = out[len(out)-1].DiskFree, out[len(out)-1].DiskTotal
	}
	return Snapshot{
		V: 1, Seq: b.seq, At: now.UnixMilli(),
		Every: b.every.Milliseconds(), Window: b.window.Milliseconds(),
		Machine: m, Samples: out,
	}
}

// Subscribe is a snapshot and every sample after it, in order and without
// a gap between them. cancel ends it.
func (b *Buffer) Subscribe() (Snapshot, <-chan Event, func()) {
	ch := make(chan Event, 16)
	b.mu.Lock()
	snap := b.snapshotLocked()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return snap, ch, func() {
		b.mu.Lock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
}

// ---- the machine

// System is the Source that reads this machine through sysstat.
type System struct {
	// Roots are the processes whose trees are stui's: its server and the
	// multiplexer server, which daemonizes out from under it.
	Roots func() []int
	// Self is stui's own share: the trees of roots, and leaves counted
	// alone (the multiplexer server, without what it runs).
	Self func() (roots, leaves []int)

	cpu     cpuMeter
	stCPU   procMeter
	selfCPU procMeter
	mem     sysstat.Memory
	memOK   bool
}

func (s *System) Machine() Machine {
	m := Machine{OS: runtime.GOOS, CPUs: sysstat.CPUs()}
	if s.memOK {
		m.MemTotal, m.SwapTotal = ptr(s.mem.Total), ptr(s.mem.SwapTotal)
	}
	return m
}

// Sample is called by one goroutine at a time (Tick), so the CPU reading
// it keeps for the next delta needs no lock of its own.
func (s *System) Sample(now time.Time) Sample {
	out := Sample{At: now.UnixMilli()}
	out.CPU = s.cpu.next(sysstat.ReadCPU())
	if l, ok := sysstat.ReadLoad(); ok {
		out.Load1 = ptr(round2(l))
	}
	s.mem, s.memOK = sysstat.ReadMemory()
	if s.memOK {
		out.MemUsed, out.SwapUsed = ptr(s.mem.Used()), ptr(s.mem.SwapUsed)
		out.SwapTotal = ptr(s.mem.SwapTotal)
	}
	var roots []int
	if s.Roots != nil {
		roots = s.Roots()
	}
	w := sysstat.NewWalk()
	if len(roots) > 0 {
		u := sysstat.Measure(w, roots, nil)
		if u.Procs > 0 {
			out.StRSS, out.StProcs = ptr(u.RSS), ptr(u.Procs)
		}
		out.StCPU = s.stCPU.next(now, u.CPU, roots)
	}
	if s.Self != nil {
		r, l := s.Self()
		u := sysstat.Measure(w, r, l)
		if u.Procs > 0 {
			out.StSelfRSS, out.StSelfProcs = ptr(u.RSS), ptr(u.Procs)
		}
		out.StSelfCPU = s.selfCPU.next(now, u.CPU, append(append([]int(nil), r...), l...))
	}
	return out
}

// procMeter turns the CPU time of a set of processes into a share of the
// machine between two samples. A process in both counts what it used in
// between; one new since the last sample counts all it has used, since it
// started in between (a compiler an agent ran). One that ended in between
// loses its last few seconds. When the roots changed -- the multiplexer
// server found after a minute -- whole trees join at once with all the time
// they ever used, so that sample is only the start of the next delta.
type procMeter struct {
	prev  map[int]time.Duration
	at    time.Time
	roots string
}

func (m *procMeter) next(now time.Time, cpu map[int]time.Duration, roots []int) *float64 {
	key := fmt.Sprint(roots)
	prev, at, sameRoots := m.prev, m.at, key == m.roots
	m.prev, m.at, m.roots = cpu, now, key
	if cpu == nil || prev == nil || !sameRoots {
		return nil
	}
	wall := now.Sub(at)
	if wall <= 0 {
		return nil
	}
	var used time.Duration
	for pid, t := range cpu {
		if p, ok := prev[pid]; ok {
			if t >= p {
				used += t - p
			}
			// Less than before: another process with a used pid; nothing
			// to say about it this time.
		} else {
			used += t
		}
	}
	pct := used.Seconds() / (wall.Seconds() * float64(sysstat.CPUs())) * 100
	return ptr(round1(min(max(pct, 0), 100)))
}

// cpuMeter turns CPU counters into a percentage, which needs two of them:
// the first reading is null, and so is one after a reading that failed.
type cpuMeter struct {
	prev sysstat.CPUTimes
	has  bool
}

func (c *cpuMeter) next(now sysstat.CPUTimes, ok bool) *float64 {
	if !ok {
		c.has = false
		return nil
	}
	prev, had := c.prev, c.has
	c.prev, c.has = now, true
	if !had {
		return nil
	}
	p, ok := sysstat.Percent(prev, now)
	if !ok {
		return nil
	}
	return ptr(round1(p))
}

func ptr[T any](v T) *T { return &v }

func round1(f float64) float64 { return float64(int64(f*10+0.5)) / 10 }
func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }
