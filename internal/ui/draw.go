package ui

import (
	"regexp"
	"strings"
	"unicode"
)

// Colours: k9s's skin (aqua rows, dodger-blue frame, orange labels).
const (
	reset    = "\x1b[0m"
	bold     = "\x1b[1m"
	dim      = "\x1b[2m"
	cAqua    = "\x1b[38;5;51m"
	cTeal    = "\x1b[38;2;140;222;226m" // #8cdee2: an agent at work
	cBlue    = "\x1b[38;5;33m"
	cOrange  = "\x1b[38;5;214m"
	cWhite   = "\x1b[38;5;255m"
	cGray    = "\x1b[38;5;245m"
	cGreen   = "\x1b[38;5;48m"
	cRed     = "\x1b[38;5;203m"
	cYellow  = "\x1b[38;5;227m"
	cPink    = "\x1b[38;5;213m"
	selected = "\x1b[38;5;16;48;5;51m"
	crumbOn  = "\x1b[38;5;16;48;5;214m"
	crumbOff = "\x1b[38;5;16;48;5;33m"
)

// Cells is how many terminal columns s takes: wide East Asian letters and
// emoji two, combining marks none.
func Cells(s string) int {
	n := 0
	for _, r := range s {
		n += runeCells(r)
	}
	return n
}

func runeCells(r rune) int {
	switch {
	case unicode.Is(unicode.Mn, r), r >= 0x1160 && r <= 0x11ff:
		return 0
	case r >= 0x1100 && r <= 0x115f, r >= 0x2e80 && r <= 0xa4cf, r >= 0xac00 && r <= 0xd7a3,
		r >= 0xf900 && r <= 0xfaff, r >= 0xfe30 && r <= 0xfe4f, r >= 0xff00 && r <= 0xff60,
		r >= 0xffe0 && r <= 0xffe6, r >= 0x1f000 && r <= 0x1faff, r >= 0x20000 && r <= 0x3fffd:
		return 2
	case r >= 0x2300 && r <= 0x2bff:
		return wideSymbol(r)
	}
	return 1
}

// wideSymbols are the symbols below the emoji block that terminals draw two
// columns wide (Unicode's East Asian Width W): ⌚ ⏳ ⚡ ✅ ❌ ⭐ and the like.
var wideSymbols = [][2]rune{
	{0x231a, 0x231b}, {0x2329, 0x232a}, {0x23e9, 0x23ec}, {0x23f0, 0x23f0}, {0x23f3, 0x23f3}, {0x25fd, 0x25fe},
	{0x2614, 0x2615}, {0x2648, 0x2653}, {0x267f, 0x267f}, {0x2693, 0x2693}, {0x26a1, 0x26a1}, {0x26aa, 0x26ab},
	{0x26bd, 0x26be}, {0x26c4, 0x26c5}, {0x26ce, 0x26ce}, {0x26d4, 0x26d4}, {0x26ea, 0x26ea}, {0x26f2, 0x26f3},
	{0x26f5, 0x26f5}, {0x26fa, 0x26fa}, {0x26fd, 0x26fd}, {0x2705, 0x2705}, {0x270a, 0x270b}, {0x2728, 0x2728},
	{0x274c, 0x274c}, {0x274e, 0x274e}, {0x2753, 0x2755}, {0x2757, 0x2757}, {0x2795, 0x2797}, {0x27b0, 0x27b0},
	{0x27bf, 0x27bf}, {0x2b1b, 0x2b1c}, {0x2b50, 0x2b50}, {0x2b55, 0x2b55},
}

func wideSymbol(r rune) int {
	for _, w := range wideSymbols {
		if r >= w[0] && r <= w[1] {
			return 2
		}
	}
	return 1
}

// fit is plain text cut (with …) or padded to exactly w columns; control
// characters become spaces so a title with a newline can't break a row.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	var b strings.Builder
	n := 0
	cut := Cells(s) > w
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			r = ' '
		}
		c := runeCells(r)
		if cut && n+c > w-1 || n+c > w {
			break
		}
		b.WriteRune(r)
		n += c
	}
	if cut {
		b.WriteString("…")
		n++
	}
	return b.String() + strings.Repeat(" ", max(0, w-n))
}

// span is fitted text in a colour.
func span(color, s string, w int) string { return color + fit(s, w) + reset }

var ansi = regexp.MustCompile(`\x1b(\[[0-9;?]*[ -/]*[@-~]|\][^\x07\x1b]*(\x07|\x1b\\)|[@-Z\\-_])`)

// plain is s without terminal escapes (a pane's captured colours).
func plain(s string) string { return ansi.ReplaceAllString(s, "") }

// widths shares avail columns among the columns: each as wide as its
// widest cell (at most 60), the widest given up first when they don't
// fit, and the last one taking what is left.
func widths(header []string, rows []row, avail int) []int {
	w := make([]int, len(header))
	for i, h := range header {
		w[i] = Cells(h)
	}
	for _, r := range rows {
		for i := range min(len(r.cells), len(w)) {
			w[i] = max(w[i], min(60, Cells(r.cells[i])))
		}
	}
	gap := len(w) - 1
	total := func() int {
		t := gap
		for _, x := range w {
			t += x
		}
		return t
	}
	for total() > avail {
		widest := 0
		for i := range w {
			if w[i] > w[widest] {
				widest = i
			}
		}
		if w[widest] <= 1 {
			break
		}
		w[widest]--
	}
	if len(w) > 0 {
		w[len(w)-1] += max(0, avail-total())
	}
	return w
}

// bar is a gauge: pct of width filled, coloured by how full.
func bar(pct float64, width int) string {
	pct = max(0, min(100, pct))
	full := int(pct/100*float64(width) + 0.5)
	color := cGreen
	switch {
	case pct >= 90:
		color = cRed
	case pct >= 75:
		color = cOrange
	}
	return color + strings.Repeat("█", full) + cGray + strings.Repeat("░", width-full) + reset
}
