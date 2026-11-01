package chat

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Reading what is typed in an agent's input box, from the screen as
// `tmux capture-pane -p -e` gives it (with its colors): what the person at
// the machine may be in the middle of typing, which a phone's message would
// otherwise be glued onto (or which Ctrl-U would wipe).
//
// What the boxes look like, captured from the real programs in a sandbox
// tmux (testdata/input, Claude Code 2.1.294, Codex 0.159.3):
//
//	Claude: a rule, "❯ " and the text, its further lines indented two
//	        spaces, a rule. Empty, it shows a dim placeholder:
//	        ❯ \e[2mTry "edit <filepath> to..."\e[0m
//	Codex:  "\e[1m›\e[0m " and the text, further lines indented two spaces,
//	        then its footer. Empty: a dim placeholder ("Ask Codex to do
//	        anything"). A slash command's menu draws "›" lines above it: the
//	        box is the last one.
//
// Dim text, or text in a grey, is a placeholder or a hint, never typed
// text: typed text is drawn in the default color.
//
// Claude Code can draw its cursor itself, as one cell in reverse video over
// whatever is under it: 2.1.85 always (testdata/input), 2.1.303 in a tmux
// started without a config (seen in a sandbox). With
// the box empty that is the placeholder's first letter, the rest of it dim:
//
//	❯ \e[7mT\e[0;2mry\e[0m \e[2m"fix lint errors"\e[0m
//
// so the reverse cell is the placeholder's when the cell right after it is
// dim, and typed otherwise. Typed text is never followed by dim text there:
// the placeholder goes at the first key, and a letter typed and stepped back
// onto is drawn \e[7mx\e[0m with nothing after it.

// cell is one character on screen and how it is drawn.
type cell struct {
	r   rune
	dim bool // SGR 2, or a grey foreground
	// Drawn in a color of its own rather than the default one.
	color bool
	// Reverse video (SGR 7): an agent drawing its own cursor.
	cursor bool
}

// sgrRE is any CSI sequence; only SGR (ending in m) changes how text is
// drawn.
var sgrRE = regexp.MustCompile(`^\x1b\[[0-9;:?]*[ -/]*[@-~]`)

// styledLine reads one captured line into cells, carrying the drawing state
// across lines (tmux does not reset it at a line's end).
type styleState struct{ dim, grey, color, inverse bool }

func (s *styleState) apply(params string) {
	if params == "" {
		*s = styleState{}
		return
	}
	ps := strings.FieldsFunc(params, func(r rune) bool { return r == ';' || r == ':' })
	for i := 0; i < len(ps); i++ {
		n, err := strconv.Atoi(ps[i])
		if err != nil {
			continue
		}
		switch {
		case n == 0:
			*s = styleState{}
		case n == 2:
			s.dim = true
		case n == 22:
			s.dim = false
		case n == 7:
			s.inverse = true
		case n == 27:
			s.inverse = false
		case n == 39:
			s.grey, s.color = false, false
		case n == 90:
			s.grey, s.color = true, true
		case n >= 30 && n <= 37, n >= 91 && n <= 97:
			s.grey, s.color = false, true
		case n == 38 && i+2 < len(ps) && ps[i+1] == "5":
			c, _ := strconv.Atoi(ps[i+2])
			// 8 (bright black) and the grey ramp, but not its near-white end.
			s.grey, s.color = c == 8 || (c >= 232 && c <= 250), true
			i += 2
		case n == 38 && i+4 < len(ps) && ps[i+1] == "2":
			r, _ := strconv.Atoi(ps[i+2])
			g, _ := strconv.Atoi(ps[i+3])
			b, _ := strconv.Atoi(ps[i+4])
			s.grey, s.color = isGrey(r, g, b), true
			i += 4
		case n == 48 && i+1 < len(ps) && ps[i+1] == "5":
			i += 2
		case n == 48 && i+1 < len(ps) && ps[i+1] == "2":
			i += 4
		}
	}
}

func isGrey(r, g, b int) bool {
	hi, lo := max(r, g, b), min(r, g, b)
	return hi-lo <= 16 && hi >= 64 && hi <= 200
}

func styledLines(screen string) [][]cell {
	var out [][]cell
	var st styleState
	for _, raw := range strings.Split(strings.ReplaceAll(screen, "\r", ""), "\n") {
		var line []cell
		for i := 0; i < len(raw); {
			if raw[i] == 0x1b {
				if m := sgrRE.FindString(raw[i:]); m != "" {
					if strings.HasSuffix(m, "m") {
						st.apply(m[2 : len(m)-1])
					}
					i += len(m)
					continue
				}
				i++
				continue
			}
			r, n := utf8.DecodeRuneInString(raw[i:])
			i += n
			if r < 0x20 {
				continue
			}
			line = append(line, cell{r: r, dim: st.dim || st.grey, color: st.color, cursor: st.inverse})
		}
		// A drawn cursor is the placeholder's when what follows it is.
		for i := range line {
			if line[i].cursor && !line[i].dim && i+1 < len(line) && line[i+1].dim {
				line[i].dim = true
			}
		}
		out = append(out, line)
	}
	return out
}

func cellText(cs []cell) string {
	var b strings.Builder
	for _, c := range cs {
		b.WriteRune(c.r)
	}
	return b.String()
}

// typed says whether any of cs is text drawn as typed: not dim, not grey.
func typed(cs []cell) bool {
	for _, c := range cs {
		if !c.dim && c.r != ' ' && c.r != ' ' {
			return true
		}
	}
	return false
}

func isRule(cs []cell) bool {
	s := strings.TrimSpace(cellText(cs))
	return utf8.RuneCountInString(s) >= 20 && strings.Trim(s, "─") == ""
}

// InputBox is what an agent's input box holds.
type InputBox struct {
	// The box was found on screen.
	Found bool
	// What is typed in it, lines joined with "\n"; "" when it is empty (or
	// shows only its placeholder).
	Text string
}

// ReadInput finds agent's input box on screen (captured with -e) and reads
// what is typed in it.
func ReadInput(screen, agent string) InputBox {
	lines := styledLines(screen)
	// Only the bottom of the screen: the box is there, and a conversation
	// above may quote one.
	from := max(len(lines)-30, 0)
	switch agent {
	case "claude":
		return readClaude(lines, from)
	case "codex":
		return readCodex(lines, from)
	case "opencode":
		return readOpencode(lines, from)
	case "pi":
		return readPi(lines, from)
	}
	return InputBox{}
}

// piBusyRule is the rule over Pi's box while it works: a spinner and a
// label in it ("── ⠇ Working ───…").
var piBusyRule = regexp.MustCompile(`^─{2,} [\x{2801}-\x{28FF}] \S.* ─{3,}$`)

// readPi: Pi (1.4.0) draws its box as the lines between two rules the width
// of the screen, with no marker or placeholder; below it the folder and the
// model, and a command's completions between the box and them. While it
// works the upper rule carries the spinner, and the box still takes text (it
// is sent when the turn ends). A dialog an extension opens takes the box's
// place between the same rules, and is not a box ("↑↓ navigate").
func readPi(lines [][]cell, from int) InputBox {
	for b := len(lines) - 1; b > from; b-- {
		if !isRule(lines[b]) {
			continue
		}
		top := -1
		for i := b - 1; i >= from; i-- {
			if t := strings.TrimSpace(cellText(lines[i])); isRule(lines[i]) || piBusyRule.MatchString(t) {
				top = i
				break
			}
		}
		if top < 0 {
			return InputBox{}
		}
		var parts []string
		any := false
		for i := top + 1; i < b; i++ {
			t := strings.TrimRight(strings.ReplaceAll(cellText(lines[i]), "\u00a0", " "), " ")
			if strings.Contains(t, "↑↓ navigate") {
				return InputBox{}
			}
			if typed(lines[i]) && strings.TrimSpace(t) != "" {
				any = true
			}
			parts = append(parts, t)
		}
		if !any {
			return InputBox{Found: true}
		}
		return InputBox{Found: true, Text: strings.Trim(strings.Join(parts, "\n"), "\n")}
	}
	return InputBox{}
}

// readOpencode: opencode (1.19.4) draws its box as a run of lines that start
// with "┃", closed by a "╹▀▀▀" edge. From the bottom: the edge, the agent and
// model line ("┃  Build · Claude Sonnet 4.6 Anthropic"), a blank "┃", the
// typed lines, a blank "┃" on top. Empty, the one line is a grey
// placeholder ("Ask anything… …"). On a wide screen the sidebar shares the
// box's lines, to the right of its edge. Its messages above are "┃" runs too,
// apart from the box: a line that is not one comes between. A permission
// dialog has no edge: no box.
func readOpencode(lines [][]cell, from int) InputBox {
	bar := func(i int) (int, bool) {
		t := cellText(lines[i])
		at := strings.Index(t, "┃")
		if at < 0 || strings.TrimSpace(t[:at]) != "" {
			return 0, false
		}
		return utf8.RuneCountInString(t[:at]), true
	}
	for b := len(lines) - 1; b >= from; b-- {
		t := strings.TrimSpace(cellText(lines[b]))
		if !strings.HasPrefix(t, "╹▀") {
			continue
		}
		top := b
		for top-1 >= from {
			if _, ok := bar(top - 1); !ok {
				break
			}
			top--
		}
		// top: blank; top+1 .. b-3: typed; b-2: blank; b-1: the model.
		if b-top < 4 {
			return InputBox{}
		}
		// The box is as wide as its edge: on a wide screen the sidebar is
		// drawn on the same lines, to its right.
		edge := 0
		for i, c := range lines[b] {
			if c.r == '▀' {
				edge = i + 1
			}
		}
		var parts []string
		any := false
		for i := top + 1; i <= b-3; i++ {
			at, _ := bar(i)
			rest := lines[i][min(at+3, len(lines[i])):min(max(edge, at+3), len(lines[i]))]
			if typed(rest) {
				any = true
			}
			parts = append(parts, strings.TrimRight(strings.ReplaceAll(cellText(rest), "\u00a0", " "), " "))
		}
		if !any {
			return InputBox{Found: true}
		}
		return InputBox{Found: true, Text: strings.Trim(strings.Join(parts, "\n"), "\n")}
	}
	return InputBox{}
}

// readClaude: the last rule followed by a "❯" line, up to the next rule. In
// its shell mode the box starts "! " instead: what is typed there is a
// command, and even an empty one is not an empty box (a message would run in
// the shell), so it reads as "!" and what follows.
func readClaude(lines [][]cell, from int) InputBox {
	start, marker := -1, ""
	for i := len(lines) - 2; i >= from; i-- {
		if !isRule(lines[i]) {
			continue
		}
		first := strings.TrimLeft(cellText(lines[i+1]), " ")
		switch {
		case strings.HasPrefix(first, "❯"):
			start, marker = i+1, "❯"
		case strings.HasPrefix(first, "! "):
			start, marker = i+1, "!"
		}
		if start >= 0 {
			break
		}
	}
	if start < 0 {
		return InputBox{}
	}
	end := -1
	for i := start + 1; i < len(lines); i++ {
		if isRule(lines[i]) {
			end = i
			break
		}
	}
	if end < 0 {
		return InputBox{}
	}
	box := boxText(lines[start:end], marker)
	if marker == "!" {
		box.Text = "!" + box.Text
	}
	return box
}

// readCodex: the last line starting with "›" near the bottom, and the
// indented lines under it.
func readCodex(lines [][]cell, from int) InputBox {
	for i := len(lines) - 1; i >= from; i-- {
		if !strings.HasPrefix(cellText(lines[i]), "›") {
			continue
		}
		box := [][]cell{lines[i]}
		for j := i + 1; j < len(lines); j++ {
			p := cellText(lines[j])
			if !strings.HasPrefix(p, "  ") || strings.TrimSpace(p) == "" || !typed(lines[j][2:]) {
				break
			}
			// The footer (model, folder) is drawn in colors, the typed
			// continuation in the default one.
			if colored(lines[j]) {
				break
			}
			box = append(box, lines[j])
		}
		return boxText(box, "›")
	}
	return InputBox{}
}

func colored(cs []cell) bool {
	for _, c := range cs {
		if c.color && c.r != ' ' {
			return true
		}
	}
	return false
}

// boxText reads the box's lines: the first after its marker, the others
// after their two-space indent.
func boxText(box [][]cell, marker string) InputBox {
	var parts []string
	any := false
	for i, l := range box {
		p := cellText(l)
		var rest []cell
		if i == 0 {
			idx := strings.Index(p, marker)
			skip := utf8.RuneCountInString(p[:idx]) + utf8.RuneCountInString(marker)
			if skip < len(l) && (l[skip].r == ' ' || l[skip].r == ' ') {
				skip++
			}
			rest = l[min(skip, len(l)):]
		} else {
			rest = l[min(2, len(l)):]
		}
		if typed(rest) {
			any = true
		}
		parts = append(parts, strings.TrimRight(strings.ReplaceAll(cellText(rest), " ", " "), " "))
	}
	if !any {
		return InputBox{Found: true}
	}
	return InputBox{Found: true, Text: strings.Trim(strings.Join(parts, "\n"), "\n")}
}
