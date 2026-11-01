package orch

import (
	"os"
	"path/filepath"
)

// mailHold keeps the carrier's lock for as long as this process runs.
var mailHold *os.File

// mailLock is whether this process is the machine's mail carrier: the one
// holding ~/.stui/orch/mail.lock. The lock goes when the process does.
//
// Every stui agent that runs orchestration starts a carrier (agent.go): on
// tmux, and on Windows on psmux once its probe passes. Two stui on one
// machine (another port, a second login) would otherwise both type the
// same mail. The lock is flock on Unix and LockFileEx on Windows
// (maillock_*.go); both hold per open file, so a second open in the same
// process is kept out too.
func mailLock() bool {
	if mailHold != nil {
		return true
	}
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return false
	}
	f, err := os.OpenFile(filepath.Join(Dir(), "mail.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return false
	}
	if !tryMailLock(f) {
		f.Close()
		return false
	}
	mailHold = f
	return true
}
