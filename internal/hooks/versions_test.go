package hooks

import (
	"strings"
	"testing"
)

func TestHookVersionMarker(t *testing.T) {
	for _, c := range []struct {
		cmd  string
		ours bool
		v    int
	}{
		{"/usr/local/bin/stui hook claude", true, 0},
		{"/usr/local/bin/stui hook claude --hv 2", true, 2},
		{"  /usr/local/bin/stui hook claude --hv 17  ", true, 17},
		{"'/Users/a b/bin/stui' hook claude --hv 3", true, 3},
		{`"C:\Users\a\stui.exe" hook claude --hv 2`, true, 2},
		{"/usr/local/bin/stui hook claude --hv", false, 0},
		{"/usr/local/bin/stui hook claude --hv x", false, 0},
		{"/usr/local/bin/stui hook claude --hv 2 && rm -rf /", false, 0},
		{"/usr/local/bin/best hook claude", false, 0},
		{"say done", false, 0},
	} {
		ok, v := hookVersion(c.cmd)
		if ok != c.ours || v != c.v {
			t.Errorf("%q: %v %d, want %v %d", c.cmd, ok, v, c.ours, c.v)
		}
	}
	for _, c := range []struct {
		line string
		ours bool
		v    int
	}{
		{`notify = ["/usr/local/bin/stui", "hook", "codex"]`, true, 1},
		{`notify = ["/usr/local/bin/stui", "hook", "codex", "--hv", "2"]`, true, 2},
		{`notify = ["notify-send", "codex"]`, false, 0},
	} {
		ok, v := codexLineVersion(c.line)
		if ok != c.ours || v != c.v {
			t.Errorf("%q: %v %d, want %v %d", c.line, ok, v, c.ours, c.v)
		}
	}
}

// settingsWith is a Claude settings file with stui's entries on events,
// each running cmd, and the user's own Stop and PreToolUse hooks.
func settingsWith(cmd string, events []string) string {
	var b strings.Builder
	b.WriteString("{\n  \"hooks\": {\n")
	b.WriteString(`    "Stop": [{ "hooks": [{ "type": "command", "command": "say done" }] }],` + "\n")
	b.WriteString(`    "PreToolUse": [{ "matcher": "Bash", "hooks": [{ "type": "command", "command": "~/bin/guard" }] }]`)
	for _, ev := range events {
		b.WriteString(",\n")
		b.WriteString(`    "` + ev + `X": [{ "hooks": [{ "type": "command", "command": "` + cmd + `", "timeout": 10 }] }]`)
	}
	b.WriteString("\n  }\n}\n")
	// Events the user also hooks keep both: put stui's in a second group.
	out := b.String()
	for _, ev := range events {
		out = strings.Replace(out, `"`+ev+`X"`, `"`+ev+`"`, 1)
	}
	return out
}

func mustInfo(t *testing.T, e Env) SetInfo {
	t.Helper()
	info, err := ClaudeInfo(e)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// What each stui before versions left behind, and what this one makes of it.
func TestClaudeInfoAcrossVersions(t *testing.T) {
	v1 := claudeSets[1]
	for _, c := range []struct {
		name    string
		file    string
		state   string
		version int
		missing int
	}{
		{"nothing", `{ "model": "opus" }`, "none", 0, len(ClaudeEvents)},
		{"stui 0.3.23, unmarked v1", settingsWith("/usr/local/bin/stui hook claude", v1), "outdated", 1, 2},
		{"stui 0.3.49, unmarked v2", settingsWith("/usr/local/bin/stui hook claude", ClaudeEvents), "installed", 2, 0},
		{"this stui", settingsWith("/usr/local/bin/stui hook claude --hv 2", ClaudeEvents), "installed", 2, 0},
		{"this stui, one taken out", settingsWith("/usr/local/bin/stui hook claude --hv 2", ClaudeEvents[1:]), "partial", 2, 1},
		{"v1 marked", settingsWith("/usr/local/bin/stui hook claude --hv 1", ClaudeEvents), "outdated", 1, 0},
		{"a newer stui", settingsWith("/usr/local/bin/stui hook claude --hv 9", ClaudeEvents), "installed", 9, 0},
		{"unmarked, one of v1 taken out", settingsWith("/usr/local/bin/stui hook claude", v1[1:]), "outdated", 0, 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := testEnv(t)
			write(t, e.ClaudeSettings(), c.file)
			info := mustInfo(t, e)
			if info.State != c.state || info.Version != c.version || len(info.Missing) != c.missing || info.Required != ClaudeVersion {
				t.Fatalf("%+v", info)
			}
		})
	}
}

// An outdated set is brought up to date in place: stui's entries carry the
// new version, the missing events are added, the user's hooks stay where
// they were, and a backup holds what was there.
func TestOutdatedUpdatedInPlace(t *testing.T) {
	e := testEnv(t)
	path := e.ClaudeSettings()
	before := settingsWith("/usr/local/bin/stui hook claude", claudeSets[1])
	write(t, path, before)

	res, err := InstallClaude(e)
	if err != nil || !res.Changed || res.Backup == "" {
		t.Fatalf("%+v %v", res, err)
	}
	if read(t, res.Backup) != before {
		t.Fatal("backup is not what was there")
	}
	got := read(t, path)
	if n := strings.Count(got, "/usr/local/bin/stui hook claude --hv 2"); n != len(ClaudeEvents) {
		t.Fatalf("%d marked entries:\n%s", n, got)
	}
	if strings.Count(got, `hook claude"`) != 0 {
		t.Fatalf("unmarked entry left:\n%s", got)
	}
	for _, want := range []string{`"say done"`, `"~/bin/guard"`, `"matcher": "Bash"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("user's %s lost:\n%s", want, got)
		}
	}
	if info := mustInfo(t, e); info.State != "installed" || info.Version != ClaudeVersion {
		t.Fatalf("%+v", info)
	}
	if res, err := InstallClaude(e); err != nil || res.Changed {
		t.Fatalf("second install %+v %v", res, err)
	}
}

// Two of stui's entries on one event -- an older stui that did not know the
// marker installing beside it -- become one, and an event stui stopped
// listening to loses stui's entry and keeps the user's.
func TestInstallDropsDuplicatesAndRetiredEvents(t *testing.T) {
	e := testEnv(t)
	path := e.ClaudeSettings()
	write(t, path, `{
  "hooks": {
    "Stop": [
      { "hooks": [{ "type": "command", "command": "/usr/local/bin/stui hook claude --hv 2", "timeout": 10 }] },
      { "hooks": [{ "type": "command", "command": "/usr/local/bin/stui hook claude", "timeout": 10 }, { "type": "command", "command": "say done" }] }
    ],
    "Retired": [
      { "hooks": [{ "type": "command", "command": "/usr/local/bin/stui hook claude --hv 1" }, { "type": "command", "command": "mine" }] }
    ],
    "AlsoRetired": [
      { "hooks": [{ "type": "command", "command": "/usr/local/bin/stui hook claude" }] }
    ]
  }
}
`)
	if _, err := InstallClaude(e); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	doc, _, _ := loadSettings(path)
	h, _ := claudeHooks(doc, false)
	count := func(ev string) (st, other int) {
		v, _ := h.get(ev)
		groups, _ := v.([]any)
		for _, g := range groups {
			hv, _ := g.(*object).get("hooks")
			list, _ := hv.([]any)
			for _, x := range list {
				if isOurs(x) {
					st++
				} else {
					other++
				}
			}
		}
		return
	}
	if st, other := count("Stop"); st != 1 || other != 1 {
		t.Fatalf("Stop: %d stui, %d other\n%s", st, other, got)
	}
	if st, other := count("Retired"); st != 0 || other != 1 {
		t.Fatalf("Retired: %d stui, %d other\n%s", st, other, got)
	}
	if _, ok := h.get("AlsoRetired"); ok {
		t.Fatalf("an event left with nothing in it:\n%s", got)
	}
	if info := mustInfo(t, e); info.State != "installed" {
		t.Fatalf("%+v", info)
	}
}

// The legacy hooks map says partial for an outdated set; hookSets says
// which.
func TestStatesAndSets(t *testing.T) {
	e := testEnv(t)
	write(t, e.ClaudeSettings(), settingsWith("/usr/local/bin/stui hook claude", claudeSets[1]))
	if s := States(e)["claude"]; s != "partial" {
		t.Fatal(s)
	}
	if s := Sets(e)["claude"]; s.State != "outdated" || s.Version != 1 || s.Required != ClaudeVersion {
		t.Fatalf("%+v", s)
	}
	if c := Sets(e)["codex"]; c.State != "none" || c.Required != CodexVersion {
		t.Fatalf("%+v", c)
	}
	if len(Incomplete(e)) != 1 {
		t.Fatal("incomplete")
	}
}

func autoEnv(t *testing.T) Env {
	e := testEnv(t)
	e.StateDir = t.TempDir()
	return e
}

func TestAutoUpdateNeedsConsent(t *testing.T) {
	// Nothing installed and nothing recorded: nothing to update, and no
	// consent made up.
	e := autoEnv(t)
	write(t, e.ClaudeSettings(), `{ "model": "opus" }`)
	if ch, err := AutoUpdate(e); err != nil || len(ch) != 0 {
		t.Fatalf("%v %v", ch, err)
	}
	if AutoOn(e) || ReadPrefs(e).Consent {
		t.Fatal("consent from nothing")
	}
	if read(t, e.ClaudeSettings()) != `{ "model": "opus" }` {
		t.Fatal("file touched")
	}
}

// stui's entries in the file are the consent: the outdated set is updated,
// with a backup, the consent is recorded, and the run is noted.
func TestAutoUpdateOutdated(t *testing.T) {
	e := autoEnv(t)
	before := settingsWith("/usr/local/bin/stui hook claude", claudeSets[1])
	write(t, e.ClaudeSettings(), before)
	if !AutoOn(e) {
		t.Fatal("stui's entries are consent")
	}
	ch, err := AutoUpdate(e)
	if err != nil || len(ch) != 1 || ch[0].ID != "claude" || ch[0].From != 1 || ch[0].To != ClaudeVersion || ch[0].Backup == "" {
		t.Fatalf("%+v %v", ch, err)
	}
	if read(t, ch[0].Backup) != before {
		t.Fatal("backup")
	}
	p := ReadPrefs(e)
	if !p.Consent || p.LastAuto == nil || len(p.LastAuto.Changes) != 1 {
		t.Fatalf("%+v", p)
	}
	if mustInfo(t, e).State != "installed" {
		t.Fatal("not updated")
	}
	// Up to date now: a second round changes nothing.
	if ch, err := AutoUpdate(e); err != nil || len(ch) != 0 {
		t.Fatalf("%v %v", ch, err)
	}
}

func TestAutoOffLeavesItAlone(t *testing.T) {
	e := autoEnv(t)
	before := settingsWith("/usr/local/bin/stui hook claude", claudeSets[1])
	write(t, e.ClaudeSettings(), before)
	if err := SetAuto(e, false); err != nil {
		t.Fatal(err)
	}
	if AutoOn(e) {
		t.Fatal("off")
	}
	if ch, _ := AutoUpdate(e); len(ch) != 0 || read(t, e.ClaudeSettings()) != before {
		t.Fatal("updated while off")
	}
	SetAuto(e, true)
	if ch, _ := AutoUpdate(e); len(ch) != 1 {
		t.Fatal("not updated once on")
	}
}

// A set at this version with an event taken out is somebody's edit:
// automatic updates leave it.
func TestAutoLeavesPartial(t *testing.T) {
	e := autoEnv(t)
	before := settingsWith("/usr/local/bin/stui hook claude --hv 2", ClaudeEvents[1:])
	write(t, e.ClaudeSettings(), before)
	if ch, _ := AutoUpdate(e); len(ch) != 0 || read(t, e.ClaudeSettings()) != before {
		t.Fatal("partial changed")
	}
}

func TestRecordConsent(t *testing.T) {
	e := autoEnv(t)
	if Consented(e) {
		t.Fatal("nothing yet")
	}
	RecordConsent(e)
	if !Consented(e) || !AutoOn(e) || ReadPrefs(e).ConsentAt == "" {
		t.Fatalf("%+v", ReadPrefs(e))
	}
}
