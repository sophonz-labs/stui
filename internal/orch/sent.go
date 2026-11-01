package orch

// What stui worker send --file hands a worker: an
// unchanging copy under <run>/sent/, and one line typed into the worker
// that says where it is. The body itself is never typed: a long one would
// burst the input box, and the worker can read a file.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SentDir is where a run's sent copies are kept; run "" is a send made
// outside any run.
func SentDir(run string) string {
	if run == "" {
		return filepath.Join(Dir(), "sent")
	}
	return filepath.Join(RunDir(run), "sent")
}

// WriteSent keeps content as <YYYYMMDD-HHMMSS>-<worker>.md (0600) and
// returns its path. A second one in the same second gets -2, and so on.
// It never writes over a copy already there.
func WriteSent(run, worker string, content []byte, now time.Time) (string, error) {
	if run != "" && !runID.MatchString(run) {
		return "", os.ErrInvalid
	}
	d := SentDir(run)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	who := Name(worker)
	if who == "" {
		who = "worker"
	}
	base := now.Format("20060102-150405") + "-" + who
	for i := 1; i < 100; i++ {
		name := base + ".md"
		if i > 1 {
			name = fmt.Sprintf("%s-%d.md", base, i)
		}
		p := filepath.Join(d, name)
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, err = f.Write(content)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(p)
			return "", err
		}
		return p, nil
	}
	return "", fmt.Errorf("%s: too many sent in one second", base)
}

// SentLine is what is typed into the worker: the note first when there is
// one.
//
//	A message from coordinator is in /…/sent/20261019-140500-ux.md ("Title", 4.1 KB). Read it and do it.
func SentLine(from, file, title string, size int, note string) string {
	if from == "" {
		from = "your coordinator"
	}
	line := fmt.Sprintf("A message from %s is in %s (%q, %s). Read it and do it.", from, file, strings.Join(strings.Fields(title), " "), Size(size))
	if note = strings.Join(strings.Fields(note), " "); note != "" {
		return note + " -- " + line
	}
	return line
}

// Size is a byte count as people read it: 812 B, 4.1 KB, 1.0 MB.
func Size(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}

// remoteDoc is a file sent to a worker of another machine: its body, and
// who sent it and the note to type before it.
type remoteDoc struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	From    string `json:"from,omitempty"`
	Note    string `json:"note,omitempty"`
}

func firstOr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
