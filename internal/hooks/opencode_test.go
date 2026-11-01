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

func TestInstallOpencode(t *testing.T) {
	e := testEnv(t)
	if got := OpencodeInfo(e).State; got != "none" {
		t.Fatalf("before install: %s", got)
	}
	res, err := InstallOpencode(e)
	if err != nil || !res.Changed {
		t.Fatalf("%+v %v", res, err)
	}
	// plugins, plural: opencode does not look in "plugin".
	if want := filepath.Join(e.Home, ".config", "opencode", "plugins", "stui-hook.js"); res.File != want {
		t.Errorf("wrote %s, want %s", res.File, want)
	}
	text := read(t, e.OpencodePlugin())
	for _, s := range []string{"const STUI_HOOK_VERSION = 1;", `const STUI_COMMAND = "/usr/local/bin/stui";`, `from "node:child_process"`} {
		if !strings.Contains(text, s) {
			t.Errorf("plugin lacks %q", s)
		}
	}
	if strings.Contains(text, "__") {
		t.Errorf("a placeholder is left in the plugin")
	}
	if info := OpencodeInfo(e); info.State != "installed" || info.Version != OpencodeVersion {
		t.Errorf("after install: %+v", info)
	}
	// Again: nothing to do.
	if res, _ := InstallOpencode(e); res.Changed {
		t.Errorf("a second install rewrote it")
	}
	// Another path to stui is the same plugin.
	e2 := e
	e2.St = "/opt/homebrew/bin/stui"
	if got := OpencodeInfo(e2).State; got != "installed" {
		t.Errorf("another stui path: %s", got)
	}
	if res, err := UninstallOpencode(e); err != nil || !res.Changed {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := os.Stat(e.OpencodePlugin()); !os.IsNotExist(err) {
		t.Errorf("uninstall left the file")
	}
}

// A line changed by hand is partial; one that drops an event names it; an
// older stui's plugin is outdated, and install puts each right.
func TestOpencodeInfoSeesChanges(t *testing.T) {
	e := testEnv(t)
	InstallOpencode(e)
	orig := read(t, e.OpencodePlugin())

	write(t, e.OpencodePlugin(), strings.Replace(orig, "setTimeout(finish, 3000)", "setTimeout(finish, 9000)", 1))
	if info := OpencodeInfo(e); info.State != "partial" || len(info.Missing) != 0 {
		t.Errorf("edited: %+v", info)
	}
	write(t, e.OpencodePlugin(), strings.Replace(orig, `send("Stop", p.sessionID);`, "", 1))
	if info := OpencodeInfo(e); info.State != "partial" || !reflect.DeepEqual(info.Missing, []string{"Stop"}) {
		t.Errorf("Stop taken out: %+v", info)
	}
	write(t, e.OpencodePlugin(), strings.Replace(orig, "const STUI_HOOK_VERSION = 1;", "const STUI_HOOK_VERSION = 0;", 1))
	if info := OpencodeInfo(e); info.State != "outdated" {
		t.Errorf("older: %+v", info)
	}
	if a, _ := Find("opencode"); StateName(a.Status(e)) != "outdated" {
		t.Errorf("Status: %s", StateName(a.Status(e)))
	}
	res, err := InstallOpencode(e)
	if err != nil || !res.Changed || res.Backup == "" {
		t.Fatalf("%+v %v", res, err)
	}
	if read(t, e.OpencodePlugin()) != orig || OpencodeInfo(e).State != "installed" {
		t.Errorf("install did not put it right")
	}
}

// A file of that name stui did not write is the user's.
func TestOpencodeLeavesOthersAlone(t *testing.T) {
	e := testEnv(t)
	mine := "export const Mine = async () => ({})\n"
	write(t, e.OpencodePlugin(), mine)
	if info := OpencodeInfo(e); info.State != "none" {
		t.Errorf("%+v", info)
	}
	for _, f := range []func(Env) (Result, error){InstallOpencode, UninstallOpencode} {
		res, err := f(e)
		if err != nil || res.Changed || res.Note == "" {
			t.Errorf("%+v %v", res, err)
		}
	}
	if read(t, e.OpencodePlugin()) != mine {
		t.Errorf("the user's plugin was changed")
	}
}

// A first run offers it where opencode is, and not once it is installed.
func TestOpencodeIsHookable(t *testing.T) {
	a, ok := Find("opencode")
	if !ok || !a.Hookable() || a.Why != "" {
		t.Fatalf("%+v", a)
	}
	e := testEnv(t)
	os.MkdirAll(filepath.Join(e.Home, ".config", "opencode"), 0o755)
	offered := func() bool {
		for _, a := range Hookable(e) {
			if a.ID == "opencode" {
				return true
			}
		}
		return false
	}
	if !offered() {
		t.Error("not offered")
	}
	if States(e)["opencode"] != "none" {
		t.Errorf("%v", States(e))
	}
	InstallOpencode(e)
	if offered() || States(e)["opencode"] != "installed" {
		t.Errorf("installed, still offered (%v)", States(e))
	}
}

// The plugin itself, under node, given the events opencode 1.19.4 was seen
// to send: what reaches stui hook, in order.
func TestOpencodePluginTranslates(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil || runtime.GOOS == "windows" {
		t.Skip("no node")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	fake := filepath.Join(dir, "stui")
	// The last argument is the payload; one line per call.
	write(t, fake, "#!/bin/sh\nfor a; do last=$a; done\nprintf '%s %s %s\\n' \"$1\" \"$2\" \"$last\" >> "+log+"\n")
	os.Chmod(fake, 0o755)
	e := testEnv(t)
	e.St = fake
	plugin := filepath.Join(dir, "stui-hook.mjs")
	write(t, plugin, opencodeModule.text(e))
	driver := `
import { StHook } from ` + jsString(plugin) + `;
const h = await StHook({ directory: "/work" });
const ev = (type, properties) => h.event({ event: { type, properties } });
const root = "ses_root", kid = "ses_kid";
// A turn with a tool, and a subagent (task) whose idle comes mid-turn.
await ev("session.created", { sessionID: root, info: { id: root } });
await h["chat.message"]({ sessionID: root }, {});
await ev("session.status", { sessionID: root, status: { type: "busy" } });
await ev("session.status", { sessionID: root, status: { type: "busy" } });
await h["tool.execute.before"]({ tool: "task", sessionID: root, callID: "c1" }, { args: { description: "add" } });
await ev("session.created", { sessionID: kid, info: { id: kid, parentID: root } });
await h["chat.message"]({ sessionID: kid }, {});
await ev("session.status", { sessionID: kid, status: { type: "busy" } });
await ev("session.idle", { sessionID: kid });
await h["tool.execute.after"]({ tool: "task", sessionID: root, callID: "c1" }, {});
await ev("message.part.updated", { sessionID: root });
await ev("message.part.delta", { sessionID: root });
await ev("session.status", { sessionID: root, status: { type: "idle" } });
await ev("session.idle", { sessionID: root });
// A permission, then Esc twice: an error, and two idles.
await h["chat.message"]({ sessionID: root }, {});
await ev("permission.asked", { sessionID: root, permission: "bash", patterns: ["date"], metadata: { command: "date" } });
await ev("session.error", { sessionID: root, error: { name: "MessageAbortedError", data: { message: "Aborted" } } });
await ev("session.idle", { sessionID: root });
await ev("session.idle", { sessionID: root });
// A turn opencode began without a prompt, ending on an API error.
await ev("session.status", { sessionID: root, status: { type: "busy" } });
await ev("session.error", { sessionID: root, error: { name: "APIError", data: { message: "overloaded" } } });
await ev("session.idle", { sessionID: root });
await h.dispose();
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
		if len(f) != 3 || f[0] != "hook" || f[1] != "opencode" {
			t.Fatalf("called as %q", l)
		}
		var c call
		if err := json.Unmarshal([]byte(f[2]), &c); err != nil {
			t.Fatalf("%q: %v", f[2], err)
		}
		if c.Cwd != "/work" {
			t.Errorf("cwd %q", c.Cwd)
		}
		s := c.Event + " " + c.Session
		if c.Tool != "" {
			s += " " + c.Tool
		}
		if c.Error != "" {
			s += " " + c.Error
		}
		if c.Event == "PermissionRequest" && c.Input["command"] != "date" {
			t.Errorf("permission input %v", c.Input)
		}
		got = append(got, s)
	}
	want := []string{
		"SessionStart ses_root",
		"UserPromptSubmit ses_root",
		"PreToolUse ses_root task",
		"PostToolUse ses_root task",
		"Stop ses_root",
		"UserPromptSubmit ses_root",
		"PermissionRequest ses_root bash",
		"Interrupt ses_root",
		"PreToolUse ses_root",
		"StopFailure ses_root overloaded",
		"SessionEnd ",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sent:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
