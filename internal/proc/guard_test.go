//go:build !windows

package proc

import (
	"os/exec"
	"testing"
	"time"
)

func start(t *testing.T) *exec.Cmd {
	t.Helper()
	c := exec.Command("sleep", "30")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Process.Kill(); c.Wait() })
	return c
}

// The parent killed outright: the child goes too.
func TestGuardStopsChildWhenParentDies(t *testing.T) {
	parent, child := start(t), start(t)
	done := make(chan struct{})
	go func() { Guard(parent.Process.Pid, child.Process.Pid); close(done) }()
	time.Sleep(200 * time.Millisecond)
	parent.Process.Kill()
	parent.Wait()
	exited := make(chan struct{})
	go func() { child.Wait(); close(exited) }()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the child outlived its parent")
	}
	<-done
}

// The child going first leaves the parent alone and ends the guard.
func TestGuardEndsWithChild(t *testing.T) {
	parent, child := start(t), start(t)
	done := make(chan struct{})
	go func() { Guard(parent.Process.Pid, child.Process.Pid); close(done) }()
	time.Sleep(200 * time.Millisecond)
	child.Process.Kill()
	child.Wait()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the guard did not end with its child")
	}
	if !Alive(parent.Process.Pid) {
		t.Fatal("the parent was touched")
	}
}
