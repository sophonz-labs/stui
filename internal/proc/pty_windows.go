package proc

import "errors"

// PtyFree: a Windows terminal is a ConPTY, which has no table to run out of.
func PtyFree() error { return nil }

// NoPty is whether err is ErrNoPty.
func NoPty(err error) bool { return errors.Is(err, ErrNoPty) }
