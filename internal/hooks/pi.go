package hooks

// Pi (pi.dev) has no place to register a command against its events: its
// extensions are TypeScript or JavaScript modules it loads from a directory
// on start. So stui writes one, ~/.pi/agent/extensions/stui-hook.js, and that
// module runs `stui hook pi` on each event under Claude's names (pi.js says
// which). Pi loads a plain .js file there without configuration, and the
// file is stui's alone. Installing, removing and reading it are module.go's.

import (
	_ "embed"
	"path/filepath"
)

//go:embed pi.js
var piExtension string

// PiVersion is the version of the extension stui writes (module.version).
const PiVersion = 1

// PiEvents are the stui events the extension sends (module.events).
var PiEvents = []string{
	"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse",
	"PermissionRequest", "PermissionResult", "Stop", "StopFailure", "Interrupt", "SessionEnd",
}

// piDir is Pi's configuration directory: $PI_CODING_AGENT_DIR, else
// ~/.pi/agent.
func (e Env) piDir() string {
	if e.PiDir != "" {
		return e.PiDir
	}
	return filepath.Join(e.Home, ".pi", "agent")
}

// PiExtension is the file stui writes.
func (e Env) PiExtension() string {
	return filepath.Join(e.piDir(), "extensions", "stui-hook.js")
}

var piModule = module{
	noun:    "extension",
	path:    Env.PiExtension,
	source:  piExtension,
	version: PiVersion,
	events:  PiEvents,
}

// InstallPi writes stui's extension, or brings one an older stui wrote up to
// date. A file of that name stui did not write is left alone.
func InstallPi(e Env) (Result, error) { return piModule.install(e) }

// UninstallPi removes stui's extension, and only stui's.
func UninstallPi(e Env) (Result, error) { return piModule.uninstall(e) }

// PiInfo reads stui's extension against what this stui writes (module.info).
func PiInfo(e Env) SetInfo { return piModule.info(e) }
