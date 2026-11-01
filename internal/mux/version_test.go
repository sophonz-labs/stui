package mux

import "testing"

func TestParseVersion(t *testing.T) {
	for _, c := range []struct{ id, out, want string }{
		{"tmux", "tmux 3.5a\n", "3.5a"},
		{"tmux", "tmux next-3.6\n", "next-3.6"},
		{"psmux", "tmux 3.4.0\npsmux 3.4.0 (20aa1d7 2026-10-13)\n", "3.4.0"},
		{"psmux", "tmux 3.3.8\n", ""},
		{"zellij", "zellij 0.41.2\n", "0.41.2"},
		{"tmux", "", ""},
	} {
		if got := parseVersion(c.id, c.out); got != c.want {
			t.Errorf("%s %q: %q, want %q", c.id, c.out, got, c.want)
		}
	}
}

// What this machine's tmux says, and nothing for --mux none.
func TestVersionAsked(t *testing.T) {
	if v := Version(t.Context(), NewNone("/bin/zsh")); v != "" {
		t.Errorf("none: %q", v)
	}
	tm := NewTmux()
	if !tm.Available() {
		t.Skip("no tmux")
	}
	v := Version(t.Context(), tm)
	t.Logf("tmux here: %q", v)
	if v == "" {
		t.Error("tmux did not say its version")
	}
}
