package orch

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// fakeTmux keeps pane options and answers the commands adopting runs.
type fakeTmux struct {
	mu    sync.Mutex
	order []string
	where map[string][2]string // id -> session, window name
	opts  map[string]map[string]string
}

func newFake(panes ...[3]string) *fakeTmux {
	f := &fakeTmux{where: map[string][2]string{}, opts: map[string]map[string]string{}}
	for _, p := range panes {
		f.order = append(f.order, p[0])
		f.where[p[0]] = [2]string{p[1], p[2]}
		f.opts[p[0]] = map[string]string{}
	}
	return f
}

func (f *fakeTmux) run(_ context.Context, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case args[0] == "list-panes" && args[len(args)-1] == rebindFormat:
		var b strings.Builder
		for _, id := range f.order {
			o := f.opts[id]
			fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", id, o[OptKey], o["@stui-run"], o["@stui-role"], o["@stui-parent"], o[OptInRun], f.where[id][0], f.where[id][1])
		}
		return b.String(), nil
	case args[0] == "list-panes" && args[len(args)-1] == mailFormat:
		var b strings.Builder
		for _, id := range f.order {
			o := f.opts[id]
			fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", id, o["@stui-run"], o[OptInRun], o["@stui-role"], o["@stui-parent"], o["@stui-name"], o["@stui-done"], o["@stui-sent"], o["@stui-adopted"])
		}
		return b.String(), nil
	case args[0] == "list-panes" && args[len(args)-1] == AnchorFormat:
		var b strings.Builder
		for _, id := range f.order {
			o := f.opts[id]
			fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", id, o[OptKey], o["@stui-run"], o[OptInRun])
		}
		return b.String(), nil
	case args[0] == "list-panes":
		var b strings.Builder
		for i, id := range f.order {
			o := f.opts[id]
			fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n", id, f.where[id][0], o["@stui-run"], o["@stui-role"], o["@stui-parent"], o["@stui-name"], o["@stui-adopted"], i, o["@stui-in-run"], f.where[id][1])
		}
		return b.String(), nil
	case args[0] == "set-option" && args[1] == "-p":
		f.opts[args[3]][args[4]] = args[5]
	case args[0] == "set-option" && args[1] == "-pu":
		delete(f.opts[args[3]], args[4])
	case args[0] == "show-options":
		return f.opts[args[3]][args[4]], nil
	}
	return "", nil
}

func TestAdoptAndRelease(t *testing.T) {
	t.Setenv("STUI_HOME", t.TempDir())
	f := newFake([3]string{"%1", "work", "lead"}, [3]string{"%2", "work", "API tests"}, [3]string{"%3", "other", "docs"}, [3]string{"%4", "work", "zsh"})
	var told []string
	var mu sync.Mutex
	a := NewAdopter(AdoptOptions{
		Tmux:   f.run,
		Agents: func() map[string]string { return map[string]string{"%1": "claude", "%2": "claude", "%3": "codex"} },
		Deliver: func(_ context.Context, pane, text string) (int, map[string]any) {
			mu.Lock()
			told = append(told, pane+" "+text)
			mu.Unlock()
			return 200, map[string]any{"delivered": true}
		},
	})
	ctx := context.Background()

	res, err := a.Adopt(ctx, "%1", "%2", AdoptHow{Tell: true})
	if err != nil {
		t.Fatal(err)
	}
	if f.opts["%2"]["@stui-parent"] != "%1" || f.opts["%2"]["@stui-role"] != "worker" || f.opts["%2"]["@stui-name"] != "api-tests" || f.opts["%2"]["@stui-adopted"] != "1" {
		t.Fatalf("child options: %v", f.opts["%2"])
	}
	if f.opts["%1"]["@stui-role"] != "coordinator" || f.opts["%1"]["@stui-run"] != res.Run {
		t.Fatalf("parent options: %v", f.opts["%1"])
	}
	if res.Told["%1"] != "ok" || res.Told["%2"] != "ok" || len(told) != 2 {
		t.Fatalf("told: %v %v", res.Told, told)
	}
	r, err := Load(res.Run)
	if err != nil || r.Worker("%2") == nil || !r.Worker("%2").Adopted {
		t.Fatalf("run record: %v %+v", err, r)
	}

	// Another session's agent, under the worker: two levels, allowed.
	if _, err := a.Adopt(ctx, "%2", "%3", AdoptHow{Tell: false}); err != nil {
		t.Fatal(err)
	}
	// The top under its own worker's worker: round.
	if _, err := a.Adopt(ctx, "%3", "%1", AdoptHow{Tell: false}); err == nil || err.(*AdoptError).Reason != "loop" {
		t.Fatalf("loop: %v", err)
	}
	// A shell is no agent.
	if _, err := a.Adopt(ctx, "%1", "%4", AdoptHow{Tell: false}); err == nil || err.(*AdoptError).Reason != "child-not-agent" {
		t.Fatalf("shell: %v", err)
	}
	// Three levels: too deep.
	f.where["%5"] = [2]string{"work", "five"}
	f.order = append(f.order, "%5")
	f.opts["%5"] = map[string]string{}
	a.o.Agents = func() map[string]string {
		return map[string]string{"%1": "claude", "%2": "claude", "%3": "codex", "%5": "claude"}
	}
	if _, err := a.Adopt(ctx, "%3", "%5", AdoptHow{Tell: false}); err == nil || err.(*AdoptError).Reason != "too-deep" {
		t.Fatalf("depth: %v", err)
	}

	// Let %2 go: no parent, and since it directs %3 it goes on doing that.
	if _, err := a.Release(ctx, "%2", false); err != nil {
		t.Fatal(err)
	}
	if f.opts["%2"]["@stui-parent"] != "" || f.opts["%2"]["@stui-adopted"] != "" || f.opts["%2"]["@stui-role"] != "coordinator" {
		t.Fatalf("released: %v", f.opts["%2"])
	}
	if r, _ := Load(res.Run); r.Worker("%2") != nil {
		t.Fatal("still in the run record")
	}
	// %1 was a coordinator only because it adopted %2: with no worker left
	// it is a plain agent again.
	if f.opts["%1"]["@stui-role"] != "" || f.opts["%1"]["@stui-run"] != "" {
		t.Fatalf("the parent kept its run: %v", f.opts["%1"])
	}
	// One with workers of its own still coordinates.
	if f.opts["%3"]["@stui-parent"] != "%2" {
		t.Fatalf("%%3: %v", f.opts["%3"])
	}
}

// renumber is a tmux server handing pane from's place to id to.
func (f *fakeTmux) renumber(from, to string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, id := range f.order {
		if id == from {
			f.order[i] = to
		}
	}
	f.where[to], f.opts[to] = f.where[from], f.opts[from]
	delete(f.where, from)
	delete(f.opts, from)
}
