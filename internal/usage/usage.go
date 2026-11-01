// Package usage is how much of their plan's limits the coding agents on this
// machine have used: Claude Code's 5-hour and weekly windows, Codex's.
//
// Three sources, the newest reading winning. Claude Code hands its rate
// limits to its status line command on every turn; with `stui hooks install
// --usage` that command is stui, which keeps them in ~/.stui/usage and
// runs the status line that was there before (statusline.go). Codex writes
// its limits into its own session files, and those are read as they are.
// And every few minutes stui asks each provider itself with the login its
// CLI keeps on this machine (poll.go), so the numbers move while the agents
// are quiet. Clients read GET /api/usage from stui's local server.
//
// Which fields carry the limits, and how a window is told from another,
// follow Orca (MIT; see NOTICE).
package usage

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Window is one limit: how much of it is used, and when it starts over.
type Window struct {
	// five_hour, seven_day, seven_day_opus, spend_limit (Claude); primary,
	// secondary (Codex).
	ID string `json:"id"`
	// session (the 5-hour window), weekly, spend (a gateway's spend limit)
	// or other.
	Kind string `json:"kind"`
	// The model a weekly limit is for ("Opus", "Sonnet"); "" for all models.
	Label string `json:"label,omitempty"`
	// The window's length; 0 when not known.
	Minutes int `json:"minutes"`
	// 0-100.
	UsedPercent float64 `json:"usedPercent"`
	// Unix ms; 0 when not known.
	ResetsAt int64 `json:"resetsAt,omitempty"`
	// The reset time has passed since this was seen: what was used is gone,
	// and UsedPercent is 0 until the agent runs again.
	Reset bool `json:"reset,omitempty"`
}

// Agent is one agent's limits.
type Agent struct {
	Agent string `json:"agent"`
	// ok: Windows are there. setup: stui is not given them (Claude without
	// stui hooks install --usage). waiting: stui is given them, but nothing
	// has come yet -- the agent has not run since, or its login has no
	// plan limits (an API key).
	State string `json:"state"`
	// statusline, statusline-off, session-file or poll (stui asked the
	// provider itself).
	Source string `json:"source,omitempty"`
	// When the agent last said (unix ms).
	ObservedAt int64    `json:"observedAt,omitempty"`
	Plan       string   `json:"plan,omitempty"`
	Windows    []Window `json:"windows"`
	// Whose limits they are: the same ID on every machine signed in to the
	// same account. nil when it cannot be told (the apps then keep the
	// machine's own).
	Account *AccountInfo `json:"account,omitempty"`
	// CLAUDE_CONFIG_DIR's last element, when it was not the default.
	Profile string `json:"profile,omitempty"`
}

// Sources of an Agent's windows.
const (
	SourceStatusline    = "statusline"
	SourceStatuslineOff = "statusline-off"
	SourceSessionFile   = "session-file"
	SourcePoll          = "poll"
)

// Report is GET /api/usage.
type Report struct {
	Agents []Agent `json:"agents"`
	// The levels the apps colour at and stui notifies at (usage-high).
	Levels []int `json:"levels"`
}

// Levels are 80 % and 95 %.
var Levels = []int{80, 95}

// Env is where the files are; tests point it at a temporary home.
type Env struct {
	// The home directory (~/.claude.json is in it).
	Home string
	// ~/.stui
	StateDir string
	// ~/.claude, or CLAUDE_CONFIG_DIR.
	ClaudeDir string
	// ~/.codex, or CODEX_HOME.
	CodexDir string
	// Whether stui's status line is in Claude's settings.
	ClaudeFeed func() bool
	Now        func() time.Time
}

// DefaultEnv reads the real environment.
func DefaultEnv(stateDir string) Env {
	h, _ := os.UserHomeDir()
	e := Env{Home: h, StateDir: stateDir, ClaudeDir: os.Getenv("CLAUDE_CONFIG_DIR"), CodexDir: os.Getenv("CODEX_HOME"), Now: time.Now}
	if e.ClaudeDir == "" {
		e.ClaudeDir = filepath.Join(h, ".claude")
	}
	if e.CodexDir == "" {
		e.CodexDir = filepath.Join(h, ".codex")
	}
	return e
}

func (e Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Dir is ~/.stui/usage.
func (e Env) Dir() string { return filepath.Join(e.StateDir, "usage") }

// ---- Claude: what stui statusline kept

// claudeRecord is ~/.stui/usage/claude-<key>.json.
type claudeRecord struct {
	ConfigDir  string       `json:"configDir,omitempty"`
	ObservedAt int64        `json:"observedAt"`
	Windows    []Window     `json:"windows"`
	Account    *AccountInfo `json:"account,omitempty"`
}

// claudeFile is the record for one CLAUDE_CONFIG_DIR ("" is the default).
func claudeFile(stateDir, configDir string) string {
	key := "default"
	if configDir != "" {
		s := sha256.Sum256([]byte(filepath.Clean(configDir)))
		key = hex.EncodeToString(s[:6])
	}
	return filepath.Join(stateDir, "usage", "claude-"+key+".json")
}

// readClaude is the status line's records and the poller's, one Agent per
// config directory, the newer reading of the two.
func readClaude(e Env) []Agent {
	files, _ := filepath.Glob(filepath.Join(e.Dir(), "claude-*.json"))
	sort.Strings(files)
	polled := polledClaude(e)
	var out []Agent
	seen := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var r claudeRecord
		if json.Unmarshal(b, &r) != nil || len(r.Windows) == 0 {
			continue
		}
		a := Agent{Agent: "claude", State: "ok", Source: SourceStatusline, ObservedAt: r.ObservedAt, Windows: r.Windows, Account: r.Account}
		if p, ok := polled[r.ConfigDir]; ok && p.newer(r.ObservedAt) {
			a.Source, a.ObservedAt, a.Windows = SourcePoll, p.ObservedAt, p.Windows
			if p.Account != nil {
				a.Account = p.Account
			}
		}
		seen[r.ConfigDir] = true
		out = append(out, finishClaude(e, a, r.ConfigDir))
	}
	dirs := make([]string, 0, len(polled))
	for d := range polled {
		if !seen[d] {
			dirs = append(dirs, d)
		}
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		p := polled[d]
		a := Agent{Agent: "claude", State: "ok", Source: SourcePoll, ObservedAt: p.ObservedAt, Windows: p.Windows, Account: p.Account}
		out = append(out, finishClaude(e, a, d))
	}
	return out
}

func finishClaude(e Env, a Agent, configDir string) Agent {
	if configDir != "" {
		a.Profile = filepath.Base(configDir)
	}
	if a.Account == nil {
		a.Account = ClaudeAccount(e.Home, configDir)
	}
	if a.Account != nil {
		a.Plan = a.Account.Plan
	}
	return a
}

// ---- Codex: its session files

// codexLimits is a token_count event's rate_limits.
type codexLimits struct {
	Primary   *codexWindow `json:"primary"`
	Secondary *codexWindow `json:"secondary"`
	PlanType  string       `json:"plan_type"`
}

type codexWindow struct {
	UsedPercent *float64 `json:"used_percent"`
	// window_minutes, and older builds' window_duration_mins.
	WindowMinutes  *float64 `json:"window_minutes"`
	WindowDuration *float64 `json:"window_duration_mins"`
	// Unix seconds; older builds give resets_in_seconds from the event.
	ResetsAt        *float64 `json:"resets_at"`
	ResetsInSeconds *float64 `json:"resets_in_seconds"`
}

// How far back stui looks for Codex's newest session file.
const codexLookback = 21 * 24 * time.Hour

// The end of a session file read for its last rate_limits.
const codexTail = 512 << 10

// codexFiles is Codex's session files modified since, newest first.
func codexFiles(dir string, since time.Time) []string {
	root := filepath.Join(dir, "sessions")
	type f struct {
		path string
		mod  time.Time
	}
	var fs []f
	years, _ := os.ReadDir(root)
	for _, y := range years {
		if !y.IsDir() {
			continue
		}
		months, _ := os.ReadDir(filepath.Join(root, y.Name()))
		for _, m := range months {
			if !m.IsDir() {
				continue
			}
			days, _ := os.ReadDir(filepath.Join(root, y.Name(), m.Name()))
			for _, d := range days {
				if !d.IsDir() {
					continue
				}
				// Codex files a session under the day it started. A day
				// folder older than since is passed over: a session started
				// that long ago and still written to is rare, and walking
				// every one Codex ever kept is not.
				if day, err := time.ParseInLocation("2006/01/02", y.Name()+"/"+m.Name()+"/"+d.Name(), time.Local); err == nil && day.Before(since.Truncate(24*time.Hour).Add(-24*time.Hour)) {
					continue
				}
				dd := filepath.Join(root, y.Name(), m.Name(), d.Name())
				ents, _ := os.ReadDir(dd)
				for _, ent := range ents {
					name := ent.Name()
					if ent.IsDir() || !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
						continue
					}
					info, err := ent.Info()
					if err != nil || info.ModTime().Before(since) {
						continue
					}
					fs = append(fs, f{filepath.Join(dd, name), info.ModTime()})
				}
			}
		}
	}
	sort.Slice(fs, func(i, j int) bool { return fs[i].mod.After(fs[j].mod) })
	out := make([]string, len(fs))
	for i, x := range fs {
		out[i] = x.path
	}
	return out
}

// lastCodexLimits finds the last rate_limits in path's tail, with the time
// of the event that carried it.
func lastCodexLimits(path string) (codexLimits, time.Time, bool) {
	fh, err := os.Open(path)
	if err != nil {
		return codexLimits{}, time.Time{}, false
	}
	defer fh.Close()
	if fi, err := fh.Stat(); err == nil && fi.Size() > codexTail {
		fh.Seek(fi.Size()-codexTail, io.SeekStart)
	}
	var (
		found codexLimits
		at    time.Time
		ok    bool
	)
	r := bufio.NewReaderSize(fh, 64<<10)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && bytes.Contains(line, []byte(`"rate_limits"`)) && bytes.Contains(line, []byte(`"token_count"`)) {
			var rec struct {
				Timestamp string `json:"timestamp"`
				Payload   struct {
					Type       string       `json:"type"`
					RateLimits *codexLimits `json:"rate_limits"`
				} `json:"payload"`
			}
			if json.Unmarshal(line, &rec) == nil && rec.Payload.Type == "token_count" && rec.Payload.RateLimits != nil &&
				(usable(rec.Payload.RateLimits.Primary) || usable(rec.Payload.RateLimits.Secondary)) {
				found, ok = *rec.Payload.RateLimits, true
				at, _ = time.Parse(time.RFC3339Nano, rec.Timestamp)
			}
		}
		if err != nil {
			break
		}
	}
	return found, at, ok
}

func usable(w *codexWindow) bool {
	return w != nil && w.UsedPercent != nil && !math.IsNaN(*w.UsedPercent) && !math.IsInf(*w.UsedPercent, 0)
}

// codexWindowOut is one Codex window as the API gives it. Codex gives reset
// times in seconds; a duration a minute off the 5-hour or weekly length
// (older builds) is read as that length.
func codexWindowOut(id string, w *codexWindow, at time.Time) Window {
	out := Window{ID: id, Kind: "other", UsedPercent: clampPercent(*w.UsedPercent)}
	var mins float64
	switch {
	case w.WindowMinutes != nil:
		mins = *w.WindowMinutes
	case w.WindowDuration != nil:
		mins = *w.WindowDuration
	}
	for _, canon := range []float64{300, 10080} {
		if math.Abs(mins-canon) <= 1 {
			mins = canon
		}
	}
	out.Minutes = int(mins)
	switch out.Minutes {
	case 300:
		out.Kind = "session"
	case 10080:
		out.Kind = "weekly"
	}
	switch {
	case w.ResetsAt != nil && *w.ResetsAt > 0:
		out.ResetsAt = epochMillis(*w.ResetsAt)
	case w.ResetsInSeconds != nil && !at.IsZero():
		out.ResetsAt = at.Add(time.Duration(*w.ResetsInSeconds * float64(time.Second))).UnixMilli()
	}
	return out
}

func readCodex(e Env) (Agent, bool) {
	if fi, err := os.Stat(e.CodexDir); err != nil || !fi.IsDir() {
		return Agent{}, false
	}
	a := Agent{Agent: "codex", State: "waiting", Source: SourceSessionFile, Windows: []Window{}, Account: CodexAccount(e.CodexDir)}
	files := codexFiles(e.CodexDir, e.now().Add(-codexLookback))
	for i, f := range files {
		if i >= 6 {
			break
		}
		lim, at, ok := lastCodexLimits(f)
		if !ok {
			continue
		}
		if at.IsZero() {
			if fi, err := os.Stat(f); err == nil {
				at = fi.ModTime()
			}
		}
		a.State, a.ObservedAt, a.Plan = "ok", at.UnixMilli(), codexPlan(lim.PlanType)
		if a.Plan == "" && a.Account != nil {
			a.Plan = a.Account.Plan
		}
		if usable(lim.Primary) {
			a.Windows = append(a.Windows, codexWindowOut("primary", lim.Primary, at))
		}
		if usable(lim.Secondary) {
			a.Windows = append(a.Windows, codexWindowOut("secondary", lim.Secondary, at))
		}
		break
	}
	// What stui asked ChatGPT itself, when that is the newer word.
	if p, ok := readPoll(pollFile(e, "codex", "")); ok && p.newer(a.ObservedAt) {
		a.State, a.Source, a.ObservedAt, a.Windows = "ok", SourcePoll, p.ObservedAt, p.Windows
		if p.Plan != "" {
			a.Plan = p.Plan
		}
		if p.Account != nil {
			a.Account = p.Account
		}
		if a.Plan == "" && a.Account != nil {
			a.Plan = a.Account.Plan
		}
	}
	return a, true
}

// ---- the report

// Read is every agent's limits as they stand now.
func Read(e Env) Report {
	rep := Report{Agents: []Agent{}, Levels: Levels}
	claude := readClaude(e)
	if len(claude) == 0 {
		if fi, err := os.Stat(e.ClaudeDir); err == nil && fi.IsDir() {
			state := "setup"
			if e.ClaudeFeed != nil && e.ClaudeFeed() {
				state = "waiting"
			}
			a := Agent{Agent: "claude", State: state, Windows: []Window{}, Account: ClaudeAccount(e.Home, os.Getenv("CLAUDE_CONFIG_DIR"))}
			if a.Account != nil {
				a.Plan = a.Account.Plan
			}
			claude = []Agent{a}
		}
	} else if e.ClaudeFeed != nil && !e.ClaudeFeed() {
		// Kept from before the status line was taken out, or changed by
		// hand: still the last word, but nothing new will come that way.
		for i := range claude {
			if claude[i].Source == SourceStatusline {
				claude[i].Source = SourceStatuslineOff
			}
		}
	}
	rep.Agents = append(rep.Agents, claude...)
	if c, ok := readCodex(e); ok {
		rep.Agents = append(rep.Agents, c)
	}
	markReset(rep.Agents, e.now().UnixMilli())
	return rep
}

// markReset zeroes the windows whose reset time has passed.
func markReset(agents []Agent, now int64) {
	for i := range agents {
		for j := range agents[i].Windows {
			w := &agents[i].Windows[j]
			if w.ResetsAt > 0 && w.ResetsAt <= now {
				w.UsedPercent, w.Reset = 0, true
			}
		}
	}
}

// ---- helpers

func clampPercent(v float64) float64 {
	v = math.Max(0, math.Min(100, v))
	return math.Round(v*10) / 10
}

// epochMillis reads a reset time given in seconds or milliseconds: 1e10
// is past any seconds value before 2286 and before any milliseconds value
// after 1970-04 (Orca's rule).
func epochMillis(v float64) int64 {
	if v > 1e10 {
		return int64(v)
	}
	return int64(v * 1000)
}

// parseResets reads a reset time that is a number, a numeric string or an
// RFC 3339 date.
func parseResets(raw json.RawMessage) int64 {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		if n <= 0 || math.IsNaN(n) || math.IsInf(n, 0) {
			return 0
		}
		return epochMillis(n)
	}
	var s string
	if json.Unmarshal(raw, &s) != nil || strings.TrimSpace(s) == "" {
		return 0
	}
	if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil && f > 0 {
		return epochMillis(f)
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UnixMilli()
	}
	return 0
}
