package orch

import (
	"strings"
	"testing"
)

func names(ms []TreeMember) string {
	var s []string
	for _, m := range ms {
		x := m.Name + "@" + m.Pane
		if m.Machine != "" {
			x += "(" + m.Machine + ")"
		}
		s = append(s, x)
	}
	return strings.Join(s, " ")
}

// A tree of two levels, two workers on another machine.
func TestSubtree(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	r := &Run{ID: NewID(), Coordinator: "%9", CoordinatorKey: "k-c", Created: Now(), Workers: []Worker{
		{ID: "%11", Name: "web", Key: "k-web"},
		{ID: "%13", Name: "ui-desktop", Key: "k-desk"},
		{ID: "%12", Name: "terminal-go", Key: "k-go", ParentKey: "k-desk"},
		{ID: "%8", Name: "mobile", Key: "k-mob"},
		{ID: "mac/%31", Name: "simulator-ci", ParentKey: "k-mob", Machine: "mac"},
		{ID: "mac/%34", Name: "action-runner", ParentKey: "k-desk", Machine: "mac"},
	}}
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	// A team: the lead's own run.
	team := &Run{ID: NewID(), Coordinator: "%8", CoordinatorKey: "k-mob", Created: Now() - 1, Workers: []Worker{
		{ID: "%40", Name: "android", Key: "k-and"},
	}}
	team.Save()
	if got, want := names(Subtree("%9", "k-c")), "coordinator@%9 web@%11 ui-desktop@%13 terminal-go@%12 action-runner@mac/%34(mac) mobile@%8 android@%40 simulator-ci@mac/%31(mac)"; got != want {
		t.Errorf("from the coordinator:\n %s\nwant\n %s", got, want)
	}
	if got, want := names(Subtree("%13", "k-desk")), "ui-desktop@%13 terminal-go@%12 action-runner@mac/%34(mac)"; got != want {
		t.Errorf("from a worker: %s", got)
	}
	if got := names(Subtree("%12", "k-go")); got != "terminal-go@%12" {
		t.Errorf("a leaf: %s", got)
	}
	if got := names(Subtree("%77", "")); got != "@%77" {
		t.Errorf("a pane in no run: %s", got)
	}
}
