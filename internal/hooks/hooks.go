// Package hooks puts `stui hook` into the coding agents' own configuration,
// and takes it out again: Claude Code's ~/.claude/settings.json, and Codex's
// ~/.codex/hooks.json with its trust and notify in ~/.codex/config.toml.
// When someone runs stui hooks install (or presses Install in an app), and -- once they have -- to bring a set an
// older stui wrote up to date (auto.go).
package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/sophonz-labs/stui/internal/home"
)

// ClaudeEvents are the hooks stui listens to. PermissionRequest and
// PostToolUse are observed only: stui hook prints nothing, which leaves every
// decision to Claude and the terminal. PreToolUse is too, except for a
// worker's edit outside the paths it owns and a shell command that would
// stop the tmux server the agent runs in (internal/hookclient), which it
// refuses. The tool hooks say the main
// agent is working when a turn started without UserPromptSubmit (a Stop
// hook that blocked, a background task's result).
var ClaudeEvents = []string{
	"UserPromptSubmit", "Stop", "StopFailure", "PermissionRequest", "Notification",
	"SubagentStart", "SubagentStop", "SessionStart", "SessionEnd",
	"TeammateIdle", "TaskCreated", "TaskCompleted", "PreToolUse", "PostToolUse",
}

// ClaudeVersion is the version of the set stui installs into Claude Code. It
// goes up whenever ClaudeEvents or the command changes, and is written into
// the command itself (`stui hook claude --hv 2`): the command is the one part
// of an entry Claude Code keeps exactly as written when it rewrites the file
// (its /hooks screen does), and a key of stui's own beside it might not be.
//
// The stui before versions read `--hv 2` as not its own; an older stui runs
// the command all the same (stui hook ignores what follows the agent's name).
const ClaudeVersion = 2

// claudeSets is every version's events, for telling which version an
// unmarked set -- written before versions were -- is.
var claudeSets = map[int][]string{
	1: {"UserPromptSubmit", "Stop", "StopFailure", "PermissionRequest", "Notification",
		"SubagentStart", "SubagentStop", "SessionStart", "SessionEnd",
		"TeammateIdle", "TaskCreated", "TaskCompleted"},
	2: ClaudeEvents,
}

// CodexVersion is the version of stui's set in Codex. 1 was the notify line
// alone, which never carried a version and is read as 1; 2 adds hooks.json
// (codexhooks.go), whose commands carry it (`stui hook codex --hv 2`). 3
// writes the command so PowerShell can run it (codexQuote): on Windows the
// v2 command exited 1 on every hook.
const CodexVersion = 3

// Env is where the files are; tests point it at a temporary home.
type Env struct {
	Home string
	// CLAUDE_CONFIG_DIR and CODEX_HOME, when set.
	ClaudeDir, CodexDir string
	// Where Gemini CLI keeps its settings, when not ~/.gemini.
	GeminiDir string
	// opencode's configuration directory, when not ~/.config/opencode.
	OpencodeDir string
	// $PI_CODING_AGENT_DIR, when set: Pi's configuration instead of
	// ~/.pi/agent.
	PiDir string
	// $KIMI_CODE_HOME and $QWEN_HOME, when set: where Kimi Code and Qwen
	// Code keep their configuration instead of ~/.kimi-code and ~/.qwen.
	KimiDir, QwenDir string
	// The command that runs stui, as it goes into the files.
	St string
	// Whether Codex is on this machine.
	CodexInstalled bool
	Now            func() time.Time
	// Where stui keeps what it was told about hooks (hooks.json): ~/.stui.
	StateDir string
}

// DefaultEnv reads the real environment.
func DefaultEnv() Env {
	h, _ := os.UserHomeDir()
	e := Env{Home: h, ClaudeDir: os.Getenv("CLAUDE_CONFIG_DIR"), CodexDir: os.Getenv("CODEX_HOME"), St: StCommand(), Now: time.Now, StateDir: home.Dir()}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" && filepath.IsAbs(x) {
		e.OpencodeDir = filepath.Join(x, "opencode")
	}
	e.PiDir = homeVar(os.Getenv("PI_CODING_AGENT_DIR"), h)
	e.KimiDir = homeVar(os.Getenv("KIMI_CODE_HOME"), h)
	e.QwenDir = homeVar(os.Getenv("QWEN_HOME"), h)
	if _, err := exec.LookPath("codex"); err == nil {
		e.CodexInstalled = true
	} else if fi, err := os.Stat(e.codexDir()); err == nil && fi.IsDir() {
		e.CodexInstalled = true
	}
	return e
}

// homeVar is an agent's home directory variable as a path stui can use: a
// leading ~ is the home (Qwen expands it), and a relative one is the agent's
// own working directory's, which stui does not know, so it is not used.
func homeVar(v, home string) string {
	if v == "~" || strings.HasPrefix(v, "~/") {
		v = filepath.Join(home, v[1:])
	}
	if !filepath.IsAbs(v) {
		return ""
	}
	return filepath.Clean(v)
}

func (e Env) claudeDir() string {
	if e.ClaudeDir != "" {
		return e.ClaudeDir
	}
	return filepath.Join(e.Home, ".claude")
}

func (e Env) codexDir() string {
	if e.CodexDir != "" {
		return e.CodexDir
	}
	return filepath.Join(e.Home, ".codex")
}

func (e Env) ClaudeSettings() string { return filepath.Join(e.claudeDir(), "settings.json") }
func (e Env) CodexConfig() string    { return filepath.Join(e.codexDir(), "config.toml") }

// StCommand is how the agents should run stui: the name on PATH, as a path,
// when it is this same binary -- a Homebrew or install-script link survives
// an upgrade, the Cellar path behind it does not -- and otherwise this
// executable. A development build (not installed: no STUI_SELF) is not
// written into hooks when an installed stui is there: hooks written for a
// worktree's stui exit 127 once the worktree is gone.
func StCommand() string {
	self, _ := os.Executable()
	onPath := ""
	if p, err := exec.LookPath("stui"); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			onPath = abs
			a, _ := filepath.EvalSymlinks(abs)
			b, _ := filepath.EvalSymlinks(self)
			if a != "" && a == b {
				return abs
			}
		}
	}
	if os.Getenv("STUI_SELF") == "" {
		if h, err := os.UserHomeDir(); err == nil {
			local := filepath.Join(h, ".local", "bin", "stui"+exeSuffix())
			if fi, err := os.Stat(local); err == nil && fi.Mode().IsRegular() {
				return local
			}
		}
		if onPath != "" {
			return onPath
		}
	}
	if self == "" {
		return "stui"
	}
	return self
}

func exeSuffix() string {
	if hostOS == "windows" {
		return ".exe"
	}
	return ""
}

// hostOS is runtime.GOOS; tests set it to write another system's commands.
var hostOS = runtime.GOOS

func quoteCommand(path string) string {
	if strings.ContainsAny(path, " \t'\"$`\\") {
		if hostOS == "windows" {
			return `"` + path + `"`
		}
		return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
	}
	return path
}

// ours matches any command stui hooks install wrote, whatever stui's path was,
// and the set's version after it when there is one.
var ours = regexp.MustCompile(`(^|[/\\'"])stui(\.exe)?['"]? hook (claude|codex|gemini|kimi|qwen)(?: --hv ([0-9]{1,4}))?$`)

// hookVersion is whether command is one stui wrote, and the version it
// carries; 0 when it carries none.
func hookVersion(command string) (bool, int) {
	m := ours.FindStringSubmatch(strings.TrimSpace(command))
	if m == nil {
		return false, 0
	}
	v, _ := strconv.Atoi(m[4])
	return true, v
}

func (e Env) claudeCommand() string {
	return quoteCommand(e.St) + " hook claude --hv " + strconv.Itoa(ClaudeVersion)
}

// claudeSet is Claude Code's settings.json as a settingsSet. Its timeout is
// in seconds.
func claudeSet(e Env) settingsSet {
	return settingsSet{
		path: e.ClaudeSettings(), events: ClaudeEvents, command: e.claudeCommand(),
		timeout: json.Number("10"), version: ClaudeVersion, sets: claudeSets,
	}
}

// Result says what one file went through.
type Result struct {
	File    string
	Changed bool
	Backup  string
	Note    string
}

func backup(e Env, path string, b []byte) (string, error) {
	now := time.Now
	if e.Now != nil {
		now = e.Now
	}
	name := path + ".stui-backup-" + now().Format("20060102-150405")
	return name, os.WriteFile(name, b, 0o600)
}

// writeKeepingMode replaces path's content, with its mode as it was.
func writeKeepingMode(path string, b []byte) error {
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".stui-tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ---- Claude Code

func loadSettings(path string) (doc *object, raw []byte, err error) {
	raw, err = os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &object{}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return &object{}, raw, nil
	}
	doc, err = parseDoc(raw)
	if err != nil {
		return nil, raw, fmt.Errorf("%s is not valid JSON (%v); it was left alone", path, err)
	}
	return doc, raw, nil
}

func saveSettings(e Env, path string, doc *object, raw []byte) (Result, error) {
	indent, nl := "  ", true
	if raw != nil {
		indent = detectIndent(raw)
		nl = bytes.HasSuffix(raw, []byte("\n"))
	}
	out := writeDoc(doc, indent, nl)
	res := Result{File: path}
	if raw != nil && bytes.Equal(out, raw) {
		return res, nil
	}
	if raw != nil {
		b, err := backup(e, path, raw)
		if err != nil {
			return res, err
		}
		res.Backup = b
	}
	res.Changed = true
	return res, writeKeepingMode(path, out)
}

// claudeHooks is settings.hooks, made if it is missing and create is set.
// claudeHooks is the "hooks" object in Claude's settings.
func claudeHooks(doc *object, create bool) (*object, error) { return hooksObject(doc, create) }

// ourVersion is whether h is an entry stui wrote, and the version it carries.
func ourVersion(h any) (bool, int) {
	o, ok := h.(*object)
	if !ok {
		return false, 0
	}
	c, _ := o.get("command")
	s, _ := c.(string)
	return hookVersion(s)
}

func isOurs(h any) bool {
	ok, _ := ourVersion(h)
	return ok
}

// InstallClaude makes sure every event in ClaudeEvents runs stui hook claude,
// once, at ClaudeVersion. An entry stui wrote before -- another path to stui, an
// older version -- is updated in place; a second one for the same event, or
// one for an event stui no longer listens to, is taken out. Everything that is
// not stui's stays as it was.
func InstallClaude(e Env) (Result, error) { return claudeSet(e).install(e) }

// UninstallClaude removes every hook stui wrote, and any group, event or the
// hooks object itself that only held stui's.
func UninstallClaude(e Env) (Result, error) { return claudeSet(e).uninstall(e) }

// SetInfo is what stui's hooks for one agent are, against what this stui wants.
type SetInfo struct {
	// installed, outdated (an older stui wrote them), partial (this version,
	// with events taken out since) or none.
	State string `json:"state"`
	// The version the set is at; 0 for none.
	Version int `json:"version,omitempty"`
	// The version this stui installs.
	Required int `json:"required"`
	// Events stui listens to that do not run it, for Claude.
	Missing []string `json:"missing,omitempty"`
}

// ClaudeInfo reads the set in Claude's settings.
//
// A set carries its version in each command; one from before that is the
// newest version whose events it all has. The lowest of its entries counts:
// a set is as old as the oldest thing in it.
func ClaudeInfo(e Env) (SetInfo, error) { return claudeSet(e).info(e) }

// ClaudeStatus lists which of ClaudeEvents run stui hook claude.
func ClaudeStatus(e Env) (installed []string, err error) { return claudeSet(e).status(e) }

// ---- Codex

var tomlNotify = regexp.MustCompile(`^\s*notify\s*=`)

func codexLine(e Env) string {
	return "notify = [" + strconv.Quote(e.St) + ", \"hook\", \"codex\"]"
}

var codexOurs = regexp.MustCompile(`stui(?:\.exe)?", "hook", "codex"(?:, "--hv", "([0-9]{1,4})")?\]`)

func isOurCodexLine(line string) bool {
	ok, _ := codexLineVersion(line)
	return ok
}

// codexLineVersion is whether line is the notify stui wrote, and the version
// it carries; 1 when it carries none.
func codexLineVersion(line string) (bool, int) {
	if !tomlNotify.MatchString(line) {
		return false, 0
	}
	m := codexOurs.FindStringSubmatch(line)
	if m == nil {
		return false, 0
	}
	v, _ := strconv.Atoi(m[1])
	if v == 0 {
		v = 1
	}
	return true, v
}

// topLevelNotify is the index of the top-level notify line, or -1. Top
// level is everything before the first table header.
func topLevelNotify(lines []string) int {
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") {
			return -1
		}
		if tomlNotify.MatchString(l) {
			return i
		}
	}
	return -1
}

// installCodexNotify adds notify = ["stui", "hook", "codex"] when Codex has no
// notify of its own. One it already has is never replaced.
func installCodexNotify(e Env) (Result, error) {
	path := e.CodexConfig()
	res := Result{File: path}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return res, err
	}
	text := string(raw)
	lines := strings.Split(text, "\n")
	if i := topLevelNotify(lines); i >= 0 {
		if !isOurCodexLine(lines[i]) {
			res.Note = "Codex already has a notify program; left alone"
			return res, nil
		}
		if lines[i] == codexLine(e) {
			return res, nil
		}
		lines[i] = codexLine(e)
	} else {
		lines = append([]string{codexLine(e)}, lines...)
		if text == "" {
			lines = []string{codexLine(e), ""}
		}
	}
	out := []byte(strings.Join(lines, "\n"))
	if raw != nil {
		b, err := backup(e, path, raw)
		if err != nil {
			return res, err
		}
		res.Backup = b
	}
	res.Changed = true
	return res, writeKeepingMode(path, out)
}

// InstallCodex puts stui's hooks into Codex's hooks.json, trusts them in
// config.toml, and keeps the notify line for a Codex from before hooks.
// What it reports is hooks.json; a note says when notify was left alone.
func InstallCodex(e Env) (Result, error) {
	if !e.CodexInstalled {
		return Result{File: e.CodexHooksFile(), Note: "Codex is not installed"}, nil
	}
	res, entries, err := installCodexHooks(e)
	if err != nil {
		return res, err
	}
	p := ReadPrefs(e)
	trust, keys, err := writeCodexTrust(e, entries, p.CodexTrust)
	if err != nil {
		return res, err
	}
	p = ReadPrefs(e)
	p.CodexTrust = keys
	if err := writePrefs(e, p); err != nil {
		return res, err
	}
	notify, err := installCodexNotify(e)
	if err != nil {
		return res, err
	}
	res.Changed = res.Changed || trust.Changed || notify.Changed
	res.Note = notify.Note
	return res, nil
}

// UninstallCodex takes out stui's hooks, the trust stui wrote for them, and
// stui's notify line; nothing else.
func UninstallCodex(e Env) (Result, error) {
	res, err := removeCodexHooks(e)
	if err != nil {
		return res, err
	}
	p := ReadPrefs(e)
	if len(p.CodexTrust) > 0 {
		trust, _, err := writeCodexTrust(e, nil, p.CodexTrust)
		if err != nil {
			return res, err
		}
		p = ReadPrefs(e)
		p.CodexTrust = nil
		if err := writePrefs(e, p); err != nil {
			return res, err
		}
		res.Changed = res.Changed || trust.Changed
	}
	notify, err := uninstallCodexNotify(e)
	res.Changed = res.Changed || notify.Changed
	return res, err
}

// uninstallCodexNotify removes the notify line stui wrote, and only that.
func uninstallCodexNotify(e Env) (Result, error) {
	path := e.CodexConfig()
	res := Result{File: path}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return res, nil
	}
	if err != nil {
		return res, err
	}
	lines := strings.Split(string(raw), "\n")
	i := topLevelNotify(lines)
	if i < 0 || !isOurCodexLine(lines[i]) {
		return res, nil
	}
	lines = append(lines[:i], lines[i+1:]...)
	b, err := backup(e, path, raw)
	if err != nil {
		return res, err
	}
	res.Backup, res.Changed = b, true
	return res, writeKeepingMode(path, []byte(strings.Join(lines, "\n")))
}

// CodexInfo reads stui's set in Codex: hooks.json first; the notify line
// alone is the set of version 1.
func CodexInfo(e Env) SetInfo {
	info := SetInfo{State: "none", Required: CodexVersion}
	if present, v := codexHooksInfo(e); len(present) > 0 {
		info.Version = v
		for _, ev := range CodexEvents {
			if !present[ev] {
				info.Missing = append(info.Missing, ev)
			}
		}
		switch {
		case v < CodexVersion:
			info.State = "outdated"
		case len(info.Missing) > 0:
			info.State = "partial"
		default:
			info.State = "installed"
		}
		return info
	}
	raw, err := os.ReadFile(e.CodexConfig())
	if err != nil {
		return info
	}
	lines := strings.Split(string(raw), "\n")
	i := topLevelNotify(lines)
	if i < 0 {
		return info
	}
	ok, v := codexLineVersion(lines[i])
	if !ok {
		return info
	}
	info.Version = v
	info.State = "installed"
	if v < CodexVersion {
		info.State = "outdated"
	}
	return info
}

// CodexStatus is whether Codex runs stui: its hooks, or its notify program.
func CodexStatus(e Env) bool {
	if present, _ := codexHooksInfo(e); len(present) > 0 {
		return true
	}
	raw, err := os.ReadFile(e.CodexConfig())
	if err != nil {
		return false
	}
	lines := strings.Split(string(raw), "\n")
	i := topLevelNotify(lines)
	return i >= 0 && isOurCodexLine(lines[i])
}
