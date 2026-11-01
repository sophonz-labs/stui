package orch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// askTmux is a tmux with the panes it was told of, recording every call.
type askTmux struct {
	mu    sync.Mutex
	calls [][]string
	panes map[string]map[string]string // id -> options
	next  int
}

func (f *askTmux) run(_ context.Context, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, args)
	target := func() string {
		for i, a := range args {
			if a == "-t" && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	switch args[0] {
	case "list-panes":
		var b strings.Builder
		for id, o := range f.panes {
			fmt.Fprintf(&b, "%s\t%s\n", id, o[OptAsk])
		}
		return b.String(), nil
	case "has-session":
		return "", nil
	case "new-window", "new-session":
		f.next++
		id := fmt.Sprintf("%%%d", 40+f.next)
		f.panes[id] = map[string]string{}
		return fmt.Sprintf("%s\t%d", id, 7+f.next), nil
	case "set-option":
		o := f.panes[target()]
		if o == nil {
			return "", fmt.Errorf("no pane")
		}
		o[args[len(args)-2]] = args[len(args)-1]
		return "", nil
	case "display-message":
		if f.panes[target()] == nil {
			return "", fmt.Errorf("no pane")
		}
		return "8\tdev", nil
	}
	return "", nil
}

func (f *askTmux) windows() (n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c[0] == "new-window" || c[0] == "new-session" {
			n++
		}
	}
	return n
}

type typed struct {
	mu    sync.Mutex
	lines map[string][]string
}

func (t *typed) deliver(_ context.Context, pane, text string) (int, map[string]any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines[pane] = append(t.lines[pane], text)
	return 200, map[string]any{"delivered": "transcript"}
}

func (t *typed) to(pane string) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.lines[pane]...)
}

// A run with worker api in pane %5, in its own worktree with one change.
func askRun(t *testing.T) (*Run, *Worktree) {
	t.Helper()
	dir := repo(t)
	w, err := MakeWorktree(dir, "r-0a0b0c0d", "api")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(w.Dir, "src", "a.txt"), []byte("one\nwhy\n"), 0o644)
	r := &Run{ID: "r-0a0b0c0d", Session: "dev", Created: Now(), Workers: []Worker{{ID: "%5", Name: "api", Agent: "claude", Worktree: w, Done: true, Summary: "added why"}}}
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	return r, w
}

func waitStage(t *testing.T, a *Adopter, pane string) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if s, why := a.AskStage(pane); s == "asked" {
			return
		} else if s == "failed" {
			t.Fatal(why)
		}
	}
	t.Fatal("the question was not typed")
}

// The worker is gone (no pane %5): its worktree is still asked, by its id,
// in a window of its own opened there; asked again, the same window.
func TestAskAWorktreeWhoseWorkerIsGone(t *testing.T) {
	_, w := askRun(t)
	tm := &askTmux{panes: map[string]map[string]string{}}
	ty := &typed{lines: map[string][]string{}}
	a := NewAdopter(AdoptOptions{Tmux: tm.run, Deliver: ty.deliver})

	res, code, err := a.Ask(context.Background(), AskRequest{Tree: "r-0a0b0c0d/api", Question: "why the second line?"})
	if err != nil || code != 200 || res.Reused || res.Pane == "" || res.Session != "dev" {
		t.Fatalf("%+v %d %v", res, code, err)
	}
	waitStage(t, a, res.Pane)
	var opened []string
	for _, c := range tm.calls {
		if c[0] == "new-window" {
			opened = c
		}
	}
	if !strings.Contains(strings.Join(opened, " "), "-c "+w.Dir) || opened[len(opened)-1] != "claude --permission-mode plan" {
		t.Fatalf("opened %q", opened)
	}
	if tm.panes[res.Pane][OptAsk] != "r-0a0b0c0d/api" {
		t.Fatalf("%v", tm.panes[res.Pane])
	}
	first := ty.to(res.Pane)
	if len(first) != 1 || !strings.Contains(first[0], res.File) || !strings.Contains(first[0], "why the second line?") || !strings.Contains(first[0], "from commit") {
		t.Fatalf("%q", first)
	}
	if b, _ := os.ReadFile(res.File); !strings.Contains(string(b), "+why") || !strings.Contains(string(b), "added why") {
		t.Fatalf("the file has no diff: %s", b)
	}

	again, _, err := a.Ask(context.Background(), AskRequest{Tree: "r-0a0b0c0d/api", Question: "and the test?"})
	if err != nil || !again.Reused || again.Pane != res.Pane {
		t.Fatalf("%+v %v", again, err)
	}
	for end := time.Now().Add(5 * time.Second); len(ty.to(res.Pane)) < 2 && time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
	}
	if n := tm.windows(); n != 1 {
		t.Fatalf("%d windows for one worktree", n)
	}
	if got := ty.to(res.Pane); len(got) != 2 || !strings.HasSuffix(got[1], "and the test?") {
		t.Fatalf("%q", got)
	}
}

// Asking while the worker works types nothing into the worker's pane and
// touches nothing of it, so its turn goes on; and the reading agent is not
// made a worker of the run.
func TestAskNeverTypesIntoTheWorker(t *testing.T) {
	r, _ := askRun(t)
	tm := &askTmux{panes: map[string]map[string]string{"%5": {"@stui-run": r.ID, "@stui-role": "worker"}}}
	ty := &typed{lines: map[string][]string{}}
	a := NewAdopter(AdoptOptions{Tmux: tm.run, Deliver: ty.deliver, State: func(string) (string, int64) { return "working", 1 }})

	res, _, err := a.Ask(context.Background(), AskRequest{Tree: "%5", Question: "what is it doing?"})
	if err != nil {
		t.Fatal(err)
	}
	waitStage(t, a, res.Pane)
	if got := ty.to("%5"); len(got) != 0 {
		t.Fatalf("typed into the worker: %q", got)
	}
	for _, c := range tm.calls {
		for _, arg := range c {
			if arg == "%5" {
				t.Fatalf("a call on the worker's pane: %q", c)
			}
		}
	}
	if o := tm.panes[res.Pane]; o["@stui-run"] != "" || o["@stui-role"] != "" {
		t.Fatalf("the reader was made a worker: %v", o)
	}
	if got, _ := Load(r.ID); len(got.Workers) != 1 {
		t.Fatalf("the run has %d workers", len(got.Workers))
	}
}

// A record from before ids is found by its branch; the routes give a
// worktree by id, what its diff is from, and who reads it.
func TestTreesByIDAndTheirRoutes(t *testing.T) {
	r, w := askRun(t)
	r.Workers[0].Worktree.ID = "" // as recorded before
	r.Save()
	if _, wk := FindTree("r-0a0b0c0d/api"); wk == nil || wk.Worktree.Path != w.Path {
		t.Fatal("not found by its branch")
	}
	if _, wk := FindTree("r-0a0b0c0d/nope"); wk != nil {
		t.Fatal("found one that is not there")
	}
	tm := &askTmux{panes: map[string]map[string]string{"%9": {OptAsk: "r-0a0b0c0d/api"}}}
	a := NewAdopter(AdoptOptions{Tmux: tm.run})

	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequest("GET", "/api/orch/trees", nil))
	var list struct{ Trees []Tree }
	json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != 200 || len(list.Trees) != 1 || list.Trees[0].ID != "r-0a0b0c0d/api" || list.Trees[0].Asker != "%9" || list.Trees[0].BaseKind != "commit" || list.Trees[0].Pane != "%5" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequest("GET", "/api/orch/diff?tree=r-0a0b0c0d/api", nil))
	var c Changes
	json.Unmarshal(rec.Body.Bytes(), &c)
	if rec.Code != 200 || c.Tree != "r-0a0b0c0d/api" || c.BaseKind != "commit" || !strings.HasPrefix(c.BaseLine, "from commit ") || !strings.Contains(c.Diff, "+why") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// A folder with no git: the diff says it is from stui's snapshot, and when.
func TestADiffSaysItIsFromASnapshot(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644)
	w, err := MakeWorktree(dir, "r-0a0b0c0d", "api")
	if err != nil {
		t.Fatal(err)
	}
	(&Run{ID: "r-0a0b0c0d", Created: Now(), Workers: []Worker{{ID: "%5", Name: "api", Worktree: w}}}).Save()
	a := NewAdopter(AdoptOptions{Tmux: (&askTmux{panes: map[string]map[string]string{}}).run})
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequest("GET", "/api/orch/diff?tree=r-0a0b0c0d/api", nil))
	var c Changes
	json.Unmarshal(rec.Body.Bytes(), &c)
	when := time.UnixMilli(w.Snapshot).Format("2006-01-02 15:04:05")
	if c.BaseKind != "snapshot" || c.Snapshot != w.Snapshot || c.BaseLine != "from stui's snapshot of "+w.Repo+" at "+when+" (not a git repository)" {
		t.Fatalf("%s", rec.Body)
	}
}

// Picking one of a fan removes the others' worktrees, and closes what was
// reading them with them; the picked one's reader stays, its worktree does.
func TestPickClosesTheReadersOfWhatItRemoves(t *testing.T) {
	dir := repo(t)
	r := &Run{ID: "r-0000aaaa", Created: Now()}
	for i, name := range []string{"fix-1", "fix-2"} {
		wt, err := MakeWorktree(dir, r.ID, name)
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(wt.Path, "src", name+".txt"), []byte("answer\n"), 0o644)
		r.Workers = append(r.Workers, Worker{ID: []string{"%11", "%12"}[i], Name: name, Agent: "claude", Fan: "fix", Worktree: wt})
	}
	r.Save()
	tm := &askTmux{panes: map[string]map[string]string{
		"%11": {}, "%12": {},
		"%31": {OptAsk: "r-0000aaaa/fix-1"},
		"%32": {OptAsk: "r-0000aaaa/fix-2"},
	}}
	a := NewAdopter(AdoptOptions{Tmux: tm.run})
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequest("POST", "/api/orch/pick", strings.NewReader(`{"pane":"%12","commit":true}`)))
	var got struct {
		Result PickResult `json:"result"`
	}
	json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != 200 || len(got.Result.Trees) != 1 || got.Result.Trees[0] != "r-0000aaaa/fix-1" || len(got.Result.Readers) != 1 || got.Result.Readers[0] != "%31" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var killed []string
	for _, c := range tm.calls {
		if c[0] == "kill-pane" {
			killed = append(killed, c[len(c)-1])
		}
	}
	if strings.Join(killed, ",") != "%11,%31" {
		t.Fatalf("closed %v", killed)
	}
}
