package orch

import (
	"context"
	"testing"
)

// A worker whose @stui-parent named another worker's pane (terminal-go's
// said %13, the desktop worker) gets that worker's key
// as its parent in the record. Adopting it again under the coordinator
// must take that key back, or the next restore -- which writes the
// options again from the record -- puts it under the other worker again.
func TestReadoptClearsAStaleParentKey(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	f := newFake([3]string{"%9", "s", "lead"}, [3]string{"%13", "s", "ui-desktop"}, [3]string{"%12", "s", "terminal-go"})
	r := &Run{ID: "r-0000000b", Coordinator: "%9", Session: "s", Created: Now(), Workers: []Worker{
		{ID: "%13", Name: "ui-desktop", Agent: "claude", Started: 1, Adopted: true},
		{ID: "%12", Name: "terminal-go", Agent: "claude", Started: 1, Adopted: true},
	}}
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	f.opts["%9"] = map[string]string{"@stui-run": r.ID, "@stui-role": RoleCoordinatorName}
	f.opts["%13"] = map[string]string{"@stui-run": r.ID, "@stui-role": RoleWorkerName, "@stui-parent": "%9", "@stui-name": "ui-desktop", "@stui-adopted": "1"}
	f.opts["%12"] = map[string]string{"@stui-run": r.ID, "@stui-role": RoleWorkerName, "@stui-parent": "%13", "@stui-name": "terminal-go", "@stui-adopted": "1"}
	ctx := context.Background()

	Rebind(ctx, f.run, nil, "1:1")
	kd := f.opts["%13"][OptKey]
	r, _ = Load(r.ID)
	if w := r.Worker("%12"); w == nil || w.ParentKey != kd {
		t.Fatalf("the option's parent was not taken into the record: %+v", w)
	}

	// The coordinator takes it back.
	a := NewAdopter(AdoptOptions{Tmux: f.run, Agents: func() map[string]string {
		return map[string]string{"%9": "claude", "%13": "claude", "%12": "claude"}
	}})
	if _, err := a.Adopt(ctx, "%9", "%12", AdoptHow{NoBrief: true}); err != nil {
		t.Fatal(err)
	}
	if p := f.opts["%12"]["@stui-parent"]; p != "%9" {
		t.Fatalf("option after adopting again: %q", p)
	}
	Rebind(ctx, f.run, nil, "1:1")
	r, _ = Load(r.ID)
	if w := r.Worker("%12"); w.ParentKey != "" && w.ParentKey != r.CoordinatorKey {
		t.Fatalf("adopted again under the coordinator, the record still says ui-desktop: parentKey %s", w.ParentKey)
	}

	// tmux restarts; stui restore makes the panes again with their keys.
	after := newFake([3]string{"%0", "s", "lead"}, [3]string{"%1", "s", "ui-desktop"}, [3]string{"%2", "s", "terminal-go"})
	after.opts["%0"][OptKey] = f.opts["%9"][OptKey]
	after.opts["%1"][OptKey] = kd
	after.opts["%2"][OptKey] = f.opts["%12"][OptKey]
	Rebind(ctx, after.run, nil, "2:2")
	if p := after.opts["%2"]["@stui-parent"]; p != "%0" {
		t.Fatalf("after a restore terminal-go works for %q, want the coordinator %%0", p)
	}
}
