package hooks

import (
	"encoding/json"
	"path/filepath"
	"strconv"
)

// QwenEvents are the hooks stui listens to in Qwen Code
// (docs/users/features/hooks.md). Its hooks are Claude Code's in shape,
// names and payload (session_id, transcript_path, cwd, hook_event_name;
// agent_id inside a subagent; Notification's permission_prompt and
// idle_prompt), so stui's reducer reads them as Claude's. Observed only: stui
// hook prints nothing, and Qwen reads an exit 0 with nothing printed as no
// decision.
var QwenEvents = []string{
	"SessionStart", "UserPromptSubmit", "Stop", "StopFailure", "PermissionRequest", "Notification",
	"SubagentStart", "SubagentStop", "PreToolUse", "PostToolUse", "PostToolUseFailure", "SessionEnd",
}

// QwenVersion is the version of the set stui installs into Qwen Code, in the
// command (`stui hook qwen --hv 1`).
const QwenVersion = 1

var qwenSets = map[int][]string{1: QwenEvents}

// qwenDir is $QWEN_HOME (DefaultEnv), else ~/.qwen: Qwen's settings
// documentation names ~/.qwen/settings.json as the user settings file
// (docs/users/configuration/settings.md). Project files (.qwen/settings.json)
// load only in a trusted folder; stui writes the user's.
func (e Env) qwenDir() string {
	if e.QwenDir != "" {
		return e.QwenDir
	}
	return filepath.Join(e.Home, ".qwen")
}

// QwenSettings is the file stui writes.
func (e Env) QwenSettings() string { return filepath.Join(e.qwenDir(), "settings.json") }

func (e Env) qwenCommand() string {
	return quoteCommand(e.St) + " hook qwen --hv " + strconv.Itoa(QwenVersion)
}

// qwenSet is Qwen's settings.json as a settingsSet: Claude Code's shape.
//
// **Its timeout is seconds, but 1000 or more is read as milliseconds** (a
// command hook used to take milliseconds, and old settings keep working).
// So stui writes 10, not Gemini's 10000: that would also be ten seconds here,
// by the exception, and the exception is the kind of thing that goes.
//
// A top-level "disableAllHooks": true turns every hook off, stui's too; stui
// does not change it (it is the user's) and SetInfo does not say it yet.
func qwenSet(e Env) settingsSet {
	return settingsSet{
		path: e.QwenSettings(), events: QwenEvents, command: e.qwenCommand(),
		timeout: json.Number("10"), version: QwenVersion, sets: qwenSets,
	}
}

// InstallQwen puts stui hook qwen into ~/.qwen/settings.json, leaving
// everything that is not stui's as it was.
func InstallQwen(e Env) (Result, error) { return qwenSet(e).install(e) }

// UninstallQwen takes it out again.
func UninstallQwen(e Env) (Result, error) { return qwenSet(e).uninstall(e) }

// QwenInfo reads the set in Qwen's settings.
func QwenInfo(e Env) (SetInfo, error) { return qwenSet(e).info(e) }
