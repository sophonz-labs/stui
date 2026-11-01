package chat

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A turn the hooks reported, entirely after the transcript's last write:
// the agent is not writing it. A turn still being written, or one that
// ended a moment ago, is not.
func TestNotRecording(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(path, []byte("{}\n"), 0o600)
	written := time.Now().Add(-35 * time.Minute)
	os.Chtimes(path, written, written)
	now := time.Now()

	if at, stale := notRecording(path, now.Add(-2*time.Minute), now.Add(-time.Minute), now); !stale || at.Unix() != written.Unix() {
		t.Fatalf("missed turn: %v %v", at, stale)
	}
	if _, stale := notRecording(path, now.Add(-2*time.Minute), now.Add(-2*time.Second), now); stale {
		t.Fatal("a turn that just ended may still be written")
	}
	if _, stale := notRecording(path, now.Add(-time.Minute), now.Add(-2*time.Minute), now); stale {
		t.Fatal("a turn in progress (submitted after the last stop)")
	}
	if _, stale := notRecording(path, time.Time{}, time.Time{}, now); stale {
		t.Fatal("no hooks: nothing to go by")
	}
	os.Chtimes(path, now, now)
	if _, stale := notRecording(path, now.Add(-2*time.Minute), now.Add(-time.Minute), now); stale {
		t.Fatal("written after the prompt")
	}
}
