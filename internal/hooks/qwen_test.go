package hooks

import (
	"os"
	"path/filepath"
	"testing"
)

const qwenUsers = `{
  "disableAllHooks": true,
  "model": { "name": "qwen3-coder-plus" },
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "^run_shell_command$",
        "hooks": [
          { "type": "command", "command": "~/bin/guard.sh", "name": "guard", "timeout": 5 }
        ]
      }
    ]
  }
}
`

func golden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The user's hook and disableAllHooks stay; stui's set goes beside them, ten
// seconds each (not Gemini's 10000: Qwen reads 1000 or more as ms).
func TestInstallQwen(t *testing.T) {
	e := testEnv(t)
	write(t, e.QwenSettings(), qwenUsers)
	res, err := InstallQwen(e)
	if err != nil || !res.Changed || res.Backup == "" {
		t.Fatalf("%+v %v", res, err)
	}
	if got, want := read(t, e.QwenSettings()), golden(t, "qwen-installed.json"); got != want {
		t.Fatalf("got:\n%s", got)
	}
	if info, err := QwenInfo(e); err != nil || info.State != "installed" || info.Version != QwenVersion {
		t.Errorf("%+v %v", info, err)
	}
	before := read(t, e.QwenSettings())
	if res, _ := InstallQwen(e); res.Changed || read(t, e.QwenSettings()) != before {
		t.Error("a second install rewrote it")
	}
	if _, err := UninstallQwen(e); err != nil {
		t.Fatal(err)
	}
	// The user's settings, as stui's JSON writer lays them out (what it does to
	// Claude's and Gemini's too): every key and value, in order.
	if got, want := read(t, e.QwenSettings()), golden(t, "qwen-uninstalled.json"); got != want {
		t.Fatalf("after uninstall:\n%s", got)
	}
	if info, _ := QwenInfo(e); info.State != "none" {
		t.Errorf("%+v", info)
	}
}

// Another agent's stui hook in Qwen's file is not Qwen's set.
func TestQwenCommand(t *testing.T) {
	e := testEnv(t)
	if c := e.qwenCommand(); c != "/usr/local/bin/stui hook qwen --hv 1" {
		t.Errorf("%q", c)
	}
	if ok, v := hookVersion(e.qwenCommand()); !ok || v != 1 {
		t.Errorf("%v %d", ok, v)
	}
}
