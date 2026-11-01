package actions

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A tree of panes: two in
// one checkout, one in another, a worker in a folder with two repositories
// below it (and a worktree beside them), one with too many, one with none,
// one whose pane is gone, and two on another machine.
type boardsWorld struct {
	base, cp, gor, mobile, ios, android, wt, many, plain string
	f                                                    *fake
	panes                                                map[string]string
	tree                                                 []Member
}

const headSha = "c75f7fafb374ef42aa"

func runsAnswer(etag string) fakeOut {
	return fakeOut{out: "HTTP/2.0 200 OK\r\nEtag: " + etag + "\r\nX-Ratelimit-Remaining: 4900\r\n\r\n" +
		`{"total_count":1,"workflow_runs":[` + restRunJSON(105, "ci.yml", "completed", "success", "2026-10-25T04:00:00Z") + `]}`}
}

func repoAt(t *testing.T, f *fake, dir, name string) {
	t.Helper()
	wf := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(wf, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(wf, "ci.yml"), []byte("name: CI\njobs:\n  test:\n    runs-on: ubuntu-latest\n"), 0o644)
	os.Mkdir(filepath.Join(dir, ".git"), 0o755)
	f.gitAt[dir] = map[string]string{
		"rev-parse --show-toplevel":         dir,
		"symbolic-ref --quiet --short HEAD": "main",
		"config --get branch.main.remote":   "origin",
		"remote":                            "origin",
		"remote get-url -- origin":          "https://github.com/me/" + name + ".git",
		"rev-parse HEAD":                    headSha,
		"rev-list -1 HEAD --not --remotes":  "",
	}
	f.gh["api --hostname github.com -i -X GET repos/me/"+name+"/actions/runs"] = runsAnswer(`W/"` + name + `"`)
}

func newBoardsWorld(t *testing.T) *boardsWorld {
	// t.TempDir on macOS is under /var, a link to /private/var: the paths
	// below are the unresolved ones, git would print the resolved ones.
	base := t.TempDir()
	w := &boardsWorld{base: base, f: &fake{gitAt: map[string]map[string]string{}, gh: map[string]fakeOut{"auth status": {}}}}
	w.cp = filepath.Join(base, "server-main")
	w.gor = filepath.Join(base, "server-go")
	w.mobile = filepath.Join(base, "mobile")
	w.ios = filepath.Join(w.mobile, "mobile-ios")
	w.android = filepath.Join(w.mobile, "mobile-android")
	w.wt = filepath.Join(w.mobile, "mobile-ios-wt")
	w.many = filepath.Join(base, "many")
	w.plain = filepath.Join(base, "plain")
	repoAt(t, w.f, w.cp, "server")
	repoAt(t, w.f, w.gor, "server-go")
	repoAt(t, w.f, w.ios, "mobile-ios")
	repoAt(t, w.f, w.android, "mobile-android")
	// A worktree: .git is a file. Git would answer in it; the board does
	// not look.
	os.MkdirAll(w.wt, 0o755)
	os.WriteFile(filepath.Join(w.wt, ".git"), []byte("gitdir: "+w.ios+"/.git/worktrees/wt\n"), 0o644)
	w.f.gitAt[w.wt] = w.f.gitAt[w.ios]
	for i := 0; i <= MaxBelow; i++ {
		repoAt(t, w.f, filepath.Join(w.many, "r"+itoa(i)), "many"+itoa(i))
	}
	os.MkdirAll(filepath.Join(w.plain, "notes"), 0o755)
	w.panes = map[string]string{"%9": w.cp, "%11": w.cp, "%12": w.gor, "%8": w.mobile, "%20": w.many, "%21": w.plain}
	w.tree = []Member{
		{Name: "coordinator", Pane: "%9"}, {Name: "web", Pane: "%11"}, {Name: "terminal-go", Pane: "%12"},
		{Name: "mobile", Pane: "%8"}, {Name: "simulator-ci", Pane: "mac/%31", Machine: "mac"},
		{Name: "many", Pane: "%20"}, {Name: "plain", Pane: "%21"}, {Name: "gone", Pane: "%22"},
		{Name: "action-runner", Pane: "mac/%34", Machine: "mac"},
	}
	return w
}

func (w *boardsWorld) cwd(_ context.Context, pane string) (string, bool) {
	d, ok := w.panes[pane]
	return d, ok
}

func (w *boardsWorld) service(a *Actions) *Service {
	return NewService(ServiceOptions{PaneCwd: w.cwd, Actions: a, Tree: func(context.Context, string) []Member { return w.tree },
		Open: func(_ context.Context, o OpenWindow) (Opened, error) {
			return Opened{Session: "s", Window: 1, Name: o.Name}, nil
		}})
}

func groupsOf(t *testing.T, m map[string]any) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, g := range m["groups"].([]any) {
		out = append(out, g.(map[string]any))
	}
	return out
}

func ownersOf(g map[string]any) string {
	var s []string
	for _, o := range g["owners"].([]any) {
		o := o.(map[string]any)
		s = append(s, o["name"].(string)+":"+o["how"].(string))
	}
	return strings.Join(s, ",")
}

// Brief 1, 2, 3, 4: one group per checkout, two panes in one checkout read
// once, a folder outside a repository read one level down (main checkouts
// only), too many there given up and said, the other machine a group that
// is not read.
func TestBoardsGroups(t *testing.T) {
	w := newBoardsWorld(t)
	s := w.service(&Actions{Runner: w.f, Gh: "gh"})
	code, m := do(t, s, "GET", "/api/actions/boards?pane=%259", "")
	if code != 200 || m["pane"] != "%9" {
		t.Fatalf("%d %v", code, m)
	}
	gs := groupsOf(t, m)
	var got []string
	for _, g := range gs {
		got = append(got, g["project"].(string)+"="+g["state"].(string)+"["+ownersOf(g)+"]")
	}
	want := "server=ok[coordinator:cwd,web:cwd] server-go=ok[terminal-go:cwd] " +
		"mobile-android=ok[mobile:below] mobile-ios=ok[mobile:below] =remote[simulator-ci:remote,action-runner:remote]"
	if strings.Join(got, " ") != want {
		t.Errorf("groups:\n %s\nwant\n %s", strings.Join(got, " "), want)
	}
	cp := gs[0]
	if cp["repo"] != "me/server" || cp["root"] != w.cp || cp["branch"] != "main" || cp["sha"] != "c75f7fa" ||
		cp["pushed"] != true || cp["pane"] != "%9" || cp["stale"] != false || cp["polledAt"] == nil {
		t.Errorf("group: %v", cp)
	}
	if ios := gs[3]; ios["pane"] != "%8" || ios["root"] != w.ios {
		t.Errorf("below: %v", ios)
	}
	if r := gs[4]; r["machine"] != "mac" || r["workflows"] != nil || r["root"] != nil {
		t.Errorf("remote: %v", r)
	}
	wf := cp["workflows"].([]any)[0].(map[string]any)
	if run := wf["run"].(map[string]any); wf["name"] != "CI" || run["id"].(float64) != 105 {
		t.Errorf("workflow %v", wf)
	} else if j, ok := run["jobs"]; !ok || j != nil {
		t.Errorf("the overview has no jobs: %v", run["jobs"])
	}
	var skipped []string
	for _, sk := range m["skipped"].([]any) {
		sk := sk.(map[string]any)
		skipped = append(skipped, sk["name"].(string)+":"+sk["why"].(string))
	}
	if strings.Join(skipped, " ") != "many:too-many plain:not-git gone:no-pane" {
		t.Errorf("skipped %v", skipped)
	}
	// One list per checkout, the two panes of one checkout included; no
	// jobs; nothing for the worktree, the many, the other machine.
	if n := w.f.count("gh api"); n != 4 {
		t.Errorf("%d gh api calls, want 4:\n%s", n, strings.Join(w.f.calls, "\n"))
	}
	if n := w.f.count("gh api --hostname github.com -i -X GET repos/me/server/"); n != 1 {
		t.Errorf("the checkout of %%9 and %%11 read %d times", n)
	}
	if strings.Contains(strings.Join(w.f.calls, "\n"), "/jobs") {
		t.Error("the overview read jobs")
	}
	// Kept BoardsTTL: asked again, nothing is read.
	do(t, s, "GET", "/api/actions/boards?pane=%259", "")
	if n := w.f.count("gh api"); n != 4 {
		t.Errorf("asked again within %v: %d", BoardsTTL, n)
	}
}

// settle waits for the reads that went on after a request.
func settle(a *Actions) {
	for {
		var ch chan struct{}
		a.mu.Lock()
		for _, e := range a.overview {
			if e.reading != nil {
				ch = e.reading
			}
		}
		a.mu.Unlock()
		if ch == nil {
			return
		}
		<-ch
	}
}

func age(a *Actions, d time.Duration) {
	a.mu.Lock()
	for _, e := range a.overview {
		e.at = e.at.Add(-d)
	}
	a.mu.Unlock()
}

// After BoardsTTL a group is asked with its ETag; a 304 keeps its runs.
func TestBoardsNotModified(t *testing.T) {
	w := newBoardsWorld(t)
	a := &Actions{Runner: w.f, Gh: "gh"}
	s := w.service(a)
	do(t, s, "GET", "/api/actions/boards?pane=%259", "")
	age(a, BoardsTTL+time.Second)
	w.f.mu.Lock()
	for _, n := range []string{"server", "server-go", "mobile-ios", "mobile-android"} {
		w.f.gh["api --hostname github.com -i -X GET repos/me/"+n+"/actions/runs -F per_page=50 -f branch=main -H If-None-Match: W/\""+n+"\""] =
			fakeOut{out: "HTTP/2.0 304 Not Modified\r\nX-Ratelimit-Remaining: 4900\r\n\r\n", code: 1, err: "gh: HTTP 304"}
	}
	w.f.mu.Unlock()
	// Runs read before come at once, stale, while they are read again.
	_, m := do(t, s, "GET", "/api/actions/boards?pane=%259", "")
	for _, g := range groupsOf(t, m)[:4] {
		if g["state"] != "ok" || g["stale"] != true || g["workflows"] == nil {
			t.Errorf("while read again: %v", g)
		}
	}
	settle(a)
	_, m = do(t, s, "GET", "/api/actions/boards?pane=%259", "")
	if n := w.f.count("gh api"); n != 8 {
		t.Errorf("%d calls, want 8", n)
	}
	if n := strings.Count(strings.Join(w.f.calls, "\n"), "If-None-Match"); n != 4 {
		t.Errorf("%d conditional", n)
	}
	for _, g := range groupsOf(t, m)[:4] {
		if g["state"] != "ok" || g["stale"] != false || g["workflows"].([]any)[0].(map[string]any)["run"] == nil {
			t.Errorf("after a 304: %v", g)
		}
	}
}

// Brief 5: a group that fails does not empty the board; a rate limit backs
// the whole host off and the groups keep what they had.
func TestBoardsOneFails(t *testing.T) {
	w := newBoardsWorld(t)
	w.f.gh["api --hostname github.com -i -X GET repos/me/mobile-ios/actions/runs"] = fakeOut{
		out: "HTTP/2.0 404 Not Found\r\n\r\n{\"message\":\"Not Found\"}", code: 1, err: "gh: Not Found (HTTP 404)"}
	a := &Actions{Runner: w.f, Gh: "gh"}
	s := w.service(a)
	_, m := do(t, s, "GET", "/api/actions/boards?pane=%259", "")
	gs := groupsOf(t, m)
	if gs[3]["project"] != "mobile-ios" || gs[3]["state"] != "error" || !strings.Contains(gs[3]["detail"].(string), "Not Found") {
		t.Errorf("failed group: %v", gs[3])
	}
	for _, i := range []int{0, 1, 2} {
		if gs[i]["state"] != "ok" || gs[i]["workflows"] == nil {
			t.Errorf("group %d: %v", i, gs[i])
		}
	}

	// A secondary rate limit on one group: the host backs off for what it
	// said, every group keeps its runs, stale, and nothing more is asked.
	age(a, BoardsTTL+time.Second)
	w.f.mu.Lock()
	w.f.gh["api --hostname github.com -i -X GET repos/me/server/actions/runs"] = fakeOut{
		out:  "HTTP/2.0 403 Forbidden\r\nRetry-After: 120\r\n\r\n{\"message\":\"You have exceeded a secondary rate limit.\"}",
		code: 1, err: "gh: You have exceeded a secondary rate limit. (HTTP 403)"}
	for _, n := range []string{"server-go", "mobile-android", "mobile-ios"} {
		w.f.gh["api --hostname github.com -i -X GET repos/me/"+n+"/actions/runs"] = fakeOut{
			out: "HTTP/2.0 403 Forbidden\r\nRetry-After: 120\r\n\r\n{}", code: 1, err: "gh: (HTTP 403)"}
	}
	w.f.mu.Unlock()
	do(t, s, "GET", "/api/actions/boards?pane=%259", "")
	settle(a)
	_, m = do(t, s, "GET", "/api/actions/boards?pane=%259", "")
	gs = groupsOf(t, m)
	for _, g := range gs[:3] {
		if g["state"] != "ok" || g["stale"] != true || g["limitedUntil"] == nil || g["workflows"] == nil {
			t.Errorf("limited: %v", g)
		}
	}
	if gs[3]["state"] != "limited" {
		t.Errorf("a group with nothing read, limited: %v", gs[3])
	}
	until, _ := time.Parse(time.RFC3339, gs[0]["limitedUntil"].(string))
	if d := time.Until(until); d < 100*time.Second || d > 121*time.Second {
		t.Errorf("Retry-After 120: backs off %v", d)
	}
	n := w.f.count("gh api")
	age(a, BoardsTTL+time.Second)
	do(t, s, "GET", "/api/actions/boards?pane=%259", "")
	settle(a)
	if m := w.f.count("gh api"); m != n {
		t.Errorf("asked %d times while backing off", m-n)
	}
}

func TestBackoff(t *testing.T) {
	a := &Actions{}
	a.backOff("h", 0, time.Time{})
	if d := time.Until(a.limited["h"].until); d < 50*time.Second || d > backoffFirst {
		t.Errorf("first %v", d)
	}
	a.backOff("h", 0, time.Time{})
	if d := time.Until(a.limited["h"].until); d < 110*time.Second {
		t.Errorf("doubled %v", d)
	}
	for i := 0; i < 10; i++ {
		a.backOff("h", 0, time.Time{})
	}
	if d := time.Until(a.limited["h"].until); d > backoffMax {
		t.Errorf("capped %v", d)
	}
	reset := time.Now().Add(7 * time.Minute).Truncate(time.Second)
	if d, until, ok := rateLimited(map[string]string{"x-ratelimit-remaining": "0", "x-ratelimit-reset": itoa(int(reset.Unix()))}, nil); !ok || d != 0 || !until.Equal(reset) {
		t.Errorf("reset: %v %v %v", d, until, ok)
	}
	if _, _, ok := rateLimited(map[string]string{}, []byte(`{"message":"Resource not accessible by integration"}`)); ok {
		t.Error("a 403 that is not a rate limit")
	}
}

func TestSplitHTTP(t *testing.T) {
	st, h, b := splitHTTP([]byte("HTTP/2.0 304 Not Modified\r\nEtag: W/\"x\"\r\n\r\n"))
	if st != 304 || h["etag"] != `W/"x"` || len(b) != 0 {
		t.Errorf("%d %v %q", st, h, b)
	}
	st, _, b = splitHTTP([]byte("HTTP/2.0 200 OK\nA: b\n\n{\"x\":1}"))
	if st != 200 || string(b) != `{"x":1}` {
		t.Errorf("%d %q", st, b)
	}
	if st, _, _ := splitHTTP([]byte(`{"x":1}`)); st != 0 {
		t.Error("no status line")
	}
}

// Condition 1 of the review: a root is taken only when it is one of the
// checkouts found for the pane, compared as files, on every route that
// takes one -- else root would be a way to read workflow files anywhere,
// or open a window in any folder.
func TestRootIsOneOfThePanes(t *testing.T) {
	w := newBoardsWorld(t)
	// A link inside mobile to a repository that is not the pane's, and a
	// link to one that is.
	other := filepath.Join(w.many, "r0")
	os.Symlink(other, filepath.Join(w.mobile, "elsewhere"))
	os.Symlink(w.android, filepath.Join(w.base, "android-link"))
	// git prints the resolved path (/private/var/... on macOS).
	resolved, _ := filepath.EvalSymlinks(w.ios)
	w.f.gh["api --hostname github.com -X GET repos/me/mobile-ios/actions/runs"] = fakeOut{out: `{"total_count":0,"workflow_runs":[]}`}
	w.f.gh["api --hostname github.com -X GET repos/me/mobile-ios/actions/runs/7"] = fakeOut{code: 1, err: "gh: Not Found (HTTP 404)"}
	w.f.gh["api --hostname github.com -X GET repos/me/mobile-ios/actions/jobs"] = fakeOut{code: 1, err: "gh: Not Found (HTTP 404)"}
	w.f.gh["run "] = fakeOut{code: 1, err: "gh: Not Found (HTTP 404)"}
	w.f.gh["api --hostname github.com --allow-escape-sequences"] = fakeOut{code: 1, err: "gh: Not Found (HTTP 404)"}
	s := w.service(&Actions{Runner: w.f, Gh: "gh"})

	type route struct{ method, path, body string }
	routes := func(root string) []route {
		q := url.QueryEscape(root)
		j := strings.ReplaceAll(root, `"`, `\"`)
		return []route{
			{"GET", "/api/actions/board?pane=%258&root=" + q, ""},
			{"GET", "/api/actions/run?pane=%258&id=7&root=" + q, ""},
			{"GET", "/api/actions/log?pane=%258&id=7&root=" + q, ""},
			{"POST", "/api/actions/rerun", `{"pane":"%8","id":7,"root":"` + j + `"}`},
			{"POST", "/api/actions/cancel", `{"pane":"%8","id":7,"root":"` + j + `"}`},
			{"POST", "/api/actions/open", `{"pane":"%8","run":7,"job":9,"root":"` + j + `"}`},
		}
	}
	bad := []string{
		other,                                          // a checkout, but not one found for %8
		filepath.Join(w.mobile, "elsewhere"),           // a link below the pane's folder to it
		w.wt,                                           // a worktree below it: not counted
		w.mobile,                                       // the pane's folder, in no checkout
		w.cp,                                           // another pane's checkout
		"/etc", "relative/mobile-ios", w.ios + "-nope", // elsewhere, not absolute, not there
		w.ios + "/.github", // inside a found checkout, not it
	}
	for _, root := range bad {
		for _, r := range routes(root) {
			code, m := do(t, s, r.method, r.path, r.body)
			if code != 400 || m["error"] != "bad root" {
				t.Errorf("%s %s root=%s: %d %v", r.method, r.path, root, code, m)
			}
		}
	}
	// The found ones pass, however the path to them is spelled: as found,
	// resolved, through a link. What is used is the one found.
	for _, root := range []string{w.ios, resolved, w.android, filepath.Join(w.base, "android-link")} {
		for _, r := range routes(root) {
			if code, m := do(t, s, r.method, r.path, r.body); code == 400 && m["error"] == "bad root" {
				t.Errorf("%s %s root=%s refused", r.method, r.path, root)
			}
		}
	}
	code, m := do(t, s, "GET", "/api/actions/board?pane=%258&root="+url.QueryEscape(resolved), "")
	if code != 200 || m["repo"] != "me/mobile-ios" || m["root"] != w.ios || m["state"] != "ok" {
		t.Errorf("the board of a root: %d %v", code, m)
	}
	// Without root, the routes are as they were: %8's folder is in no
	// checkout.
	if _, m := do(t, s, "GET", "/api/actions/board?pane=%258", ""); m["state"] != StateNotGit {
		t.Errorf("no root: %v", m)
	}
}
