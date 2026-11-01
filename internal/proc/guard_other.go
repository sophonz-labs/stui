//go:build !darwin

package proc

import (
	"time"
)

// waitEither polls once a second; only platforms with neither pdeathsig nor
// kqueue here (the BSDs) get this far, and Windows never starts a guard.
func waitEither(parent, child int) (parentWent bool) {
	for {
		if !Alive(child) {
			return false
		}
		if !Present(parent) {
			return true
		}
		time.Sleep(time.Second)
	}
}
