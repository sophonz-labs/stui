package ui

import (
	"bufio"
	"errors"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/term"
)

// screen is the terminal while stui ui has it: raw input, the alternate
// screen, keys on a channel. Handing it to another program (an stui command,
// tmux) pauses the key reader first, so the program gets every key.
type screen struct {
	in, out int
	old     *term.State
	w       *bufio.Writer
	keys    chan string

	mu     sync.Mutex // held by the reader while it waits for and reads input
	paused bool
}

func openScreen() (*screen, error) {
	in, out := int(os.Stdin.Fd()), int(os.Stdout.Fd())
	if !term.IsTerminal(in) || !term.IsTerminal(out) {
		return nil, errors.New("stui ui needs a terminal")
	}
	s := &screen{in: in, out: out, w: bufio.NewWriterSize(os.Stdout, 64<<10), keys: make(chan string, 64)}
	if err := s.enter(); err != nil {
		return nil, err
	}
	go s.read()
	return s, nil
}

func (s *screen) enter() error {
	old, err := term.MakeRaw(s.in)
	if err != nil {
		return err
	}
	s.old = old
	s.w.WriteString("\x1b[?1049h\x1b[?25l\x1b[2J")
	return s.w.Flush()
}

func (s *screen) leave() {
	s.w.WriteString("\x1b[0m\x1b[2J\x1b[H\x1b[?25h\x1b[?1049l")
	s.w.Flush()
	if s.old != nil {
		term.Restore(s.in, s.old)
	}
}

func (s *screen) close() { s.pause(); s.leave() }

// pause stops the key reader; once it returns nothing reads stdin.
func (s *screen) pause() {
	s.mu.Lock()
	s.paused = true
	s.mu.Unlock()
}

func (s *screen) resume() {
	s.mu.Lock()
	s.paused = false
	s.mu.Unlock()
}

func (s *screen) size() (int, int) {
	w, h, err := term.GetSize(s.out)
	if err != nil || w < 20 || h < 8 {
		return 80, 24
	}
	return w, h
}

// read turns stdin into keys. It only reads when input is waiting, and
// holds mu while it does, so pause knows no read is left pending.
func (s *screen) read() {
	buf := make([]byte, 256)
	for {
		s.mu.Lock()
		if s.paused {
			s.mu.Unlock()
			time.Sleep(50 * time.Millisecond)
			continue
		}
		var n int
		var err error
		if ready(os.Stdin.Fd(), 100) {
			n, err = os.Stdin.Read(buf)
		}
		s.mu.Unlock()
		if err != nil {
			close(s.keys)
			return
		}
		for _, k := range splitKeys(buf[:n]) {
			s.keys <- k
		}
	}
}

// draw puts lines on the screen, top to bottom.
func (s *screen) draw(lines []string) {
	s.w.WriteString("\x1b[H")
	for i, l := range lines {
		if i > 0 {
			s.w.WriteString("\r\n")
		}
		s.w.WriteString(l)
		s.w.WriteString(reset)
	}
	s.w.Flush()
}

var csiKeys = map[string]string{
	"A": "up", "B": "down", "C": "right", "D": "left", "H": "home", "F": "end", "Z": "backtab",
	"1~": "home", "4~": "end", "7~": "home", "8~": "end", "5~": "pgup", "6~": "pgdn", "3~": "delete",
}

var ctrlKeys = map[byte]string{
	'\r': "enter", '\n': "enter", '\t': "tab", 0x7f: "backspace", 0x08: "backspace",
	0x03: "ctrl-c", 0x04: "ctrl-d", 0x15: "ctrl-u", 0x17: "ctrl-w", 0x02: "ctrl-b", 0x06: "ctrl-f",
	0x12: "ctrl-r", 0x0c: "ctrl-l",
}

// splitKeys is the keys in one read: names for the special ones
// ("up", "enter", "ctrl-d"), the character for the rest.
func splitKeys(b []byte) []string {
	var out []string
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c == 0x1b && i+1 < len(b) && (b[i+1] == '[' || b[i+1] == 'O'):
			j := i + 2
			for j < len(b) && (b[j] < 0x40 || b[j] > 0x7e) {
				j++
			}
			if j >= len(b) {
				return append(out, "esc")
			}
			// Modifiers (1;5A, 5;2~) don't matter here: the arrow is the arrow.
			body := string(b[j])
			if b[j] == '~' {
				first, _, _ := strings.Cut(string(b[i+2:j]), ";")
				body = first + "~"
			}
			if name, ok := csiKeys[body]; ok {
				out = append(out, name)
			}
			i = j + 1
		case c == 0x1b:
			out = append(out, "esc")
			i++
		case ctrlKeys[c] != "":
			out = append(out, ctrlKeys[c])
			i++
		case c < 0x20:
			i++
		default:
			r, n := utf8.DecodeRune(b[i:])
			out = append(out, string(r))
			i += n
		}
	}
	return out
}
