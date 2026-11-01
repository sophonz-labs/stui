package proc

import (
	"os"
	"os/exec"
	"strconv"
	"testing"
)

// Without GuardBinary nothing is started: a test binary given the guard's
// arguments would run its own tests again instead of guarding anything.
func TestGuardChildNeedsTheBinary(t *testing.T) {
	if GuardBinary != "" {
		t.Fatalf("GuardBinary is %q outside stui", GuardBinary)
	}
	c := exec.Command("sleep", "5")
	if err := c.Start(); err != nil {
		t.Skip(err)
	}
	defer c.Process.Kill()
	before := children(t)
	GuardChild(c.Process.Pid)
	if after := children(t); after != before {
		t.Fatalf("children went from %d to %d", before, after)
	}
}

func children(t *testing.T) int {
	out, _ := exec.Command("pgrep", "-P", strconv.Itoa(os.Getpid())).Output()
	n := 0
	for _, b := range out {
		if b == '\n' {
			n++
		}
	}
	return n
}
