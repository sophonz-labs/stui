package hooks

import (
	"os/exec"
	"strings"
	"testing"
)

func TestCodexQuoteWindows(t *testing.T) {
	defer func(o string) { hostOS = o }(hostOS)
	hostOS = "windows"
	cases := map[string]string{
		`C:\Users\me\AppData\Local\stui\bin\stui.exe`: `C:\Users\me\AppData\Local\stui\bin\stui.exe`,
		`C:\Program Files\stui\stui.exe`:              `& 'C:\Program Files\stui\stui.exe'`,
		`C:\Users\O'Neil\stui.exe`:                    `& 'C:\Users\O''Neil\stui.exe'`,
	}
	for in, want := range cases {
		if got := codexQuote(in); got != want {
			t.Errorf("codexQuote(%q) = %q, want %q", in, got, want)
		}
		// stui still knows the command for its own, at this version.
		ok, v := hookVersion(Env{St: in}.codexHookCommand())
		if !ok || v != CodexVersion {
			t.Errorf("%q: hookVersion = %v, %d", in, ok, v)
		}
	}
}

func TestCodexQuoteUnixUnchanged(t *testing.T) {
	defer func(o string) { hostOS = o }(hostOS)
	hostOS = "darwin"
	for _, p := range []string{"/usr/local/bin/stui", "/Users/a b/stui"} {
		if codexQuote(p) != quoteCommand(p) {
			t.Errorf("%q: %q != %q", p, codexQuote(p), quoteCommand(p))
		}
	}
}

// The command stui wrote before v3, run the way Codex on Windows runs it:
// PowerShell reads the quoted path as a string and fails on the next word,
// exit 1 -- what a Windows Codex showed on every hook. The v3 form runs.
func TestProbePowerShell(t *testing.T) {
	ps, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("no pwsh")
	}
	shell := []string{ps, "-NoProfile", "-Command"}
	old := runProbe(shell, `"/bin/echo" hook codex --hv 2`)
	if old.OK || old.Exit != 1 || !strings.Contains(old.Output, "Unexpected token 'hook'") {
		t.Fatalf("old form: %+v", old)
	}
	// echo stands in for stui, printing what stui hook prints with STUI_HOOK_PROBE.
	for _, c := range []string{`& '/bin/echo' stui hook ok`, `/bin/echo stui hook ok`} {
		if p := runProbe(shell, c); !p.OK {
			t.Errorf("%s: %+v", c, p)
		}
	}
}

func TestProbeShellMissingCommand(t *testing.T) {
	p := runProbe([]string{"/bin/sh", "-c"}, "/nonexistent/stui hook codex --hv 3")
	if p.OK || p.Exit == 0 || p.Output == "" {
		t.Fatalf("%+v", p)
	}
}

// A Windows machine with the v2 set -- the quoted command PowerShell cannot
// run -- reads as outdated, so stui updates it on its own, and the update
// writes the command PowerShell runs, with trust for that command.
func TestWindowsV2SetIsRewritten(t *testing.T) {
	defer func(o string) { hostOS = o }(hostOS)
	hostOS = "windows"
	e := codexEnv(t)
	e.St = `C:\Users\me\AppData\Local\stui\bin\stui.exe`
	v2 := `\"C:\\Users\\me\\AppData\\Local\\stui\\bin\\stui.exe\" hook codex --hv 2`
	write(t, e.CodexHooksFile(), `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"`+v2+`","timeout":10}]}]}}`+"\n")
	if info := CodexInfo(e); info.State != "outdated" || info.Version != 2 {
		t.Fatalf("v2 set: %+v", info)
	}
	if _, entries, err := installCodexHooks(e); err != nil || len(entries) != len(CodexEvents) {
		t.Fatalf("install: %v, %d entries", err, len(entries))
	}
	want := `C:\Users\me\AppData\Local\stui\bin\stui.exe hook codex --hv 3`
	if got := codexCommand(e); got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
	if strings.Contains(read(t, e.CodexHooksFile()), `\"C:`) {
		t.Fatal("a quoted path is left")
	}
}
