package watch

import "strings"

// oscScanner finds, in a pane's raw output, what a terminal would have
// turned into an alert: a bell (BEL outside any escape sequence) and the
// desktop notifications OSC 9 and OSC 777 (tmux itself swallows both), and
// the program's own status reports, OSC 7501 (progstatus.go). It keeps its
// place across chunks, since a sequence can be split between two %output
// lines.
type oscScanner struct {
	state int // 0 text, 1 after ESC, 2 in OSC, 3 ESC in OSC
	buf   []byte
	// The OSC ran past oscMax: what is kept is not the whole of it.
	over bool
}

const oscMax = 4096

// scan returns whether a bell rang, the notification payloads seen and the
// status reports, in order.
func (s *oscScanner) scan(data []byte) (bell bool, notes []string, reports []statusReport) {
	end := func() {
		if n, ok := notification(s.buf); ok {
			notes = append(notes, n)
		}
		if b, ok := strings.CutPrefix(string(s.buf), "7501;"); ok && !s.over {
			if r, ok := parseStatus(b); ok {
				reports = append(reports, r)
			}
		}
		s.state = 0
	}
	for _, c := range data {
		switch s.state {
		case 0:
			switch c {
			case 0x07:
				bell = true
			case 0x1b:
				s.state = 1
			}
		case 1:
			if c == ']' {
				s.state, s.buf, s.over = 2, s.buf[:0], false
			} else {
				s.state = 0
			}
		case 2:
			switch c {
			case 0x07:
				end()
			case 0x1b:
				s.state = 3
			default:
				if len(s.buf) < oscMax {
					s.buf = append(s.buf, c)
				} else {
					s.over = true
				}
			}
		case 3:
			if c == '\\' {
				end()
			} else {
				// Not a terminator: the OSC was abandoned.
				s.state = 0
				if c == 0x1b {
					s.state = 1
				}
			}
		}
	}
	return bell, notes, reports
}

// notification is the text of OSC 9 (iTerm2's) or 777 (rxvt's notify), not
// OSC 9;4, which is a progress bar.
func notification(b []byte) (string, bool) {
	s := string(b)
	switch {
	case strings.HasPrefix(s, "9;4;"):
		return "", false
	case strings.HasPrefix(s, "9;"):
		return s[2:], true
	case strings.HasPrefix(s, "777;notify;"):
		return s[len("777;notify;"):], true
	}
	return "", false
}
