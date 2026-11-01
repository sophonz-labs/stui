package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sophonz-labs/stui/internal/usage"
)

const hudSettings = `{
  "model": "opus",
  "statusLine": {
    "type": "command",
    "command": "node /Users/me/.claude/hud/omc-hud.mjs",
    "padding": 0
  },
  "zeta": 1.50
}
`

func TestStatuslineKeepsAndRestoresTheUsersLine(t *testing.T) {
	e := testEnv(t)
	e.StateDir = filepath.Join(e.Home, ".stui")
	path := e.ClaudeSettings()
	write(t, path, hudSettings)

	if StatuslineOn(e) {
		t.Fatal("on before install")
	}
	res, err := InstallStatusline(e)
	if err != nil || !res.Changed || res.Backup == "" || !strings.Contains(res.Note, "omc-hud") {
		t.Fatalf("install %+v %v", res, err)
	}
	if read(t, res.Backup) != hudSettings {
		t.Error("backup is not the file as it was")
	}
	got := read(t, path)
	want := `{
  "model": "opus",
  "statusLine": {
    "type": "command",
    "command": "/usr/local/bin/stui statusline",
    "padding": 0
  },
  "zeta": 1.50
}
`
	if got != want {
		t.Errorf("settings:\n%s", got)
	}
	if !StatuslineOn(e) {
		t.Error("not on after install")
	}
	c := usage.ReadChains(e.StateDir)[e.ClaudeHome()]
	if c.Command != "node /Users/me/.claude/hud/omc-hud.mjs" || !strings.Contains(string(c.Previous), "omc-hud") {
		t.Errorf("chain %+v", c)
	}

	// Again: nothing changes, and what was kept is not overwritten by stui's own line.
	res, err = InstallStatusline(e)
	if err != nil || res.Changed {
		t.Errorf("reinstall %+v %v", res, err)
	}
	if usage.ReadChains(e.StateDir)[e.ClaudeHome()].Command != c.Command {
		t.Error("reinstall lost the user's line")
	}

	// stui moved: updated in place, the user's line still behind it.
	e.St = "/opt/homebrew/bin/stui"
	if res, err := InstallStatusline(e); err != nil || !res.Changed {
		t.Errorf("moved %+v %v", res, err)
	}
	if !strings.Contains(read(t, path), `"/opt/homebrew/bin/stui statusline"`) {
		t.Error("not moved")
	}

	res, err = UninstallStatusline(e)
	if err != nil || !res.Changed || res.Note != "put your status line back" {
		t.Fatalf("uninstall %+v %v", res, err)
	}
	if read(t, path) != hudSettings {
		t.Errorf("not restored:\n%s", read(t, path))
	}
	if _, err := os.Stat(usage.ChainFile(e.StateDir)); !os.IsNotExist(err) {
		t.Error("chain file left behind")
	}
}

func TestStatuslineWithNoneBefore(t *testing.T) {
	e := testEnv(t)
	e.StateDir = filepath.Join(e.Home, ".stui")
	path := e.ClaudeSettings()
	write(t, path, "{\n  \"model\": \"opus\"\n}\n")
	if _, err := InstallStatusline(e); err != nil {
		t.Fatal(err)
	}
	if c := usage.ReadChains(e.StateDir)[e.ClaudeHome()]; c.Command != "" || string(c.Previous) != "null" {
		t.Errorf("chain %+v", c)
	}
	if _, err := UninstallStatusline(e); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != "{\n  \"model\": \"opus\"\n}\n" {
		t.Errorf("after uninstall:\n%s", got)
	}
}

func TestStatuslineChangedByTheUserIsLeftAlone(t *testing.T) {
	e := testEnv(t)
	e.StateDir = filepath.Join(e.Home, ".stui")
	path := e.ClaudeSettings()
	write(t, path, hudSettings)
	InstallStatusline(e)
	mine := strings.Replace(hudSettings, "omc-hud.mjs", "other.mjs", 1)
	write(t, path, mine)
	res, err := UninstallStatusline(e)
	if err != nil || res.Changed || res.Note == "" {
		t.Errorf("uninstall %+v %v", res, err)
	}
	if read(t, path) != mine {
		t.Error("the user's own line was touched")
	}
	if StatuslineOn(e) {
		t.Error("on")
	}
	// The hooks' own install never touches the status line.
	if _, err := InstallClaude(e); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, path), "other.mjs") {
		t.Error("stui hooks install changed the status line")
	}
}
