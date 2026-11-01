package hooks

// opencode has no place to register a command against its events: its
// plugins are JavaScript modules it loads from a directory on start. So stui
// writes one, ~/.config/opencode/plugins/stui-hook.js, and that module runs
// `stui hook opencode` on each event, translated into the names stui already
// reads (opencode.js says which). Nothing else of opencode's is touched: the
// directory is loaded without any configuration, and the file is stui's alone.
// Installing, removing and reading it are module.go's.

import (
	_ "embed"
	"path/filepath"
)

//go:embed opencode.js
var opencodePlugin string

// OpencodeVersion is the version of the plugin stui writes (module.version).
const OpencodeVersion = 1

// OpencodeEvents are the stui events the plugin sends (module.events).
var OpencodeEvents = []string{
	"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse",
	"PermissionRequest", "Stop", "StopFailure", "Interrupt", "SessionEnd",
}

// opencodeDir is where opencode keeps its global configuration:
// $XDG_CONFIG_HOME/opencode, else ~/.config/opencode (on Windows too).
// Env.OpencodeDir overrides it in tests.
func (e Env) opencodeDir() string {
	if e.OpencodeDir != "" {
		return e.OpencodeDir
	}
	return filepath.Join(e.Home, ".config", "opencode")
}

// OpencodePlugin is the file stui writes. The directory is "plugins", plural:
// opencode does not look in "plugin".
func (e Env) OpencodePlugin() string {
	return filepath.Join(e.opencodeDir(), "plugins", "stui-hook.js")
}

var opencodeModule = module{
	noun:    "plugin",
	path:    Env.OpencodePlugin,
	source:  opencodePlugin,
	version: OpencodeVersion,
	events:  OpencodeEvents,
}

// InstallOpencode writes stui's plugin, or brings one an older stui wrote up to
// date. A file of that name stui did not write is left alone.
func InstallOpencode(e Env) (Result, error) { return opencodeModule.install(e) }

// UninstallOpencode removes stui's plugin, and only stui's.
func UninstallOpencode(e Env) (Result, error) { return opencodeModule.uninstall(e) }

// OpencodeInfo reads stui's plugin against what this stui writes (module.info).
func OpencodeInfo(e Env) SetInfo { return opencodeModule.info(e) }
