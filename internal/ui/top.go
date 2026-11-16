package ui

import (
	"fmt"
	"strings"
)

// The top view (6, :top): the machine's last ten minutes from GET
// /api/metrics -- CPU, load, memory, swap, and stui with tmux and what it
// runs.

type sample struct {
	At       int64   `json:"at"`
	CPU      float64 `json:"cpu"`
	Load1    float64 `json:"load1"`
	MemUsed  int64   `json:"memUsed"`
	SwapUsed int64   `json:"swapUsed"`
	StRss    int64   `json:"stRss"`
	StProcs  int     `json:"stProcs"`
	StCPU    float64 `json:"stCpu"`
}

type metrics struct {
	Machine struct {
		OS        string `json:"os"`
		CPUs      int    `json:"cpus"`
		MemTotal  int64  `json:"memTotal"`
		SwapTotal int64  `json:"swapTotal"`
	} `json:"machine"`
	Samples []sample `json:"samples"`
}

// latest is the newest sample, zero before the first.
func (s *snap) latest() sample {
	if n := len(s.metrics.Samples); n > 0 {
		return s.metrics.Samples[n-1]
	}
	return sample{}
}

type topView struct{}

func (topView) title() string    { return "Top" }
func (topView) scope() string    { return "last 10 minutes" }
func (topView) header() []string { return []string{"METRIC", "NOW", "AVG", "MAX", "TREND"} }
func (topView) rows(a *app) []row {
	m := a.snap.metrics
	if len(m.Samples) == 0 {
		return nil
	}
	series := func(f func(sample) float64) []float64 {
		out := make([]float64, len(m.Samples))
		for i, s := range m.Samples {
			out[i] = f(s)
		}
		return out
	}
	type metric struct {
		name   string
		vals   []float64
		hi     float64
		format func(float64) string
	}
	pctf := func(v float64) string { return fmt.Sprintf("%.0f%%", v) }
	list := []metric{
		{"CPU", series(func(s sample) float64 { return s.CPU }), 100, pctf},
		{fmt.Sprintf("Load (%d cores)", m.Machine.CPUs), series(func(s sample) float64 { return s.Load1 }), 0, func(v float64) string { return fmt.Sprintf("%.2f", v) }},
		{"Memory of " + gib(m.Machine.MemTotal), series(func(s sample) float64 { return pct(s.MemUsed, m.Machine.MemTotal) }), 100, pctf},
		{"Swap of " + gib(m.Machine.SwapTotal), series(func(s sample) float64 { return pct(s.SwapUsed, m.Machine.SwapTotal) }), 100, pctf},
		{"stui, tmux and its panes: CPU", series(func(s sample) float64 { return s.StCPU }), 0, pctf},
		{"stui, tmux and its panes: memory", series(func(s sample) float64 { return float64(s.StRss) }), 0, func(v float64) string { return gib(int64(v)) }},
		{"stui, tmux and its panes: processes", series(func(s sample) float64 { return float64(s.StProcs) }), 0, func(v float64) string { return fmt.Sprintf("%.0f", v) }},
	}
	var out []row
	for _, x := range list {
		sum, hi := 0.0, 0.0
		for _, v := range x.vals {
			sum += v
			hi = max(hi, v)
		}
		now := x.vals[len(x.vals)-1]
		color := cAqua
		if x.hi == 100 && now >= 90 {
			color = cRed
		} else if x.hi == 100 && now >= 75 {
			color = cOrange
		}
		out = append(out, row{id: x.name, color: color, cells: []string{x.name, x.format(now), x.format(sum / float64(len(x.vals))), x.format(hi), spark(x.vals, 60, x.hi)}})
	}
	return out
}
func (topView) hints() []hint               { return nil }
func (topView) key(*app, string, *row) bool { return false }

// spark is vals as a line of block heights, the last width of them; hi 0
// scales to the values' own range.
func spark(vals []float64, width int, hi float64) string {
	if len(vals) > width {
		vals = vals[len(vals)-width:]
	}
	levels := []rune("▁▂▃▄▅▆▇█")
	lo := 0.0
	if hi <= 0 && len(vals) > 0 {
		lo, hi = vals[0], vals[0]
		for _, v := range vals {
			lo, hi = min(lo, v), max(hi, v)
		}
	}
	var b strings.Builder
	for _, v := range vals {
		i := len(levels) / 2
		if hi > lo {
			i = int((v-lo)/(hi-lo)*float64(len(levels)-1) + 0.5)
		}
		b.WriteRune(levels[max(0, min(len(levels)-1, i))])
	}
	return b.String()
}

func gib(b int64) string {
	switch {
	case b >= 1<<40:
		return fmt.Sprintf("%.1fT", float64(b)/(1<<40))
	case b >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%dM", b>>20)
	}
	return fmt.Sprintf("%dK", b>>10)
}

func pct(a, b int64) float64 {
	if b <= 0 {
		return 0
	}
	return float64(a) * 100 / float64(b)
}
