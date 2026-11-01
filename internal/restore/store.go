package restore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Store keeps snapshots under dir as <id>.json, a few generations of them:
// a new file only when the arrangement changed, the newest one confirmed in
// place otherwise. Every write is a temporary file renamed into place.
type Store struct {
	Dir  string
	Keep int
}

// Generations kept by default.
const DefaultKeep = 5

func (st Store) keep() int {
	if st.Keep > 0 {
		return st.Keep
	}
	return DefaultKeep
}

func writeAtomic(path string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// List is every snapshot, newest first.
func (st Store) List() []*Snapshot {
	entries, _ := os.ReadDir(st.Dir)
	var out []*Snapshot
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".") || e.Name() == "restored.json" {
			continue
		}
		if s, err := st.Load(strings.TrimSuffix(e.Name(), ".json")); err == nil {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].First > out[j].First })
	return out
}

// Latest is the newest snapshot, or nil.
func (st Store) Latest() *Snapshot {
	if l := st.List(); len(l) > 0 {
		return l[0]
	}
	return nil
}

func (st Store) Load(id string) (*Snapshot, error) {
	if id == "" || strings.ContainsAny(id, `/\`) {
		return nil, fmt.Errorf("no snapshot %q", id)
	}
	b, err := os.ReadFile(filepath.Join(st.Dir, id+".json"))
	if err != nil {
		return nil, fmt.Errorf("no snapshot %q", id)
	}
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Save records s: a new generation when its arrangement differs from the
// newest one's, or when it is another server's; otherwise the newest one
// is confirmed (its time moved on). A snapshot with no sessions is never
// saved -- it is what a dead server looks like, and would push the one
// worth restoring out.
func (st Store) Save(s *Snapshot, now time.Time) (saved *Snapshot, fresh bool, err error) {
	if s == nil || len(s.Sessions) == 0 {
		return nil, false, errors.New("nothing to save")
	}
	ms := now.UnixMilli()
	s.Taken = ms
	if last := st.Latest(); last != nil && last.Server.Same(s.Server) && last.fingerprint() == s.fingerprint() {
		s.ID, s.First = last.ID, last.First
		b, _ := json.MarshalIndent(s, "", " ")
		return s, false, writeAtomic(filepath.Join(st.Dir, s.ID+".json"), b, 0o600)
	}
	s.ID = fmt.Sprintf("%d", ms)
	s.First = ms
	b, _ := json.MarshalIndent(s, "", " ")
	if err := writeAtomic(filepath.Join(st.Dir, s.ID+".json"), b, 0o600); err != nil {
		return nil, false, err
	}
	st.prune()
	return s, true, nil
}

// prune keeps the newest few, and two more that a restore needs: the last
// one taken from the server before this one (what a death took away -- a
// restore makes several new arrangements in its first minute, which would
// otherwise push it out), and the one the last restore came from.
func (st Store) prune() {
	list := st.List()
	keep := map[string]bool{}
	if m := st.Marker(); m != nil {
		keep[m.From] = true
	}
	if len(list) > 0 {
		for _, s := range list[1:] {
			if !s.Server.Same(list[0].Server) {
				keep[s.ID] = true
				break
			}
		}
	}
	for i, s := range list {
		if i >= st.keep() && !keep[s.ID] {
			os.Remove(filepath.Join(st.Dir, s.ID+".json"))
			os.RemoveAll(filepath.Join(st.Dir, s.ID))
		}
	}
}

// Marker records that a restore happened, so the same death is not
// restored twice and the next `stui` can say so -- or that what the snapshot
// held was closed on purpose, and is not to be restored by itself at all.
type Marker struct {
	At        int64    `json:"at"`        // unix ms
	StoppedAt int64    `json:"stoppedAt"` // the snapshot's last confirmation: the server was alive then
	From      string   `json:"from"`
	Into      Server   `json:"into"`
	Sessions  []string `json:"sessions"`
	Windows   int      `json:"windows"`
	Panes     int      `json:"panes"`
	Agents    int      `json:"agents"`
	// Of the agents, how many were resumed, and how many wait at their
	// prompt (of those, how many have no record to resume).
	Resumed  int      `json:"resumed,omitempty"`
	Waiting  int      `json:"waiting,omitempty"`
	NoRecord int      `json:"noRecord,omitempty"`
	Skipped  []string `json:"skipped,omitempty"`
	Auto     bool     `json:"auto"`
	// The CLI has told someone about it.
	Shown bool `json:"shown,omitempty"`
	// Not a restore: the snapshot's last session was closed on request
	// (a phone or a browser), so the server going with it is no death.
	Closed bool `json:"closed,omitempty"`
	// Not a restore: asked whether to restore it, the person chose to
	// start fresh (FindPending, Decline).
	Declined bool `json:"declined,omitempty"`
}

func (st Store) markerPath() string { return filepath.Join(st.Dir, "restored.json") }

func (st Store) Marker() *Marker {
	b, err := os.ReadFile(st.markerPath())
	if err != nil {
		return nil
	}
	var m Marker
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return &m
}

func (st Store) SetMarker(m *Marker) error {
	b, _ := json.MarshalIndent(m, "", " ")
	return writeAtomic(st.markerPath(), b, 0o600)
}

// Config is restore.json in stui's home. Absent means the defaults.
type Config struct {
	// Restore by itself when the server died. Default true.
	Auto *bool `json:"auto,omitempty"`
	// Enter an agent's resume line, not only type it, when its
	// conversation is on record (Decide). Default true; false types it.
	ResumeAgents *bool `json:"resumeAgents,omitempty"`
	// Keep each pane's last lines with the snapshot and show them in the
	// restored pane. 0 (the default) keeps none; at most 2000.
	ScrollbackLines int `json:"scrollbackLines,omitempty"`
}

func (c Config) AutoOn() bool { return c.Auto == nil || *c.Auto }

// ResumeOn is whether agents' resume lines are entered (default true).
func (c Config) ResumeOn() bool { return c.ResumeAgents == nil || *c.ResumeAgents }

func (c Config) Lines() int { return max(0, min(c.ScrollbackLines, 2000)) }

func LoadConfig(home string) Config {
	var c Config
	if b, err := os.ReadFile(filepath.Join(home, "restore.json")); err == nil {
		json.Unmarshal(b, &c)
	}
	return c
}
