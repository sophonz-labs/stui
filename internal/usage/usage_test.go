package usage

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func testEnv(t *testing.T) Env {
	d := t.TempDir()
	return Env{
		StateDir:  filepath.Join(d, "stui"),
		ClaudeDir: filepath.Join(d, "claude"),
		CodexDir:  filepath.Join(d, "codex"),
		Now:       func() time.Time { return now },
	}
}

// What Claude Code 2.1.80+ pipes to its status line, cut down.
const statusIn = `{"session_id":"abc","cwd":"/w","model":{"id":"claude-opus","display_name":"Opus"},
"cost":{"total_cost_usd":1.2},"rate_limits":{"five_hour":{"used_percentage":82.44,"resets_at":1791468000},
"seven_day":{"used_percentage":41,"resets_at":"1791900000"}}}`

func TestParseStatusline(t *testing.T) {
	ws := ParseStatusline([]byte(statusIn))
	if len(ws) != 2 {
		t.Fatalf("windows %+v", ws)
	}
	if ws[0] != (Window{ID: "five_hour", Kind: "session", Minutes: 300, UsedPercent: 82.4, ResetsAt: 1791468000000}) {
		t.Errorf("five_hour %+v", ws[0])
	}
	if ws[1].ID != "seven_day" || ws[1].Kind != "weekly" || ws[1].Minutes != 10080 || ws[1].ResetsAt != 1791900000000 {
		t.Errorf("seven_day %+v", ws[1])
	}
	// The usage endpoint's name for the number, and an ISO reset time.
	alt := ParseStatusline([]byte(`{"rate_limits":{"five_hour":{"utilization":120,"resets_at":"2026-10-08T15:00:00Z"}}}`))
	if len(alt) != 1 || alt[0].UsedPercent != 100 || alt[0].ResetsAt != time.Date(2026, 10, 8, 15, 0, 0, 0, time.UTC).UnixMilli() {
		t.Errorf("alt %+v", alt)
	}
	for _, in := range []string{`{"model":{}}`, `not json "rate_limits"`, `{"rate_limits":{}}`, `{"rate_limits":{"five_hour":{"resets_at":1}}}`} {
		if ws := ParseStatusline([]byte(in)); len(ws) != 0 {
			t.Errorf("%s: %+v", in, ws)
		}
	}
}

func TestKeepWritesOnChangeOrRefresh(t *testing.T) {
	e := testEnv(t)
	ws := ParseStatusline([]byte(statusIn))
	if err := Keep(e.StateDir, "", ws, now, nil); err != nil {
		t.Fatal(err)
	}
	path := claudeFile(e.StateDir, "")
	fi1, _ := os.Stat(path)
	b1, _ := os.ReadFile(path)
	// Same windows a minute later: not written.
	Keep(e.StateDir, "", ws, now.Add(time.Minute), nil)
	b2, _ := os.ReadFile(path)
	if !bytes.Equal(b1, b2) {
		t.Error("rewritten without a change")
	}
	// Six minutes later: written, so observedAt moves.
	Keep(e.StateDir, "", ws, now.Add(6*time.Minute), nil)
	var rec claudeRecord
	b3, _ := os.ReadFile(path)
	json.Unmarshal(b3, &rec)
	if rec.ObservedAt != now.Add(6*time.Minute).UnixMilli() {
		t.Errorf("observedAt %d", rec.ObservedAt)
	}
	if fi1.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi1.Mode())
	}
	// Another config directory is another file, named for it.
	Keep(e.StateDir, "/Users/x/.claude-work", ws, now, nil)
	if claudeFile(e.StateDir, "/Users/x/.claude-work") == path {
		t.Error("same file for another config dir")
	}
	rep := Read(e)
	if len(rep.Agents) != 2 || rep.Agents[0].Profile == rep.Agents[1].Profile {
		t.Errorf("agents %+v", rep.Agents)
	}
}

func TestReadStates(t *testing.T) {
	e := testEnv(t)
	// Neither agent here.
	if rep := Read(e); len(rep.Agents) != 0 {
		t.Errorf("agents %+v", rep.Agents)
	}
	os.MkdirAll(e.ClaudeDir, 0o755)
	os.MkdirAll(filepath.Join(e.CodexDir, "sessions"), 0o755)
	rep := Read(e)
	if len(rep.Agents) != 2 || rep.Agents[0].State != "setup" || rep.Agents[1].State != "waiting" {
		t.Fatalf("agents %+v", rep.Agents)
	}
	on := true
	e.ClaudeFeed = func() bool { return on }
	if rep := Read(e); rep.Agents[0].State != "waiting" {
		t.Errorf("feed on: %+v", rep.Agents[0])
	}
	Keep(e.StateDir, "", ParseStatusline([]byte(statusIn)), now, nil)
	rep = Read(e)
	if rep.Agents[0].State != "ok" || rep.Agents[0].Source != "statusline" || len(rep.Agents[0].Windows) != 2 {
		t.Errorf("claude %+v", rep.Agents[0])
	}
	on = false
	if rep := Read(e); rep.Agents[0].Source != "statusline-off" {
		t.Errorf("feed off: %+v", rep.Agents[0])
	}
	// Past its reset time a window reads 0 %.
	e.Now = func() time.Time { return time.UnixMilli(1791468000000).Add(time.Second) }
	rep = Read(e)
	if w := rep.Agents[0].Windows[0]; !w.Reset || w.UsedPercent != 0 {
		t.Errorf("after reset %+v", w)
	}
	if rep.Agents[0].Windows[1].Reset {
		t.Error("the weekly window has not reset")
	}
}

func codexRollout(t *testing.T, e Env, day, name string, mod time.Time, lines ...string) string {
	t.Helper()
	dir := filepath.Join(e.CodexDir, "sessions", day)
	os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(p, mod, mod)
	return p
}

func TestCodexLimits(t *testing.T) {
	e := testEnv(t)
	meta := `{"timestamp":"2026-10-08T10:00:00Z","type":"session_meta","payload":{"id":"x","cwd":"/w"}}`
	tc := func(ts, limits string) string {
		return `{"timestamp":"` + ts + `","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":` + limits + `}}`
	}
	// The newer file's last token_count wins; one with no usable window is passed over.
	codexRollout(t, e, "2026/10/07", "rollout-a.jsonl", now.Add(-3*time.Hour), meta,
		tc("2026-10-07T09:00:00Z", `{"primary":{"used_percent":10,"window_minutes":300,"resets_at":1791460000}}`))
	codexRollout(t, e, "2026/10/08", "rollout-b.jsonl", now.Add(-time.Hour), meta,
		tc("2026-10-08T10:00:00Z", `{"primary":{"used_percent":55.55,"window_minutes":299,"resets_at":1791462000},"secondary":{"used_percent":20,"window_minutes":10080,"resets_at":1791900000},"plan_type":"plus"}`),
		`{"timestamp":"2026-10-08T10:01:00Z","type":"event_msg","payload":{"type":"agent_message","message":"rate_limits are fine"}}`,
		tc("2026-10-08T10:02:00Z", `{"primary":{"used_percent":61,"window_minutes":299,"resets_at":1791462000},"secondary":{"used_percent":21,"window_minutes":10080,"resets_at":1791900000},"plan_type":"plus"}`))
	codexRollout(t, e, "2026/10/08", "rollout-c.jsonl", now.Add(-time.Minute), meta,
		tc("2026-10-08T11:59:00Z", `{"primary":null,"secondary":null}`))
	rep := Read(e)
	if len(rep.Agents) != 1 {
		t.Fatalf("agents %+v", rep.Agents)
	}
	a := rep.Agents[0]
	if a.State != "ok" || a.Plan != "Plus" || a.ObservedAt != time.Date(2026, 10, 8, 10, 2, 0, 0, time.UTC).UnixMilli() {
		t.Errorf("codex %+v", a)
	}
	want := []Window{
		{ID: "primary", Kind: "session", Minutes: 300, UsedPercent: 61, ResetsAt: 1791462000000},
		{ID: "secondary", Kind: "weekly", Minutes: 10080, UsedPercent: 21, ResetsAt: 1791900000000},
	}
	if len(a.Windows) != 2 || a.Windows[0] != want[0] || a.Windows[1] != want[1] {
		t.Errorf("windows %+v", a.Windows)
	}
	// An older build's resets_in_seconds counts from the event.
	e2 := testEnv(t)
	codexRollout(t, e2, "2026/10/08", "rollout-d.jsonl", now, tc("2026-10-08T11:00:00Z", `{"primary":{"used_percent":5,"window_minutes":300,"resets_in_seconds":600}}`))
	if w := Read(e2).Agents[0].Windows[0]; w.ResetsAt != time.Date(2026, 10, 8, 11, 10, 0, 0, time.UTC).UnixMilli() {
		t.Errorf("resets_in_seconds %+v", w)
	}
	// A session folder from long ago is not walked.
	e3 := testEnv(t)
	codexRollout(t, e3, "2025/01/01", "rollout-e.jsonl", now, tc("2025-01-01T11:00:00Z", `{"primary":{"used_percent":5,"window_minutes":300}}`))
	if a := Read(e3).Agents[0]; a.State != "waiting" {
		t.Errorf("old folder %+v", a)
	}
}

func TestAlerts(t *testing.T) {
	e := testEnv(t)
	rep := func(p5, p7 float64) Report {
		return Report{Agents: []Agent{{Agent: "claude", State: "ok", Windows: []Window{
			{ID: "five_hour", Minutes: 300, UsedPercent: p5, ResetsAt: now.Add(2 * time.Hour).UnixMilli()},
			{ID: "seven_day", Minutes: 10080, UsedPercent: p7, ResetsAt: now.Add(72 * time.Hour).UnixMilli()},
		}}}}
	}
	a := NewAlerts(e)
	if c := a.Check(rep(50, 10), now); len(c) != 0 {
		t.Errorf("below: %+v", c)
	}
	c := a.Check(rep(81, 10), now)
	if len(c) != 1 || c[0].Level != 80 || c[0].Window.ID != "five_hour" {
		t.Fatalf("80: %+v", c)
	}
	if c := a.Check(rep(85, 10), now); len(c) != 0 {
		t.Errorf("80 again: %+v", c)
	}
	// Across a restart too.
	a = NewAlerts(e)
	if c := a.Check(rep(90, 10), now); len(c) != 0 {
		t.Errorf("after restart: %+v", c)
	}
	if c := a.Check(rep(95, 97), now); len(c) != 2 || c[0].Level != 95 || c[1].Level != 95 {
		t.Errorf("95 and a jump: %+v", c)
	}
	// The weekly window jumped straight to 97: 80 was told with it.
	if c := a.Check(rep(96, 98), now); len(c) != 0 {
		t.Errorf("after the jump: %+v", c)
	}
	// A reset time a few seconds off is the same period.
	r := rep(96, 98)
	r.Agents[0].Windows[0].ResetsAt += 5000
	if c := a.Check(r, now); len(c) != 0 {
		t.Errorf("moved reset: %+v", c)
	}
	// The next period says it again.
	r = rep(82, 10)
	r.Agents[0].Windows[0].ResetsAt = now.Add(7 * time.Hour).UnixMilli()
	if c := a.Check(r, now.Add(5*time.Hour)); len(c) != 1 || c[0].Level != 80 {
		t.Errorf("next period: %+v", c)
	}
	// What reset is not told.
	r.Agents[0].Windows[0].Reset = true
	r.Agents[0].Windows[0].ResetsAt = now.Add(9 * time.Hour).UnixMilli()
	if c := a.Check(r, now); len(c) != 0 {
		t.Errorf("reset window: %+v", c)
	}
}

func TestService(t *testing.T) {
	e := testEnv(t)
	os.MkdirAll(e.ClaudeDir, 0o755)
	Keep(e.StateDir, "", ParseStatusline([]byte(statusIn)), now, nil)
	owner := true
	s := NewService(ServiceOptions{Env: e, IsOwner: func(*http.Request) bool { return owner }})
	get := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		if !s.ServeHTTP(w, httptest.NewRequest(method, path, nil)) {
			w.Code = 0
		}
		return w
	}
	w := get("GET", "/api/usage")
	var rep Report
	json.Unmarshal(w.Body.Bytes(), &rep)
	if w.Code != 200 || len(rep.Agents) != 1 || rep.Agents[0].Windows[0].UsedPercent != 82.4 || len(rep.Levels) != 2 {
		t.Errorf("%d %s", w.Code, w.Body)
	}
	if w := get("POST", "/api/usage"); w.Code != 405 {
		t.Errorf("POST %d", w.Code)
	}
	if w := get("GET", "/api/usagex"); w.Code != 0 {
		t.Errorf("other path answered %d", w.Code)
	}
	owner = false
	if w := get("GET", "/api/usage"); w.Code != 403 {
		t.Errorf("guest %d", w.Code)
	}
}

func TestRunStatuslineChains(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	e := testEnv(t)
	t.Setenv("CLAUDE_CONFIG_DIR", e.ClaudeDir)
	var out, errb bytes.Buffer
	// Nothing to chain to: prints nothing, exits 0, keeps the limits.
	if code := RunStatusline(e.StateDir, strings.NewReader(statusIn), &out, &errb); code != 0 || out.Len() != 0 {
		t.Fatalf("code %d out %q", code, out.String())
	}
	if rep := Read(e); len(rep.Agents) != 1 || rep.Agents[0].Profile != "claude" {
		t.Errorf("kept %+v", rep.Agents)
	}
	// The status line it took the place of gets the same input, and its
	// output and exit status are stui's.
	WriteChains(e.StateDir, map[string]Chain{filepath.Clean(e.ClaudeDir): {Command: `read -r x; printf 'HUD %s' "$(printf '%s' "$x" | head -c 13)"; exit 3`}})
	out.Reset()
	code := RunStatusline(e.StateDir, strings.NewReader(statusIn), &out, &errb)
	if code != 3 || out.String() != `HUD {"session_id"` {
		t.Errorf("code %d out %q err %q", code, out.String(), errb.String())
	}
}

func TestWeeklyAndModelWindows(t *testing.T) {
	in := `{"rate_limits":{"seven_day_sonnet":{"used_percentage":12,"resets_at":1791900000},"seven_day":{"used_percentage":61,"resets_at":1791900000},
"five_hour":{"used_percentage":42,"resets_at":1791468000},"seven_day_oauth_apps":{"used_percentage":1},
"spend_limit":{"used_percentage":30,"resets_at":1791900000,"period":"monthly","used_usd":3,"limit_usd":10},
"model_scoped":[{"display_name":"Fable","utilization":70,"resets_at":"2026-10-12T00:00:00Z"},{"display_name":"","utilization":1}]}}`
	ws := ParseStatusline([]byte(in))
	var got []string
	for _, w := range ws {
		got = append(got, w.ID+"/"+w.Kind+"/"+w.Label)
	}
	want := "five_hour/session/ seven_day/weekly/ seven_day_fable/weekly/Fable seven_day_sonnet/weekly/Sonnet spend_limit/spend/"
	if strings.Join(got, " ") != want {
		t.Errorf("got %v", got)
	}
	for _, w := range ws {
		switch w.ID {
		case "seven_day_fable":
			if w.Minutes != 10080 || w.UsedPercent != 70 || w.ResetsAt != time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC).UnixMilli() {
				t.Errorf("fable %+v", w)
			}
		case "spend_limit":
			if w.Minutes != 43200 {
				t.Errorf("spend %+v", w)
			}
		}
	}
	// Weekly limits say their level too.
	a := NewAlerts(testEnv(t))
	c := a.Check(Report{Agents: []Agent{{Agent: "claude", Windows: ws}}}, now)
	if len(c) != 0 {
		t.Errorf("nothing over 80 yet: %+v", c)
	}
	ws[1].UsedPercent = 81
	if c := a.Check(Report{Agents: []Agent{{Agent: "claude", Windows: ws}}}, now); len(c) != 1 || c[0].Window.ID != "seven_day" {
		t.Errorf("weekly at 81: %+v", c)
	}
}

func TestAccounts(t *testing.T) {
	d := t.TempDir()
	write := func(p, s string) {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(s), 0o600)
	}
	write(filepath.Join(d, ".claude.json"), `{"numStartups":3,"projects":{"/x":{}},"oauthAccount":{"accountUuid":"a-1","organizationUuid":"o-1",
"emailAddress":"yuna@example.com","organizationType":"claude_max","organizationRateLimitTier":"default_claude_max_20x","displayName":"Yuna"}}`)
	a := ClaudeAccount(d, "")
	if a == nil || len(a.ID) != 16 || a.Label != "y***@example.com" || a.Plan != "Max 20x" {
		t.Fatalf("claude %+v", a)
	}
	// Another config directory, the same account: the same id.
	write(filepath.Join(d, "work", ".claude.json"), `{"oauthAccount":{"accountUuid":"a-1","organizationUuid":"o-1","emailAddress":"yuna@example.com","organizationType":"claude_pro"}}`)
	if b := ClaudeAccount(d, filepath.Join(d, "work")); b == nil || b.ID != a.ID || b.Plan != "Pro" {
		t.Errorf("same account elsewhere %+v", b)
	}
	if ClaudeAccount(d, filepath.Join(d, "none")) != nil {
		t.Error("no file, no account")
	}
	write(filepath.Join(d, "api", ".claude.json"), `{"primaryApiKey":"x"}`)
	if ClaudeAccount(d, filepath.Join(d, "api")) != nil {
		t.Error("an API key has no account")
	}

	// Codex: the account id, and the address and plan from the ID token's claims.
	claims := base64URL(`{"email":"dev@example.org","https://api.openai.com/auth":{"chatgpt_account_id":"acc-9","chatgpt_plan_type":"plus"}}`)
	write(filepath.Join(d, "codex", "auth.json"), `{"auth_mode":"chatgpt","tokens":{"id_token":"eyJhbGciOiJub25lIn0.`+claims+`.sig","access_token":"secret","refresh_token":"secret","account_id":"acc-9"}}`)
	c := CodexAccount(filepath.Join(d, "codex"))
	if c == nil || len(c.ID) != 16 || c.ID == a.ID || c.Label != "d***@example.org" || c.Plan != "Plus" {
		t.Fatalf("codex %+v", c)
	}
	write(filepath.Join(d, "codex2", "auth.json"), `{"OPENAI_API_KEY":"sk-x","tokens":null}`)
	if CodexAccount(filepath.Join(d, "codex2")) != nil {
		t.Error("an API key has no account")
	}
	b, _ := json.Marshal(c)
	if strings.Contains(string(b), "secret") || strings.Contains(string(b), "dev@") || strings.Contains(string(b), "acc-9") {
		t.Errorf("leaks %s", b)
	}

	// In the report.
	e := testEnv(t)
	e.Home = d
	os.MkdirAll(e.ClaudeDir, 0o755)
	Keep(e.StateDir, "", ParseStatusline([]byte(statusIn)), now, func() *AccountInfo { return ClaudeAccount(d, "") })
	rep := Read(e)
	if rep.Agents[0].Account == nil || rep.Agents[0].Account.ID != a.ID || rep.Agents[0].Plan != "Max 20x" {
		t.Errorf("report %+v", rep.Agents[0])
	}
	// The alert is told once per account, whichever profile it came from.
	al := NewAlerts(e)
	r := Report{Agents: []Agent{
		{Agent: "claude", Account: a, Profile: "", Windows: []Window{{ID: "five_hour", UsedPercent: 90, ResetsAt: now.Add(time.Hour).UnixMilli()}}},
		{Agent: "claude", Account: a, Profile: "work", Windows: []Window{{ID: "five_hour", UsedPercent: 91, ResetsAt: now.Add(time.Hour).UnixMilli()}}},
	}}
	if c := al.Check(r, now); len(c) != 1 {
		t.Errorf("once per account: %+v", c)
	}
	if MaskEmail("x") != "" || MaskEmail("한글@도메인.kr") != "한***@도메인.kr" {
		t.Errorf("mask %q %q", MaskEmail("x"), MaskEmail("한글@도메인.kr"))
	}
}

func base64URL(s string) string {
	return strings.TrimRight(base64.URLEncoding.EncodeToString([]byte(s)), "=")
}

// ---- asking the providers (poll.go)
