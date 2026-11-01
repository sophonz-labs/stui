package hookclient

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sophonz-labs/stui/internal/usage"
)

func TestStopDecision(t *testing.T) {
	stop := func() (usage.Verdict, bool) {
		return usage.Verdict{Stop: true, Reason: "Claude's weekly limit is nearly out"}, true
	}
	pass := func() (usage.Verdict, bool) { return usage.Verdict{}, true }
	nodata := func() (usage.Verdict, bool) { return usage.Verdict{NoData: true}, true }
	off := func() (usage.Verdict, bool) { return usage.Verdict{Stop: true}, false }
	goal := func() string { return "make the tests pass" }
	none := func() string { return "" }
	loop := []byte(`{"hook_event_name":"Stop","stop_hook_active":true}`)
	first := []byte(`{"hook_event_name":"Stop","stop_hook_active":false}`)
	for _, c := range []struct {
		name  string
		kind  string
		event string
		raw   []byte
		goal  func() string
		guard func() (usage.Verdict, bool)
		want  map[string]any // nil: nothing printed
	}{
		{"guard stops a goal", "claude", "Stop", first, goal, stop, map[string]any{"continue": false}},
		{"guard stops /goal", "claude", "Stop", loop, none, stop, map[string]any{"continue": false}},
		{"goal goes on", "claude", "Stop", first, goal, pass, map[string]any{"decision": "block"}},
		{"no data: goal goes on", "claude", "Stop", loop, goal, nodata, map[string]any{"decision": "block"}},
		{"guard off: goal goes on", "claude", "Stop", loop, goal, off, map[string]any{"decision": "block"}},
		{"no data, no goal", "claude", "Stop", loop, none, nodata, nil},
		{"not a loop", "claude", "Stop", first, none, stop, nil},
		{"guard off, no goal", "claude", "Stop", loop, none, off, nil},
		{"not Stop", "claude", "StopFailure", loop, goal, stop, nil},
		{"not claude", "codex", "Stop", loop, goal, stop, nil},
		{"pi", "pi", "Stop", loop, goal, stop, nil},
	} {
		b := stopDecision(c.kind, c.event, c.raw, c.goal, c.guard)
		if c.want == nil {
			if b != nil {
				t.Errorf("%s: printed %s", c.name, b)
			}
			continue
		}
		var got map[string]any
		if json.Unmarshal(b, &got) != nil {
			t.Errorf("%s: %q", c.name, b)
			continue
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Errorf("%s: %s = %v, want %v (%s)", c.name, k, got[k], v, b)
			}
		}
		if c.want["decision"] == "block" && !strings.Contains(got["reason"].(string), "make the tests pass") {
			t.Errorf("%s: reason %q", c.name, got["reason"])
		}
		if c.want["continue"] == false && !strings.Contains(got["stopReason"].(string), "nearly out") {
			t.Errorf("%s: stopReason %q", c.name, got["stopReason"])
		}
	}
}

// The guard as the hook runs it: stui's usage files in its home, a weekly
// window nearly out, and none at all.
func TestHomeGuard(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	e := usage.DefaultEnv(h)
	if _, on := homeGuard(h)(); on {
		t.Fatal("on with no settings")
	}
	if err := usage.SetGuard(e, true); err != nil {
		t.Fatal(err)
	}
	if v, on := homeGuard(h)(); !on || !v.NoData || v.Stop {
		t.Fatalf("no usage: %+v", v)
	}
	now := time.Now()
	rec := `{"observedAt":` + strconv.FormatInt(now.UnixMilli(), 10) + `,"windows":[` +
		`{"id":"five_hour","kind":"session","minutes":300,"usedPercent":20,"resetsAt":` + strconv.FormatInt(now.Add(3*time.Hour).UnixMilli(), 10) + `},` +
		`{"id":"seven_day","kind":"weekly","minutes":10080,"usedPercent":96,"resetsAt":` + strconv.FormatInt(now.Add(30*time.Hour).UnixMilli(), 10) + `}]}`
	os.WriteFile(filepath.Join(e.Dir(), "claude-default.json"), []byte(rec), 0o600)
	v, on := homeGuard(h)()
	if !on || !v.Stop {
		t.Fatalf("nearly out: %+v", v)
	}
	b := stopDecision("claude", "Stop", []byte(`{"stop_hook_active":true}`), func() string { return "" }, homeGuard(h))
	if !strings.Contains(string(b), `"continue":false`) || !strings.Contains(string(b), "5-hour window") {
		t.Fatalf("%s", b)
	}
}

func TestReadGoal(t *testing.T) {
	f := filepath.Join(t.TempDir(), "g.md")
	if ReadGoal(f) != "" || ReadGoal("") != "" {
		t.Fatal("no file")
	}
	os.WriteFile(f, []byte("  ship it\n"), 0o600)
	if g := ReadGoal(f); g != "ship it" {
		t.Fatalf("%q", g)
	}
}
