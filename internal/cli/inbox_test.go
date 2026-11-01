package cli

import "testing"

// A send turned down for a while waits in the inbox; one that cannot go in
// at all fails as before.
func TestOwedLater(t *testing.T) {
	for reason, want := range map[string]bool{"busy-input": true, "prompt-pending": true, "sending": true, "no-input": true, "gone": false, "no-agent": false, "": false} {
		if owedLater(reason) != want {
			t.Errorf("%q: %v", reason, !want)
		}
	}
}
