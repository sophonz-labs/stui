package orch

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestCheckName(t *testing.T) {
	for in, want := range map[string]string{
		"ios-ci":           "ios-ci",
		"iOS CI":           "ios-ci",
		"  simulator_ci  ": "simulator-ci",
		"-web-":            "web",
		"agent2":           "agent2",
	} {
		if got, err := CheckName(in); err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "   ", "%%", "한글", strings.Repeat("a", MaxNameLen+1)} {
		if got, err := CheckName(in); err == nil {
			t.Errorf("%q: %q, want refused", in, got)
		} else if ae, ok := err.(*AdoptError); !ok || ae.Reason != "bad-name" || ae.Status != 400 {
			t.Errorf("%q: %v", in, err)
		}
	}
}

// POST /api/orch/name: the worker that took "windows-ci" from a window
// title gets the name of what it does, and keeps it in its run's record.
// The window keeps its title.
func TestRenameWorker(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	f := newFake([3]string{"%1", "work", "lead"}, [3]string{"%2", "work", "Windows CI"}, [3]string{"%3", "work", "docs"})
	var mu sync.Mutex
	var cmds []string
	tmux := briefTmux(f)
	a := NewAdopter(AdoptOptions{
		Tmux: func(ctx context.Context, args ...string) (string, error) {
			mu.Lock()
			cmds = append(cmds, args[0])
			mu.Unlock()
			return tmux(ctx, args...)
		},
		Agents: func() map[string]string { return map[string]string{"%1": "claude", "%2": "claude", "%3": "codex"} },
	})
	ctx := context.Background()
	res, err := a.Adopt(ctx, "%1", "%2", AdoptHow{})
	if err != nil || res.Name != "windows-ci" {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := a.Adopt(ctx, "%1", "%3", AdoptHow{}); err != nil {
		t.Fatal(err)
	}
	post := func(body string) (int, map[string]any) {
		w := httptest.NewRecorder()
		a.ServeHTTP(w, httptest.NewRequest("POST", "/api/orch/name", strings.NewReader(body)))
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	mu.Lock()
	cmds = nil
	mu.Unlock()
	code, out := post(`{"pane":"%2","name":"iOS CI"}`)
	if code != 200 || out["name"] != "ios-ci" || out["was"] != "windows-ci" || out["id"] != "%2" {
		t.Fatalf("%d %v", code, out)
	}
	r, _ := Load(res.Run)
	if f.opts["%2"]["@stui-name"] != "ios-ci" || r.Worker("%2").Name != "ios-ci" {
		t.Fatalf("%v %+v", f.opts["%2"], r.Worker("%2"))
	}
	mu.Lock()
	for _, c := range cmds {
		if strings.Contains(c, "rename") || c == "set-window-option" {
			t.Errorf("stui touched the window: %v", cmds)
		}
	}
	mu.Unlock()
	if f.where["%2"][1] != "Windows CI" {
		t.Fatal("the window's title is the person's")
	}

	// Another worker's name: refused, whatever its case, and nothing changes.
	if code, out := post(`{"pane":"%2","name":"Docs"}`); code != 409 || out["reason"] != "name-taken" || !strings.Contains(out["error"].(string), "%3") {
		t.Fatalf("taken: %d %v", code, out)
	}
	if f.opts["%2"]["@stui-name"] != "ios-ci" {
		t.Fatal("changed after a refusal")
	}
	// Its own name again is no clash.
	if code, out := post(`{"pane":"%2","name":"ios-ci"}`); code != 200 || out["was"] != "ios-ci" {
		t.Fatalf("same: %d %v", code, out)
	}
	if code, out := post(`{"pane":"%1","name":"boss"}`); code != 409 || out["reason"] != "not-worker" {
		t.Fatalf("a coordinator: %d %v", code, out)
	}
	if code, out := post(`{"pane":"%2","name":"--"}`); code != 400 || out["reason"] != "bad-name" {
		t.Fatalf("bad: %d %v", code, out)
	}
	if code, _ := post(`{"pane":"nope","name":"x"}`); code != 400 {
		t.Fatalf("bad pane: %d", code)
	}

	// A restore puts back the name the record keeps, not the window's.
	if r.Worker("%2").Name != "ios-ci" {
		t.Fatal("record")
	}
	// Released, it is no one's and the label goes with it (adopt.go).
	if _, err := a.Release(ctx, "%2", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.opts["%2"]["@stui-name"]; ok {
		t.Fatalf("release keeps the name: %v", f.opts["%2"])
	}
}
