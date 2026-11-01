package orch

// Why a worker's turn ended (stopReason):
// carried on its idle event, so the parent told about it knows whether
// reading the worker gives it anything. A worker at its usage limit cannot
// run a command to say so, so stui reads it off what the agent wrote: the
// turn's last reply in its conversation (Claude writes its limit there as
// its own error), or the bottom of its pane when the conversation has no
// reply (Codex, Gemini). What it does not recognize is end_turn: an unknown
// reason is better than a wrong one.

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sophonz-labs/stui/internal/chat"
)

// The stop reasons stui writes. The others the vocabulary names
// (max_tokens, max_turn_requests, refusal, _handoff, _released) are not
// written yet; a reader takes an empty or unknown one as end_turn.
const (
	StopEndTurn   = "end_turn"
	StopLimit     = "_limit"
	StopError     = "error"
	StopCancelled = "cancelled"
)

// Stop is why a turn ended: its reason, the agent's own line saying so
// (the limit's, the error's), and when a limit resets (ms, 0: not known).
type Stop struct {
	Reason   string
	Why      string
	ResetsAt int64
}

// The limit lines, every agent's, in one place. Each is matched against
// one line, trimmed of the glyphs agents put in front ("⎿", "■", "●"),
// from its start: a reply that talks about limits does not match.
var limitLines = []*regexp.Regexp{
	// Claude Code: "You've hit your weekly limit · resets 3pm (Asia/Seoul)",
	// "You've hit your limit"; Codex: "You've hit your usage limit. ...".
	regexp.MustCompile(`(?i)^you(?:'|’)ve hit your\b[^.·]*\blimit\b`),
	// Claude Code, older: "Claude AI usage limit reached|1761300000",
	// "5-hour limit reached ∙ resets 3pm"; Gemini CLI: "Usage limit
	// reached for all Pro models."
	regexp.MustCompile(`(?i)^(?:claude (?:ai )?)?(?:usage|weekly|daily|session|opus|\d+-hour) limit reached\b`),
	// Gemini CLI's quota.
	regexp.MustCompile(`(?i)^(?:you have )?(?:exhausted|reached) your (?:daily |free )?quota\b`),
	regexp.MustCompile(`(?i)^quota exceeded\b`),
}

// Where a limit line says it resets: "resets 3pm (Asia/Seoul)", "reset at
// 3pm", "try again at 3:05 PM", "try again in 2 days 3 hours", or the
// Unix time after a "|".
var (
	resetsRE   = regexp.MustCompile(`(?i)\b(?:resets?(?: at)?|try again at)\s+(.+?)\s*\.?\s*$`)
	retryInRE  = regexp.MustCompile(`(?i)\btry again in\s+(.+?)\s*\.?\s*$`)
	unixRE     = regexp.MustCompile(`\|(\d{10})\s*$`)
	clockRE    = regexp.MustCompile(`(?i)^(?:([a-z]{3,9})\.?\s+(\d{1,2}),?\s+(?:at\s+)?)?(\d{1,2})(?::(\d{2}))?\s*([ap]m)\b\s*(?:\(([^)]+)\))?`)
	durationRE = regexp.MustCompile(`(?i)(\d+)\s*(d|days?|h|hours?|hrs?|m|min|mins|minutes?)\b`)
	glyphs     = " \t>⎿■●⏺•·∙*-│|"
)

// LimitIn finds a usage limit in what an agent wrote: the line saying so,
// and when it resets (ms, 0: not known). ok is false when there is none.
func LimitIn(text string, now time.Time) (line string, resetsAt int64, ok bool) {
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), glyphs))
		for _, re := range limitLines {
			if re.MatchString(l) {
				return clip(l, 160), resetsOf(l, now), true
			}
		}
	}
	return "", 0, false
}

func resetsOf(line string, now time.Time) int64 {
	if m := unixRE.FindStringSubmatch(line); m != nil {
		s, _ := strconv.ParseInt(m[1], 10, 64)
		return s * 1000
	}
	if m := retryInRE.FindStringSubmatch(line); m != nil {
		var d time.Duration
		for _, x := range durationRE.FindAllStringSubmatch(m[1], -1) {
			n, _ := strconv.Atoi(x[1])
			switch u := strings.ToLower(x[2]); {
			case strings.HasPrefix(u, "d"):
				d += time.Duration(n) * 24 * time.Hour
			case strings.HasPrefix(u, "h"):
				d += time.Duration(n) * time.Hour
			default:
				d += time.Duration(n) * time.Minute
			}
		}
		if d > 0 {
			return now.Add(d).UnixMilli()
		}
		return 0
	}
	m := resetsRE.FindStringSubmatch(line)
	if m == nil {
		return 0
	}
	c := clockRE.FindStringSubmatch(strings.TrimSpace(m[1]))
	if c == nil {
		return 0
	}
	loc := now.Location()
	if c[6] != "" {
		l, err := time.LoadLocation(strings.TrimSpace(c[6]))
		if err != nil {
			return 0
		}
		loc = l
	}
	h, _ := strconv.Atoi(c[3])
	mi, _ := strconv.Atoi(c[4])
	if h < 1 || h > 12 || mi > 59 {
		return 0
	}
	h %= 12
	if strings.EqualFold(c[5], "pm") {
		h += 12
	}
	n := now.In(loc)
	if c[1] != "" {
		mon, ok := monthOf(c[1])
		day, _ := strconv.Atoi(c[2])
		if !ok || day < 1 || day > 31 {
			return 0
		}
		t := time.Date(n.Year(), mon, day, h, mi, 0, 0, loc)
		if t.Before(n.Add(-24 * time.Hour)) {
			t = t.AddDate(1, 0, 0)
		}
		return t.UnixMilli()
	}
	// A time of day: the next one.
	t := time.Date(n.Year(), n.Month(), n.Day(), h, mi, 0, 0, loc)
	if !t.After(n) {
		t = t.AddDate(0, 0, 1)
	}
	return t.UnixMilli()
}

func monthOf(s string) (time.Month, bool) {
	s = strings.ToLower(s)
	for m := time.January; m <= time.December; m++ {
		if strings.HasPrefix(strings.ToLower(m.String()), s) && len(s) >= 3 {
			return m, true
		}
	}
	return 0, false
}

// JudgeStop is why a turn ended, from the agent's state and what set it
// (agent watch: "interrupt" for Codex's Esc), how its conversation says
// the turn ended, and the bottom of its pane (read only when the
// conversation has no reply for the turn).
func JudgeStop(state, source string, te chat.TurnEnd, screen string, now time.Time) Stop {
	if te.Error != "" {
		if line, at, ok := LimitIn(te.Error, now); ok {
			return Stop{Reason: StopLimit, Why: line, ResetsAt: at}
		}
	}
	if strings.TrimSpace(te.Answer) == "" {
		if line, at, ok := LimitIn(screen, now); ok {
			return Stop{Reason: StopLimit, Why: line, ResetsAt: at}
		}
	}
	switch {
	case te.Error != "":
		return Stop{Reason: StopError, Why: clip(firstLineOf(te.Error), 160)}
	case state == "error":
		return Stop{Reason: StopError}
	case te.Interrupted || source == "interrupt":
		return Stop{Reason: StopCancelled}
	}
	return Stop{Reason: StopEndTurn}
}

func firstLineOf(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return l
}

// screenLines is how much of the bottom of a pane is read for a limit: the
// line sits just above the input box.
const screenLines = 15

// bottomLines is the last n lines of s that are not blank.
func bottomLines(s string, n int) string {
	var out []string
	ls := strings.Split(s, "\n")
	for i := len(ls) - 1; i >= 0 && len(out) < n; i-- {
		if strings.TrimSpace(ls[i]) != "" {
			out = append([]string{ls[i]}, out...)
		}
	}
	return strings.Join(out, "\n")
}

// stopOf is why pane's agent's turn ended, its state being state.
func (a *Adopter) stopOf(ctx context.Context, pane, state string) Stop {
	var te chat.TurnEnd
	if a.o.LastTurn != nil {
		te = a.o.LastTurn(pane)
	}
	source := ""
	if a.o.Source != nil {
		source = a.o.Source(pane)
	}
	screen := ""
	if strings.TrimSpace(te.Answer) == "" && a.o.Tmux != nil {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		out, err := a.o.Tmux(cctx, "capture-pane", "-p", "-J", "-t", pane)
		cancel()
		if err == nil {
			screen = bottomLines(out, screenLines)
		}
	}
	return JudgeStop(state, source, te, screen, time.Now())
}

// turnEnded is whether a state is the end of a turn that is told: done
// and error, and idle when the person stopped it (Codex's Esc). Idle that
// follows a done is the same turn end, told already.
func turnEnded(state, source string) bool {
	return state == "done" || state == "error" || (state == "idle" && source == "interrupt")
}

// LastStop is the idle event that says why id last stopped, after the
// last time it was given work and after its last done; nil when there is
// none.
func LastStop(evs []Event, id string) *Event {
	var last *Event
	for i := range evs {
		ev := &evs[i]
		switch {
		case (ev.Kind == EvStart || ev.Kind == EvSend || ev.Kind == EvAdopt) && ev.To == id:
			last = nil
		case ev.Kind == EvDone && ev.From == id:
			last = nil
		case ev.Kind == EvIdle && ev.From == id:
			last = ev
		}
	}
	return last
}

// StopWord is a stop reason in the few words stui shows for it: "" for
// end_turn and for one it does not know.
func StopWord(reason string) string {
	switch reason {
	case StopLimit:
		return "limit"
	case StopError:
		return "error"
	case StopCancelled:
		return "cancelled"
	}
	return ""
}

// ResetsLine is when a limit resets, as a person reads it here: "15:00",
// "Oct 27 15:00" when not today; "" when not known.
func ResetsLine(at int64, now time.Time) string {
	if at <= 0 {
		return ""
	}
	t := time.UnixMilli(at).In(now.Location())
	if y, m, d := t.Date(); y == now.Year() && m == now.Month() && d == now.Day() {
		return t.Format("15:04")
	}
	return t.Format("Jan 2 15:04")
}
