//go:build !windows

package proc

import (
	"fmt"
	"syscall"
	"testing"
)

// The machine this runs on has a pty to spare, or nothing else in the tests
// could open a terminal either.
func TestPtyFree(t *testing.T) {
	if err := PtyFree(); err != nil {
		t.Fatalf("PtyFree: %v", err)
	}
}

// What macOS and Linux answer when the table is full, and only those.
func TestNoPty(t *testing.T) {
	for _, err := range []error{syscall.ENXIO, syscall.ENOSPC, ErrNoPty, fmt.Errorf("open: %w", syscall.ENXIO)} {
		if !NoPty(err) {
			t.Errorf("%v is not read as no pty", err)
		}
	}
	for _, err := range []error{nil, syscall.EACCES, syscall.ENOENT, syscall.EMFILE} {
		if NoPty(err) {
			t.Errorf("%v is read as no pty", err)
		}
	}
}
