package orch

import (
	"context"
	"testing"
)

// treeOf is the run's tree events by kind, oldest first.
func treeOf(run string) []Event {
	var out []Event
	for _, ev := range ReadEvents(run) {
		switch ev.Kind {
		case EvAdopt, EvRelease, EvRename, EvAbout:
			out = append(out, ev)
		}
	}
	return out
}

// Adopting, a role, a new name and letting go are each an event: who asked,
// the worker by id and key, its parent before and after by key, and the
// values before and after.
func TestTreeEventsHere(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	f := newFake([3]string{"%1", "work", "lead"}, [3]string{"%2", "work", "API tests"}, [3]string{"%3", "work", "docs"})
	f.opts["%2"]["@stui-name"] = "windows-ci"
	a := NewAdopter(AdoptOptions{Tmux: f.run, Agents: func() map[string]string { return map[string]string{"%1": "claude", "%2": "claude", "%3": "claude"} }})
	ctx := WithBy(context.Background(), "%1")

	res, err := a.Adopt(ctx, "%1", "%2", AdoptHow{About: "runs the API tests"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Adopt(ctx, "%1", "%2", AdoptHow{About: "and the load tests"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetName(ctx, "%2", "api"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetAbout(WithBy(context.Background(), ""), "%2", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Release(ctx, "%2", false); err != nil {
		t.Fatal(err)
	}
	evs := treeOf(res.Run)
	for _, ev := range evs {
		t.Logf("%-7s from=%q by=%q to=%s/%s key=%s parent=%s(%s) wasParent=%s(%s) was=%q now=%q title=%q",
			ev.Kind, ev.From, ev.By, ev.To, ev.ToName, ev.ToKey, ev.Parent, ev.ParentKey, ev.WasParent, ev.WasParentKey, ev.Was, ev.Now, ev.Title)
	}
	if len(evs) != 5 {
		t.Fatalf("%d tree events", len(evs))
	}
	key1, key2 := f.opts["%1"][OptKey], f.opts["%2"][OptKey]
	adopt, about, rename, cleared, release := evs[0], evs[1], evs[2], evs[3], evs[4]
	switch {
	case adopt.Kind != EvAdopt || adopt.From != "%1" || adopt.To != "%2" || adopt.ToKey != key2 || adopt.Parent != "%1" || adopt.ParentKey != key1 ||
		adopt.Was != "windows-ci" || adopt.Now != "api-tests" || adopt.About != "runs the API tests":
		t.Errorf("adopt: %+v", adopt)
	case about.Kind != EvAbout || about.Was != "runs the API tests" || about.Now != "and the load tests":
		t.Errorf("about: %+v", about)
	case rename.Kind != EvRename || rename.Was != "api-tests" || rename.Now != "api" || rename.ToKey != key2:
		t.Errorf("rename: %+v", rename)
	case cleared.Kind != EvAbout || cleared.By != "app" || cleared.From != "" || cleared.Now != "":
		t.Errorf("about from an app: %+v", cleared)
	case release.Kind != EvRelease || release.WasParent != "%1" || release.WasParentKey != key1 || release.ToKey != key2:
		t.Errorf("release: %+v", release)
	}
}
