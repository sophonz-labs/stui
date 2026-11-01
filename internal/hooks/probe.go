package hooks

// Whether the hook stui installed in Codex starts at all, run the way Codex
// runs it.
//
// A hook Codex cannot start shows in the Codex session as "hook exited with
// code 1" on every tool call, and stui hears nothing, so stui cannot notice it
// from its side. stui hooks status runs the installed command through the
// shell Codex uses, with STUI_HOOK_PROBE set (stui hook then only prints that it
// started), and says what came back.

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sophonz-labs/stui/internal/proc"
)

// Probe is what running a hook command came to.
type Probe struct {
	// The shell and its arguments, before the command.
	Shell   []string
	Command string
	OK      bool
	// The shell's exit code; -1 when it could not be started.
	Exit int
	// The first line it printed that says something, when it failed.
	Output string
}

// probeReply is hookclient.ProbeReply; hooks does not import hookclient.
const probeReply = "stui hook ok"

// CodexShell is the shell Codex runs hooks with on this machine, as the
// words before the command (codex-rs shell_detect default_user_shell,
// shell.rs derive_exec_args): PowerShell on Windows, the user's shell
// elsewhere.
func CodexShell() []string {
	if hostOS == "windows" {
		for _, ps := range []string{"pwsh", "powershell"} {
			if p, err := exec.LookPath(ps); err == nil {
				return []string{p, "-NoProfile", "-Command"}
			}
		}
		return nil // cmd /c: Codex quotes it its own way; not probed
	}
	sh := os.Getenv("SHELL")
	switch filepath.Base(sh) {
	case "zsh", "bash", "sh":
	default:
		sh = "/bin/sh"
	}
	return []string{sh, "-c"}
}

// codexCommand is the command of stui's first entry in Codex's hooks.json.
func codexCommand(e Env) string {
	doc, _, err := loadSettings(e.CodexHooksFile())
	if err != nil {
		return ""
	}
	hooks, _ := codexHooksObject(doc, false)
	if hooks == nil {
		return ""
	}
	for _, m := range hooks.members {
		groups, _ := m.val.([]any)
		for _, g := range groups {
			grp, ok := g.(*object)
			if !ok {
				continue
			}
			hv, _ := grp.get("hooks")
			list, _ := hv.([]any)
			for _, h := range list {
				if !isOurs(h) {
					continue
				}
				if c, ok := h.(*object).get("command"); ok {
					if s, ok := c.(string); ok {
						return s
					}
				}
			}
		}
	}
	return ""
}

// ProbeCodex runs stui's Codex hook the way Codex would. ok is false when
// there is nothing to run or no shell stui knows to run it with.
func ProbeCodex(e Env) (Probe, bool) {
	cmd := codexCommand(e)
	shell := CodexShell()
	if cmd == "" || len(shell) == 0 {
		return Probe{}, false
	}
	return runProbe(shell, cmd), true
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

func runProbe(shell []string, command string) Probe {
	p := Probe{Shell: shell, Command: command, Exit: -1}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, shell[0], append(append([]string{}, shell[1:]...), command)...)
	c.Env = append(os.Environ(), "STUI_HOOK_PROBE=1", "NO_COLOR=1")
	proc.Hide(c)
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	err := c.Run()
	if c.ProcessState != nil {
		p.Exit = c.ProcessState.ExitCode()
	}
	text := ansi.ReplaceAllString(out.String(), "")
	p.OK = err == nil && strings.Contains(text, probeReply)
	if !p.OK {
		p.Output = firstSaying(text)
		if p.Output == "" && err != nil {
			p.Output = err.Error()
		}
	}
	return p
}

// firstSaying is the line of out that says what went wrong: PowerShell
// puts "ParserError:" and a picture of the line before the reason.
func firstSaying(out string) string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "|~ "))
		if l != "" && !strings.HasPrefix(l, "Line") && !regexp.MustCompile(`^\d+\s*\|`).MatchString(l) {
			lines = append(lines, l)
		}
	}
	for _, l := range lines {
		if strings.Contains(l, "Unexpected") || strings.Contains(l, "not recognized") || strings.Contains(l, "not found") || strings.Contains(l, "No such") {
			return l
		}
	}
	if len(lines) > 0 {
		return lines[0]
	}
	return ""
}
