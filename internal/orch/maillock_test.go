package orch

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// One carrier a machine: a process holding mail.lock keeps another out,
// and the next one in once it is gone.
func TestOneMailCarrier(t *testing.T) {
	if os.Getenv("STUI_MAILLOCK_CHILD") == "1" {
		// The other stui: says whether it would carry.
		if mailLock() {
			os.Stdout.WriteString("carrier\n")
		} else {
			os.Stdout.WriteString("kept out\n")
		}
		return
	}
	home := t.TempDir()
	t.Setenv("STUI_HOME", home)
	other := func() string {
		cmd := exec.Command(os.Args[0], "-test.run=^TestOneMailCarrier$")
		cmd.Env = append(os.Environ(), "STUI_MAILLOCK_CHILD=1", "STUI_HOME="+home)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%v %s", err, out)
		}
		return string(out)
	}
	if got := other(); got != "carrier\nPASS\n" {
		t.Fatalf("with nobody holding it: %q", got)
	}
	if mailHold != nil {
		t.Fatal("held before asked")
	}
	if !mailLock() {
		t.Fatal("this process could not take it")
	}
	defer func() { mailHold.Close(); mailHold = nil }()
	if got := other(); got != "kept out\nPASS\n" {
		t.Fatalf("a second carrier: %q", got)
	}
	// A second handle in this process is kept out as well.
	f, err := os.OpenFile(filepath.Join(Dir(), "mail.lock"), os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if tryMailLock(f) {
		t.Fatal("a second handle took it")
	}
	mailHold.Close()
	mailHold = nil
	if got := other(); got != "carrier\nPASS\n" {
		t.Fatalf("once it is let go: %q", got)
	}
}
