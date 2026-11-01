package orch

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
)

// line is one list-panes -F ViewFormat line: id, key, run, in-run, role,
// parent, name, adopted, done, task, about, owns, root, session, window
// name, window index, summary.
func line(f ...string) string {
	for len(f) < 17 {
		f = append(f, "")
	}
	return strings.Join(f, "\t")
}

// tree is what a view says of each pane: run, role, parent, name.
func tree(v *View) string {
	var l []string
	for _, m := range v.Members {
		l = append(l, fmt.Sprintf("%s=%s,%s,%s,%s,gone=%v,lost=%v", m.Pane, m.Run, m.Role, m.Parent, m.Name, m.ParentGone, m.Lost))
	}
	sort.Strings(l)
	return strings.Join(l, " ")
}

func treeRuns() []*Run {
	return []*Run{{
		ID: "r-0000000a", Coordinator: "%1", CoordinatorKey: "k-c", CoordinatorLive: true,
		Workers: []Worker{
			{ID: "%2", Name: "web", Key: "k-web", Live: true, Adopted: true},
			{ID: "%3", Name: "ops", Key: "k-ops", ParentKey: "k-web", Live: true},
		},
	}}
}

// The same tree whichever way stui has it: options only (an older stui, no
// keys), the records with nothing on the panes but their keys (a restart,
// before Rebind put the options back), or both.
func TestViewSameTreeFromOptionsOrRecords(t *testing.T) {
	options := strings.Join([]string{
		line("%1", "", "r-0000000a", "", RoleCoordinatorName),
		line("%2", "", "r-0000000a", "", RoleWorkerName, "%1", "web", "1"),
		line("%3", "", "r-0000000a", "", RoleWorkerName, "%2", "ops"),
		line("%4"), // no one's
	}, "\n")
	// After a restart the panes have other ids, and only their keys.
	keysOnly := strings.Join([]string{
		line("%7", "k-c"),
		line("%8", "k-web"),
		line("%9", "k-ops"),
		line("%1"), // took the coordinator's old number
	}, "\n")
	both := strings.Join([]string{
		line("%1", "k-c", "r-0000000a", "", RoleCoordinatorName),
		line("%2", "k-web", "r-0000000a", "", RoleWorkerName, "%1", "web", "1"),
		line("%3", "k-ops", "r-0000000a", "", RoleWorkerName, "%2", "ops"),
	}, "\n")

	want := "%1=r-0000000a,coordinator,,,gone=false,lost=false %2=r-0000000a,worker,%1,web,gone=false,lost=false %3=r-0000000a,worker,%2,ops,gone=false,lost=false"
	if got := tree(ReadView(options, nil)); got != want {
		t.Errorf("options only:\n got  %s\n want %s", got, want)
	}
	if got := tree(ReadView(both, treeRuns())); got != want {
		t.Errorf("both:\n got  %s\n want %s", got, want)
	}
	moved := strings.NewReplacer("%1", "%7", "%2", "%8", "%3", "%9").Replace(want)
	if got := tree(ReadView(keysOnly, treeRuns())); got != moved {
		t.Errorf("keys only, after a restart:\n got  %s\n want %s", got, moved)
	}
	if m := ReadView(keysOnly, treeRuns()).Of("%8"); m == nil || m.From != "record" || !m.Adopted {
		t.Errorf("%%8 from the record: %+v", m)
	}
}

// Where the record and the options differ, the record wins; where the
// record says nothing (an older stui's), the options fill it in.
func TestViewRecordWinsOptionsFill(t *testing.T) {
	runs := treeRuns()
	runs[0].Workers[0].Name = "web"
	out := strings.Join([]string{
		line("%1", "k-c", "r-0000000a", "", RoleCoordinatorName),
		line("%2", "k-web", "r-0000000a", "", RoleWorkerName, "%5", "frontend", "", "", "the task", "why it is here"),
	}, "\n")
	m := ReadView(out, runs).Of("%2")
	if m.Name != "web" || m.Parent != "%1" {
		t.Errorf("record lost to options: name %q parent %q", m.Name, m.Parent)
	}
	if m.Task != "the task" || m.About != "why it is here" {
		t.Errorf("options did not fill the record's empty fields: task %q about %q", m.Task, m.About)
	}
}

// A live worker no pane has the key of is lost; one whose parent's key is
// on no pane has its parent gone, not the coordinator for a parent; a
// worker on another machine is under its parent by key.
func TestViewLostAndRemote(t *testing.T) {
	runs := treeRuns()
	runs[0].Workers = append(runs[0].Workers,
		Worker{ID: "%40", Name: "lost", Key: "k-lost", Live: true},
		Worker{ID: "ci-mac/%7", Name: "sim", Machine: "ci-mac", Parent: "%2", ParentKey: "k-web"})
	out := strings.Join([]string{
		line("%7", "k-c"),
		line("%9", "k-ops"),
		line("%2", "k-api", "r-0000000d", "", RoleCoordinatorName), // took web's number
	}, "\n")
	v := ReadView(out, runs)
	got := tree(v)
	for _, want := range []string{
		"%9=r-0000000a,worker,,ops,gone=true,lost=false", // its parent web is gone
		"=r-0000000a,worker,,lost,gone=false,lost=true",
		"=r-0000000a,worker,,web,gone=false,lost=true",
		"ci-mac/%7=r-0000000a,worker,,sim,gone=true,lost=false", // not under %2, another run's
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %s in\n %s", want, got)
		}
	}
	if m := v.Of("%2"); m == nil || m.Run != "r-0000000d" || m.From != "options" {
		t.Errorf("%%2 is another run's: %+v", m)
	}
}

// A key in two runs (an agent taken into a new run, the old one put back):
// the run where it is live.
func TestViewKeyInTwoRuns(t *testing.T) {
	old := &Run{ID: "r-0000000b", Coordinator: "%1", CoordinatorKey: "k-c",
		Workers: []Worker{{ID: "%2", Name: "web-old", Key: "k-web", Live: false}}}
	runs := append(treeRuns(), old)
	out := strings.Join([]string{line("%1", "k-c"), line("%2", "k-web")}, "\n")
	if m := ReadView(out, runs).Of("%2"); m.Run != "r-0000000a" || m.Name != "web" {
		t.Errorf("%+v", m)
	}
}

// Many stui's writing one run at once lose none of their changes.
func TestUpdateLosesNothing(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	r := &Run{ID: NewID(), Coordinator: "%1"}
	r.Save()
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := Update(r.ID, func(r *Run) error {
				r.Workers = append(r.Workers, Worker{ID: fmt.Sprintf("%%%d", 100+i)})
				return nil
			}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	got, _ := Load(r.ID)
	if len(got.Workers) != 40 {
		t.Fatalf("%d of 40 changes kept", len(got.Workers))
	}
}
