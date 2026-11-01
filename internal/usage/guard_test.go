package usage

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func weekly(used float64, now time.Time, toGo time.Duration) Window {
	return Window{ID: "seven_day", Kind: "weekly", Minutes: 10080, UsedPercent: used, ResetsAt: now.Add(toGo).UnixMilli()}
}

func TestJudge(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	week := 7 * 24 * time.Hour
	ok := func(ws ...Window) Agent {
		return Agent{Agent: "claude", State: "ok", ObservedAt: now.Add(-time.Minute).UnixMilli(), Windows: ws}
	}
	session := Window{ID: "five_hour", Kind: "session", Minutes: 300, UsedPercent: 40, ResetsAt: now.Add(2 * time.Hour).UnixMilli()}
	for _, c := range []struct {
		name         string
		a            Agent
		stop, nodata bool
		says         string
	}{
		// Half the week to go, 30 % left: under 50 - 10.
		{"pace", ok(weekly(70, now, week/2), session), true, false, "faster than the week"},
		// Half the week to go, 45 % left: within the margin.
		{"within pace", ok(weekly(55, now, week/2)), false, false, ""},
		// An hour to go, 8 % left: under the floor though on pace.
		{"floor", ok(weekly(92, now, time.Hour)), true, false, "nearly out"},
		{"reset since", ok(Window{Kind: "weekly", Minutes: 10080, Reset: true}), false, false, ""},
		{"no weekly", ok(session), false, true, "no weekly window"},
		{"no reset time", ok(Window{Kind: "weekly", Minutes: 10080, UsedPercent: 99}), false, true, "no reset time"},
		{"stale", Agent{Agent: "claude", State: "ok", ObservedAt: now.Add(-2 * time.Hour).UnixMilli(), Windows: []Window{weekly(99, now, time.Hour)}}, false, true, "old"},
		{"waiting", Agent{Agent: "claude", State: "waiting"}, false, true, "no reading"},
		// The tightest weekly window is judged.
		{"opus", ok(weekly(10, now, week/2), Window{Kind: "weekly", Label: "Opus", Minutes: 10080, UsedPercent: 95, ResetsAt: now.Add(week / 2).UnixMilli()}), true, false, ""},
	} {
		v := Judge(c.a, DefaultGuard, now)
		if v.Stop != c.stop || v.NoData != c.nodata || !strings.Contains(v.Reason, c.says) {
			t.Errorf("%s: stop %v nodata %v reason %q", c.name, v.Stop, v.NoData, v.Reason)
		}
	}
	// A stop names when the 5-hour window resets.
	if v := Judge(ok(weekly(70, now, week/2), session), DefaultGuard, now); !strings.Contains(v.Reason, "5-hour window is 40% used and resets") {
		t.Errorf("reason %q", v.Reason)
	}
}

func TestGuardSettings(t *testing.T) {
	e := Env{StateDir: t.TempDir()}
	if c := ReadGuard(e); c.On || c.Limits("claude") != DefaultGuard {
		t.Fatalf("no file: %+v", c)
	}
	os.MkdirAll(e.Dir(), 0o700)
	os.WriteFile(guardPath(e), []byte(`{"on":false,"agents":{"claude":{"m":0.2,"floor":0.05},"codex":{"m":-1,"floor":0}}}`), 0o600)
	if err := SetGuard(e, true); err != nil {
		t.Fatal(err)
	}
	c := ReadGuard(e)
	if !c.On || c.Limits("claude") != (GuardLimits{0.2, 0.05}) || c.Limits("codex") != DefaultGuard {
		t.Fatalf("%+v %+v %+v", c, c.Limits("claude"), c.Limits("codex"))
	}
}

func TestGuardFor(t *testing.T) {
	now := time.Now()
	e := Env{Home: t.TempDir(), StateDir: t.TempDir(), ClaudeDir: t.TempDir(), CodexDir: filepath.Join(t.TempDir(), "none"), Now: func() time.Time { return now }}
	os.MkdirAll(e.Dir(), 0o700)
	w := weekly(95, now, 24*time.Hour)
	b := `{"observedAt":` + strconv.FormatInt(now.UnixMilli(), 10) + `,"windows":[{"id":"seven_day","kind":"weekly","minutes":10080,"usedPercent":95,"resetsAt":` + strconv.FormatInt(w.ResetsAt, 10) + `}]}`
	os.WriteFile(claudeFile(e.StateDir, ""), []byte(b), 0o600)
	if v := GuardFor(e, GuardConfig{On: true}, "claude", ""); !v.Stop {
		t.Fatalf("default profile: %+v", v)
	}
	if v := GuardFor(e, GuardConfig{On: true}, "claude", "work"); !v.NoData {
		t.Fatalf("another profile: %+v", v)
	}
}

func TestGuardAPI(t *testing.T) {
	e := testEnv(t)
	os.MkdirAll(e.ClaudeDir, 0o755)
	Keep(e.StateDir, "", ParseStatusline([]byte(statusIn)), now, nil)
	owner := true
	s := NewService(ServiceOptions{Env: e, IsOwner: func(*http.Request) bool { return owner }})
	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		if !s.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body))) {
			w.Code = 0
		}
		return w
	}
	var got struct {
		Agents []Agent      `json:"agents"`
		Guard  *GuardReport `json:"guard"`
	}
	// Off by default, and every agent judged all the same.
	w := call("GET", "/api/usage", "")
	json.Unmarshal(w.Body.Bytes(), &got)
	if w.Code != 200 || len(got.Agents) != 1 || got.Guard == nil || got.Guard.On || len(got.Guard.Agents) != 1 ||
		got.Guard.Agents[0].M != 0.1 || !got.Guard.Agents[0].Guarded || got.Guard.Agents[0].State == "" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	// On, from an app.
	w = call("PUT", "/api/usage/guard", `{"on":true}`)
	var g GuardReport
	json.Unmarshal(w.Body.Bytes(), &g)
	if w.Code != 200 || !g.On || !ReadGuard(e).On {
		t.Fatalf("PUT on: %d %s", w.Code, w.Body)
	}
	if w := call("GET", "/api/usage/guard", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"on":true`) {
		t.Errorf("GET guard: %d %s", w.Code, w.Body)
	}
	for _, bad := range []string{"", "{}", `{"on":"yes"}`, "nope"} {
		if w := call("PUT", "/api/usage/guard", bad); w.Code != 400 {
			t.Errorf("PUT %q: %d", bad, w.Code)
		}
	}
	if w := call("POST", "/api/usage/guard", `{"on":false}`); w.Code != 405 {
		t.Errorf("POST: %d", w.Code)
	}
	// A read-only link can neither read nor turn it off.
	owner = false
	if w := call("PUT", "/api/usage/guard", `{"on":false}`); w.Code != 403 || !ReadGuard(e).On {
		t.Errorf("guest PUT: %d", w.Code)
	}
	if w := call("GET", "/api/usage/guard", ""); w.Code != 403 {
		t.Errorf("guest GET: %d", w.Code)
	}
}

func TestGuardStatusCodex(t *testing.T) {
	now := time.Now()
	rep := Report{Agents: []Agent{{Agent: "codex", State: "ok", ObservedAt: now.UnixMilli(), Windows: []Window{weekly(99, now, time.Hour)}}}}
	g := GuardStatus(GuardConfig{On: true}, rep, now)
	if a := g.Agents[0]; a.Guarded || a.State != "stop" || a.Left == nil || *a.Left > 0.02 {
		t.Fatalf("%+v", a)
	}
	rep.Agents[0].ObservedAt = now.Add(-3 * time.Hour).UnixMilli()
	if a := GuardStatus(GuardConfig{}, rep, now).Agents[0]; a.State != "nodata" || a.Left != nil || a.Reason == "" {
		t.Fatalf("stale: %+v", a)
	}
}
