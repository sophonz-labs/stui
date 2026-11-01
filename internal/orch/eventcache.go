package orch

// The carrier's view of the runs' events, read once and then only what was
// appended. It looks every 2 s at every run
// written to in the last week, and read each events.jsonl whole each time:
// this run's is 1,538 lines, 490 KB, and growing. An events file is only
// ever appended to, one write a line, so what was read stays as it was; the
// next look starts where the last one stopped.

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"sync"
)

type eventLog struct {
	file os.FileInfo
	off  int64
	evs  []Event
}

// EventCache reads runs' events, each from where it last stopped.
type EventCache struct {
	mu   sync.Mutex
	runs map[string]*eventLog
}

// Read is run's events, as ReadEvents gives them. A file that is shorter
// than what was read, or is another file now (the run archived and made
// again), is read again from its start. A last line not yet ended (being
// written, or cut short by a crash) is left for the next look.
func (c *EventCache) Read(run string) []Event {
	if !runID.MatchString(run) {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.runs == nil {
		c.runs = map[string]*eventLog{}
	}
	f, err := os.Open(EventsFile(run))
	if err != nil {
		delete(c.runs, run)
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	l := c.runs[run]
	if l == nil || !os.SameFile(l.file, fi) || fi.Size() < l.off {
		l = &eventLog{}
		c.runs[run] = l
	}
	l.file = fi
	if fi.Size() > l.off {
		if _, err := f.Seek(l.off, io.SeekStart); err == nil {
			b, _ := io.ReadAll(io.LimitReader(f, fi.Size()-l.off))
			end := bytes.LastIndexByte(b, '\n')
			if end >= 0 {
				for _, line := range bytes.Split(b[:end], []byte{'\n'}) {
					var ev Event
					// ReadEvents' scanner skips a line over 64 KB; so does this.
					if len(line) <= 64<<10 && json.Unmarshal(line, &ev) == nil && ev.Kind != "" {
						l.evs = append(l.evs, ev)
					}
				}
				l.off += int64(end) + 1
			}
		}
	}
	// The caller's to append to: a copy when it does.
	return l.evs[:len(l.evs):len(l.evs)]
}

// Keep drops what it holds of runs not in keep.
func (c *EventCache) Keep(keep []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	in := map[string]bool{}
	for _, r := range keep {
		in[r] = true
	}
	for r := range c.runs {
		if !in[r] {
			delete(c.runs, r)
		}
	}
}
