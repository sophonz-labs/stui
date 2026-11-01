package orch

import (
	"context"
	"testing"
)

// A pane is named by its window as it is now when it is adopted, not by an
// @stui-name left on it by an adoption whose record is gone: otherwise a
// Mac pane adopted again is still called "windows-ci", a name from a run
// retired long ago.
func TestAdoptNamesFromTheWindowNow(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	f := newFake([3]string{"%1", "work", "lead"}, [3]string{"%2", "default", "Simulator / CI"})
	f.opts["%2"]["@stui-name"] = "windows-ci"
	a := NewAdopter(AdoptOptions{Tmux: f.run, Agents: func() map[string]string { return map[string]string{"%1": "claude", "%2": "claude"} }})
	res, err := a.Adopt(context.Background(), "%1", "%2", AdoptHow{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("adopted %%2 (window %q, @stui-name %q left on it) as %q", "Simulator / CI", "windows-ci", res.Name)
	if res.Name != "simulator-ci" || f.opts["%2"]["@stui-name"] != "simulator-ci" {
		t.Fatalf("named %q (@stui-name %q), want simulator-ci from its window", res.Name, f.opts["%2"]["@stui-name"])
	}
}
