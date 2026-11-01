package actions

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// How a job's log looks in a terminal window (stui ci log): split by step,
// GitHub's markers turned into something a terminal shows, the failed steps
// open and the rest one header line each.

// StepLog is one step's lines of a job's log. Step is nil for a log that
// could not be split.
type StepLog struct {
	Step  *Step
	Lines []string
}

// SplitLog cuts a job's raw log (GitHub's: a timestamp on every line, a BOM
// on the first) at its steps' start times. `gh run view --log` names the
// step of each line, but not reliably: on our ARC runner every line of a job
// came as "UNKNOWN STEP". A line goes to the last step that had started by
// its second; the timestamps are taken off.
func SplitLog(raw []byte, steps []Step) []StepLog {
	text := strings.TrimPrefix(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\ufeff")
	text = strings.TrimRight(text, "\n")
	type started struct {
		at time.Time
		i  int
	}
	var starts []started
	for i, s := range steps {
		if t, err := time.Parse(time.RFC3339, s.StartedAt); err == nil {
			starts = append(starts, started{t, i})
		}
	}
	sort.SliceStable(starts, func(a, b int) bool { return starts[a].at.Before(starts[b].at) })
	if len(starts) == 0 {
		var lines []string
		if text != "" {
			for _, l := range strings.Split(text, "\n") {
				lines = append(lines, stripStamp(l))
			}
		}
		return []StepLog{{Lines: lines}}
	}
	out := make([]StepLog, len(starts))
	for k, s := range starts {
		st := steps[s.i]
		out[k].Step = &st
	}
	if text == "" {
		return out
	}
	cur := 0
	for _, l := range strings.Split(text, "\n") {
		if m := tsRE.FindString(l); m != "" {
			if t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(m)); err == nil {
				t = t.Truncate(time.Second)
				k := sort.Search(len(starts), func(k int) bool { return starts[k].at.After(t) }) - 1
				if k < 0 {
					k = 0
				}
				cur = k
			}
		}
		out[cur].Lines = append(out[cur].Lines, stripStamp(l))
	}
	return out
}

func stripStamp(l string) string { return tsRE.ReplaceAllString(l, "") }

// Colours, for a terminal.
const (
	cDim    = "\x1b[2m"
	cRed    = "\x1b[31m"
	cYellow = "\x1b[33m"
	cGreen  = "\x1b[32m"
	cReset  = "\x1b[0m"
)

// TermLine is one log line for a terminal, false when it is dropped
// (##[endgroup]). GitHub's markers become: ##[group]x a dim "▸ x",
// ##[error] red, ##[warning] yellow, ##[debug] dim, ##[command]x "$ x".
// Any other ##[…] and workflow commands (::add-mask::) stay as they are, and
// so do the line's own colours.
func TermLine(l string) (string, bool) {
	marker, rest, ok := cutMarker(l)
	if !ok {
		return l, true
	}
	switch marker {
	case "group":
		return cDim + "▸ " + rest + cReset, true
	case "endgroup":
		return "", false
	case "error":
		return cRed + rest + cReset, true
	case "warning":
		return cYellow + rest + cReset, true
	case "debug":
		return cDim + rest + cReset, true
	case "command":
		return "$ " + rest, true
	}
	return l, true
}

func cutMarker(l string) (string, string, bool) {
	rest, ok := strings.CutPrefix(l, "##[")
	if !ok {
		return "", "", false
	}
	m, text, ok := strings.Cut(rest, "]")
	return m, text, ok
}

// Mark is a step's or job's state as one character: ✓ ✗ – for a finished
// one, · for one waiting, and spin for one going.
func Mark(status, conclusion, spin string) string {
	switch {
	case status == "in_progress":
		return spin
	case status != "completed":
		return cDim + "·" + cReset
	case conclusion == "success":
		return cGreen + "✓" + cReset
	case Failed(conclusion):
		return cRed + "✗" + cReset
	}
	return cDim + "–" + cReset
}

// Took is how long from start to end ("1m 42s"), "" when either is missing.
func Took(start, end string) string {
	a, err1 := time.Parse(time.RFC3339, start)
	b, err2 := time.Parse(time.RFC3339, end)
	if err1 != nil || err2 != nil || b.Before(a) {
		return ""
	}
	return Duration(b.Sub(a))
}

// Duration is d as "42s", "1m 42s", "1h 3m".
func Duration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
}

// StepHeader is a step's line above its log: "── 3/12 Run tests ✗ 1m 42s".
func StepHeader(s Step, n, of int) string {
	h := fmt.Sprintf("── %d/%d %s %s", n, of, s.Name, Mark(s.Status, s.Conclusion, "⟳"))
	if t := Took(s.StartedAt, s.CompletedAt); t != "" {
		h += " " + cDim + t + cReset
	}
	return h
}

// RenderLog is a finished job's log for a terminal: every step's header,
// and under it the step's lines when it failed (or every step with all).
func RenderLog(parts []StepLog, all bool) []string {
	var out []string
	of := len(parts)
	for i, p := range parts {
		open := all || p.Step == nil || Failed(p.Step.Conclusion)
		if p.Step != nil {
			out = append(out, StepHeader(*p.Step, i+1, of))
		}
		if !open {
			continue
		}
		for _, l := range p.Lines {
			if t, ok := TermLine(l); ok {
				out = append(out, t)
			}
		}
	}
	return out
}
