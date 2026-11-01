//go:build !windows

package proc

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// A child that has ended but not been waited for is a zombie: still there
// to signal 0, gone to Probe.
func TestAnUnreapedChildIsGone(t *testing.T) {
	c := exec.Command("sleep", "30")
	if err := c.Start(); err != nil {
		t.Skip(err)
	}
	pid := c.Process.Pid
	defer c.Wait()
	if Probe(pid) != Running {
		t.Fatalf("a sleeping child is %v", Probe(pid))
	}
	syscall.Kill(pid, syscall.SIGKILL)
	// Not waited for yet: a zombie until c.Wait.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !zombie(pid) {
		time.Sleep(20 * time.Millisecond)
	}
	if !zombie(pid) {
		t.Fatal("the killed child never read as a zombie")
	}
	if syscall.Kill(pid, 0) != nil {
		t.Fatal("signal 0 refused a zombie; the test is not testing anything")
	}
	if Probe(pid) != Gone {
		t.Fatalf("a zombie is %v, want Gone", Probe(pid))
	}
}
