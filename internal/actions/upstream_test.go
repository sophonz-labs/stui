package actions

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// The remotes and lists below are made up but shaped like a real fork:
// parser-fork is a fork (origin acme/parser-fork) of olduser/parser,
// which GitHub now answers as parser-org/parser (default branch develop).

// The upstream's workflows as GET repos/.../actions/workflows lists them:
// twelve files (pr-writer.yml has no name:, so GitHub names it by its
// path) and four of GitHub's own.
var upstreamWorkflows = [][3]string{
	{"Browser preview", ".github/workflows/browser-preview.yml", "active"},
	{"Build", ".github/workflows/build.yml", "active"},
	{"CI (build)", ".github/workflows/ci.yml", "active"},
	{"Deploy preview", ".github/workflows/deploy-preview.yml", "active"},
	{"Lint", ".github/workflows/lint.yml", "active"},
	{"PR build links", ".github/workflows/pr-build-links.yml", "active"},
	{"PR Formatting", ".github/workflows/pr-formatting-check.yml", "active"},
	{".github/workflows/pr-writer.yml", ".github/workflows/pr-writer.yml", "active"},
	{"Build & Publish Assets", ".github/workflows/release-assets.yml", "active"},
	{"Compile Release", ".github/workflows/release.yml", "active"},
	{"Compile Release Candidate", ".github/workflows/release_candidate.yml", "active"},
	{"Simulator smoke", ".github/workflows/simulator.yml", "active"},
	{"Copilot", "dynamic/agents/copilot-pull-request-reviewer", "active"},
	{"Copilot code review", "dynamic/copilot-pull-request-reviewer/copilot-pull-request-reviewer", "active"},
	{"Copilot cloud agent", "dynamic/copilot-swe-agent/copilot", "active"},
	{"Dependency Graph", "dynamic/dependabot/update-graph", "active"},
}

// The fork's own .github/workflows (release/acme): seven files.
var forkFiles = []string{"ci.yml", "pr-build-links.yml", "pr-formatting-check.yml", "release-assets.yml", "release.yml", "release_candidate.yml", "stale.yml"}

func workflowsJSON(ws [][3]string) string {
	type w struct {
		Name  string `json:"name"`
		Path  string `json:"path"`
		State string `json:"state"`
	}
	var out struct {
		TotalCount int `json:"total_count"`
		Workflows  []w `json:"workflows"`
	}
	for _, x := range ws {
		out.Workflows = append(out.Workflows, w{x[0], x[1], x[2]})
	}
	out.TotalCount = len(ws)
	b, _ := json.Marshal(out)
	return string(b)
}

func files(ws []Workflow) []string {
	var out []string
	for _, w := range ws {
		out = append(out, w.File)
	}
	return out
}

func minus(a, b []string) []string {
	in := map[string]bool{}
	for _, x := range b {
		in[x] = true
	}
	var out []string
	for _, x := range a {
		if !in[x] {
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

// The upstream's board lists the upstream's files, not the fork's: five
// the fork has not, one the upstream has not, GitHub's own left out.
func TestUpstreamWorkflowsAreTheUpstreams(t *testing.T) {
	ws := upstreamWorkflows
	ws = append(ws[:len(ws):len(ws)], [3]string{"Old nightly", ".github/workflows/nightly.yml", "disabled_manually"})
	var g restWorkflows
	if err := json.Unmarshal([]byte(workflowsJSON(ws)), &g); err != nil {
		t.Fatal(err)
	}
	got := apiWorkflows(g)
	want := "browser-preview.yml build.yml ci.yml deploy-preview.yml lint.yml pr-build-links.yml " +
		"pr-formatting-check.yml pr-writer.yml release-assets.yml release.yml release_candidate.yml simulator.yml"
	if s := strings.Join(files(got), " "); s != want {
		t.Errorf("files:\n %s\nwant\n %s", s, want)
	}
	names := map[string]string{}
	for _, w := range got {
		names[w.File] = w.Name
		if w.Runners == nil || len(w.Runners) != 0 {
			t.Errorf("%s runners %v: an upstream's are not read", w.File, w.Runners)
		}
	}
	// No name: is its file name, as a checkout's own workflow without one.
	if names["pr-writer.yml"] != "pr-writer.yml" || names["build.yml"] != "Build" {
		t.Errorf("names %v", names)
	}
	if up := minus(files(got), forkFiles); strings.Join(up, " ") != "browser-preview.yml build.yml deploy-preview.yml lint.yml pr-writer.yml simulator.yml" {
		t.Errorf("upstream only: %v", up)
	}
	if fork := minus(forkFiles, files(got)); strings.Join(fork, " ") != "stale.yml" {
		t.Errorf("fork only: %v", fork)
	}
	// Runs go to the upstream's files: build.yml's is there, the fork's
	// stale.yml is not on the board at all, a Copilot run attaches to
	// nothing.
	runs := []Run{
		{ID: 3, file: "build.yml", Status: "completed", Conclusion: "failure"},
		{ID: 2, file: "copilot", Status: "completed", Conclusion: "success"},
		{ID: 1, file: "ci.yml", Status: "completed", Conclusion: "success"},
	}
	board := latestRuns(got, runs, nil)
	with := map[string]int64{}
	for _, b := range board {
		if b.Run != nil {
			with[b.File] = b.Run.ID
		}
	}
	if len(with) != 2 || with["build.yml"] != 3 || with["ci.yml"] != 1 {
		t.Errorf("runs on %v", with)
	}
}

func remotesGit(root, branch, branchRemote string, remotes ...[2]string) map[string]string {
	m := map[string]string{
		"rev-parse --show-toplevel":         root,
		"symbolic-ref --quiet --short HEAD": branch,
		"rev-parse HEAD":                    headSha,
		"rev-list -1 HEAD --not --remotes":  "",
	}
	if branchRemote != "" {
		m["config --get branch."+branch+".remote"] = branchRemote
	}
	var names []string
	for _, r := range remotes {
		names = append(names, r[0])
		m["remote get-url -- "+r[0]] = r[1]
	}
	m["remote"] = strings.Join(names, "\n")
	return m
}

func othersOf(w Where) string {
	var s []string
	for _, r := range w.others {
		s = append(s, r.Name+"="+r.Repo.Slug())
	}
	return strings.Join(s, " ")
}

// Which remotes are upstreams: other repositories than the one read, each
// once however its URL is spelt, the branch's remote first, at most two.
func TestLocateFindsUpstreams(t *testing.T) {
	for _, c := range []struct {
		name, branch, remote string
		remotes              [][2]string
		repo, others         string
	}{
		{"fork", "release/acme", "origin", [][2]string{
			{"origin", "git@github.com:acme/parser-fork.git"},
			{"upstream", "git@github.com:olduser/parser.git"}},
			"acme/parser-fork", "upstream=olduser/parser"},
		{"parser-lite: any name", "main", "origin", [][2]string{
			{"base", "git@github.com:acme/parser-fork.git"},
			{"origin", "https://github.com/acme/parser-lite.git"}},
			"acme/parser-lite", "base=acme/parser-fork"},
		{"parser-docs: one repository twice", "main", "", [][2]string{
			{"acme", "git@github.com:acme/parser-docs.git"},
			{"origin", "https://github.com/ACME/parser-docs"}},
			"ACME/parser-docs", ""},
		{"the branch follows upstream: origin is the other", "develop", "upstream", [][2]string{
			{"origin", "git@github.com:me/app.git"},
			{"upstream", "git@github.com:org/app.git"}},
			"org/app", "origin=me/app"},
		{"two at most, by rank", "main", "", [][2]string{
			{"zeta", "git@github.com:z/app.git"},
			{"github", "git@github.com:g/app.git"},
			{"origin", "git@github.com:me/app.git"},
			{"upstream", "git@github.com:org/app.git"},
			{"lab", "git@gitlab.com:me/app.git"}},
			"me/app", "upstream=org/app github=g/app"},
	} {
		f := &fake{git: remotesGit("/src/x", c.branch, c.remote, c.remotes...), gh: map[string]fakeOut{"auth status": {}}}
		w := (&Actions{Runner: f, Gh: "gh"}).Locate(context.Background(), "/src/x")
		if w.State != StateOK || w.Repo.Slug() != c.repo || othersOf(w) != c.others {
			t.Errorf("%s: %s %v, others %q; want %s, %q", c.name, w.State, w.Repo, othersOf(w), c.repo, c.others)
		}
	}
}

// Four checkouts side by side, as a coordinator's board sees
// them.
type upWorld struct {
	cp, lite, twins, reader string
	f                       *fake
	tree                    []Member
	panes                   map[string]string
}

func runsHTTP(etag string, runs ...string) fakeOut {
	return fakeOut{out: "HTTP/2.0 200 OK\r\nEtag: " + etag + "\r\nX-Ratelimit-Remaining: 4900\r\n\r\n" +
		`{"total_count":` + itoa(len(runs)) + `,"workflow_runs":[` + strings.Join(runs, ",") + `]}`}
}

func newUpWorld(t *testing.T) *upWorld {
	base := t.TempDir()
	w := &upWorld{f: &fake{gitAt: map[string]map[string]string{}, gh: map[string]fakeOut{"auth status": {}}}}
	w.cp, w.lite, w.twins, w.reader = filepath.Join(base, "parser-fork"), filepath.Join(base, "parser-lite"),
		filepath.Join(base, "TwinTool"), filepath.Join(base, "parser")
	for _, d := range []string{w.cp, w.lite, w.twins, w.reader} {
		os.MkdirAll(filepath.Join(d, ".github", "workflows"), 0o755)
		os.Mkdir(filepath.Join(d, ".git"), 0o755)
	}
	for _, f := range forkFiles {
		os.WriteFile(filepath.Join(w.cp, ".github", "workflows", f), []byte("name: "+f+"\non: push\n"), 0o644)
	}
	w.f.gitAt[w.cp] = remotesGit(w.cp, "release/acme", "origin",
		[2]string{"origin", "git@github.com:acme/parser-fork.git"},
		[2]string{"upstream", "git@github.com:olduser/parser.git"})
	w.f.gitAt[w.lite] = remotesGit(w.lite, "main", "origin",
		[2]string{"base", "git@github.com:acme/parser-fork.git"},
		[2]string{"origin", "https://github.com/acme/parser-lite.git"})
	w.f.gitAt[w.twins] = remotesGit(w.twins, "fix/ble", "origin",
		[2]string{"origin", "git@github.com:acme/TwinTool.git"},
		[2]string{"upstream", "https://github.com/someone/TwinTool.git"})
	w.f.gitAt[w.reader] = remotesGit(w.reader, "develop", "origin",
		[2]string{"origin", "git@github.com:parser-org/parser.git"})

	gh := w.f.gh
	api := "api --hostname github.com -X GET repos/"
	runs := "api --hostname github.com -i -X GET repos/"
	gh[api+"olduser/parser"] = fakeOut{out: `{"full_name":"parser-org/parser","default_branch":"develop","archived":false}`}
	gh[api+"olduser/parser/actions/workflows"] = fakeOut{out: workflowsJSON(upstreamWorkflows)}
	gh[api+"acme/parser-fork"] = fakeOut{out: `{"full_name":"acme/parser-fork","default_branch":"release/acme"}`}
	gh[api+"acme/parser-fork/actions/workflows"] = fakeOut{out: workflowsJSON([][3]string{
		{"CI (build)", ".github/workflows/ci.yml", "active"}, {"Close inactive issues", ".github/workflows/stale.yml", "active"}})}
	gh[api+"someone/TwinTool"] = fakeOut{out: `{"full_name":"someone/TwinTool","default_branch":"master"}`}
	gh[api+"someone/TwinTool/actions/workflows"] = fakeOut{out: workflowsJSON([][3]string{{"CI (build)", ".github/workflows/ci.yml", "active"}})}
	gh[runs+"parser-org/parser/actions/runs"] = runsHTTP(`W/"up"`,
		restRunJSON(503, "build.yml", "completed", "failure", "2026-10-25T09:00:00Z"),
		restRunJSON(502, "ci.yml", "completed", "success", "2026-10-25T08:00:00Z"))
	gh[runs+"acme/parser-fork/actions/runs"] = runsHTTP(`W/"ko"`,
		restRunJSON(402, "ci.yml", "in_progress", "", "2026-10-25T10:00:00Z"))
	gh[runs+"acme/parser-lite/actions/runs"] = runsHTTP(`W/"lite"`)
	gh[runs+"acme/TwinTool/actions/runs"] = runsHTTP(`W/"twins"`)
	gh[runs+"someone/TwinTool/actions/runs"] = runsHTTP(`W/"twin"`,
		restRunJSON(302, "ci.yml", "completed", "success", "2026-10-25T07:00:00Z"))

	w.panes = map[string]string{"%1": w.cp, "%2": w.lite, "%3": w.twins, "%4": w.reader}
	w.tree = []Member{{Name: "fork", Pane: "%1"}, {Name: "lite", Pane: "%2"}, {Name: "twins", Pane: "%3"}, {Name: "reader", Pane: "%4"}}
	return w
}

func (w *upWorld) service(a *Actions) *Service {
	return NewService(ServiceOptions{Actions: a, Tree: func(context.Context, string) []Member { return w.tree },
		PaneCwd: func(_ context.Context, p string) (string, bool) { d, ok := w.panes[p]; return d, ok },
		Open: func(_ context.Context, o OpenWindow) (Opened, error) {
			return Opened{Session: "s", Window: 1, Name: o.Name}, nil
		}})
}

// settleAll waits for every read that went on after a request.
func settleAll(a *Actions) {
	for {
		var ch chan struct{}
		a.mu.Lock()
		for _, e := range a.overview {
			if e.reading != nil {
				ch = e.reading
			}
		}
		for _, m := range a.metas {
			if m.reading != nil {
				ch = m.reading
			}
		}
		a.mu.Unlock()
		if ch == nil {
			return
		}
		<-ch
	}
}

func line(g map[string]any) string {
	s := g["project"].(string)
	if r, _ := g["remote"].(string); r != "" {
		s = "  " + r + ":" + g["repo"].(string)
	}
	s += "@" + g["branch"].(string) + "=" + g["state"].(string)
	if wfs, ok := g["workflows"].([]any); ok {
		n := 0
		for _, wf := range wfs {
			if wf.(map[string]any)["run"] != nil {
				n++
			}
		}
		s += "(" + itoa(len(wfs)) + "/" + itoa(n) + ")"
	}
	return s
}

// Each checkout's upstreams come right after it, on their default
// branches, with their own workflows. An upstream that is the repository
// of another checkout on the board is shown under its fork all the same;
// on that checkout's branch it is read once, with that checkout.
func TestBoardsUpstreams(t *testing.T) {
	w := newUpWorld(t)
	a := &Actions{Runner: w.f, Gh: "gh"}
	s := w.service(a)
	code, m := do(t, s, "GET", "/api/actions/boards?pane=%251", "")
	if code != 200 {
		t.Fatalf("%d %v", code, m)
	}
	gs := groupsOf(t, m)
	var got []string
	for _, g := range gs {
		got = append(got, line(g))
	}
	want := []string{
		"parser-fork@release/acme=ok(7/1)",
		"  upstream:parser-org/parser@develop=ok(12/2)",
		"parser-lite@main=ok",
		"  base:acme/parser-fork@release/acme=ok(2/1)",
		"TwinTool@fix/ble=ok",
		"  upstream:someone/TwinTool@master=ok(1/1)",
		// No files of its own here: no workflows, its runs kept.
		"parser@develop=ok",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("groups:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	up := gs[1]
	if up["upstreamOf"] != w.cp || up["root"] != w.cp || up["pane"] != "%1" || up["remote"] != "upstream" ||
		up["url"] != "https://github.com/parser-org/parser/actions" ||
		len(up["owners"].([]any)) != 0 || up["sha"] != nil || up["pushed"] != nil {
		t.Errorf("upstream group: %v", up)
	}
	if _, ok := gs[0]["upstreamOf"]; ok {
		t.Errorf("a checkout says upstreamOf: %v", gs[0])
	}
	// One meta read per upstream repository (acme/parser-fork once,
	// for lite), and one runs read per repository and branch: the
	// upstream of the fork and parser's develop are one,
	// lite's base and the fork's release/acme are one.
	calls := strings.Join(w.f.calls, "\n")
	for prefix, n := range map[string]int{
		"gh api --hostname github.com -i -X GET repos/parser-org/parser/":              1,
		"gh api --hostname github.com -i -X GET repos/acme/parser-fork/":               1,
		"gh api --hostname github.com -i -X GET repos/someone/TwinTool/":               1,
		"gh api --hostname github.com -X GET repos/acme/parser-fork/actions/workflows": 1,
		"gh api --hostname github.com -i -X GET repos/olduser":                         0,
	} {
		if c := w.f.count(prefix); c != n {
			t.Errorf("%d× %s, want %d\n%s", c, prefix, n, calls)
		}
	}
	if c := strings.Count(calls+"\n", "repos/olduser/parser\n"); c != 1 {
		t.Errorf("the upstream's repository read %d times", c)
	}
	if !strings.Contains(calls, "repos/parser-org/parser/actions/runs -F per_page=50 -f branch=develop") {
		t.Errorf("the upstream's runs are not its default branch's:\n%s", calls)
	}
	if n := w.f.count("gh api"); n != 11 {
		t.Errorf("%d gh api calls, want 11 (3 upstreams × 2, 5 runs lists):\n%s", n, calls)
	}

	// 30 s on: the checkouts are read again (the shared ones with them);
	// someone/TwinTool, kept 120 s, is not.
	settleAll(a)
	ageAll(a, BoardsTTL+time.Second)
	do(t, s, "GET", "/api/actions/boards?pane=%251", "")
	settleAll(a)
	if c := w.f.count("gh api --hostname github.com -i -X GET repos/someone/TwinTool/"); c != 1 {
		t.Errorf("an upstream read again after %v: %d", BoardsTTL, c)
	}
	if c := w.f.count("gh api --hostname github.com -i -X GET repos/parser-org/parser/"); c != 2 {
		t.Errorf("the shared one read %d times, want 2", c)
	}
	// 120 s on: read again, with its ETag; its repository and workflows
	// are kept 10 minutes.
	ageAll(a, UpstreamTTL)
	w.f.mu.Lock()
	w.f.gh["api --hostname github.com -i -X GET repos/someone/TwinTool/actions/runs -F per_page=50 -f branch=master -H If-None-Match: W/\"twin\""] =
		fakeOut{out: "HTTP/2.0 304 Not Modified\r\n\r\n", code: 1, err: "gh: HTTP 304"}
	w.f.mu.Unlock()
	do(t, s, "GET", "/api/actions/boards?pane=%251", "")
	settleAll(a)
	if c := w.f.count("gh api --hostname github.com -i -X GET repos/someone/TwinTool/actions/runs -F per_page=50 -f branch=master -H If-None-Match"); c != 1 {
		t.Errorf("not asked with its ETag:\n%s", strings.Join(w.f.calls, "\n"))
	}
	if c := w.f.count("gh api --hostname github.com -X GET repos/someone/TwinTool"); c != 2 {
		t.Errorf("its repository and workflows read %d times in 10 minutes, want 2", c)
	}
	_, m = do(t, s, "GET", "/api/actions/boards?pane=%251", "")
	if g := groupsOf(t, m)[5]; line(g) != "  upstream:someone/TwinTool@master=ok(1/1)" || g["stale"] != false {
		t.Errorf("after a 304: %v", g)
	}
}

func ageAll(a *Actions, d time.Duration) {
	a.mu.Lock()
	for _, e := range a.overview {
		e.at = e.at.Add(-d)
	}
	a.mu.Unlock()
}

// Upstreams do not count toward MaxGroups: sixteen checkouts and their
// upstreams are all on the board.
func TestUpstreamsAreNotCounted(t *testing.T) {
	base := t.TempDir()
	f := &fake{gitAt: map[string]map[string]string{}, gh: map[string]fakeOut{"auth status": {}}}
	var tree []Member
	panes := map[string]string{}
	for i := 0; i < MaxGroups; i++ {
		d := filepath.Join(base, "r"+itoa(i))
		os.MkdirAll(filepath.Join(d, ".git"), 0o755)
		f.gitAt[d] = remotesGit(d, "main", "origin",
			[2]string{"origin", "git@github.com:me/r" + itoa(i) + ".git"}, [2]string{"upstream", "git@github.com:org/r" + itoa(i) + ".git"})
		f.gh["api --hostname github.com -i -X GET repos/me/r"+itoa(i)+"/"] = runsHTTP(`W/"x"`)
		p := "%" + itoa(i+1)
		panes[p] = d
		tree = append(tree, Member{Name: "w" + itoa(i), Pane: p})
	}
	f.gh["api --hostname github.com -X GET repos/org/"] = fakeOut{code: 1, err: "gh: Not Found (HTTP 404)"}
	a := &Actions{Runner: f, Gh: "gh"}
	b := a.Boards(context.Background(), "%1", tree, func(_ context.Context, p string) (string, bool) { d, ok := panes[p]; return d, ok })
	ups := 0
	for _, g := range b.Groups {
		if g.UpstreamOf != "" {
			ups++
			// A repository gh cannot read is a line that says so.
			if g.State != StateError || g.Remote != "upstream" || !strings.Contains(g.Detail, "Not Found") {
				t.Errorf("upstream %+v", g)
			}
		}
	}
	if len(b.Groups) != 2*MaxGroups || ups != MaxGroups || len(b.Skipped) != 0 {
		t.Errorf("%d groups, %d upstreams, skipped %v", len(b.Groups), ups, b.Skipped)
	}
}

// An upstream's board: opened by its remote's name, never by a URL; read,
// never acted on.
func TestUpstreamBoardRoute(t *testing.T) {
	w := newUpWorld(t)
	w.f.gh["api --hostname github.com -X GET repos/parser-org/parser/actions/runs/503/jobs"] = fakeOut{out: `{"jobs":[]}`}
	w.f.gh["api --hostname github.com -X GET repos/parser-org/parser/actions/runs/503"] = fakeOut{out: restRunJSON(503, "build.yml", "completed", "failure", "2026-10-25T09:00:00Z")}
	w.f.gh["api --hostname github.com -X GET repos/parser-org/parser/actions/runs"] = fakeOut{out: `{"workflow_runs":[` +
		restRunJSON(503, "build.yml", "completed", "failure", "2026-10-25T09:00:00Z") + `]}`}
	s := w.service(&Actions{Runner: w.f, Gh: "gh"})
	root := url.QueryEscape(w.cp)
	code, m := do(t, s, "GET", "/api/actions/board?pane=%251&root="+root+"&remote=upstream", "")
	if code != 200 || m["state"] != "ok" || m["repo"] != "parser-org/parser" || m["branch"] != "develop" ||
		m["remote"] != "upstream" || m["upstreamOf"] != w.cp || m["pushed"] != true || m["sha"] != nil {
		t.Fatalf("%d %v", code, m)
	}
	wfs := m["workflows"].([]any)
	if len(wfs) != 12 || wfs[1].(map[string]any)["file"] != "build.yml" || wfs[1].(map[string]any)["run"] == nil {
		t.Errorf("workflows %v", wfs)
	}
	if !strings.Contains(strings.Join(w.f.calls, "\n"), "repos/parser-org/parser/actions/runs -F per_page=50 -f branch=develop") {
		t.Errorf("not the default branch:\n%s", strings.Join(w.f.calls, "\n"))
	}
	// Without root: the pane's own checkout's remote.
	if code, m := do(t, s, "GET", "/api/actions/board?pane=%251&remote=upstream", ""); code != 200 || m["branch"] != "develop" {
		t.Errorf("no root: %d %v", code, m)
	}
	if code, m := do(t, s, "GET", "/api/actions/run?pane=%251&id=503&remote=upstream", ""); code != 200 || m["repo"] != "parser-org/parser" {
		t.Errorf("run: %d %v", code, m)
	}
	// origin is the checkout's own repository, not an upstream; the rest
	// are no remote of it.
	for _, r := range []string{"origin", "base", "nope", "git@github.com:evil/x.git", "https://github.com/olduser/parser"} {
		if code, m := do(t, s, "GET", "/api/actions/board?pane=%251&remote="+url.QueryEscape(r), ""); code != 400 || m["error"] != "bad remote" {
			t.Errorf("remote=%s: %d %v", r, code, m)
		}
	}
	for _, p := range []string{"/api/actions/open", "/api/actions/rerun", "/api/actions/cancel"} {
		if code, m := do(t, s, "POST", p, `{"pane":"%1","run":503,"job":9,"id":503,"remote":"upstream"}`); code != 409 || m["state"] != "upstream" {
			t.Errorf("%s: %d %v", p, code, m)
		}
	}
}
