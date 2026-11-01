package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Orca's Codex hook as it sits in a real ~/.codex/hooks.json, with the
// trust Codex accepted for it (session_start, timeout 10).
const orcaCodexCommand = ": orca-agent-hook-form=1; if [ -n \"${ORCA_PANE_KEY-}\" ] && [ -n \"${ORCA_AGENT_HOOK_ROOT-}\" ] && [ -f \"${ORCA_AGENT_HOOK_ROOT-}/agent-hooks/codex-hook.sh\" ]; then /bin/sh \"${ORCA_AGENT_HOOK_ROOT-}/agent-hooks/codex-hook.sh\" || :; elif [ -z \"${ORCA_AGENT_HOOK_ROOT-}\" ] && [ -n \"${ORCA_PANE_KEY-}\" ] && [ -n \"${ORCA_AGENT_HOOK_PORT-}\" ] && [ -f \"${HOME-}/.orca/agent-hooks/codex-hook.sh\" ]; then /bin/sh \"${HOME-}/.orca/agent-hooks/codex-hook.sh\" || :; else { command -p cat 2>/dev/null || cat; } >/dev/null 2>&1 || :; fi"

func TestCodexHookHashMatchesCodex(t *testing.T) {
	// Captured by Orca from a real Codex /hooks approval (no timeout written,
	// so Codex's default of 600 s).
	if got := codexHookHash("PreToolUse", `/bin/sh "/tmp/orca-case-b-mCmCe6/agent-hooks/codex-hook.sh"`, 600); got != "sha256:bc013489dba495431d3790fda62ee5a7d907a7c491e29ad26238c3a5d6d2b163" {
		t.Fatalf("pre_tool_use hash = %s", got)
	}
	// A hook Codex runs today on a real machine.
	if got := codexHookHash("SessionStart", orcaCodexCommand, 10); got != "sha256:d853ef9fbaf2ff5c8504bf2463675664aaf29a8016c6a35293a6ddc053b4f030" {
		t.Fatalf("session_start hash = %s", got)
	}
}

func codexEnv(t *testing.T) Env {
	e := testEnv(t)
	e.CodexInstalled = true
	return e
}

// stui's entry goes after another tool's, whose index -- and so whose trust --
// stays where it was; installing again changes nothing; uninstalling leaves
// exactly what was there before.
func TestInstallCodexBesideAnotherTool(t *testing.T) {
	e := codexEnv(t)
	other := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"other-tool hook","timeout":10}]}]}}`
	write(t, e.CodexHooksFile(), other+"\n")
	config := "model = \"gpt-5\"\n\n[hooks.state.\"" + e.codexKeySource() + ":stop:0:0\"]\ntrusted_hash = \"sha256:aaaa\"\n"
	write(t, e.CodexConfig(), config)

	res, err := InstallCodex(e)
	if err != nil || !res.Changed {
		t.Fatalf("install: %+v %v", res, err)
	}
	var doc struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(read(t, e.CodexHooksFile())), &doc); err != nil {
		t.Fatal(err)
	}
	for _, ev := range CodexEvents {
		groups := doc.Hooks[ev]
		last := groups[len(groups)-1].Hooks[0]
		if !strings.HasSuffix(last.Command, "stui hook codex --hv "+strconv.Itoa(CodexVersion)) || last.Timeout != codexTimeout(ev) {
			t.Fatalf("%s: %+v", ev, groups)
		}
	}
	if doc.Hooks["Stop"][0].Hooks[0].Command != "other-tool hook" {
		t.Fatalf("the other tool's Stop moved: %+v", doc.Hooks["Stop"])
	}
	cfg := read(t, e.CodexConfig())
	src := e.codexKeySource()
	for _, want := range []string{
		"model = \"gpt-5\"",
		src + ":stop:0:0\"]\ntrusted_hash = \"sha256:aaaa\"",
		src + ":stop:1:0\"]\ntrusted_hash = \"" + codexHookHash("Stop", e.codexHookCommand(), 10),
		src + ":interrupt:0:0\"]\ntrusted_hash = \"" + codexHookHash("Interrupt", e.codexHookCommand(), 3),
		"notify = [",
	} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("config.toml lacks %q:\n%s", want, cfg)
		}
	}
	if info := CodexInfo(e); info.State != "installed" || info.Version != CodexVersion {
		t.Fatalf("info = %+v", info)
	}

	if res, err := InstallCodex(e); err != nil || res.Changed {
		t.Fatalf("second install changed something: %+v %v", res, err)
	}

	if _, err := UninstallCodex(e); err != nil {
		t.Fatal(err)
	}
	if got := read(t, e.CodexHooksFile()); !strings.Contains(got, "other-tool hook") || strings.Contains(got, "stui hook codex") {
		t.Fatalf("hooks.json after uninstall:\n%s", got)
	}
	if got := read(t, e.CodexConfig()); got != config {
		t.Fatalf("config.toml after uninstall:\n%s\nwant:\n%s", got, config)
	}
	if CodexStatus(e) {
		t.Fatal("still installed")
	}
}

// A hooks.json stui made alone goes with stui's hooks.
func TestUninstallCodexRemovesItsOwnFile(t *testing.T) {
	e := codexEnv(t)
	if _, err := InstallCodex(e); err != nil {
		t.Fatal(err)
	}
	if _, err := UninstallCodex(e); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.CodexHooksFile()); !os.IsNotExist(err) {
		t.Fatalf("hooks.json left behind: %v", err)
	}
	if got := strings.TrimSpace(read(t, e.CodexConfig())); got != "" {
		t.Fatalf("config.toml left with:\n%s", got)
	}
}

// The notify line alone -- what stui wrote before hooks -- is an outdated
// set, which the automatic update brings up to hooks.
func TestCodexNotifyAloneIsOutdated(t *testing.T) {
	e := codexEnv(t)
	write(t, e.CodexConfig(), codexLine(e)+"\n")
	if info := CodexInfo(e); info.State != "outdated" || info.Version != 1 {
		t.Fatalf("info = %+v", info)
	}
	changes, err := AutoUpdate(e)
	if err != nil || len(changes) != 1 || changes[0].ID != "codex" || changes[0].To != CodexVersion {
		t.Fatalf("auto: %+v %v", changes, err)
	}
	if !strings.Contains(read(t, filepath.Join(e.Home, ".codex", "hooks.json")), "stui hook codex --hv "+strconv.Itoa(CodexVersion)) {
		t.Fatal("no hooks after the update")
	}
}

func TestHookStateKey(t *testing.T) {
	for line, want := range map[string]string{
		`[hooks.state."/a/hooks.json:stop:1:0"]`:      "/a/hooks.json:stop:1:0",
		`  [hooks.state.'C:\x\hooks.json:stop:0:0'] `: `C:\x\hooks.json:stop:0:0`,
		`[hooks.state."a\"b:stop:0:0"] # note`:        `a"b:stop:0:0`,
		`[hooks.state]`:                               "",
		`[model]`:                                     "",
	} {
		if got := hookStateKey(line); got != want {
			t.Fatalf("%s: %q, want %q", line, got, want)
		}
	}
}
