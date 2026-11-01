package chat

// Finding a pane's transcript. The agent's hooks name it (transcript_path),
// and that wins: recent Claude Code can name the file with an id other than
// the hook's session_id. Without it: Claude's
// <config dir>/projects/*/<session id>.jsonl, Codex's
// $CODEX_HOME/sessions/YYYY/MM/DD/rollout-<time>-<session id>.jsonl, and as a
// last resort for Codex the newest rollout started in the pane's directory
// since the conversation began there. (Orca's session-file-resolver.ts, MIT,
// Copyright (c) 2026 Lovecast Inc.)

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Source is what agent watch knows of a pane's conversation.
type Source struct {
	Agent     string
	SessionID string
	Path      string
	Cwd       string
	Since     time.Time
	// Claude config directories to look in; empty: $CLAUDE_CONFIG_DIR or
	// ~/.claude.
	ClaudeDirs []string
	// Codex's home; "": $CODEX_HOME or ~/.codex.
	CodexHome string
	// When the agent last took a prompt and last ended a turn (its hooks).
	Submitted, Stopped time.Time
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// usable is a transcript file stui will read: a regular .jsonl file.
func usable(path string) bool {
	if path == "" || !filepath.IsAbs(path) || !strings.HasSuffix(path, ".jsonl") {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

func defaultClaudeDirs() []string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return []string{d}
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{filepath.Join(h, ".claude")}
}

func codexHome(s Source) string {
	if s.CodexHome != "" {
		return s.CodexHome
	}
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return d
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(h, ".codex")
}

// Locate is the transcript of s, or "" when there is none to read.
func Locate(s Source) string {
	if usable(s.Path) {
		return s.Path
	}
	switch s.Agent {
	case "claude":
		if !safeID.MatchString(s.SessionID) {
			return ""
		}
		dirs := s.ClaudeDirs
		if len(dirs) == 0 {
			dirs = defaultClaudeDirs()
		}
		var best string
		var bestAt time.Time
		for _, d := range dirs {
			matches, _ := filepath.Glob(filepath.Join(d, "projects", "*", s.SessionID+".jsonl"))
			for _, m := range matches {
				if st, err := os.Stat(m); err == nil && st.Mode().IsRegular() && st.ModTime().After(bestAt) {
					best, bestAt = m, st.ModTime()
				}
			}
		}
		return best
	case "codex":
		return locateCodex(s)
	}
	return ""
}

// codexRollouts is every rollout under home's sessions, newest day first.
func codexRollouts(home string, keep func(name string) bool) []string {
	root := filepath.Join(home, "sessions")
	var out []string
	years, _ := os.ReadDir(root)
	for i := len(years) - 1; i >= 0; i-- {
		months, _ := os.ReadDir(filepath.Join(root, years[i].Name()))
		for j := len(months) - 1; j >= 0; j-- {
			days, _ := os.ReadDir(filepath.Join(root, years[i].Name(), months[j].Name()))
			for k := len(days) - 1; k >= 0; k-- {
				dir := filepath.Join(root, years[i].Name(), months[j].Name(), days[k].Name())
				files, _ := os.ReadDir(dir)
				for _, f := range files {
					if strings.HasPrefix(f.Name(), "rollout-") && strings.HasSuffix(f.Name(), ".jsonl") && keep(f.Name()) {
						out = append(out, filepath.Join(dir, f.Name()))
					}
				}
			}
		}
	}
	return out
}

func locateCodex(s Source) string {
	home := codexHome(s)
	if home == "" {
		return ""
	}
	if safeID.MatchString(s.SessionID) {
		if found := codexRollouts(home, func(n string) bool { return strings.HasSuffix(n, "-"+s.SessionID+".jsonl") }); len(found) > 0 {
			return found[0]
		}
	}
	if s.Cwd == "" || s.Since.IsZero() {
		return ""
	}
	// The newest rollout written to since the conversation began, that
	// began in the pane's directory.
	type cand struct {
		path string
		at   time.Time
	}
	var cands []cand
	for _, p := range codexRollouts(home, func(string) bool { return true }) {
		st, err := os.Stat(p)
		if err != nil || st.ModTime().Before(s.Since.Add(-time.Minute)) {
			continue
		}
		cands = append(cands, cand{p, st.ModTime()})
		if len(cands) >= 50 {
			break
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].at.After(cands[j].at) })
	for _, c := range cands {
		if codexCwd(c.path) == s.Cwd {
			return c.path
		}
	}
	return ""
}

// codexCwd is the directory a rollout's session_meta names.
func codexCwd(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	var line []byte
	for len(line) <= MaxRecord {
		part, isPrefix, err := r.ReadLine()
		line = append(line, part...)
		if err != nil || !isPrefix {
			break
		}
	}
	var rec struct {
		Type    string `json:"type"`
		Payload struct {
			Cwd string `json:"cwd"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &rec) != nil || rec.Type != "session_meta" {
		return ""
	}
	return rec.Payload.Cwd
}

// claudeSubagent is how this package reads a Claude subagent's own file:
// as Claude, keeping the sidechain lines the main file's reading skips.
const claudeSubagent = "claude/subagent"

// LocateSubagent is the file of subagent id of the conversation in main
// (Claude's <dir>/<session>.jsonl): <dir>/<session>/subagents/agent-<id>.jsonl,
// or "" when there is none.
func LocateSubagent(main, id string) string {
	if !safeID.MatchString(id) || !strings.HasSuffix(main, ".jsonl") {
		return ""
	}
	p := filepath.Join(strings.TrimSuffix(main, ".jsonl"), "subagents", "agent-"+id+".jsonl")
	if !usable(p) {
		return ""
	}
	return p
}
