package hooks

import (
	"reflect"
	"strings"
	"testing"
)

// A config.toml as a person writes it: settings, a provider table, and two
// rules of their own, one with a comment above it and a matcher.
const kimiUsers = `default_model = "kimi-k2"

[providers.kimi]
api_key = "sk-not-a-real-key"

[[hooks]]
event = "Notification"           # background tasks
matcher = "task\\.completed"
command = "terminal-notifier -message 'done'"
timeout = 5

# block grep
[[hooks]]
event = 'PreToolUse'
matcher = "Bash"
command = "node ~/.kimi-code/no-grep.js"
`

const kimiOurs = `[[hooks]]
event = "SessionStart"
command = "/usr/local/bin/stui hook kimi --hv 1"
timeout = 10

[[hooks]]
event = "UserPromptSubmit"
command = "/usr/local/bin/stui hook kimi --hv 1"
timeout = 10

[[hooks]]
event = "TurnStarted"
command = "/usr/local/bin/stui hook kimi --hv 1"
timeout = 10

[[hooks]]
event = "Stop"
command = "/usr/local/bin/stui hook kimi --hv 1"
timeout = 10

[[hooks]]
event = "StopFailure"
command = "/usr/local/bin/stui hook kimi --hv 1"
timeout = 10

[[hooks]]
event = "Interrupt"
command = "/usr/local/bin/stui hook kimi --hv 1"
timeout = 10

[[hooks]]
event = "PermissionRequest"
command = "/usr/local/bin/stui hook kimi --hv 1"
timeout = 10

[[hooks]]
event = "PermissionResult"
command = "/usr/local/bin/stui hook kimi --hv 1"
timeout = 10

[[hooks]]
event = "SubagentStart"
command = "/usr/local/bin/stui hook kimi --hv 1"
timeout = 10

[[hooks]]
event = "SubagentStop"
command = "/usr/local/bin/stui hook kimi --hv 1"
timeout = 10

[[hooks]]
event = "PreToolUse"
command = "/usr/local/bin/stui hook kimi --hv 1"
timeout = 10

[[hooks]]
event = "PostToolUse"
command = "/usr/local/bin/stui hook kimi --hv 1"
timeout = 10

[[hooks]]
event = "PostToolUseFailure"
command = "/usr/local/bin/stui hook kimi --hv 1"
timeout = 10

[[hooks]]
event = "SessionEnd"
command = "/usr/local/bin/stui hook kimi --hv 1"
timeout = 10
`

func TestInstallKimiKeepsTheUsersRules(t *testing.T) {
	e := testEnv(t)
	write(t, e.KimiConfig(), kimiUsers)
	if info := KimiInfo(e); info.State != "none" {
		t.Fatalf("before: %+v", info)
	}
	res, err := InstallKimi(e)
	if err != nil || !res.Changed || res.Backup == "" {
		t.Fatalf("%+v %v", res, err)
	}
	// The user's file to the byte, then stui's rules.
	if got, want := read(t, e.KimiConfig()), kimiUsers+"\n"+kimiOurs; got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if info := KimiInfo(e); info.State != "installed" || info.Version != 1 || len(info.Missing) != 0 {
		t.Errorf("%+v", info)
	}
	// Again: not touched.
	if res, err := InstallKimi(e); err != nil || res.Changed {
		t.Errorf("second install: %+v %v", res, err)
	}
	// Out again: the user's file as it was.
	if res, err := UninstallKimi(e); err != nil || !res.Changed {
		t.Fatalf("%+v %v", res, err)
	}
	if got := read(t, e.KimiConfig()); got != kimiUsers {
		t.Fatalf("after uninstall:\n%s", got)
	}
	if res, _ := UninstallKimi(e); res.Changed {
		t.Error("a second uninstall changed it")
	}
}

// stui's rules among the user's (Kimi rewrote the file, or the user moved
// them): replaced at the end, the user's left where they were, comments too.
func TestInstallKimiReplacesOldRules(t *testing.T) {
	e := testEnv(t)
	old := `[[hooks]]
event = "Stop"
command = '/old/path/stui hook kimi'
timeout = 30

# mine
[[hooks]]
event = "Stop"
command = "say done"
`
	write(t, e.KimiConfig(), old)
	if info := KimiInfo(e); info.State != "outdated" || info.Version != 0 {
		t.Errorf("unversioned: %+v", info)
	}
	if _, err := InstallKimi(e); err != nil {
		t.Fatal(err)
	}
	want := `# mine
[[hooks]]
event = "Stop"
command = "say done"

` + kimiOurs
	if got := read(t, e.KimiConfig()); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestKimiInfoPartial(t *testing.T) {
	e := testEnv(t)
	InstallKimi(e)
	text := read(t, e.KimiConfig())
	cut := "[[hooks]]\nevent = \"Interrupt\"\ncommand = \"/usr/local/bin/stui hook kimi --hv 1\"\ntimeout = 10\n\n"
	if !strings.Contains(text, cut) {
		t.Fatal("no Interrupt rule")
	}
	write(t, e.KimiConfig(), strings.Replace(text, cut, "", 1))
	if info := KimiInfo(e); info.State != "partial" || !reflect.DeepEqual(info.Missing, []string{"Interrupt"}) {
		t.Errorf("%+v", info)
	}
	if res, _ := InstallKimi(e); !res.Changed || KimiInfo(e).State != "installed" {
		t.Error("install did not put it back")
	}
}

// Hooks written as a key or a plain table cannot have [[hooks]] beside them:
// Kimi would not load the file. stui leaves it.
func TestInstallKimiLeavesOtherShapes(t *testing.T) {
	for _, text := range []string{
		"hooks = []\n",
		"[hooks]\n",
	} {
		e := testEnv(t)
		write(t, e.KimiConfig(), text)
		res, err := InstallKimi(e)
		if err != nil || res.Changed || res.Note == "" || read(t, e.KimiConfig()) != text {
			t.Errorf("%q: %+v %v", text, res, err)
		}
	}
}

// No config yet: stui's rules are the file. Another kind's command is not stui's
// Kimi rule.
func TestInstallKimiNew(t *testing.T) {
	e := testEnv(t)
	if _, err := InstallKimi(e); err != nil {
		t.Fatal(err)
	}
	if got := read(t, e.KimiConfig()); got != kimiOurs {
		t.Fatalf("%s", got)
	}
	write(t, e.KimiConfig(), "[[hooks]]\nevent = \"Stop\"\ncommand = \"/usr/local/bin/stui hook claude --hv 2\"\n")
	if info := KimiInfo(e); info.State != "none" {
		t.Errorf("%+v", info)
	}
}

func TestTomlString(t *testing.T) {
	for in, want := range map[string]string{
		`"a\"b"`:              `a"b`,
		`'C:\stui.exe' # x`:   `C:\stui.exe`,
		`"x" # comment`:       "x",
		`"/stui hook kimi"  `: "/stui hook kimi",
	} {
		if got, ok := tomlString(strings.TrimSpace(in)); !ok || got != want {
			t.Errorf("%s: %q %v", in, got, ok)
		}
	}
	for _, in := range []string{`"open`, `x`, `"a" b`} {
		if _, ok := tomlString(in); ok {
			t.Errorf("%s read as a string", in)
		}
	}
}
