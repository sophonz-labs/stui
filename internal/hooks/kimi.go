package hooks

// Kimi Code CLI keeps its hooks in ~/.kimi-code/config.toml as an array of
// tables, one rule each (docs/en/customization/hooks.md):
//
//	[[hooks]]
//	event = "Stop"
//	command = "stui hook kimi --hv 1"
//	timeout = 10
//
// **A rule takes exactly four fields** (event, matcher, command, timeout):
// Kimi validates them strictly and refuses to load a config with any other,
// so the CLI would not start. stui's version therefore goes inside the
// command, as Claude's does, never beside it.
//
// The file is the user's -- providers, keys, their own rules -- and stui has no
// TOML library (go.mod), so it is edited as lines, as Codex's trust is
// (codexhooks.go): stui's rules are the [[hooks]] tables whose command is
// `stui hook kimi`, and nothing else in the file changes, to the byte.

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// KimiEvents are the hooks stui listens to in Kimi Code. Only events Kimi
// documents go in (an unknown one fails the load, as an unknown field does).
//
//   - TurnStarted is here because Kimi starts turns without a prompt (its
//     origin is "task" or "system_trigger" as well as "user"). Kimi awaits
//     UserPromptSubmit while it submits a prompt and fires TurnStarted after,
//     so stui reads TurnStarted as a tool hook: it starts a turn only when none
//     is running, and never twice.
//   - Interrupt comes instead of Stop when the person presses Esc.
//   - PermissionResult says the approval is over, so the pane is working
//     again (Claude has no such event; stui reads its screen for it).
//   - Not Notification: Kimi's is a background task changing status
//     ("task.completed"), and stui reads a notification it does not know as
//     someone being wanted.
//   - Not SessionHeartbeat: once a minute, for what tmux already says.
var KimiEvents = []string{
	"SessionStart", "UserPromptSubmit", "TurnStarted", "Stop", "StopFailure", "Interrupt",
	"PermissionRequest", "PermissionResult", "SubagentStart", "SubagentStop",
	"PreToolUse", "PostToolUse", "PostToolUseFailure", "SessionEnd",
}

// KimiVersion is the version of the set stui installs into Kimi Code, in each
// command (`stui hook kimi --hv 1`).
const KimiVersion = 1

var kimiSets = map[int][]string{1: KimiEvents}

// kimiTimeout is seconds (Kimi: 1-600, default 30). stui hook gives up on the
// agent after 300ms; this is only a ceiling.
const kimiTimeout = 10

// kimiDir is $KIMI_CODE_HOME (DefaultEnv), else ~/.kimi-code.
func (e Env) kimiDir() string {
	if e.KimiDir != "" {
		return e.KimiDir
	}
	return filepath.Join(e.Home, ".kimi-code")
}

// KimiConfig is the file stui writes.
func (e Env) KimiConfig() string { return filepath.Join(e.kimiDir(), "config.toml") }

func (e Env) kimiCommand() string {
	return quoteCommand(e.St) + " hook kimi --hv " + strconv.Itoa(KimiVersion)
}

var (
	kimiHeader = regexp.MustCompile(`^\s*\[\[\s*hooks\s*\]\]\s*(?:#.*)?$`)
	// hooks written another way, which a [[hooks]] beside would break.
	kimiOther = regexp.MustCompile(`^\s*(?:hooks\s*=|\[\s*hooks\s*\])`)
	kimiKey   = regexp.MustCompile(`^\s*(event|command|matcher|timeout)\s*=\s*(.*?)\s*$`)
)

// tomlString reads a TOML basic or literal string value, with a comment
// after it allowed; false when it is not one.
func tomlString(v string) (string, bool) {
	switch {
	case strings.HasPrefix(v, `"`):
		for i := 1; i < len(v); i++ {
			switch v[i] {
			case '\\':
				i++
			case '"':
				s, err := strconv.Unquote(v[:i+1])
				rest := strings.TrimSpace(v[i+1:])
				return s, err == nil && (rest == "" || strings.HasPrefix(rest, "#"))
			}
		}
	case strings.HasPrefix(v, "'"):
		if j := strings.IndexByte(v[1:], '\''); j >= 0 {
			rest := strings.TrimSpace(v[j+2:])
			return v[1 : j+1], rest == "" || strings.HasPrefix(rest, "#")
		}
	}
	return "", false
}

// kimiRule is one [[hooks]] table stui wrote: its lines in the file, and what
// it says.
type kimiRule struct {
	start, end   int // lines[start:end]
	event        string
	command      string
	version      int
	timeout, mat string
}

// kimiRules are stui's [[hooks]] tables in lines. A table runs from its header
// to the next header; a blank line after stui's own goes with it.
func kimiRules(lines []string) []kimiRule {
	var out []kimiRule
	for i := 0; i < len(lines); i++ {
		if !kimiHeader.MatchString(lines[i]) {
			continue
		}
		r := kimiRule{start: i}
		j := i + 1
		for ; j < len(lines) && !isTableHeader(lines[j]); j++ {
			m := kimiKey.FindStringSubmatch(lines[j])
			if m == nil {
				continue
			}
			switch m[1] {
			case "event":
				r.event, _ = tomlString(m[2])
			case "command":
				r.command, _ = tomlString(m[2])
			case "timeout":
				r.timeout = m[2]
			case "matcher":
				r.mat = m[2]
			}
		}
		ok, v := hookVersion(r.command)
		if !ok || !strings.Contains(r.command, " hook kimi") {
			continue
		}
		r.version = v
		// What follows stui's last key -- blank lines, a comment above the
		// user's next table -- stays, but for one blank line stui put there.
		end := i + 1
		for k := i + 1; k < j; k++ {
			if kimiKey.MatchString(lines[k]) {
				end = k + 1
			}
		}
		if end < len(lines) && strings.TrimSpace(lines[end]) == "" {
			end++
		}
		r.end = end
		out = append(out, r)
		i = j - 1
	}
	return out
}

func kimiBlock(event, command string) string {
	return "[[hooks]]\nevent = " + strconv.Quote(event) + "\ncommand = " + strconv.Quote(command) +
		"\ntimeout = " + strconv.Itoa(kimiTimeout) + "\n"
}

// kimiWant is stui's rules as this stui writes them, in order.
func (e Env) kimiWant() string {
	var b strings.Builder
	for i, ev := range KimiEvents {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(kimiBlock(ev, e.kimiCommand()))
	}
	return b.String()
}

func readKimi(e Env) (string, error) {
	b, err := os.ReadFile(e.KimiConfig())
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}

// withoutKimi is text with stui's rules taken out.
func withoutKimi(text string) (string, bool) {
	lines := strings.Split(text, "\n")
	rules := kimiRules(lines)
	if len(rules) == 0 {
		return text, false
	}
	var out []string
	at := 0
	for _, r := range rules {
		out = append(out, lines[at:r.start]...)
		at = r.end
	}
	out = append(out, lines[at:]...)
	return strings.Join(out, "\n"), true
}

// kimiUpToDate is whether text's stui rules are exactly the ones this stui
// writes: each event once, this command, this timeout.
func (e Env) kimiUpToDate(text string) bool {
	rules := kimiRules(strings.Split(text, "\n"))
	if len(rules) != len(KimiEvents) {
		return false
	}
	seen := map[string]bool{}
	for _, r := range rules {
		if seen[r.event] || r.command != e.kimiCommand() || r.timeout != strconv.Itoa(kimiTimeout) || r.mat != "" {
			return false
		}
		seen[r.event] = true
	}
	for _, ev := range KimiEvents {
		if !seen[ev] {
			return false
		}
	}
	return true
}

// InstallKimi puts stui's rules at the end of Kimi's config.toml, replacing
// any stui wrote before. The user's rules and everything else stay as they
// were; a file whose stui rules are already right is not touched.
func InstallKimi(e Env) (Result, error) {
	path := e.KimiConfig()
	res := Result{File: path}
	text, err := readKimi(e)
	if err != nil {
		return res, err
	}
	for _, l := range strings.Split(text, "\n") {
		if kimiOther.MatchString(l) {
			res.Note = "its hooks are not written as [[hooks]] tables; left alone"
			return res, nil
		}
	}
	if e.kimiUpToDate(text) {
		return res, nil
	}
	out, _ := withoutKimi(text)
	out = strings.TrimRight(out, "\n")
	if out != "" {
		out += "\n\n"
	}
	out += e.kimiWant()
	if text != "" {
		b, err := backup(e, path, []byte(text))
		if err != nil {
			return res, err
		}
		res.Backup = b
	}
	res.Changed = true
	return res, writeKeepingMode(path, []byte(out))
}

// UninstallKimi takes stui's rules out, and nothing else.
func UninstallKimi(e Env) (Result, error) {
	path := e.KimiConfig()
	res := Result{File: path}
	text, err := readKimi(e)
	if err != nil || text == "" {
		return res, err
	}
	out, changed := withoutKimi(text)
	if !changed {
		return res, nil
	}
	if strings.HasSuffix(text, "\n") {
		out = strings.TrimRight(out, "\n") + "\n"
	}
	b, err := backup(e, path, []byte(text))
	if err != nil {
		return res, err
	}
	res.Backup, res.Changed = b, true
	return res, writeKeepingMode(path, []byte(out))
}

// KimiInfo reads stui's rules in Kimi's config: as old as the oldest of them,
// partial when an event stui listens to has none.
func KimiInfo(e Env) SetInfo {
	info := SetInfo{State: "none", Required: KimiVersion}
	text, err := readKimi(e)
	if err != nil {
		return info
	}
	rules := kimiRules(strings.Split(text, "\n"))
	if len(rules) == 0 {
		return info
	}
	present := map[string]bool{}
	for _, r := range rules {
		present[r.event] = true
		if info.Version == 0 || r.version < info.Version {
			info.Version = r.version
		}
	}
	for _, ev := range KimiEvents {
		if !present[ev] {
			info.Missing = append(info.Missing, ev)
		}
	}
	switch {
	case info.Version < KimiVersion:
		info.State = "outdated"
	case len(info.Missing) > 0:
		info.State = "partial"
	default:
		info.State = "installed"
	}
	return info
}
