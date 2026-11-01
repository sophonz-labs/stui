package hooks

import (
	"encoding/json"
	"path/filepath"
	"strconv"
)

// GeminiEvents are the hooks stui listens to in Gemini CLI. They are the ones
// agent watch already reads (internal/watch/engine.go): BeforeAgent starts a
// turn, AfterAgent ends it, Notification carries a tool-permission ask, and
// the tool hooks say the main agent is working when a turn began without a
// BeforeAgent. Observed only -- stui hook prints nothing, so every decision
// stays with Gemini and the terminal.
//
// Only events Gemini CLI documents go in (docs/hooks/reference.md): an
// unknown name in settings.json is not specified to be ignored, and this
// file is the user's.
var GeminiEvents = []string{
	"SessionStart", "BeforeAgent", "AfterAgent", "Notification", "SessionEnd",
	"BeforeTool", "AfterTool",
}

// GeminiVersion is the version of the set stui installs into Gemini CLI, written
// into the command itself (`stui hook gemini --hv 1`), as Claude's is.
const GeminiVersion = 1

var geminiSets = map[int][]string{1: GeminiEvents}

// geminiDir is ~/.gemini; Env.GeminiDir overrides it in tests. Gemini CLI
// documents no environment variable for it, so stui does not invent one.
func (e Env) geminiDir() string {
	if e.GeminiDir != "" {
		return e.GeminiDir
	}
	return filepath.Join(e.Home, ".gemini")
}

// GeminiSettings is the file stui writes.
func (e Env) GeminiSettings() string { return filepath.Join(e.geminiDir(), "settings.json") }

func (e Env) geminiCommand() string {
	return quoteCommand(e.St) + " hook gemini --hv " + strconv.Itoa(GeminiVersion)
}

// geminiSet is Gemini CLI's settings.json as a settingsSet. Its shape is
// Claude Code's, and its payload carries the same session_id,
// transcript_path, cwd and hook_event_name -- so stui's reducer needs nothing
// new. The one difference that matters: **timeout is milliseconds here**
// (Gemini's default is 60000), where Claude Code's is seconds. Ten of the
// wrong unit would kill the hook before it connected.
func geminiSet(e Env) settingsSet {
	return settingsSet{
		path: e.GeminiSettings(), events: GeminiEvents, command: e.geminiCommand(),
		timeout: json.Number("10000"), version: GeminiVersion, sets: geminiSets,
	}
}

// InstallGemini puts stui hook gemini into ~/.gemini/settings.json, leaving
// everything that is not stui's as it was.
func InstallGemini(e Env) (Result, error) { return geminiSet(e).install(e) }

// UninstallGemini takes it out again.
func UninstallGemini(e Env) (Result, error) { return geminiSet(e).uninstall(e) }

// GeminiInfo reads the set in Gemini's settings.
func GeminiInfo(e Env) (SetInfo, error) { return geminiSet(e).info(e) }

// GeminiStatus lists which of GeminiEvents run stui hook gemini.
func GeminiStatus(e Env) (installed []string, err error) { return geminiSet(e).status(e) }
