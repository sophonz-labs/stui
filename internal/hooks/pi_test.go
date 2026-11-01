package hooks

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestInstallPi(t *testing.T) {
	e := testEnv(t)
	if got := PiInfo(e).State; got != "none" {
		t.Fatalf("before install: %s", got)
	}
	res, err := InstallPi(e)
	if err != nil || !res.Changed {
		t.Fatalf("%+v %v", res, err)
	}
	if want := filepath.Join(e.Home, ".pi", "agent", "extensions", "stui-hook.js"); res.File != want {
		t.Errorf("wrote %s, want %s", res.File, want)
	}
	text := read(t, e.PiExtension())
	for _, s := range []string{"const STUI_HOOK_VERSION = 1;", `const STUI_COMMAND = "/usr/local/bin/stui";`, "export default function"} {
		if !strings.Contains(text, s) {
			t.Errorf("extension lacks %q", s)
		}
	}
	if strings.Contains(text, "__") {
		t.Errorf("a placeholder is left in the extension")
	}
	if info := PiInfo(e); info.State != "installed" || info.Version != PiVersion {
		t.Errorf("after install: %+v", info)
	}
	if res, _ := InstallPi(e); res.Changed {
		t.Errorf("a second install rewrote it")
	}
	// $PI_CODING_AGENT_DIR moves it.
	e2 := e
	e2.PiDir = filepath.Join(e.Home, "elsewhere")
	if got := PiInfo(e2).State; got != "none" {
		t.Errorf("another dir: %s", got)
	}
	if res, err := UninstallPi(e); err != nil || !res.Changed {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := os.Stat(e.PiExtension()); !os.IsNotExist(err) {
		t.Errorf("uninstall left the file")
	}
}

func TestPiInfoSeesChanges(t *testing.T) {
	e := testEnv(t)
	InstallPi(e)
	orig := read(t, e.PiExtension())
	write(t, e.PiExtension(), strings.Replace(orig, `else send("Stop", ctx);`, "", 1))
	if info := PiInfo(e); info.State != "partial" || !reflect.DeepEqual(info.Missing, []string{"Stop"}) {
		t.Errorf("Stop taken out: %+v", info)
	}
	write(t, e.PiExtension(), strings.Replace(orig, "const STUI_HOOK_VERSION = 1;", "const STUI_HOOK_VERSION = 0;", 1))
	if a, _ := Find("pi"); StateName(a.Status(e)) != "outdated" {
		t.Errorf("Status: %s", StateName(a.Status(e)))
	}
	if res, err := InstallPi(e); err != nil || !res.Changed || res.Backup == "" {
		t.Fatalf("%+v %v", res, err)
	}
	mine := "export default function (pi) {}\n"
	write(t, e.PiExtension(), mine)
	for _, f := range []func(Env) (Result, error){InstallPi, UninstallPi} {
		if res, err := f(e); err != nil || res.Changed || !strings.Contains(res.Note, "extension stui did not write") {
			t.Errorf("%+v %v", res, err)
		}
	}
	if read(t, e.PiExtension()) != mine {
		t.Errorf("the user's extension was changed")
	}
}

func TestPiIsHookable(t *testing.T) {
	e := testEnv(t)
	os.MkdirAll(filepath.Join(e.Home, ".pi", "agent"), 0o755)
	offered := func() bool {
		for _, a := range Hookable(e) {
			if a.ID == "pi" {
				return true
			}
		}
		return false
	}
	if !offered() || States(e)["pi"] != "none" {
		t.Errorf("not offered (%v)", States(e))
	}
	InstallPi(e)
	if offered() || States(e)["pi"] != "installed" {
		t.Errorf("installed, still offered (%v)", States(e))
	}
}

// The extension itself, under node, given the events Pi 1.4.0 was seen to
// send (a probe extension in a sandbox): what reaches stui hook.
func TestPiExtensionTranslates(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil || runtime.GOOS == "windows" {
		t.Skip("no node")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	fake := filepath.Join(dir, "stui")
	write(t, fake, "#!/bin/sh\nfor a; do last=$a; done\nprintf '%s %s %s\\n' \"$1\" \"$2\" \"$last\" >> "+log+"\n")
	os.Chmod(fake, 0o755)
	e := testEnv(t)
	e.St = fake
	ext := filepath.Join(dir, "stui-hook.mjs")
	write(t, ext, piModule.text(e))
	driver := `
import ext from ` + jsString(ext) + `;
const on = {};
ext({ on: (n, f) => { on[n] = f; } });
const ctx = { cwd: "/work", sessionManager: { getSessionId: () => "s1" } };
const ev = (n, e = {}) => on[n]?.({ type: n, ...e }, ctx);
const done = (stop, err) => ev("agent_end", { messages: [{ role: "user" }, { role: "assistant", stopReason: stop, errorMessage: err }] });
await ev("session_start", { reason: "startup" });
// A prompt, a tool an extension confirms, and a second turn.
await ev("before_agent_start", { messageType: "user" });
await ev("agent_start");
await ev("turn_start");
await ev("tool_execution_start", { toolName: "bash", toolCallId: "c1", args: { command: "date" } });
await ev("ui_prompt_start", { reason: "ui_prompt", kind: "confirm", title: "Allow bash?" });
await ev("ui_prompt_end", { reason: "ui_prompt", kind: "confirm", title: "Allow bash?" });
await ev("tool_execution_start", { toolName: "read", toolCallId: "c1/1", parentToolCallId: "c1", args: {} });
await ev("tool_execution_end", { toolName: "read", toolCallId: "c1/1", parentToolCallId: "c1" });
await ev("tool_execution_end", { toolName: "bash", toolCallId: "c1" });
await ev("turn_end", { outcome: "completed" });
await ev("turn_start");
await ev("turn_end", { outcome: "completed" });
await done("stop");
await ev("agent_before_settle", { outcome: "completed" });
await ev("agent_settled", { aborted: false });
// Esc in the middle: no agent_before_settle, and an aborted error.
await ev("before_agent_start", { messageType: "user" });
await ev("agent_start");
await ev("tool_execution_start", { toolName: "bash", toolCallId: "c2", args: { command: "sleep 30" } });
await ev("tool_execution_end", { toolName: "bash", toolCallId: "c2" });
await done("error", "This operation was aborted");
await ev("agent_settled", { aborted: true });
// A run an extension's message started, failing at the provider.
await ev("before_agent_start", { messageType: "custom" });
await ev("agent_start");
await done("error", "connection refused");
await ev("agent_before_settle", { outcome: "error" });
await ev("agent_settled", { aborted: false });
// A dialog a command opens, between turns: nobody is waited for.
await ev("ui_prompt_start", { reason: "ui_prompt", kind: "select", title: "Model" });
await ev("ui_prompt_end", { reason: "ui_prompt", kind: "select", title: "Model" });
await ev("session_shutdown", { reason: "new" });
await ev("session_start", { reason: "new" });
await ev("session_shutdown", { reason: "quit" });
`
	drv := filepath.Join(dir, "drive.mjs")
	write(t, drv, driver)
	if out, err := exec.Command(node, drv).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	type call struct {
		Event   string         `json:"hook_event_name"`
		Session string         `json:"session_id"`
		Cwd     string         `json:"cwd"`
		Tool    string         `json:"tool_name"`
		Error   string         `json:"error"`
		Input   map[string]any `json:"tool_input"`
	}
	var got []string
	for _, l := range strings.Split(strings.TrimSpace(read(t, log)), "\n") {
		f := strings.SplitN(l, " ", 3)
		if len(f) != 3 || f[0] != "hook" || f[1] != "pi" {
			t.Fatalf("called as %q", l)
		}
		var c call
		if err := json.Unmarshal([]byte(f[2]), &c); err != nil {
			t.Fatalf("%q: %v", f[2], err)
		}
		if c.Cwd != "/work" || c.Session != "s1" {
			t.Errorf("cwd %q session %q", c.Cwd, c.Session)
		}
		s := c.Event
		if c.Tool != "" {
			s += " " + c.Tool
		}
		if c.Error != "" {
			s += " " + c.Error
		}
		if c.Event == "PreToolUse" && c.Tool == "bash" && c.Input["command"] == nil {
			t.Errorf("tool input %v", c.Input)
		}
		got = append(got, s)
	}
	want := []string{
		"SessionStart",
		"UserPromptSubmit",
		"PreToolUse bash",
		"PermissionRequest Allow bash?",
		"PermissionResult",
		"PostToolUse bash",
		"Stop",
		"UserPromptSubmit",
		"PreToolUse bash",
		"PostToolUse bash",
		"Interrupt",
		"PreToolUse",
		"StopFailure connection refused",
		"SessionStart",
		"SessionEnd",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sent:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}
