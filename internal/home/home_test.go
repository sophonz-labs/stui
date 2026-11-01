package home

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A test that does not say where stui's home is never gets the real one:
// the keeper's tests retired a machine's live runs that way (home.go).
func TestNotTheRealHomeInATest(t *testing.T) {
	t.Setenv("STUI_HOME", "")
	h, _ := os.UserHomeDir()
	real := filepath.Join(h, ".stui")
	if d := Dir(); d == real || strings.HasPrefix(d, real+string(os.PathSeparator)) {
		t.Fatalf("home.Dir() is %s, the real one, in a test", d)
	}
	// One that gave itself a HOME keeps HOME/.stui.
	own := t.TempDir()
	t.Setenv("HOME", own)
	t.Setenv("USERPROFILE", own)
	if d, want := Dir(), filepath.Join(own, ".stui"); d != want {
		t.Fatalf("with HOME=%s: %s, want %s", own, d, want)
	}
	t.Setenv("STUI_HOME", "/x/st")
	if d := Dir(); d != "/x/st" {
		t.Fatalf("STUI_HOME: %s", d)
	}
}
