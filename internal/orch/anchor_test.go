package orch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// After A restarts, the coordinator of a run with a worker on ci-mac is in
// %3, and %1 -- the pane the record and ci-mac still name -- is another
// run's agent ("api root"). The worker's done goes to %3 by
// the coordinator's key, or to no one when no pane has its parent's key;
// never to %1.
func TestRemoteDoneNotToThePaneThatTookItsNumber(t *testing.T) {
	for _, c := range []struct {
		name, parentKey, want string
	}{
		{"a record from before keys", "", "%3"},
		{"its parent's key", "k-coord", "%3"},
		{"a parent no pane has the key of", "k-gone", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("STUI_HOME", t.TempDir())
			run := &Run{ID: NewID(), Coordinator: "%1", CoordinatorKey: "k-coord", CoordinatorLive: true, Created: Now()}
			run.Workers = []Worker{{ID: "ci-mac/%7", Name: "sim", Machine: "ci-mac", Parent: "%1", ParentKey: c.parentKey, Adopted: true}}
			if err := run.Save(); err != nil {
				t.Fatal(err)
			}
			tm := newFake([3]string{"%1", "work", "api root"}, [3]string{"%3", "default", "lead"})
			tm.opts["%1"] = map[string]string{"@stui-run": "r-0000aaaa", "@stui-role": RoleCoordinatorName, OptKey: "k-api"}
			tm.opts["%3"] = map[string]string{"@stui-run": run.ID, "@stui-role": RoleCoordinatorName, OptKey: "k-coord"}
			var mu sync.Mutex
			var told []string
			a := NewAdopter(AdoptOptions{
				Tmux:  tm.run,
				Self:  func() string { return "main-pc" },
				State: func(string) (string, int64) { return "idle", 1 },
				Deliver: func(_ context.Context, pane, text string) (int, map[string]any) {
					mu.Lock()
					told = append(told, pane)
					mu.Unlock()
					return 200, map[string]any{"delivered": true}
				},
				Remote: func(_ context.Context, slug, _, path string, _ any) (int, []byte, error) {
					b, _ := json.Marshal(RemoteWorker{ID: "%7", Name: "sim", Run: run.ID, Parent: "main-pc/%1", Done: true, Summary: "built it"})
					return 200, b, nil
				},
			})
			a.MailTick(context.Background())
			a.MailTick(context.Background())
			var to []string
			for _, ev := range ReadEvents(run.ID) {
				if ev.Kind == EvDone {
					to = append(to, ev.To)
				}
			}
			t.Logf("done events to %v; typed into %v", to, told)
			for _, p := range told {
				if p == "%1" {
					t.Fatalf("the worker's done was typed into %%1, the pane that took its parent's number")
				}
			}
			switch {
			case c.want == "" && (len(to) != 0 || len(told) != 0):
				t.Fatalf("a parent that is lost was told: %v %v", to, told)
			case c.want != "" && (len(told) != 1 || told[0] != c.want):
				t.Fatalf("typed into %v, want %s", told, c.want)
			}
		})
	}
}

// A worker whose pane is gone from the tmux server it was seen in was
// closed: no longer live. One gone after the server changed is lost, and
// stays live for stui orch status to list.
func TestRebindClosedIsNotLost(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	run := &Run{ID: NewID(), Coordinator: "%1", CoordinatorKey: "k-c", CoordinatorLive: true, Created: Now(), Server: "1:1"}
	run.Workers = []Worker{
		{ID: "%2", Name: "closed", Key: "k-closed", Live: true, Seen: "1:1"},
		{ID: "%3", Name: "lost", Key: "k-lost", Live: true, Seen: "0:0"},
	}
	run.Save()
	tm := newFake([3]string{"%1", "app", "lead"})
	tm.opts["%1"] = map[string]string{"@stui-run": run.ID, "@stui-role": RoleCoordinatorName, OptKey: "k-c"}
	Rebind(context.Background(), tm.run, nil, "1:1")
	r, _ := Load(run.ID)
	if r.Worker("%2").Live || !r.Worker("%3").Live {
		t.Fatalf("closed live %v, lost live %v", r.Worker("%2").Live, r.Worker("%3").Live)
	}
}

// A retired run comes back from the archive, merged with the directory stui
// went on writing its events into after it was retired.
func TestUnarchive(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	r := &Run{ID: NewID(), Coordinator: "%9", CoordinatorKey: "k-c", Created: Now()}
	r.Save()
	AppendEvent(Event{Run: r.ID, Kind: EvSend, TS: 100, From: "%9", To: "%2"})
	if err := Archive(r.ID, "test"); err != nil {
		t.Fatal(err)
	}
	// Written after it was retired, as stui worker done did.
	AppendEvent(Event{Run: r.ID, Kind: EvDone, TS: 200, From: "%2", To: "%9"})
	if err := Unarchive("r-00000000"); err == nil {
		t.Fatal("a run that is not archived came back")
	}
	if err := Unarchive(r.ID); err != nil {
		t.Fatal(err)
	}
	back, err := Load(r.ID)
	if err != nil || back.Retired != 0 || back.RetiredWhy != "" {
		t.Fatalf("record: %+v %v", back, err)
	}
	evs := ReadEvents(r.ID)
	if len(evs) != 2 || evs[0].Kind != EvSend || evs[1].Kind != EvDone {
		t.Fatalf("events: %+v", evs)
	}
	if _, err := os.Stat(filepath.Join(ArchiveDir(), r.ID)); !os.IsNotExist(err) {
		t.Fatalf("still in the archive: %v", err)
	}
	if err := Unarchive(r.ID); err == nil {
		t.Fatal("unarchived twice")
	}
}

// An old run put back from the archive whose worker on another machine was
// taken into a new run since, by the same coordinator: the old run does not
// report that worker's done to the coordinator a second time.
func TestOldRunDoesNotReportAWorkerOfANewOne(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	old := &Run{ID: NewID(), Coordinator: "%9", CoordinatorKey: "k-coord", Created: Now()}
	old.Workers = []Worker{{ID: "ci-mac/%31", Name: "simulator-ci", Machine: "ci-mac", Adopted: true}}
	old.Save()
	newRun := "r-0000cccc"
	tm := newFake([3]string{"%9", "default", "lead"})
	tm.opts["%9"] = map[string]string{"@stui-run": newRun, "@stui-role": RoleCoordinatorName, OptKey: "k-coord"}
	var told []string
	a := NewAdopter(AdoptOptions{
		Tmux:  tm.run,
		Self:  func() string { return "main-pc" },
		State: func(string) (string, int64) { return "idle", 1 },
		Deliver: func(_ context.Context, pane, text string) (int, map[string]any) {
			told = append(told, pane)
			return 200, map[string]any{"delivered": true}
		},
		Remote: func(context.Context, string, string, string, any) (int, []byte, error) {
			b, _ := json.Marshal(RemoteWorker{ID: "%31", Name: "simulator-ci", Run: newRun, Parent: "main-pc/%9", Done: true, Summary: "x"})
			return 200, b, nil
		},
	})
	a.MailTick(context.Background())
	if len(told) != 0 || len(ReadEvents(old.ID)) != 0 {
		t.Fatalf("the old run reported it: told %v, events %v", told, ReadEvents(old.ID))
	}
	// Nor is a parent found by its key when that pane is in another run.
	if p, ok := old.ParentHere(&old.Workers[0], map[string]PaneAnchor{"%9": {Key: "k-coord", Run: newRun}}); ok {
		t.Fatalf("parent %s, in another run", p)
	}
}
