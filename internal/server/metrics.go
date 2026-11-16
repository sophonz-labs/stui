package server

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/sophonz-labs/stui/internal/metrics"
	"github.com/sophonz-labs/stui/internal/mux"
	"github.com/sophonz-labs/stui/internal/proc"
)

// The machine's last ten minutes (GET /api/metrics, the top view): CPU,
// load, memory, swap, and stui's share -- this server and the tmux server
// with what it runs.

// muxRoot remembers the tmux server's pid between samples; one that was
// not found is looked for again a minute later, not every five seconds.
type muxRoot struct {
	mu    sync.Mutex
	m     *mux.Tmux
	pid   int
	tried time.Time
}

func (r *muxRoot) get() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pid > 0 && proc.Present(r.pid) {
		return r.pid
	}
	r.pid = 0
	if !r.tried.IsZero() && time.Since(r.tried) < time.Minute {
		return 0
	}
	r.tried = time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r.pid = r.m.ServerPID(ctx)
	return r.pid
}

func newMetrics(m *mux.Tmux) *metrics.Buffer {
	root := &muxRoot{m: m}
	self := os.Getpid()
	return metrics.New(metrics.Options{Source: &metrics.System{
		Roots: func() []int { return []int{self, root.get()} },
		Self:  func() ([]int, []int) { return []int{self}, []int{root.get()} },
	}})
}
