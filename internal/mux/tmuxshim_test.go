package mux

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A script named tmux in a folder of its own.
func fakeTmux(t *testing.T, dir, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "tmux")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// Orca's stand-in as its bundle has it (1.4.229): "tmux 3.4" to -V, 0 and
// nothing for has-session, "unsupported command" for new-session.
const orcaShim = `case "$1" in
  -V) echo "tmux 3.4";;
  has-session|has|set-option|show-options) exit 0;;
  *) echo "tmux: unsupported command: $1" >&2; exit 1;;
esac
`

func TestLookTmuxPassesOverOrcasShim(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no tmux on Windows")
	}
	root := t.TempDir()
	shim := filepath.Join(root, "shim")
	fakeTmux(t, shim, orcaShim)
	real := fakeTmux(t, filepath.Join(root, "bin"), "exit 0\n")
	path := strings.Join([]string{shim, filepath.Join(root, "bin")}, string(os.PathListSeparator))

	if got, skipped := lookTmux(path, shim); got != real || !skipped {
		t.Errorf("with the variable: %q skipped=%v, want %q skipped", got, skipped, real)
	}
	// The variable gone, Orca's default folder still on PATH.
	home := filepath.Join(root, ".orca", "claude-agent-teams-bin")
	fakeTmux(t, home, orcaShim)
	path2 := strings.Join([]string{home, filepath.Join(root, "bin")}, string(os.PathListSeparator))
	if got, skipped := lookTmux(path2, ""); got != real || !skipped {
		t.Errorf("default folder: %q skipped=%v, want %q skipped", got, skipped, real)
	}
	// Only the stand-in: no tmux at all.
	if got, skipped := lookTmux(shim, shim); got != "" || !skipped {
		t.Errorf("only the shim: %q skipped=%v, want none, skipped", got, skipped)
	}
	// No shim: the first tmux on PATH, as exec.LookPath.
	if got, skipped := lookTmux(filepath.Join(root, "bin"), ""); got != real || skipped {
		t.Errorf("plain PATH: %q skipped=%v", got, skipped)
	}
}

func TestOnlyTheShimIsNoTmux(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no tmux on Windows")
	}
	shim := filepath.Join(t.TempDir(), "shim")
	fakeTmux(t, shim, orcaShim)
	t.Setenv("PATH", shim)
	t.Setenv(orcaShimEnv, shim)
	t.Setenv("STUI_TMUX_BIN", "")
	if tm := NewTmux(); tm.Available() {
		t.Errorf("the stand-in counted as tmux: %q", tm.Command())
	}
}

func TestCreateSessionSaysAStandInCannotMakeOne(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no tmux on Windows")
	}
	bin := fakeTmux(t, t.TempDir(), orcaShim)
	tm := &Tmux{id: "tmux", bin: bin}
	err := tm.CreateSession(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "unsupported command") {
		t.Fatalf("err = %v, want the stand-in's refusal", err)
	}
}

func TestCreateSessionStillReadsADuplicateAsThere(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no tmux on Windows")
	}
	// What tmux says to new-session for a name that is taken.
	bin := fakeTmux(t, t.TempDir(), `case "$1" in
  new-session) echo "duplicate session: x" >&2; exit 1;;
  *) exit 0;;
esac
`)
	tm := &Tmux{id: "tmux", bin: bin}
	if err := tm.CreateSession(context.Background(), "x"); err != nil {
		t.Fatalf("a taken name is a session that is there: %v", err)
	}
}
