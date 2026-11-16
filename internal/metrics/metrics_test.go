package metrics

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sophonz-labs/stui/internal/sysstat"
)

// clock is a time the test moves.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// counting is a Source whose memUsed is the sample's number, 1, 2, 3 ...
type counting struct{ n uint64 }

func (c *counting) Machine() Machine {
	return Machine{OS: "test", CPUs: 4, MemTotal: ptr(uint64(64)), SwapTotal: ptr(uint64(8))}
}
func (c *counting) Sample(now time.Time) Sample {
	c.n++
	return Sample{At: now.UnixMilli(), MemUsed: ptr(c.n)}
}

func newTest() (*Buffer, *clock) {
	c := &clock{t: time.UnixMilli(1790609196273)}
	return New(Options{Source: &counting{}, Every: 5 * time.Second, Window: 10 * time.Minute, Now: c.now}), c
}

func used(s Snapshot) []uint64 {
	var out []uint64
	for _, x := range s.Samples {
		out = append(out, *x.MemUsed)
	}
	return out
}

func TestRingOrderAndWrap(t *testing.T) {
	b, c := newTest()
	if len(b.ring) != 120 {
		t.Fatalf("ring holds %d, want 120", len(b.ring))
	}
	if s := b.Snapshot(); len(s.Samples) != 0 || s.Samples == nil {
		t.Fatalf("empty buffer gave %v; want [] rather than null", s.Samples)
	}
	for i := 0; i < 3; i++ {
		b.Tick()
		c.add(5 * time.Second)
	}
	if got := used(b.Snapshot()); !reflect.DeepEqual(got, []uint64{1, 2, 3}) {
		t.Fatalf("before wrapping = %v", got)
	}
	// 250 in all: the last 120, oldest first.
	for i := 3; i < 250; i++ {
		b.Tick()
		c.add(5 * time.Second)
	}
	s := b.Snapshot()
	got := used(s)
	if len(got) != 120 || got[0] != 131 || got[119] != 250 {
		t.Fatalf("after wrapping: %d samples, %d..%d; want 120, 131..250", len(got), got[0], got[len(got)-1])
	}
	for i := 1; i < len(s.Samples); i++ {
		if s.Samples[i].At <= s.Samples[i-1].At {
			t.Fatalf("sample %d is not newer than the one before it", i)
		}
	}
	if s.Seq != 250 {
		t.Errorf("seq = %d, want 250", s.Seq)
	}
}

// A laptop that slept: what is older than the window is not sent.
func TestWindow(t *testing.T) {
	b, c := newTest()
	for i := 0; i < 10; i++ {
		b.Tick()
		c.add(5 * time.Second)
	}
	c.add(9*time.Minute + 20*time.Second)
	// now is 50 s + 9 m 20 s after the first: the first two are past it.
	b.Tick()
	got := used(b.Snapshot())
	if !reflect.DeepEqual(got, []uint64{3, 4, 5, 6, 7, 8, 9, 10, 11}) {
		t.Fatalf("inside the window = %v", got)
	}
	c.add(time.Hour)
	if got := used(b.Snapshot()); len(got) != 0 {
		t.Fatalf("an hour later = %v, want nothing", got)
	}
}

func TestCPUMeter(t *testing.T) {
	var m cpuMeter
	a := sysstat.CPUTimes{Busy: []uint64{100}, Idle: []uint64{900}}
	b := sysstat.CPUTimes{Busy: []uint64{130}, Idle: []uint64{970}}
	if p := m.next(a, true); p != nil {
		t.Fatalf("first sample = %v, want null", *p)
	}
	if p := m.next(b, true); p == nil || *p != 30 {
		t.Fatalf("second sample = %v, want 30", p)
	}
	if p := m.next(b, false); p != nil {
		t.Fatal("a failed reading gave a number")
	}
	// After a failed reading there is nothing to take a difference from.
	if p := m.next(a, true); p != nil {
		t.Fatal("the reading after a failure gave a number")
	}
}

// The real machine: the first sample's cpu is null, the rest are numbers.
func TestSystemFirstCPUNull(t *testing.T) {
	if _, ok := sysstat.ReadCPU(); !ok {
		t.Skip("no CPU counters here")
	}
	s := &System{}
	if first := s.Sample(time.Now()); first.CPU != nil {
		t.Fatalf("first cpu = %v, want null", *first.CPU)
	}
	time.Sleep(50 * time.Millisecond)
	if second := s.Sample(time.Now()); second.CPU == nil {
		t.Log("second cpu is null: no ticks passed in 50 ms")
	}
}

func keys(t *testing.T, raw json.RawMessage) []string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestSnapshotFormat(t *testing.T) {
	c := &clock{t: time.UnixMilli(1790609196273)}
	src := &fixed{}
	b := New(Options{Source: src, Now: c.now})
	b.Tick()
	c.add(5 * time.Second)
	b.Tick()

	rec := httptest.NewRecorder()
	if !b.ServeHTTP(rec, httptest.NewRequest("GET", "/api/metrics", nil)) {
		t.Fatal("GET /api/metrics not served")
	}
	if ct := rec.Header().Get("content-type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type %q", ct)
	}
	var top map[string]json.RawMessage
	json.Unmarshal(rec.Body.Bytes(), &top)
	if got, want := keys(t, rec.Body.Bytes()), []string{"at", "every", "machine", "samples", "seq", "v", "window"}; !reflect.DeepEqual(got, want) {
		t.Errorf("snapshot keys = %v, want %v", got, want)
	}
	if string(top["v"]) != "1" || string(top["every"]) != "5000" || string(top["window"]) != "600000" || string(top["seq"]) != "2" {
		t.Errorf("envelope = v %s every %s window %s seq %s", top["v"], top["every"], top["window"], top["seq"])
	}
	if got, want := keys(t, top["machine"]), []string{"cpus", "memTotal", "os", "swapTotal"}; !reflect.DeepEqual(got, want) {
		t.Errorf("machine keys = %v, want %v", got, want)
	}
	var samples []json.RawMessage
	json.Unmarshal(top["samples"], &samples)
	if len(samples) != 2 {
		t.Fatalf("%d samples, want 2", len(samples))
	}
	want := []string{"at", "cpu", "load1", "memUsed", "stCpu", "stProcs", "stRss", "stSelfCpu", "stSelfProcs", "stSelfRss", "swapTotal", "swapUsed"}
	if got := keys(t, samples[0]); !reflect.DeepEqual(got, want) {
		t.Errorf("sample keys = %v, want %v", got, want)
	}
	// cpu and load1 are there and null when unknown, not left out.
	var first map[string]any
	json.Unmarshal(samples[0], &first)
	if v, ok := first["cpu"]; !ok || v != nil {
		t.Errorf("first cpu = %v (present %v), want null", v, ok)
	}
	if first["at"].(float64) != 1790609196273 {
		t.Errorf("first at = %v", first["at"])
	}

	rec = httptest.NewRecorder()
	b.ServeHTTP(rec, httptest.NewRequest("POST", "/api/metrics", nil))
	if rec.Code != 405 {
		t.Errorf("POST gave %d, want 405", rec.Code)
	}
	if b.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/metricsx", nil)) {
		t.Error("served a route that is not its own")
	}
}

// fixed is a Source with every number known but cpu on the first sample.
type fixed struct{ n int }

func (f *fixed) Machine() Machine {
	return Machine{OS: "darwin", CPUs: 10, MemTotal: ptr(uint64(68719476736)), SwapTotal: ptr(uint64(22548578304))}
}
func (f *fixed) Sample(now time.Time) Sample {
	f.n++
	s := Sample{At: now.UnixMilli(), Load1: ptr(4.2), MemUsed: ptr(uint64(41231686656)),
		SwapUsed: ptr(uint64(20834959360)), SwapTotal: ptr(uint64(22548578304)),
		StRSS: ptr(uint64(486539264)), StProcs: ptr(7)}
	if f.n > 1 {
		s.CPU = ptr(31.4)
	}
	return s
}

type event struct{ name, id, data string }

func readEvent(t *testing.T, r *bufio.Reader) event {
	var ev event
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended: %v", err)
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case line == "" && ev.name != "":
			return ev
		case strings.HasPrefix(line, "event: "):
			ev.name = line[7:]
		case strings.HasPrefix(line, "id: "):
			ev.id = line[4:]
		case strings.HasPrefix(line, "data: "):
			ev.data = line[6:]
		}
	}
}

func TestStreamFormat(t *testing.T) {
	c := &clock{t: time.UnixMilli(1790609196273)}
	b := New(Options{Source: &fixed{}, Now: c.now})
	b.Tick()
	b.Tick()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { b.ServeHTTP(w, r) }))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/metrics/stream", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("content-type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type %q", ct)
	}
	r := bufio.NewReader(res.Body)

	ev := readEvent(t, r)
	if ev.name != "snapshot" || ev.id != "2" {
		t.Fatalf("first event %s id %s, want snapshot id 2", ev.name, ev.id)
	}
	var snap Snapshot
	if err := json.Unmarshal([]byte(ev.data), &snap); err != nil || len(snap.Samples) != 2 || snap.Seq != 2 {
		t.Fatalf("snapshot = %+v, %v", snap, err)
	}

	for want := 3; want <= 4; want++ {
		c.add(5 * time.Second)
		b.Tick()
		ev = readEvent(t, r)
		if ev.name != "sample" || ev.id != itoa(want) {
			t.Fatalf("event %s id %s, want sample id %d", ev.name, ev.id, want)
		}
		if got := keys(t, json.RawMessage(ev.data)); len(got) != 12 {
			t.Errorf("sample event keys = %v", got)
		}
		var s Sample
		json.Unmarshal([]byte(ev.data), &s)
		if s.At != c.now().UnixMilli() {
			t.Errorf("sample at %d, want %d", s.At, c.now().UnixMilli())
		}
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }
