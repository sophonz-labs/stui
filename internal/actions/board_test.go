package actions

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseWorkflowRunsOn(t *testing.T) {
	cases := []struct {
		name, src, wantName string
		want                []string
	}{
		{"a string", "name: CI\non: push\njobs:\n  test:\n    runs-on: my-runner-x64\n    steps: []\n", "CI", []string{KindSelfHosted}},
		{"a flow list", "name: \"iOS # app\"\njobs:\n  build:\n    runs-on: [self-hosted, macOS] # the mini\n", "iOS # app", []string{KindSelfHosted}},
		{"an expression", "jobs:\n  t:\n    strategy:\n      matrix:\n        os: [ubuntu-latest]\n    runs-on: ${{ matrix.os }}\n", "", []string{KindUnknown}},
		{"a block list", "jobs:\n  b:\n    runs-on:\n      - self-hosted\n      - Windows\n", "", []string{KindSelfHosted}},
		{"hosted, two jobs", "name: Lint\njobs:\n  a:\n    runs-on: ubuntu-latest\n  b:\n    name: other\n    runs-on: 'macos-14'\n", "Lint", []string{KindHosted}},
		{"mixed", "jobs:\n  a:\n    runs-on: ubuntu-24.04\n  b:\n    runs-on: my-runner-arm64\n", "", []string{KindHosted, KindSelfHosted}},
		{"a group only", "jobs:\n  a:\n    runs-on:\n      group: big-runners\n", "", []string{KindUnknown}},
		{"a group with labels", "jobs:\n  a:\n    runs-on:\n      group: ours\n      labels: [my-runner-x64]\n", "", []string{KindSelfHosted}},
		{"a reusable workflow has no runs-on", "jobs:\n  call:\n    uses: ./.github/workflows/x.yml\n", "", []string{}},
		// A step's `runs-on:` inside `with:` is not the job's.
		{"deeper keys are not the job's", "jobs:\n  a:\n    runs-on: ubuntu-latest\n    steps:\n      - with:\n          runs-on: arc\n", "", []string{KindHosted}},
	}
	for _, c := range cases {
		name, jobs := parseWorkflow(c.src)
		if got := runnerKinds(jobs); name != c.wantName || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: name %q runners %v, want %q %v", c.name, name, got, c.wantName, c.want)
		}
	}
}

func TestReadWorkflowsCaps(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".github", "workflows")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "b.yaml"), []byte("name: B\njobs:\n  x:\n    runs-on: ubuntu-latest\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "a.yml"), []byte("jobs:\n  x:\n    runs-on: my-runner-x64\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.md"), []byte("not a workflow"), 0o644)
	os.WriteFile(filepath.Join(dir, "huge.yml"), make([]byte, MaxWorkflowBytes+1), 0o644)
	got := ReadWorkflows(root)
	want := []Workflow{
		{File: "a.yml", Name: "a.yml", Runners: []string{KindSelfHosted}},
		{File: "b.yaml", Name: "B", Runners: []string{KindHosted}},
		{File: "huge.yml", Name: "huge.yml", Runners: []string{KindUnknown}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
	for i := 0; i < MaxWorkflows+5; i++ {
		os.WriteFile(filepath.Join(dir, "w"+strings.Repeat("x", i%3)+string(rune('a'+i%26))+string(rune('a'+i/26))+".yml"), []byte("jobs: {}\n"), 0o644)
	}
	if n := len(ReadWorkflows(root)); n != MaxWorkflows {
		t.Errorf("read %d workflows, want the cap %d", n, MaxWorkflows)
	}
	if ReadWorkflows(t.TempDir()) != nil {
		t.Error("no .github/workflows should be none")
	}
}

// The runner of each job, from GitHub's own answers.
func TestRunnerKind(t *testing.T) {
	var arc, hosted, notYet restJob
	json.Unmarshal([]byte(`{"labels":["my-runner-x64"],"runner_group_name":"default","runner_name":"my-runner-x64-wnc6t-runner-zps27"}`), &arc)
	json.Unmarshal([]byte(`{"labels":["ubuntu-latest"],"runner_group_name":"GitHub Actions","runner_name":"GitHub Actions 1021165924"}`), &hosted)
	json.Unmarshal([]byte(`{"labels":[],"runner_group_name":null,"runner_name":null}`), &notYet)
	for _, c := range []struct {
		j    restJob
		want string
	}{{arc, KindSelfHosted}, {hosted, KindHosted}, {notYet, ""}} {
		if got := runnerOf(c.j); got.Kind != c.want || got.Labels == nil {
			t.Errorf("%+v: %+v, want kind %q", c.j, got, c.want)
		}
	}
}

func TestStepNow(t *testing.T) {
	steps := []Step{{Number: 1, Status: "completed"}, {Number: 2, Status: "completed"}, {Number: 3, Name: "Run tests", Status: "in_progress"}, {Number: 15, Status: "queued"}}
	got := stepNow(Job{Status: "in_progress", Steps: steps})
	if got == nil || *got != (StepNow{Number: 3, Name: "Run tests", Of: 4}) {
		t.Errorf("%+v", got)
	}
	// Between two steps: the next one.
	steps[2].Status = "completed"
	if got := stepNow(Job{Status: "in_progress", Steps: steps}); got == nil || got.Number != 4 {
		t.Errorf("between steps: %+v", got)
	}
	if stepNow(Job{Status: "completed", Steps: steps}) != nil {
		t.Error("a finished job has no step")
	}
}

func writeWorkflows(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".github", "workflows")
	os.MkdirAll(dir, 0o755)
	for n, src := range files {
		os.WriteFile(filepath.Join(dir, n), []byte(src), 0o644)
	}
	return root
}

func checkoutAt(root string, pushed bool) map[string]string {
	g := checkout()
	g["rev-parse --show-toplevel"] = root
	g["rev-parse HEAD"] = "c75f7fafb374ef42aa"
	g["rev-list -1 HEAD --not --remotes"] = ""
	if !pushed {
		g["rev-list -1 HEAD --not --remotes"] = "c75f7fafb374ef42aa"
	}
	return g
}

func restRunJSON(id int, file, status, conclusion, created string) string {
	c := "null"
	if conclusion != "" {
		c = `"` + conclusion + `"`
	}
	return `{"id":` + itoa(id) + `,"run_number":` + itoa(id-90) + `,"run_attempt":1,"name":"` + file + `","path":".github/workflows/` + file +
		`","display_title":"t","head_branch":"feature/x","head_sha":"c75f7fafb374ef42aa","event":"push","status":"` + status + `","conclusion":` + c +
		`,"created_at":"` + created + `","run_started_at":"` + created + `","updated_at":"` + created + `","html_url":"https://github.com/me/app/actions/runs/` + itoa(id) + `"}`
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

const arcJobs = `{"jobs":[
 {"id":117694395762,"run_id":105,"name":"e2e","status":"in_progress","conclusion":null,"started_at":"2026-10-19T03:58:20Z","completed_at":null,
  "html_url":"https://github.com/me/app/actions/runs/105/job/117694395762",
  "runner_name":"my-runner-x64-wnc6t-runner-zps27","runner_group_name":"default","labels":["my-runner-x64"],
  "steps":[{"number":1,"name":"Set up job","status":"completed","conclusion":"success","started_at":"2026-10-19T03:58:20Z","completed_at":"2026-10-19T03:58:30Z"},
           {"number":2,"name":"Run tests","status":"in_progress","conclusion":null,"started_at":"2026-10-19T03:58:30Z","completed_at":null},
           {"number":3,"name":"Complete job","status":"pending","conclusion":null,"started_at":null,"completed_at":null}]},
 {"id":117694396000,"run_id":105,"name":"web","status":"queued","conclusion":null,"started_at":null,"completed_at":null,
  "html_url":"https://github.com/me/app/actions/runs/105/job/117694396000","runner_name":null,"runner_group_name":null,"labels":[],"steps":[]}]}`

const hostedJobs = `{"jobs":[{"id":117731080473,"run_id":103,"name":"no-response / noResponse","status":"completed","conclusion":"success",
 "started_at":"2026-10-19T03:55:10Z","completed_at":"2026-10-19T03:55:20Z","html_url":"https://github.com/me/app/actions/runs/103/job/117731080473",
 "runner_name":"GitHub Actions 1021165924","runner_group_name":"GitHub Actions","labels":["ubuntu-latest"],"steps":[]}]}`

func boardFake(root string, pushed bool) *fake {
	runs := `{"total_count":6,"workflow_runs":[` + strings.Join([]string{
		restRunJSON(105, "ci.yml", "in_progress", "", "2026-10-19T04:00:00Z"),
		restRunJSON(103, "matrix.yml", "queued", "", "2026-10-19T03:55:00Z"),
		restRunJSON(104, "hosted.yml", "completed", "failure", "2026-10-19T03:50:00Z"),
		restRunJSON(102, "ios.yml", "in_progress", "", "2026-10-19T03:40:00Z"),
		restRunJSON(101, "ci.yml", "completed", "success", "2026-10-19T03:00:00Z"),
	}, ",") + `]}`
	api := "api --hostname github.com -X GET repos/me/app/actions/runs"
	return &fake{git: checkoutAt(root, pushed), gh: map[string]fakeOut{
		"auth status": {},
		api + " -F per_page=50 -f branch=feature/x": {out: runs},
		api + "/105/jobs": {out: arcJobs},
		api + "/103/jobs": {out: hostedJobs},
		api + "/102/jobs": {out: `{"jobs":[]}`},
	}}
}

var boardFiles = map[string]string{
	"ci.yml":      "name: CI\njobs:\n  e2e:\n    runs-on: my-runner-x64\n  web:\n    runs-on: my-runner-x64\n",
	"hosted.yml":  "name: Hosted\njobs:\n  a:\n    runs-on: ubuntu-latest\n",
	"ios.yml":     "name: iOS\njobs:\n  a:\n    runs-on: [self-hosted, macOS]\n",
	"matrix.yml":  "jobs:\n  a:\n    runs-on: ${{ matrix.os }}\n",
	"windows.yml": "name: Windows\njobs:\n  a:\n    runs-on: [self-hosted, Windows, X64]\n",
}

func TestBoard(t *testing.T) {
	root := writeWorkflows(t, boardFiles)
	f := boardFake(root, true)
	a := &Actions{Runner: f, Gh: "gh"}
	s := NewService(ServiceOptions{PaneCwd: func(context.Context, string) (string, bool) { return root, true }, Actions: a})
	code, m := do(t, s, "GET", "/api/actions/board?pane=%253", "")
	if code != 200 || m["state"] != "ok" || m["provider"] != "github" || m["repo"] != "me/app" || m["branch"] != "feature/x" ||
		m["sha"] != "c75f7fa" || m["pushed"] != true || m["url"] != "https://github.com/me/app/actions" || m["polledAt"] == nil {
		t.Fatalf("%d %v", code, m)
	}
	wfs := m["workflows"].([]any)
	var files []string
	byFile := map[string]map[string]any{}
	for _, w := range wfs {
		w := w.(map[string]any)
		files = append(files, w["file"].(string))
		byFile[w["file"].(string)] = w
	}
	if strings.Join(files, " ") != "ci.yml hosted.yml ios.yml matrix.yml windows.yml" {
		t.Errorf("workflows in file order: %v", files)
	}
	if byFile["windows.yml"]["run"] != nil || byFile["matrix.yml"]["name"] != "matrix.yml" {
		t.Errorf("never run / no name: %v %v", byFile["windows.yml"], byFile["matrix.yml"])
	}
	if r := byFile["matrix.yml"]["runners"].([]any); len(r) != 1 || r[0] != "unknown" {
		t.Errorf("matrix runners %v", r)
	}
	// The latest run per workflow: ci's is 105, not the older 101.
	ci := byFile["ci.yml"]["run"].(map[string]any)
	if ci["id"].(float64) != 105 || ci["status"] != "in_progress" {
		t.Fatalf("ci run %v", ci)
	}
	jobs := ci["jobs"].([]any)
	e2e, web := jobs[0].(map[string]any), jobs[1].(map[string]any)
	if e2e["runner"].(map[string]any)["kind"] != "self-hosted" || web["runner"].(map[string]any)["kind"] != "" {
		t.Errorf("kinds %v %v", e2e["runner"], web["runner"])
	}
	if st := e2e["step"].(map[string]any); st["number"].(float64) != 2 || st["name"] != "Run tests" || st["of"].(float64) != 3 {
		t.Errorf("step %v", st)
	}
	if _, ok := web["step"]; ok {
		t.Error("a queued job has no step")
	}
	hosted := byFile["matrix.yml"]["run"].(map[string]any)["jobs"].([]any)[0].(map[string]any)
	if hosted["runner"].(map[string]any)["kind"] != "hosted" {
		t.Errorf("hosted %v", hosted["runner"])
	}
	// Three runs with jobs -- the unfinished ones, newest first -- and the
	// failed one is left with its status: jobs null.
	failed := byFile["hosted.yml"]["run"].(map[string]any)
	if j, ok := failed["jobs"]; !ok || j != nil || failed["conclusion"] != "failure" {
		t.Errorf("the fourth run: %v", failed)
	}
	if n := f.count("gh api"); n != 4 {
		t.Errorf("%d gh api calls, want 1 + 3", n)
	}
	// Kept five seconds, for every pane on the repository.
	do(t, s, "GET", "/api/actions/board?pane=%253", "")
	if n := f.count("gh api"); n != 4 {
		t.Errorf("asked again within 5 s: %d", n)
	}
	a.mu.Lock()
	for _, e := range a.boards {
		e.at = e.at.Add(-boardTTL - time.Second)
	}
	a.mu.Unlock()
	do(t, s, "GET", "/api/actions/board?pane=%253", "")
	if n := f.count("gh api"); n != 8 {
		t.Errorf("after 5 s: %d gh api calls, want 8", n)
	}
}

func TestBoardFailedRunsWhenNothingRuns(t *testing.T) {
	runs := []Run{
		{ID: 1, file: "a.yml", Status: "completed", Conclusion: "failure", CreatedAt: "2026-10-19T01:00:00Z"},
		{ID: 2, file: "b.yml", Status: "completed", Conclusion: "success", CreatedAt: "2026-10-19T02:00:00Z"},
		{ID: 3, file: "c.yml", Status: "completed", Conclusion: "timed_out", CreatedAt: "2026-10-19T03:00:00Z"},
		{ID: 4, file: "a.yml", Status: "in_progress", CreatedAt: "2026-10-19T00:00:00Z"}, // not a's latest
	}
	if got := watchRuns(runs, 3); !reflect.DeepEqual(got, []int64{3, 1}) {
		t.Errorf("%v", got)
	}
}

func TestBoardStates(t *testing.T) {
	f := &fake{git: map[string]string{}}
	code, m := serve(t, f, "gh", "GET", "/api/actions/board?pane=%253", "")
	if code != 200 || m["state"] != StateNotGit || m["provider"] != "github" || m["workflows"] != nil {
		t.Errorf("not-git: %d %v", code, m)
	}
	f = &fake{git: checkout(), gh: map[string]fakeOut{"auth status": {code: 1, err: "You are not logged into any GitHub hosts."}}}
	if _, m := serve(t, f, "gh", "GET", "/api/actions/board?pane=%253", ""); m["state"] != StateNoAuth || m["repo"] != "me/app" {
		t.Errorf("no-auth: %v", m)
	}
	// Not pushed: the commit is on no remote branch.
	root := writeWorkflows(t, boardFiles)
	f = boardFake(root, false)
	s := NewService(ServiceOptions{PaneCwd: func(context.Context, string) (string, bool) { return root, true }, Actions: &Actions{Runner: f, Gh: "gh"}})
	if _, m := do(t, s, "GET", "/api/actions/board?pane=%253", ""); m["pushed"] != false {
		t.Errorf("pushed: %v", m["pushed"])
	}
	// gh failing is 502 with its line.
	f = &fake{git: checkoutAt(root, true), gh: map[string]fakeOut{"auth status": {}, "api": {code: 1, err: "HTTP 403: API rate limit exceeded"}}}
	s = NewService(ServiceOptions{PaneCwd: func(context.Context, string) (string, bool) { return root, true }, Actions: &Actions{Runner: f, Gh: "gh"}})
	if code, m := do(t, s, "GET", "/api/actions/board?pane=%253", ""); code != 502 || !strings.Contains(m["error"].(string), "rate limit") {
		t.Errorf("gh failing: %d %v", code, m)
	}
}

func TestOpen(t *testing.T) {
	f := &fake{git: checkout(), gh: map[string]fakeOut{
		"auth status": {},
		"api --hostname github.com -X GET repos/me/app/actions/runs/41":      {out: runView},
		"api --hostname github.com -X GET repos/me/app/actions/runs/41/jobs": {out: runJobs},
	}}
	var asked []OpenWindow
	open := func(ctx context.Context, o OpenWindow) (Opened, error) {
		asked = append(asked, o)
		return Opened{Session: "work", Window: 7, Name: o.Name}, nil
	}
	s := NewService(ServiceOptions{
		PaneCwd: func(context.Context, string) (string, bool) { return "/src/app/web", true },
		Actions: &Actions{Runner: f, Gh: "gh"}, Open: open, Self: "/usr/local/bin/st",
	})
	code, m := do(t, s, "POST", "/api/actions/open", `{"pane":"%3","run":41,"job":9}`)
	if code != 200 || m["session"] != "work" || m["window"].(float64) != 7 || m["name"] != "ci:ci/test" {
		t.Fatalf("%d %v", code, m)
	}
	o := asked[0]
	if o.Pane != "%3" || o.Key != "9" || o.Dir != "/src/app" || !strings.HasSuffix(strings.Join(o.Argv, " "), "/usr/local/bin/st ci log --follow --job 9 -R me/app") {
		t.Errorf("%+v", o)
	}
	// The same job again goes through Open again, which finds its window
	// (mux.NamedWindow, tested there): same name, same key.
	do(t, s, "POST", "/api/actions/open", `{"pane":"%3","run":41,"job":9}`)
	if len(asked) != 2 || asked[1].Name != o.Name || asked[1].Key != o.Key {
		t.Errorf("again: %+v", asked)
	}
	if code, _ := do(t, s, "POST", "/api/actions/open", `{"pane":"%3","run":41,"job":10}`); code != 404 {
		t.Errorf("a job not in the run: %d", code)
	}
	if code, _ := do(t, s, "POST", "/api/actions/open", `{"pane":"%3","run":41}`); code != 400 {
		t.Errorf("no job: %d", code)
	}
	// No multiplexer that opens windows.
	s = NewService(ServiceOptions{PaneCwd: func(context.Context, string) (string, bool) { return "/src/app", true }, Actions: &Actions{Runner: f, Gh: "gh"}})
	if code, m := do(t, s, "POST", "/api/actions/open", `{"pane":"%3","run":41,"job":9}`); code != 409 || m["state"] != "no-mux" {
		t.Errorf("no mux: %d %v", code, m)
	}
	s = NewService(ServiceOptions{PaneCwd: func(context.Context, string) (string, bool) { return "/src/app", true }, Actions: &Actions{Runner: f, Gh: "gh"},
		Open: func(context.Context, OpenWindow) (Opened, error) { return Opened{}, ErrNoMux }})
	if code, m := do(t, s, "POST", "/api/actions/open", `{"pane":"%3","run":41,"job":9}`); code != 409 || m["state"] != "no-mux" {
		t.Errorf("no mux from Open: %d %v", code, m)
	}
	// A read-only link sees neither the board nor opens anything.
	s = NewService(ServiceOptions{IsOwner: func(*http.Request) bool { return false }, Open: open,
		PaneCwd: func(context.Context, string) (string, bool) { return "/src/app", true }})
	if code, _ := do(t, s, "POST", "/api/actions/open", `{"pane":"%3","run":41,"job":9}`); code != 403 {
		t.Errorf("guest open: %d", code)
	}
	if code, _ := do(t, s, "GET", "/api/actions/board?pane=%253", ""); code != 403 {
		t.Errorf("guest board: %d", code)
	}
	if code, _ := do(t, s, "GET", "/api/actions/open?pane=%253", ""); code != 403 {
		t.Errorf("guest GET open: %d", code)
	}
}

func TestSplitAndRenderLog(t *testing.T) {
	raw := "\ufeff2026-10-18T12:45:48.49Z Current runner version: '2.338.0'\n" +
		"2026-10-18T12:46:00.47Z ##[group]Run go test ./...\n" +
		"2026-10-18T12:46:00.48Z \x1b[36mgo test ./...\x1b[0m\n" +
		"2026-10-18T12:46:00.49Z ##[endgroup]\n" +
		"2026-10-18T12:47:31.10Z --- FAIL: TestX\n" +
		"continued line without a stamp\n" +
		"2026-10-18T12:47:31.20Z ##[error]Process completed with exit code 1.\n"
	steps := []Step{
		{Number: 1, Name: "Set up job", Status: "completed", Conclusion: "success", StartedAt: "2026-10-18T12:45:48Z", CompletedAt: "2026-10-18T12:46:00Z"},
		{Number: 2, Name: "Run go test", Status: "completed", Conclusion: "failure", StartedAt: "2026-10-18T12:46:00Z", CompletedAt: "2026-10-18T12:47:31Z"},
		{Number: 15, Name: "Complete job", Status: "completed", Conclusion: "success", StartedAt: "2026-10-18T12:47:32Z", CompletedAt: "2026-10-18T12:47:32Z"},
	}
	parts := SplitLog([]byte(raw), steps)
	if len(parts) != 3 || len(parts[0].Lines) != 1 || len(parts[1].Lines) != 6 || len(parts[2].Lines) != 0 {
		t.Fatalf("%+v", parts)
	}
	if parts[0].Lines[0] != "Current runner version: '2.338.0'" {
		t.Errorf("BOM and stamp: %q", parts[0].Lines[0])
	}
	out := RenderLog(parts, false)
	text := strings.Join(out, "\n")
	for _, want := range []string{"── 1/3 Set up job", "── 2/3 Run go test", "▸ Run go test ./...", "\x1b[36mgo test ./...\x1b[0m", cRed + "Process completed with exit code 1." + cReset, "── 3/3 Complete job"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	if strings.Contains(text, "Current runner version") || strings.Contains(text, "endgroup") {
		t.Errorf("a passed step's lines or ##[endgroup] shown:\n%s", text)
	}
	if all := strings.Join(RenderLog(parts, true), "\n"); !strings.Contains(all, "Current runner version") {
		t.Error("--all shows every step")
	}
	// No step times: one piece, every line.
	if p := SplitLog([]byte(raw), nil); len(p) != 1 || p[0].Step != nil || len(p[0].Lines) != 7 {
		t.Errorf("unsplit: %+v", p)
	}
	for in, want := range map[string]string{
		"##[warning]careful": cYellow + "careful" + cReset,
		"##[debug]x":         cDim + "x" + cReset,
		"##[command]go test": "$ go test",
		"::add-mask::***":    "::add-mask::***",
		"##[section]Start":   "##[section]Start",
	} {
		if got, _ := TermLine(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestWindowName(t *testing.T) {
	if got := WindowName("ci", "build (1.22)\nx"); got != "ci:ci/build (1.22) x" {
		t.Errorf("%q", got)
	}
}
