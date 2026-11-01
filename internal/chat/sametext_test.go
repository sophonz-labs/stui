package chat

import (
	"encoding/json"
	"testing"
)

// The box's own copy of stui's line, whatever its wrapping did, and nothing
// a person added to it.
func TestSameText(t *testing.T) {
	line := "[stui] worker ux (%3) is done: Made the design doc -- stui worker read %3"
	for _, c := range []struct {
		box  string
		same bool
	}{
		{line, true},
		{"[stui] worker ux (%3) is done: Made the de\nsign doc -- stui worker read %3", true},
		{"  " + line + "  ", true},
		{line + " and mine", false},
		{"", false},
	} {
		if SameText(c.box, line) != c.same {
			t.Errorf("%q: %v", c.box, !c.same)
		}
	}
	// A request cannot say it: only stui's DeliverOver can.
	var b sendBody
	json.Unmarshal([]byte(`{"text":"x","Replace":"anything","replace":"anything"}`), &b)
	if b.Replace != "" {
		t.Fatal("replace taken from a request's body")
	}
}
