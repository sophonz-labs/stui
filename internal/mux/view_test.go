package mux

import "testing"

func TestParseHistoryState(t *testing.T) {
	h, ok := parseHistoryState("80 24 0 1 0 1532 3 22")
	if !ok || h.Width != 80 || h.Height != 24 || h.Alt || !h.Mouse || h.InMode || h.HistorySize != 1532 || h.CursorX != 3 || h.CursorY != 22 {
		t.Fatalf("%+v %v", h, ok)
	}
	for _, bad := range []string{"", "80 24 0 1 0 1532 3", "80 24 x 1 0 1532 3 22"} {
		if _, ok := parseHistoryState(bad); ok {
			t.Fatalf("%q parsed", bad)
		}
	}
}

func TestUIModeNeedsAViewOfItsOwn(t *testing.T) {
	ungrouped := &Tmux{bin: "/nonexistent"}
	if ungrouped.SetUIMode(t.Context(), "work", "abc123", true) {
		t.Fatal("changed a session every client shares")
	}
}
