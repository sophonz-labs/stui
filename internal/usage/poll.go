package usage

// What stui's poller left: stui asks no provider itself, but reads a
// reading of the same shape if one is there, and keeps `stui usage poll`'s
// switch (PollOff) so the API reports usage the same way.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// PollEvery is the ordinary interval, with PollJitter either way.
const (
	PollEvery  = 5 * time.Minute
	PollJitter = 30 * time.Second
	pollMax    = time.Hour
)

// ---- the switch

type pollPrefs struct {
	Off bool `json:"off,omitempty"`
}

func pollPrefsPath(e Env) string { return filepath.Join(e.Dir(), "poll.json") }

// PollOff is whether `stui usage poll off` was said.
func PollOff(e Env) bool {
	b, err := os.ReadFile(pollPrefsPath(e))
	if err != nil {
		return false
	}
	var p pollPrefs
	return json.Unmarshal(b, &p) == nil && p.Off
}

// SetPoll turns polling on or off for good.
func SetPoll(e Env, on bool) error {
	if on {
		err := os.Remove(pollPrefsPath(e))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(e.Dir(), 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(pollPrefs{Off: true})
	return writeFile(pollPrefsPath(e), b)
}

// ---- what a poll kept

// pollRecord is ~/.stui/usage/poll-<agent>[-<key>].json.
type pollRecord struct {
	Agent      string       `json:"agent"`
	ConfigDir  string       `json:"configDir,omitempty"`
	ObservedAt int64        `json:"observedAt"`
	Plan       string       `json:"plan,omitempty"`
	Windows    []Window     `json:"windows"`
	Account    *AccountInfo `json:"account,omitempty"`
}

func pollFile(e Env, agent, configDir string) string {
	name := "poll-" + agent
	if agent == "claude" {
		name += "-" + strings.TrimPrefix(strings.TrimSuffix(filepath.Base(claudeFile(e.StateDir, configDir)), ".json"), "claude-")
	}
	return filepath.Join(e.Dir(), name+".json")
}

func readPoll(path string) (pollRecord, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return pollRecord{}, false
	}
	var r pollRecord
	if json.Unmarshal(b, &r) != nil || len(r.Windows) == 0 {
		return pollRecord{}, false
	}
	return r, true
}

// polledClaude is every Claude poll record, keyed by config directory.
func polledClaude(e Env) map[string]pollRecord {
	out := map[string]pollRecord{}
	files, _ := filepath.Glob(filepath.Join(e.Dir(), "poll-claude-*.json"))
	sort.Strings(files)
	for _, f := range files {
		if r, ok := readPoll(f); ok {
			out[r.ConfigDir] = r
		}
	}
	return out
}

// Newer says whether a poll record is the fresher word than a reading
// observed at the given time (0: none). Equal goes to the other reading:
// what the agent itself said is as good, and already shown.
func (r pollRecord) newer(observedAt int64) bool { return r.ObservedAt > observedAt }
