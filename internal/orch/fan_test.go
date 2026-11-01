package orch

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAFanIsComparedAndOnePicked(t *testing.T) {
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
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	// The diff and the fan, as the apps read them.
	rec := httptest.NewRecorder()
	if !ServeDiff(rec, httptest.NewRequest("GET", "/api/orch/diff?pane=%2512", nil), nil, nil) || rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var c Changes
	json.Unmarshal(rec.Body.Bytes(), &c)
	if c.ID != "%12" || c.Fan != "fix" || len(c.Members) != 2 || !strings.Contains(c.Status, "fix-2.txt") || !c.Members[1].Dirty {
		t.Fatalf("%+v", c)
	}
	// Picking without commit refuses changes not committed.
	if _, err := r.Pick("%12", false, false, nil); err == nil {
		t.Fatal("not committed")
	}
	var closed []string
	res, err := r.Pick("%12", true, false, func(p string) error { closed = append(closed, p); return nil })
	if err != nil || !res.Merge.Merged || len(res.Removed) != 1 || len(closed) != 1 || closed[0] != "%11" {
		t.Fatalf("%+v %v %v", res, err, closed)
	}
	if _, err := os.Stat(filepath.Join(dir, "src", "fix-2.txt")); err != nil {
		t.Fatal("the pick is merged")
	}
	if _, err := os.Stat(filepath.Join(dir, "src", "fix-1.txt")); err == nil {
		t.Fatal("the other is not")
	}
	r2, _ := Load(r.ID)
	if r2.Worker("%11").Worktree != nil {
		t.Fatal("the other's worktree is gone from the record")
	}
	// Over HTTP, an unknown pane is 404.
	rec = httptest.NewRecorder()
	ServeDiff(rec, httptest.NewRequest("POST", "/api/orch/pick", strings.NewReader(`{"pane":"%99"}`)), func(context.Context, string) error { return nil }, nil)
	if rec.Code != 404 {
		t.Fatal(rec.Code)
	}
}
