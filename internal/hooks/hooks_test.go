package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testEnv(t *testing.T) Env {
	home := t.TempDir()
	n := 0
	return Env{
		Home: home,
		St:   "/usr/local/bin/stui",
		Now: func() time.Time {
			n++
			return time.Date(2026, 9, 28, 12, 0, n, 0, time.UTC)
		},
	}
}

func write(t *testing.T, path, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const userSettings = `{
    "model": "opus",
    "permissions": {
        "allow": ["Bash(ls:*)"],
        "defaultMode": "default"
    },
    "hooks": {
        "Stop": [
            {
                "matcher": "",
                "hooks": [
                    { "type": "command", "command": "afplay /System/Library/Sounds/Glass.aiff" }
                ]
            }
        ],
        "PreToolUse": [
            { "matcher": "Bash", "hooks": [ { "type": "command", "command": "~/bin/guard <&0" } ] }
        ]
    },
    "zeta": 1.50,
    "alpha": "<b>&</b>"
}
`

func TestInstallClaudeKeepsUserEntries(t *testing.T) {
	e := testEnv(t)
	path := e.ClaudeSettings()
	write(t, path, userSettings)

	res, err := InstallClaude(e)
	if err != nil || !res.Changed || res.Backup == "" {
		t.Fatalf("%+v %v", res, err)
	}
	if read(t, res.Backup) != userSettings {
		t.Fatal("backup is not the original")
	}
	got := read(t, path)
	// The user's own entries and key order survive, and so does the indent.
	for _, want := range []string{
		`"afplay /System/Library/Sounds/Glass.aiff"`, `"~/bin/guard <&0"`, `"zeta": 1.50`, `"alpha": "<b>&</b>"`,
		"\n    \"model\": \"opus\",",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in\n%s", want, got)
		}
	}
	if strings.Index(got, `"model"`) > strings.Index(got, `"permissions"`) || strings.Index(got, `"zeta"`) > strings.Index(got, `"alpha"`) {
		t.Fatal("key order changed")
	}
	installed, _ := ClaudeStatus(e)
	if len(installed) != len(ClaudeEvents) {
		t.Fatalf("installed %v", installed)
	}
	if n := strings.Count(got, "/usr/local/bin/stui hook claude"); n != len(ClaudeEvents) {
		t.Fatalf("%d entries", n)
	}

	// Again: nothing to do, no backup.
	res, err = InstallClaude(e)
	if err != nil || res.Changed || res.Backup != "" {
		t.Fatalf("second install %+v %v", res, err)
	}

	// A new path to stui updates the entries rather than adding more.
	e.St = "/opt/homebrew/bin/stui"
	if _, err := InstallClaude(e); err != nil {
		t.Fatal(err)
	}
	got = read(t, path)
	if strings.Contains(got, "/usr/local/bin/stui") || strings.Count(got, "/opt/homebrew/bin/stui hook claude") != len(ClaudeEvents) {
		t.Fatalf("path not updated:\n%s", got)
	}

	res, err = UninstallClaude(e)
	if err != nil || !res.Changed {
		t.Fatalf("%+v %v", res, err)
	}
	// The same document, in the same order. Inline lists are laid out the
	// way Claude Code itself writes the file (JSON.stringify, indented).
	if got, want := canonical(t, read(t, path)), canonical(t, userSettings); got != want {
		t.Fatalf("uninstall did not restore the file:\n%s\nwant\n%s", got, want)
	}
	if installed, _ := ClaudeStatus(e); len(installed) != 0 {
		t.Fatalf("still installed %v", installed)
	}
}

// canonical is the document written back with one-space indent, which
// keeps order and spelling but not layout.
func canonical(t *testing.T, s string) string {
	t.Helper()
	doc, err := parseDoc([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return string(writeDoc(doc, " ", false))
}

// A file as Claude Code writes it comes back byte for byte.
func TestClaudeStyleRoundTrip(t *testing.T) {
	e := testEnv(t)
	const claudeStyle = "{\n  \"permissions\": {\n    \"allow\": [\n      \"Bash(ls:*)\"\n    ],\n    \"deny\": []\n  },\n  \"hooks\": {\n    \"Stop\": [\n      {\n        \"hooks\": [\n          {\n            \"type\": \"command\",\n            \"command\": \"say done\"\n          }\n        ]\n      }\n    ]\n  },\n  \"feedbackSurveyState\": {\n    \"lastShownTime\": 1754000000000\n  }\n}"
	write(t, e.ClaudeSettings(), claudeStyle)
	if _, err := InstallClaude(e); err != nil {
		t.Fatal(err)
	}
	if _, err := UninstallClaude(e); err != nil {
		t.Fatal(err)
	}
	if got := read(t, e.ClaudeSettings()); got != claudeStyle {
		t.Fatalf("%q", got)
	}
}

func TestInstallClaudeFreshAndBroken(t *testing.T) {
	e := testEnv(t)
	res, err := InstallClaude(e)
	if err != nil || !res.Changed || res.Backup != "" {
		t.Fatalf("%+v %v", res, err)
	}
	if !strings.Contains(read(t, e.ClaudeSettings()), `"timeout": 10`) {
		t.Fatal("no timeout")
	}
	UninstallClaude(e)
	if got := strings.TrimSpace(read(t, e.ClaudeSettings())); got != "{}" {
		t.Fatalf("left %q", got)
	}

	write(t, e.ClaudeSettings(), "{ not json")
	if _, err := InstallClaude(e); err == nil {
		t.Fatal("broken file accepted")
	}
	if read(t, e.ClaudeSettings()) != "{ not json" {
		t.Fatal("broken file changed")
	}
}

func TestQuotedPath(t *testing.T) {
	e := testEnv(t)
	e.St = "/Users/a b/bin/stui"
	InstallClaude(e)
	if !strings.Contains(read(t, e.ClaudeSettings()), `"'/Users/a b/bin/stui' hook claude --hv 2"`) {
		t.Fatal(read(t, e.ClaudeSettings()))
	}
	if installed, _ := ClaudeStatus(e); len(installed) != len(ClaudeEvents) {
		t.Fatal("quoted command not recognised")
	}
	UninstallClaude(e)
	if installed, _ := ClaudeStatus(e); len(installed) != 0 {
		t.Fatal("quoted command not removed")
	}
}

func TestCodex(t *testing.T) {
	e := testEnv(t)
	if res, _ := InstallCodex(e); res.Changed {
		t.Fatal("installed without Codex")
	}
	e.CodexInstalled = true
	const cfg = "model = \"o4\"\n\n[mcp_servers.x]\ncommand = \"y\"\n"
	write(t, e.CodexConfig(), cfg)
	res, err := installCodexNotify(e)
	if err != nil || !res.Changed || res.Backup == "" {
		t.Fatalf("%+v %v", res, err)
	}
	got := read(t, e.CodexConfig())
	if !strings.HasPrefix(got, `notify = ["/usr/local/bin/stui", "hook", "codex"]`+"\n") || !strings.HasSuffix(got, cfg) {
		t.Fatalf("%q", got)
	}
	if !CodexStatus(e) {
		t.Fatal("status")
	}
	if res, _ := installCodexNotify(e); res.Changed {
		t.Fatal("second install changed it")
	}
	uninstallCodexNotify(e)
	if read(t, e.CodexConfig()) != cfg {
		t.Fatalf("%q", read(t, e.CodexConfig()))
	}

	// Someone else's notify is left alone, even one under a table later on;
	// the hooks go in all the same.
	const theirs = "notify = [\"notify-send\", \"codex\"]\nmodel = \"o4\"\n"
	write(t, e.CodexConfig(), theirs)
	res, _ = InstallCodex(e)
	if !res.Changed || res.Note == "" || !strings.HasPrefix(read(t, e.CodexConfig()), theirs) {
		t.Fatalf("%+v %q", res, read(t, e.CodexConfig()))
	}
	UninstallCodex(e)
	if got := read(t, e.CodexConfig()); got != theirs {
		t.Fatalf("removed someone else's notify: %q", got)
	}
	const tableNotify = "model = \"o4\"\n[tui]\nnotify = true\n"
	write(t, e.CodexConfig(), tableNotify)
	installCodexNotify(e)
	if got := read(t, e.CodexConfig()); !strings.HasPrefix(got, "notify = [") || !strings.HasSuffix(got, tableNotify) {
		t.Fatalf("%q", got)
	}

	// No file at all.
	os.Remove(e.CodexConfig())
	InstallCodex(e)
	if !CodexStatus(e) {
		t.Fatal("fresh config")
	}
}

// An install from before the tool hooks gets them on the next install, and
// only them; the one after that changes nothing.
func TestInstallClaudeUpgradesOlderInstall(t *testing.T) {
	e := testEnv(t)
	path := e.ClaudeSettings()
	var b strings.Builder
	b.WriteString("{\n  \"hooks\": {\n")
	old := []string{"UserPromptSubmit", "Stop", "StopFailure", "PermissionRequest", "Notification",
		"SubagentStart", "SubagentStop", "SessionStart", "SessionEnd", "TeammateIdle", "TaskCreated", "TaskCompleted"}
	for i, ev := range old {
		b.WriteString(`    "` + ev + `": [{ "hooks": [{ "type": "command", "command": "/usr/local/bin/stui hook claude", "timeout": 10 }] }]`)
		if i < len(old)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("  }\n}\n")
	write(t, path, b.String())

	if installed, _ := ClaudeStatus(e); len(installed) != len(old) {
		t.Fatalf("before: %v", installed)
	}
	// Partly installed: said, not fixed on its own.
	if s := States(e); s["claude"] != "partial" || s["codex"] != "none" {
		t.Fatalf("%v", s)
	}
	if p := Incomplete(e); len(p) != 1 || p[0].ID != "claude" {
		t.Fatalf("%+v", p)
	}
	res, err := InstallClaude(e)
	if err != nil || !res.Changed {
		t.Fatalf("%+v %v", res, err)
	}
	got := read(t, path)
	if n := strings.Count(got, "/usr/local/bin/stui hook claude"); n != len(ClaudeEvents) {
		t.Fatalf("%d entries:\n%s", n, got)
	}
	for _, ev := range []string{"PreToolUse", "PostToolUse"} {
		if !strings.Contains(got, `"`+ev+`"`) {
			t.Fatalf("no %s:\n%s", ev, got)
		}
	}
	if installed, _ := ClaudeStatus(e); len(installed) != len(ClaudeEvents) {
		t.Fatalf("after: %v", installed)
	}
	if res, err := InstallClaude(e); err != nil || res.Changed {
		t.Fatalf("second install %+v %v", res, err)
	}
	if s := States(e); s["claude"] != "installed" || len(Incomplete(e)) != 0 {
		t.Fatalf("%v", s)
	}
}

// One event stui listens to removed by hand, beside the user's own hook on
// it: partial, and stui hooks install puts back only stui's, once.
func TestPartialInstallRepaired(t *testing.T) {
	e := testEnv(t)
	path := e.ClaudeSettings()
	write(t, path, userSettings)
	if s := States(e); s["claude"] != "none" {
		t.Fatalf("%v", s)
	}
	if _, err := InstallClaude(e); err != nil {
		t.Fatal(err)
	}
	full := read(t, path)
	// Drop stui's PostToolUse entry only.
	doc, _, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := claudeHooks(doc, false)
	h.del("PostToolUse")
	if _, err := saveSettings(e, path, doc, []byte(full)); err != nil {
		t.Fatal(err)
	}
	if s := States(e); s["claude"] != "partial" {
		t.Fatalf("%v", s)
	}
	if res, err := InstallClaude(e); err != nil || !res.Changed {
		t.Fatalf("%+v %v", res, err)
	}
	got := read(t, path)
	if n := strings.Count(got, "/usr/local/bin/stui hook claude"); n != len(ClaudeEvents) {
		t.Fatalf("%d entries:\n%s", n, got)
	}
	if !strings.Contains(got, `"~/bin/guard <&0"`) || !strings.Contains(got, "Glass.aiff") {
		t.Fatalf("user hooks lost:\n%s", got)
	}
	if res, err := InstallClaude(e); err != nil || res.Changed {
		t.Fatalf("second install %+v %v", res, err)
	}
	if s := States(e); s["claude"] != "installed" {
		t.Fatalf("%v", s)
	}
}
