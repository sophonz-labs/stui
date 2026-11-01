package usage

// stui statusline: Claude Code's status line command, when the user asked
// for it with stui hooks install --usage.
//
// Claude Code runs its status line command after each message and pipes it
// a JSON object about the session; for a Claude.ai plan it has rate_limits
// in it (Claude Code 2.1.80 and later), with five_hour and seven_day, each
// a used_percentage and a resets_at in Unix seconds. stui keeps those two,
// nothing else from the object, and then runs the status line that was
// there before it with the same input, so what the user sees at the bottom
// of Claude does not change. With none before, it prints nothing.
//
// Like stui hook it must be quick and must not fail: it never prints on its
// own, and it exits with the status of the command it ran (0 without one).

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/sophonz-labs/stui/internal/proc"
)

// ChainFile is ~/.stui/statusline.json: the status line stui took the
// place of, for each Claude config directory, so stui statusline can run it
// and stui hooks uninstall --usage can put it back.
func ChainFile(stateDir string) string { return filepath.Join(stateDir, "statusline.json") }

// Chain is one config directory's entry in ChainFile.
type Chain struct {
	// settings.json's statusLine as it was, verbatim; null when there was
	// none.
	Previous json.RawMessage `json:"previous"`
	// Its command, for stui statusline (read from Previous when installed).
	Command     string `json:"command,omitempty"`
	InstalledAt int64  `json:"installedAt"`
}

// ReadChains is ChainFile, keyed by the config directory.
func ReadChains(stateDir string) map[string]Chain {
	out := map[string]Chain{}
	b, err := os.ReadFile(ChainFile(stateDir))
	if err == nil {
		json.Unmarshal(b, &out)
	}
	return out
}

// WriteChains replaces ChainFile; an empty map removes it.
func WriteChains(stateDir string, m map[string]Chain) error {
	if len(m) == 0 {
		err := os.Remove(ChainFile(stateDir))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	return writeFile(ChainFile(stateDir), append(b, '\n'))
}

// How often an unchanged record is written again anyway, so observedAt
// says the agent is still there.
const refreshEvery = 5 * time.Minute

type statusWindow struct {
	UsedPercentage *float64        `json:"used_percentage"`
	Utilization    *float64        `json:"utilization"`
	ResetsAt       json.RawMessage `json:"resets_at"`
	// spend_limit
	Period string `json:"period"`
	// model_scoped
	DisplayName string `json:"display_name"`
}

// claudeWindow is a rate_limits key's place: its length, kind and model.
// five_hour and seven_day are what the status line has carried since
// 2.1.80; the per-model weekly ones and model_scoped are what Claude
// Code's /usage data has, read here too in case the status line carries
// them one day.
func claudeWindow(key string) (minutes int, kind, label string, ok bool) {
	switch key {
	case "five_hour":
		return 300, "session", "", true
	case "seven_day":
		return 10080, "weekly", "", true
	case "spend_limit":
		return 0, "spend", "", true
	}
	if m, found := strings.CutPrefix(key, "seven_day_"); found {
		switch m {
		case "opus", "sonnet", "haiku", "fable":
			return 10080, "weekly", strings.ToUpper(m[:1]) + m[1:], true
		}
	}
	return 0, "", "", false
}

func spendMinutes(period string) int {
	switch period {
	case "daily":
		return 1440
	case "weekly":
		return 10080
	case "monthly":
		return 43200
	}
	return 0
}

// ParseStatusline is the windows in a status line input; none when it has
// no rate_limits (an API-key login, or before the first answer).
func ParseStatusline(raw []byte) []Window {
	if !bytes.Contains(raw, []byte(`"rate_limits"`)) {
		return nil
	}
	var in struct {
		RateLimits map[string]json.RawMessage `json:"rate_limits"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return nil
	}
	window := func(id string, minutes int, kind, label string, w statusWindow) (Window, bool) {
		// utilization is the usage endpoint's name for the same number,
		// taken in case the status line ever uses it.
		p := w.UsedPercentage
		if p == nil {
			p = w.Utilization
		}
		if p == nil {
			return Window{}, false
		}
		return Window{ID: id, Kind: kind, Label: label, Minutes: minutes, UsedPercent: clampPercent(*p), ResetsAt: parseResets(w.ResetsAt)}, true
	}
	var out []Window
	keys := make([]string, 0, len(in.RateLimits))
	for k := range in.RateLimits {
		keys = append(keys, k)
	}
	// five_hour, then seven_day, then the rest in name order.
	sort.Slice(keys, func(i, j int) bool {
		rank := func(k string) int {
			switch k {
			case "five_hour":
				return 0
			case "seven_day":
				return 1
			}
			return 2
		}
		if rank(keys[i]) != rank(keys[j]) {
			return rank(keys[i]) < rank(keys[j])
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		if k == "model_scoped" {
			var list []statusWindow
			json.Unmarshal(in.RateLimits[k], &list)
			for _, w := range list {
				name := strings.TrimSpace(w.DisplayName)
				if name == "" {
					continue
				}
				id := "seven_day_" + strings.ToLower(strings.Join(strings.Fields(name), "_"))
				if win, ok := window(id, 10080, "weekly", name, w); ok {
					out = append(out, win)
				}
			}
			continue
		}
		minutes, kind, label, ok := claudeWindow(k)
		if !ok {
			continue
		}
		var w statusWindow
		if json.Unmarshal(in.RateLimits[k], &w) != nil {
			continue
		}
		if kind == "spend" {
			minutes = spendMinutes(w.Period)
		}
		if win, ok := window(k, minutes, kind, label, w); ok {
			out = append(out, win)
		}
	}
	return out
}

// Keep writes the windows for configDir unless they are what is kept
// already and that was written less than refreshEvery ago. account is
// asked only when it writes: reading .claude.json on every message is not
// needed to know whose limits they are.
func Keep(stateDir, configDir string, ws []Window, now time.Time, account func() *AccountInfo) error {
	if len(ws) == 0 {
		return nil
	}
	path := claudeFile(stateDir, configDir)
	if b, err := os.ReadFile(path); err == nil {
		var old claudeRecord
		if json.Unmarshal(b, &old) == nil && sameWindows(old.Windows, ws) && now.Sub(time.UnixMilli(old.ObservedAt)) < refreshEvery {
			return nil
		}
	}
	rec := claudeRecord{ConfigDir: configDir, ObservedAt: now.UnixMilli(), Windows: ws}
	if account != nil {
		rec.Account = account()
	}
	b, _ := json.Marshal(rec)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeFile(path, b)
}

func sameWindows(a, b []Window) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func writeFile(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// RunStatusline is stui statusline. stateDir is ~/.stui.
func RunStatusline(stateDir string, stdin io.Reader, stdout, stderr io.Writer) int {
	raw, _ := io.ReadAll(io.LimitReader(stdin, 1<<20))
	configDir := os.Getenv("CLAUDE_CONFIG_DIR")
	// A background job's status line, whose window may not be the one the
	// limits are about, is still the same account's: kept all the same.
	home, _ := os.UserHomeDir()
	Keep(stateDir, configDir, ParseStatusline(raw), time.Now(), func() *AccountInfo { return ClaudeAccount(home, configDir) })

	key := configDir
	if key == "" {
		key = filepath.Join(home, ".claude")
	}
	c, ok := ReadChains(stateDir)[filepath.Clean(key)]
	if !ok || c.Command == "" {
		return 0
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", c.Command)
	} else {
		cmd = exec.Command("/bin/sh", "-c", c.Command)
	}
	// Its output goes to Claude through a pipe; no console of its own.
	proc.Hide(cmd)
	cmd.Stdin = bytes.NewReader(raw)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		return 1
	}
	return 0
}
