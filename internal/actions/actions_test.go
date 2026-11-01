package actions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseRemote(t *testing.T) {
	cases := []struct {
		in   string
		want Repo
		ok   bool
	}{
		{"https://github.com/acme/mobile-app.git", Repo{"github.com", "acme", "mobile-app"}, true},
		{"https://github.com/acme/mobile-app", Repo{"github.com", "acme", "mobile-app"}, true},
		{"git@github.com:acme/mobile-app.git", Repo{"github.com", "acme", "mobile-app"}, true},
		{"ssh://git@ssh.github.com:443/a/b.git", Repo{"github.com", "a", "b"}, true},
		{"https://user:tok@GitHub.com/a/b/", Repo{"github.com", "a", "b"}, true},
		{"git@ghe.corp.io:team/app.git", Repo{"ghe.corp.io", "team", "app"}, true},
		{"https://gitlab.com/group/sub/proj.git", Repo{}, false},
		{"/srv/git/repo.git", Repo{}, false},
		{`C:\repos\app`, Repo{}, false},
		{"file:///tmp/x/y", Repo{}, false},
	}
	for _, c := range cases {
		got, ok := ParseRemote(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("%q: got %+v %v, want %+v %v", c.in, got, ok, c.want, c.ok)
		}
	}
	if (Repo{"ghe.corp.io", "a", "b"}).Arg() != "ghe.corp.io/a/b" || (Repo{"github.com", "a", "b"}).Arg() != "a/b" {
		t.Error("Arg")
	}
}

func TestParseLog(t *testing.T) {
	log := "build\tRun tests\t\ufeff2026-10-14T09:07:49.8777100Z ##[group]Run go test\n" +
		"build\tRun tests\t2026-10-14T09:07:50.1Z \x1b[31mFAIL\x1b[0m pkg\r\n" +
		"build\tRun tests\t2026-10-14T09:07:51Z ##[error]Process completed with exit code 1.\n"
	lines, cut := ParseLog([]byte(log), 10)
	if cut || len(lines) != 3 {
		t.Fatalf("%v %+v", cut, lines)
	}
	if lines[0] != (LogLine{"build", "Run tests", "##[group]Run go test"}) || lines[1].Text != "FAIL pkg" {
		t.Errorf("%+v", lines)
	}
	lines, cut = ParseLog([]byte(log), 2)
	if !cut || len(lines) != 2 || !strings.Contains(lines[1].Text, "exit code 1") {
		t.Errorf("tail: %v %+v", cut, lines)
	}
	if l, _ := ParseLog(nil, 5); len(l) != 0 {
		t.Error("empty")
	}
}

// fake answers git and gh by their arguments.
type fake struct {
	mu    sync.Mutex
	calls []string
	git   map[string]string // joined args -> stdout; absent fails
	// Per folder, when set: dir -> joined args -> stdout; a folder absent
	// is in no checkout.
	gitAt map[string]map[string]string
	gh    map[string]fakeOut
}

type fakeOut struct {
	out  string
	code int
	err  string
}

func (f *fake) Run(ctx context.Context, dir, bin string, max int, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := strings.Join(args, " ")
	f.calls = append(f.calls, bin+" "+key)
	if bin == "git" {
		key = strings.TrimPrefix(key, "-c core.fsmonitor=false --no-pager ")
		git := f.git
		if f.gitAt != nil {
			git = f.gitAt[dir]
		}
		if out, ok := git[key]; ok {
			return []byte(out), nil
		}
		return nil, &ExitError{Code: 128, Stderr: "fatal: not a git repository"}
	}
	// The longest prefix that matches: ".../runs/41/jobs" before ".../runs/41".
	best, found := "", false
	for prefix := range f.gh {
		if strings.HasPrefix(key, prefix) && len(prefix) >= len(best) {
			best, found = prefix, true
		}
	}
	if found {
		o := f.gh[best]
		if o.code != 0 {
			return []byte(o.out), &ExitError{Code: o.code, Stderr: o.err}
		}
		return []byte(o.out), nil
	}
	return nil, &ExitError{Code: 1, Stderr: "unexpected: " + key}
}

func (f *fake) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func checkout() map[string]string {
	return map[string]string{
		"rev-parse --show-toplevel":            "/src/app",
		"symbolic-ref --quiet --short HEAD":    "feature/x",
		"config --get branch.feature/x.remote": "origin",
		"remote":                               "upstream\norigin",
		"remote get-url -- origin":             "git@github.com:me/app.git",
		"remote get-url -- upstream":           "https://github.com/org/app.git",
	}
}

const runList = `[{"databaseId":42,"number":7,"attempt":1,"workflowName":"CI","displayTitle":"Fix login","headBranch":"feature/x","headSha":"c75f7fafb374ef42","event":"push","status":"in_progress","conclusion":"","createdAt":"2026-10-14T19:54:00Z","startedAt":"2026-10-14T19:54:05Z","updatedAt":"2026-10-14T19:55:00Z","url":"https://github.com/me/app/actions/runs/42"}]`

// The REST answers for run 41: the run, and its one job with a step not
// started.
const runView = `{"id":41,"run_number":6,"run_attempt":2,"name":"CI","path":".github/workflows/ci.yml","display_title":"Add x","head_branch":"main","head_sha":"abc","event":"pull_request","status":"completed","conclusion":"failure","created_at":"2026-10-14T19:00:00Z","run_started_at":"2026-10-14T19:00:01Z","updated_at":"2026-10-14T19:05:00Z","html_url":"u"}`

const runJobs = `{"total_count":1,"jobs":[{"id":9,"run_id":41,"name":"test","status":"completed","conclusion":"failure","started_at":"2026-10-14T19:00:02Z","completed_at":"2026-10-14T19:04:00Z","html_url":"ju","runner_name":"my-runner-x64-wnc6t-runner-zps27","runner_group_name":"default","labels":["my-runner-x64"],"steps":[{"name":"Set up job","number":1,"status":"completed","conclusion":"success","started_at":"2026-10-14T19:00:02Z","completed_at":"2026-10-14T19:00:03Z"},{"name":"Later","number":2,"status":"pending","conclusion":null,"started_at":null,"completed_at":null}]}]}`

func serve(t *testing.T, f *fake, gh string, method, target, body string) (int, map[string]any) {
	t.Helper()
	s := NewService(ServiceOptions{
		PaneCwd: func(ctx context.Context, pane string) (string, bool) { return "/src/app/web", pane == "%3" },
		Actions: &Actions{Runner: f, Gh: gh},
	})
	return do(t, s, method, target, body)
}

func do(t *testing.T, s *Service, method, target, body string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	w := httptest.NewRecorder()
	if !s.ServeHTTP(w, r) {
		t.Fatalf("%s not served", target)
	}
	var m map[string]any
	json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

func TestListRuns(t *testing.T) {
	f := &fake{git: checkout(), gh: map[string]fakeOut{
		"auth status --hostname github.com": {},
		"run list -R me/app -L 30":          {out: runList},
	}}
	code, m := serve(t, f, "gh", "GET", "/api/actions?pane=%253", "")
	if code != 200 || m["state"] != "ok" || m["repo"] != "me/app" || m["branch"] != "feature/x" || m["url"] != "https://github.com/me/app/actions" {
		t.Fatalf("%d %v", code, m)
	}
	runs := m["runs"].([]any)
	r := runs[0].(map[string]any)
	if r["id"].(float64) != 42 || r["workflow"] != "CI" || r["title"] != "Fix login" || r["sha"] != "c75f7fa" || r["status"] != "in_progress" {
		t.Errorf("%v", r)
	}
	if _, ok := r["conclusion"]; ok {
		t.Error("empty conclusion should be left out")
	}
	// Kept five seconds: a second ask runs no gh.
	s := NewService(ServiceOptions{
		PaneCwd: func(ctx context.Context, pane string) (string, bool) { return "/src/app", true },
		Actions: &Actions{Runner: f, Gh: "gh"},
	})
	do(t, s, "GET", "/api/actions?pane=%253", "")
	do(t, s, "GET", "/api/actions?pane=%253", "")
	if n := f.count("gh run list"); n != 2 {
		t.Errorf("gh run list ran %d times, want 2 (one per Actions)", n)
	}
	if n := f.count("gh auth status"); n != 2 {
		t.Errorf("auth asked %d times", n)
	}
}

func TestBranchFilter(t *testing.T) {
	f := &fake{git: checkout(), gh: map[string]fakeOut{
		"auth status": {},
		"run list -R me/app -L 5 --json " + runFields + " -b main": {out: "[]"},
	}}
	code, m := serve(t, f, "gh", "GET", "/api/actions?pane=%253&branch=main&limit=5", "")
	if code != 200 || len(m["runs"].([]any)) != 0 {
		t.Fatalf("%d %v", code, m)
	}
	if code, _ := serve(t, f, "gh", "GET", "/api/actions?pane=%253&branch=--evil", ""); code != 400 {
		t.Errorf("flag branch: %d", code)
	}
}

func TestStates(t *testing.T) {
	// No checkout.
	f := &fake{git: map[string]string{}, gh: map[string]fakeOut{}}
	if _, m := serve(t, f, "gh", "GET", "/api/actions?pane=%253", ""); m["state"] != StateNotGit {
		t.Errorf("not-git: %v", m)
	}
	// A GitLab checkout.
	g := checkout()
	g["remote get-url -- origin"] = "git@gitlab.com:me/app.git"
	g["remote get-url -- upstream"] = "https://gitlab.com/org/app.git"
	f = &fake{git: g, gh: map[string]fakeOut{"auth status": {code: 1, err: "not logged in to gitlab.com"}}}
	if _, m := serve(t, f, "gh", "GET", "/api/actions?pane=%253", ""); m["state"] != StateNotGitHub {
		t.Errorf("not-github: %v", m)
	}
	// gh missing.
	f = &fake{git: checkout()}
	a := &Actions{Runner: f}
	a.ghAt, a.ghBin = time.Now(), ""
	s := NewService(ServiceOptions{PaneCwd: func(context.Context, string) (string, bool) { return "/src/app", true }, Actions: a})
	if _, m := do(t, s, "GET", "/api/actions?pane=%253", ""); m["state"] != StateNoGh || m["repo"] != "me/app" {
		t.Errorf("no-gh: %v", m)
	}
	// Signed out.
	f = &fake{git: checkout(), gh: map[string]fakeOut{"auth status": {code: 1, err: "You are not logged into any GitHub hosts. To log in, run: gh auth login\nmore"}}}
	_, m := serve(t, f, "gh", "GET", "/api/actions?pane=%253", "")
	if m["state"] != StateNoAuth || !strings.HasPrefix(m["detail"].(string), "You are not logged") {
		t.Errorf("no-auth: %v", m)
	}
	// Detail of a run needs a readable repository.
	if code, m := serve(t, f, "gh", "GET", "/api/actions/run?pane=%253&id=41", ""); code != 409 || m["state"] != StateNoAuth {
		t.Errorf("run without auth: %d %v", code, m)
	}
}

func TestRunDetailAndLog(t *testing.T) {
	f := &fake{git: checkout(), gh: map[string]fakeOut{
		"auth status": {},
		"api --hostname github.com -X GET repos/me/app/actions/runs/41":      {out: runView},
		"api --hostname github.com -X GET repos/me/app/actions/runs/41/jobs": {out: runJobs},
		"run view 41 -R me/app --log-failed --job 9":                         {out: "test\tgo test\t2026-10-14T19:03:00Z FAIL\n"},
		"run view 42 -R me/app --log-failed":                                 {code: 1, err: "run 42 is still in progress; logs will be available when it is complete"},
		"api --hostname github.com -X GET repos/me/app/actions/runs/43":      {code: 1, err: "gh: Not Found (HTTP 404)"},
	}}
	code, m := serve(t, f, "gh", "GET", "/api/actions/run?pane=%253&id=41", "")
	if code != 200 {
		t.Fatalf("%d %v", code, m)
	}
	run := m["run"].(map[string]any)
	jobs := run["jobs"].([]any)
	steps := jobs[0].(map[string]any)["steps"].([]any)
	if run["conclusion"] != "failure" || len(steps) != 2 {
		t.Fatalf("%v", run)
	}
	if _, ok := steps[1].(map[string]any)["startedAt"]; ok {
		t.Error("a step not started has a time")
	}
	// What the jobs answer adds, and what was there before stays.
	j := jobs[0].(map[string]any)
	if r := j["runner"].(map[string]any); r["kind"] != "self-hosted" || r["name"] != "my-runner-x64-wnc6t-runner-zps27" {
		t.Errorf("runner %v", r)
	}
	if j["id"].(float64) != 9 || j["url"] != "ju" || run["number"].(float64) != 6 || run["workflow"] != "CI" || run["sha"] != "abc" {
		t.Errorf("fields the phones read: %v", run)
	}
	code, m = serve(t, f, "gh", "GET", "/api/actions/log?pane=%253&id=41&job=9", "")
	if code != 200 || m["lines"].([]any)[0].(map[string]any)["text"] != "FAIL" {
		t.Errorf("log: %d %v", code, m)
	}
	if code, m := serve(t, f, "gh", "GET", "/api/actions/log?pane=%253&id=42", ""); code != 409 || m["state"] != "in-progress" {
		t.Errorf("in progress: %d %v", code, m)
	}
	if code, _ := serve(t, f, "gh", "GET", "/api/actions/run?pane=%253&id=43", ""); code != 404 {
		t.Errorf("missing run: %d", code)
	}
	for _, bad := range []string{"id=-1", "id=4x", "id=", "id=1&job=x"} {
		if code, _ := serve(t, f, "gh", "GET", "/api/actions/log?pane=%253&"+bad, ""); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
}

func TestRerunCancel(t *testing.T) {
	f := &fake{git: checkout(), gh: map[string]fakeOut{
		"auth status":                     {},
		"run rerun 41 -R me/app --failed": {},
		"run cancel 42 -R me/app":         {},
	}}
	if code, _ := serve(t, f, "gh", "POST", "/api/actions/rerun", `{"pane":"%3","id":41,"failed":true}`); code != 200 {
		t.Errorf("rerun %d", code)
	}
	if code, _ := serve(t, f, "gh", "POST", "/api/actions/cancel", `{"pane":"%3","id":42}`); code != 200 {
		t.Errorf("cancel %d", code)
	}
	if code, _ := serve(t, f, "gh", "GET", "/api/actions/cancel?pane=%253&id=42", ""); code != 405 {
		t.Errorf("GET cancel %d", code)
	}
}

func TestGates(t *testing.T) {
	f := &fake{}
	if code, _ := serve(t, f, "gh", "GET", "/api/actions?pane=3", ""); code != 400 {
		t.Errorf("bad pane %d", code)
	}
	if code, _ := serve(t, f, "gh", "GET", "/api/actions?pane=%259", ""); code != 404 {
		t.Errorf("no pane %d", code)
	}
	s := NewService(ServiceOptions{IsOwner: func(*http.Request) bool { return false }, PaneCwd: func(context.Context, string) (string, bool) { return "/x", true }})
	if code, _ := do(t, s, "GET", "/api/actions?pane=%253", ""); code != 403 {
		t.Errorf("guest %d", code)
	}
	s = NewService(ServiceOptions{})
	if s.Serves() {
		t.Error("serves without panes")
	}
	if code, _ := do(t, s, "GET", "/api/actions?pane=%253", ""); code != 404 {
		t.Errorf("no mux %d", code)
	}
	r := httptest.NewRequest("GET", "/api/actionsx", nil)
	if s.ServeHTTP(httptest.NewRecorder(), r) {
		t.Error("served another path")
	}
}
