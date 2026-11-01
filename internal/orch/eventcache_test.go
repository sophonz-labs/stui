package orch

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// The cache gives what ReadEvents gives, reading only what was appended,
// and starts over when the file is shorter or another file.
func TestEventCacheReadsWhatWasAppended(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	var c EventCache
	if got := c.Read(run3); got != nil {
		t.Fatalf("no file: %v", got)
	}
	a, _ := AppendEvent(Event{Run: run3, Kind: EvSend, To: "%2"})
	b, _ := AppendEvent(Event{Run: run3, Kind: EvDone, From: "%2"})
	if got := c.Read(run3); !reflect.DeepEqual(got, ReadEvents(run3)) || len(got) != 2 || got[0].ID != a.ID || got[1].ID != b.ID {
		t.Fatalf("first read: %+v", got)
	}
	off := c.runs[run3].off
	d, _ := AppendEvent(Event{Run: run3, Kind: EvDelivered, Ref: b.ID})
	if got := c.Read(run3); len(got) != 3 || got[2].ID != d.ID || !reflect.DeepEqual(got, ReadEvents(run3)) {
		t.Fatalf("after an append: %+v", got)
	}
	if c.runs[run3].off <= off {
		t.Fatal("did not move on")
	}
	// A line being written (no newline yet) waits for its end.
	f, _ := os.OpenFile(EventsFile(run3), os.O_WRONLY|os.O_APPEND, 0o600)
	f.WriteString(`{"v":1,"id":"e-half","kind":"idle"`)
	if got := c.Read(run3); len(got) != 3 {
		t.Fatalf("read half a line: %d", len(got))
	}
	f.WriteString(`,"from":"%2"}` + "\n")
	f.Close()
	if got := c.Read(run3); len(got) != 4 || got[3].ID != "e-half" {
		t.Fatalf("the line once ended: %+v", got)
	}
	// What a caller appends does not land in the cache.
	got := c.Read(run3)
	_ = append(got, Event{ID: "mine"})
	if again := c.Read(run3); len(again) != 4 {
		t.Fatalf("the caller's append reached the cache: %d", len(again))
	}
	// Made again, shorter: read from the start.
	os.Remove(EventsFile(run3))
	e, _ := AppendEvent(Event{Run: run3, Kind: EvSend, To: "%9"})
	if got := c.Read(run3); len(got) != 1 || got[0].ID != e.ID {
		t.Fatalf("a new file: %+v", got)
	}
	c.Keep(nil)
	if len(c.runs) != 0 {
		t.Fatal("kept a run not asked for")
	}
}

// The carrier reads a run's file once and then only what grew: measured on
// a run the size of the largest here (1,538 lines).
func BenchmarkMailReread(b *testing.B) {
	b.Setenv("STUI_HOME", b.TempDir())
	for i := 0; i < 1538; i++ {
		AppendEvent(Event{Run: run3, Kind: EvSend, From: "%1", To: "%2", Title: strings.Repeat("a brief title ", 6), File: "/Users/me/.stui/orch/r-0000aaaa/sent/20261029-031636-terminal-go.md", Text: strings.Repeat("x", 200)})
	}
	b.Run("whole file", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			ReadEvents(run3)
		}
	})
	b.Run("from where it stopped", func(b *testing.B) {
		var c EventCache
		c.Read(run3)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			c.Read(run3)
		}
	})
}
