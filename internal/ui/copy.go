package ui

import (
	"fmt"
	"os"
	"strings"

	"github.com/sophonz-labs/tmui/pkg/clip"
)

// Copying. The screen is the alternate one and the mouse is the terminal's,
// so a mouse selection takes the frame and the columns with it; these keys
// copy the content alone:
//
//	a table      y  the row's value (copyValue)   Y  the whole row, tab-separated
//	an output    v  select lines from the bottom one on screen, j k g G to extend
//	             y  the selection, or with none the whole output (every line, not
//	                just those on screen)
//	             z  the output alone on the screen, for the terminal's own selection

// clip puts text on the clipboard and says what went: both ways at once,
// since neither works everywhere. The terminal's (OSC 52) crosses ssh; this
// machine's own tool works where the terminal ignores OSC 52.
func (a *app) clip(text, what string) {
	if text == "" {
		a.say("nothing to copy", true)
		return
	}
	a.copied = text
	deliver(a, text)
	a.say("copied "+what, false)
}

// deliver is where clip sends the text; the tests leave the clipboard alone.
var deliver = func(a *app, text string) {
	if os.Getenv("TMUX") != "" {
		go clip.Tmux(text)
	} else if a.scr != nil {
		a.scr.w.WriteString(clip.OSC52(text)) // written with the next frame
	}
	go clip.Local(text)
}

// copyRow is y (whole false) and Y (whole true) on a table row.
func (a *app) copyRow(p *page, r *row, whole bool) {
	if whole {
		cells := make([]string, len(r.cells))
		for i, c := range r.cells {
			cells[i] = strings.TrimSpace(c)
		}
		a.clip(strings.Join(cells, "\t"), "the row")
		return
	}
	v := a.copyValue(p, r)
	a.clip(v, short(v))
}

// copyValue is what y copies from a row: what it names, as a command takes
// it. A plugin row with a url copies the url; a view can say otherwise
// (copyValuer); the rest copy the row's id.
func (a *app) copyValue(p *page, r *row) string {
	if u := r.vars["url"]; u != "" {
		return u
	}
	if c, ok := p.v.(copyValuer); ok {
		return c.copyValue(r)
	}
	return r.id
}

type copyValuer interface{ copyValue(r *row) string }

func short(s string) string { return strings.TrimRight(fit(s, 40), " ") }

// selection is the lines a pager's v marked, top to bottom, or (none)
// every line: plain text, trailing blanks gone.
func (pg *pager) selection() []string {
	from, to := 0, len(pg.lines)-1
	if pg.selecting {
		from, to = min(pg.anchor, pg.cur), max(pg.anchor, pg.cur)
	}
	var out []string
	for i := max(0, from); i <= to && i < len(pg.lines); i++ {
		out = append(out, strings.TrimRight(pg.lines[i], " \t"))
	}
	return out
}

// copyPager is y in an output: the selection, or all of it.
func (a *app) copyPager(pg *pager) {
	lines := pg.selection()
	pg.selecting = false
	n := len(lines)
	if pg.lines == nil || n == 0 {
		a.say("nothing to copy", true)
		return
	}
	what := fmt.Sprintf("%d lines", n)
	if n == 1 {
		what = "1 line"
	}
	a.clip(strings.Join(lines, "\n"), what)
}

// selectKey is a key in an output while v's selection is on: moving moves
// its end, and the view follows it. It says whether it took the key.
func (a *app) selectKey(pg *pager, k string) bool {
	n := a.bodyRows()
	switch k {
	case "up", "k":
		pg.cur--
	case "down", "j":
		pg.cur++
	case "pgup", "ctrl-b", "b":
		pg.cur -= n
	case "pgdn", "ctrl-f", " ":
		pg.cur += n
	case "home", "g":
		pg.cur = 0
	case "end", "G":
		pg.cur = len(pg.lines) - 1
	default:
		return false
	}
	pg.cur = max(0, min(pg.cur, len(pg.lines)-1))
	if pg.cur < pg.top {
		pg.top = pg.cur
	}
	if pg.cur >= pg.top+n {
		pg.top = pg.cur - n + 1
	}
	return true
}

// startSelect is v: the selection starts on the bottom line on screen,
// where an output's newest lines are, and the pager stops following.
func (a *app) startSelect(pg *pager) {
	if len(pg.lines) == 0 {
		return
	}
	pg.selecting, pg.follow = true, false
	pg.cur = max(0, min(pg.top+a.bodyRows()-1, len(pg.lines)-1))
	pg.anchor = pg.cur
	a.say("v: j k g G to extend, y to copy, esc to stop", false)
}

// bare is the output alone, the whole screen, no colour or frame, every
// line ended with erase-to-end so a terminal's selection takes no padding.
func (a *app) bare(pg *pager) []string {
	out := make([]string, a.h)
	for i := range out {
		l := ""
		if j := pg.top + i; j < len(pg.lines) {
			l = strings.TrimRight(fit(pg.lines[j], a.w), " ")
		}
		out[i] = l + "\x1b[K"
	}
	return out
}
