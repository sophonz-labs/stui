package orch

// A run's record written by more than one stui at once: the agent's Rebind and
// mail, stui worker done, send and rename in the panes. Each read the record,
// changed its part and wrote it back, so two at once lost one's change
// (saveRebound reads again first to make that rarer). Update holds the
// run's lock (<run>/run.lock) from the read to the write, so none is lost.

import (
	"os"
	"path/filepath"
)

// Update reads run id, gives it to change, and writes it back, holding the
// run's lock throughout. Nothing is written when change returns an error.
func Update(id string, change func(r *Run) error) error {
	if !runID.MatchString(id) {
		return os.ErrInvalid
	}
	if err := os.MkdirAll(RunDir(id), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(RunDir(id), "run.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		return err
	}
	defer unlockFile(f)
	r, err := Load(id)
	if err != nil {
		return err
	}
	if err := change(r); err != nil {
		return err
	}
	return r.Save()
}
