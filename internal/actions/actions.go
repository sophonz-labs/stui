// Package actions reads the GitHub Actions runs of the repository a
// pane is in, read with the machine's own `gh` and its own login. stui never
// sees the token: gh holds it, and stui only reads what gh prints.
package actions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sophonz-labs/stui/internal/proc"
)

// Caps.
const (
	// Bytes of one command's output read.
	MaxOutput = 4 << 20
	// Runs in a list, by default and at most.
	DefaultRuns = 30
	MaxRuns     = 100
	// What a failed log answer keeps: the last lines, each cut.
	MaxLogLines    = 400
	MaxLogLineSize = 1000
	// Bytes of one job's whole log read, for stui ci log.
	MaxLogBytes = 64 << 20
)

// Runner runs a program (git or gh) in dir and returns what it printed on
// stdout, at most max bytes. A test gives a fake one.
type Runner interface {
	Run(ctx context.Context, dir, bin string, max int, args ...string) ([]byte, error)
}

// ExitError is a program that ran and failed: its exit status and stderr.
type ExitError struct {
	Code   int
	Stderr string
}

func (e *ExitError) Error() string {
	if e.Stderr != "" {
		return e.Stderr
	}
	return fmt.Sprintf("exit status %d", e.Code)
}

// ExecRunner runs real programs, with prompts, colour, pagers and gh's
// update check off.
type ExecRunner struct{ Timeout time.Duration }

func (x ExecRunner) Run(ctx context.Context, dir, bin string, max int, args ...string) ([]byte, error) {
	t := x.Timeout
	if t == 0 {
		t = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, t)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	proc.Hide(cmd)
	cmd.Env = append(os.Environ(),
		"GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "GH_SPINNER_DISABLED=1", "NO_COLOR=1", "CLICOLOR=0",
		"GH_PAGER=cat", "PAGER=cat", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	b, _ := io.ReadAll(io.LimitReader(out, int64(max)))
	io.Copy(io.Discard, out)
	err = cmd.Wait()
	if ctx.Err() != nil {
		return nil, errors.New("timed out")
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return b, &ExitError{Code: ee.ExitCode(), Stderr: strings.TrimSpace(stderr.String())}
		}
		return nil, err
	}
	return b, nil
}

// States a list answers with instead of runs.
const (
	StateOK        = "ok"
	StateNoGh      = "no-gh"      // gh is not installed
	StateNoAuth    = "no-auth"    // gh is not signed in to the repository's host
	StateNotGit    = "not-git"    // the pane is in no git checkout
	StateNotGitHub = "not-github" // the checkout has no GitHub remote
)

// Repo is a GitHub repository a checkout pushes to.
type Repo struct {
	Host  string `json:"host"`
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

func (r Repo) Slug() string { return r.Owner + "/" + r.Name }

// Arg is gh's -R value: OWNER/REPO, or HOST/OWNER/REPO off github.com.
func (r Repo) Arg() string {
	if r.Host == "github.com" {
		return r.Slug()
	}
	return r.Host + "/" + r.Slug()
}

func (r Repo) URL() string { return "https://" + r.Host + "/" + r.Slug() }

var scpRE = regexp.MustCompile(`^(?:[^@/]+@)?([^:/]+):(.+)$`)

// ParseRemote reads a git remote URL (https, ssh, scp-like) as a
// repository; false when it does not name host/owner/repo.
func ParseRemote(raw string) (Repo, bool) {
	raw = strings.TrimSpace(raw)
	var host, path string
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" && u.Host != "" {
		switch u.Scheme {
		case "https", "http", "ssh", "git":
		default:
			return Repo{}, false
		}
		host, path = u.Hostname(), u.Path
	} else if m := scpRE.FindStringSubmatch(raw); m != nil && !strings.Contains(m[1], `\`) && len(m[1]) > 1 {
		host, path = m[1], m[2]
	} else {
		return Repo{}, false
	}
	host = strings.ToLower(host)
	// ssh.github.com:443 is github.com's ssh over https.
	if host == "ssh.github.com" {
		host = "github.com"
	}
	path = strings.Trim(strings.TrimSuffix(strings.Trim(path, "/"), ".git"), "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Repo{}, false
	}
	return Repo{Host: host, Owner: parts[0], Name: parts[1]}, true
}

// Run is one workflow run in a list.
type Run struct {
	ID         int64  `json:"id"`
	Number     int    `json:"number,omitempty"`
	Attempt    int    `json:"attempt,omitempty"`
	Workflow   string `json:"workflow"`
	Title      string `json:"title"`
	Branch     string `json:"branch,omitempty"`
	Sha        string `json:"sha,omitempty"`
	Event      string `json:"event,omitempty"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
	CreatedAt  string `json:"createdAt,omitempty"`
	StartedAt  string `json:"startedAt,omitempty"`
	UpdatedAt  string `json:"updatedAt,omitempty"`
	URL        string `json:"url,omitempty"`
	// Its workflow's file name ("ci.yml"), when read from REST.
	file string
}

// Step is one step of a job.
type Step struct {
	Number      int    `json:"number"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion,omitempty"`
	StartedAt   string `json:"startedAt,omitempty"`
	CompletedAt string `json:"completedAt,omitempty"`
}

// Job is one job of a run.
type Job struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion,omitempty"`
	StartedAt   string `json:"startedAt,omitempty"`
	CompletedAt string `json:"completedAt,omitempty"`
	URL         string `json:"url,omitempty"`
	Steps       []Step `json:"steps"`
	// Which runner: GitHub's or ours, and its name and labels.
	Runner RunnerInfo `json:"runner"`
	// The step it is on, while it runs.
	Step  *StepNow `json:"step,omitempty"`
	runID int64
}

// RunDetail is a run with its jobs.
type RunDetail struct {
	Run
	Jobs []Job `json:"jobs"`
}

// LogLine is one line of a failed step's log.
type LogLine struct {
	Job  string `json:"job"`
	Step string `json:"step"`
	Text string `json:"text"`
}

// gh's JSON field names.
const runFields = "databaseId,number,attempt,workflowName,displayTitle,headBranch,headSha,event,status,conclusion,createdAt,startedAt,updatedAt,url"

type ghRun struct {
	DatabaseID   int64  `json:"databaseId"`
	Number       int    `json:"number"`
	Attempt      int    `json:"attempt"`
	WorkflowName string `json:"workflowName"`
	DisplayTitle string `json:"displayTitle"`
	HeadBranch   string `json:"headBranch"`
	HeadSha      string `json:"headSha"`
	Event        string `json:"event"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion"`
	CreatedAt    string `json:"createdAt"`
	StartedAt    string `json:"startedAt"`
	UpdatedAt    string `json:"updatedAt"`
	URL          string `json:"url"`
	Jobs         []struct {
		DatabaseID  int64  `json:"databaseId"`
		Name        string `json:"name"`
		Status      string `json:"status"`
		Conclusion  string `json:"conclusion"`
		StartedAt   string `json:"startedAt"`
		CompletedAt string `json:"completedAt"`
		URL         string `json:"url"`
		Steps       []Step `json:"steps"`
	} `json:"jobs"`
}

func (g ghRun) run() Run {
	sha := g.HeadSha
	if len(sha) > 7 {
		sha = sha[:7]
	}
	return Run{
		ID: g.DatabaseID, Number: g.Number, Attempt: g.Attempt, Workflow: g.WorkflowName, Title: g.DisplayTitle,
		Branch: g.HeadBranch, Sha: sha, Event: g.Event, Status: g.Status, Conclusion: g.Conclusion,
		CreatedAt: zeroTime(g.CreatedAt), StartedAt: zeroTime(g.StartedAt), UpdatedAt: zeroTime(g.UpdatedAt), URL: g.URL,
	}
}

// gh prints the zero time for a step not started.
func zeroTime(s string) string {
	if strings.HasPrefix(s, "0001-01-01") {
		return ""
	}
	return s
}

// Actions answers for one machine. Zero values work: gh and git on PATH.
type Actions struct {
	Runner Runner
	// Gh is gh's path; "" finds it on PATH and in the usual places.
	Gh  string
	Git string

	mu    sync.Mutex
	ghAt  time.Time
	ghBin string
	auth  map[string]cached[bool]
	lists map[string]cached[[]Run]
	sem   chan struct{}
	// The boards, kept boardTTL, and the one being read for each.
	boards map[boardKey]*boardEntry
	// The coordinator's board (boards.go): each group's runs, kept
	// BoardsTTL; the groups read at once; the hosts backing off a rate
	// limit; and finished runs' jobs, which no longer change.
	overview map[boardKey]*ovEntry
	ovSem    chan struct{}
	limited  map[string]*backoff
	doneJobs map[string]cached[[]Job]
	// Upstreams' default branches and workflows (upstream.go).
	metas map[string]*upMeta
}

type cached[T any] struct {
	v  T
	at time.Time
}

const (
	listTTL = 5 * time.Second
	authTTL = time.Minute
	ghTTL   = 5 * time.Minute
)

func (a *Actions) runner() Runner {
	if a.Runner != nil {
		return a.Runner
	}
	return ExecRunner{}
}

// limit runs at most four gh commands at once: a dozen phones refreshing
// should not start a dozen network calls each.
func (a *Actions) limit(ctx context.Context) (func(), error) {
	a.mu.Lock()
	if a.sem == nil {
		a.sem = make(chan struct{}, 4)
	}
	sem := a.sem
	a.mu.Unlock()
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ghCandidates are where gh is installed when stui runs as a service whose
// PATH has none of the user's.
func ghCandidates() []string {
	if runtime.GOOS == "windows" {
		var c []string
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LOCALAPPDATA"} {
			if d := os.Getenv(env); d != "" {
				c = append(c, filepath.Join(d, "GitHub CLI", "gh.exe"), filepath.Join(d, "Programs", "GitHub CLI", "gh.exe"))
			}
		}
		if d := os.Getenv("USERPROFILE"); d != "" {
			c = append(c, filepath.Join(d, "scoop", "shims", "gh.exe"))
		}
		return c
	}
	c := []string{"/opt/homebrew/bin/gh", "/usr/local/bin/gh", "/usr/bin/gh", "/snap/bin/gh", "/home/linuxbrew/.linuxbrew/bin/gh"}
	if h, err := os.UserHomeDir(); err == nil {
		c = append(c, filepath.Join(h, ".local", "bin", "gh"), filepath.Join(h, "bin", "gh"))
	}
	return c
}

// gh is gh's path, "" when it is not installed.
func (a *Actions) gh() string {
	if a.Gh != "" {
		return a.Gh
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.ghAt.IsZero() && time.Since(a.ghAt) < ghTTL {
		return a.ghBin
	}
	bin := ""
	if p, err := exec.LookPath("gh"); err == nil {
		bin = p
	} else {
		for _, c := range ghCandidates() {
			if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
				bin = c
				break
			}
		}
	}
	a.ghBin, a.ghAt = bin, time.Now()
	return bin
}

func (a *Actions) git() string {
	if a.Git != "" {
		return a.Git
	}
	return "git"
}

// Where is what a pane's checkout is on GitHub.
type Where struct {
	State  string `json:"state"`
	Root   string `json:"root,omitempty"`
	Branch string `json:"branch,omitempty"`
	Repo   *Repo  `json:"repo,omitempty"`
	// The gh message for no-auth.
	Detail string `json:"detail,omitempty"`
	gh     string
	// The checkout's other GitHub remotes, each another repository than
	// Repo and than each other, at most MaxUpstreams (upstream.go). Only
	// when State is ok.
	others []Remote
	// Set when this is one of those, read on its default branch: its
	// remote's name, the checkout it is a remote of, and its workflows as
	// GitHub lists them.
	remote     string
	upstreamOf string
	wfs        []Workflow
}

// Locate finds the checkout cwd is in, the GitHub repository it pushes to,
// and whether gh can read it.
func (a *Actions) Locate(ctx context.Context, cwd string) Where {
	r := a.runner()
	git := func(max int, args ...string) (string, error) {
		out, err := r.Run(ctx, cwd, a.git(), max, append([]string{"-c", "core.fsmonitor=false", "--no-pager"}, args...)...)
		return strings.TrimSpace(string(out)), err
	}
	root, err := git(8<<10, "rev-parse", "--show-toplevel")
	if err != nil || root == "" {
		return Where{State: StateNotGit}
	}
	w := Where{Root: root}
	if b, err := git(1<<10, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		w.Branch = b
	}
	// The remote the branch pushes to, then origin, then upstream, then
	// any other: the first that is a GitHub repository.
	var names []string
	if w.Branch != "" {
		if n, err := git(1<<10, "config", "--get", "branch."+w.Branch+".remote"); err == nil && n != "" && n != "." {
			names = append(names, n)
		}
	}
	all, _ := git(16<<10, "remote")
	rest := strings.Fields(all)
	sort.SliceStable(rest, func(i, j int) bool { return remoteRank(rest[i]) < remoteRank(rest[j]) })
	names = append(names, rest...)
	seen := map[string]bool{}
	var candidates []Remote
	for _, n := range names {
		if seen[n] || strings.HasPrefix(n, "-") {
			continue
		}
		seen[n] = true
		u, err := git(4<<10, "remote", "get-url", "--", n)
		if err != nil {
			continue
		}
		if repo, ok := ParseRemote(u); ok {
			candidates = append(candidates, Remote{Name: n, Repo: repo})
		}
	}
	gh := a.gh()
	if len(candidates) == 0 {
		w.State = StateNotGitHub
		return w
	}
	if gh == "" {
		// Without gh only github.com is known to be GitHub.
		for _, c := range candidates {
			if c := c.Repo; c.Host == "github.com" {
				w.Repo = &c
				w.State = StateNoGh
				return w
			}
		}
		w.State = StateNotGitHub
		return w
	}
	w.gh = gh
	var noAuth *Where
	for _, rc := range candidates {
		c := rc.Repo
		ok, detail := a.authed(ctx, gh, c.Host)
		if ok {
			w.Repo, w.State = &c, StateOK
			w.others = others(candidates, c)
			return w
		}
		if c.Host == "github.com" && noAuth == nil {
			nw := w
			nw.Repo, nw.State, nw.Detail = &c, StateNoAuth, detail
			noAuth = &nw
		}
	}
	if noAuth != nil {
		return *noAuth
	}
	w.State = StateNotGitHub
	return w
}

func remoteRank(n string) int {
	switch n {
	case "origin":
		return 0
	case "upstream":
		return 1
	case "github":
		return 2
	}
	return 3
}

// authed is whether gh is signed in to host. A host gh does not know (a
// GitLab remote) is not signed in either; the caller tells them apart.
func (a *Actions) authed(ctx context.Context, gh, host string) (bool, string) {
	a.mu.Lock()
	if c, ok := a.auth[host]; ok && time.Since(c.at) < authTTL {
		a.mu.Unlock()
		return c.v, ""
	}
	a.mu.Unlock()
	_, err := a.runner().Run(ctx, "", gh, 64<<10, "auth", "status", "--hostname", host)
	ok := err == nil
	detail := ""
	var ee *ExitError
	if errors.As(err, &ee) {
		detail = firstLine(ee.Stderr)
	} else if err != nil {
		// Not a verdict (a timeout): do not remember it.
		return false, err.Error()
	}
	a.mu.Lock()
	if a.auth == nil {
		a.auth = map[string]cached[bool]{}
	}
	a.auth[host] = cached[bool]{v: ok, at: time.Now()}
	a.mu.Unlock()
	return ok, detail
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

func (a *Actions) gho(ctx context.Context, w Where, max int, args ...string) ([]byte, error) {
	done, err := a.limit(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return a.runner().Run(ctx, w.Root, w.gh, max, args...)
}

// Runs lists the repository's latest runs, of one branch when branch is
// not "". Answers are kept five seconds.
func (a *Actions) Runs(ctx context.Context, w Where, branch string, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = DefaultRuns
	}
	limit = min(limit, MaxRuns)
	key := fmt.Sprintf("%s|%s|%d", w.Repo.Arg(), branch, limit)
	a.mu.Lock()
	if c, ok := a.lists[key]; ok && time.Since(c.at) < listTTL {
		a.mu.Unlock()
		return c.v, nil
	}
	a.mu.Unlock()
	args := []string{"run", "list", "-R", w.Repo.Arg(), "-L", fmt.Sprint(limit), "--json", runFields}
	if branch != "" {
		args = append(args, "-b", branch)
	}
	out, err := a.gho(ctx, w, MaxOutput, args...)
	if err != nil {
		return nil, err
	}
	var raw []ghRun
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("gh printed something else: %w", err)
	}
	runs := make([]Run, 0, len(raw))
	for _, g := range raw {
		runs = append(runs, g.run())
	}
	a.mu.Lock()
	if a.lists == nil {
		a.lists = map[string]cached[[]Run]{}
	}
	for k, c := range a.lists {
		if time.Since(c.at) > listTTL {
			delete(a.lists, k)
		}
	}
	a.lists[key] = cached[[]Run]{v: runs, at: time.Now()}
	a.mu.Unlock()
	return runs, nil
}

// Forget drops the kept lists, after a rerun or cancel changed them.
func (a *Actions) Forget() {
	a.mu.Lock()
	a.lists = nil
	a.mu.Unlock()
}

// Detail reads one run and its jobs, their steps and runners: two REST
// calls, since the run's own answer has no jobs and the jobs answer has no
// run.
func (a *Actions) Detail(ctx context.Context, w Where, id int64) (RunDetail, error) {
	var g restRun
	if err := a.api(ctx, w, &g, fmt.Sprintf("repos/%s/actions/runs/%d", w.Repo.Slug(), id)); err != nil {
		return RunDetail{}, err
	}
	jobs, err := a.Jobs(ctx, w, id)
	if err != nil {
		return RunDetail{}, err
	}
	return RunDetail{Run: g.run(), Jobs: jobs}, nil
}

// Jobs reads a run's jobs (the latest attempt's), at most 100.
func (a *Actions) Jobs(ctx context.Context, w Where, run int64) ([]Job, error) {
	var g restJobs
	if err := a.api(ctx, w, &g, fmt.Sprintf("repos/%s/actions/runs/%d/jobs", w.Repo.Slug(), run), "-F", "per_page=100"); err != nil {
		return nil, err
	}
	jobs := make([]Job, 0, len(g.Jobs))
	for _, j := range g.Jobs {
		jobs = append(jobs, j.job())
	}
	return jobs, nil
}

// Job reads one job.
func (a *Actions) Job(ctx context.Context, w Where, id int64) (Job, error) {
	var g restJob
	if err := a.api(ctx, w, &g, fmt.Sprintf("repos/%s/actions/jobs/%d", w.Repo.Slug(), id)); err != nil {
		return Job{}, err
	}
	return g.job(), nil
}

// api runs `gh api` for a GET of path on the repository's host and reads
// its JSON into v. Fields go as query parameters (-X GET).
func (a *Actions) api(ctx context.Context, w Where, v any, path string, fields ...string) error {
	args := []string{"api", "--hostname", w.Repo.Host, "-X", "GET", path}
	args = append(args, fields...)
	out, err := a.gho(ctx, w, MaxOutput, args...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, v); err != nil {
		return fmt.Errorf("gh printed something else: %w", err)
	}
	return nil
}

// JobLog is a finished job's whole log, as GitHub keeps it: a timestamp
// before each line, a BOM before the first. ErrNoLogYet while GitHub has
// not made it (a job still going, or one just ended).
func (a *Actions) JobLog(ctx context.Context, w Where, job int64) ([]byte, error) {
	out, err := a.gho(ctx, w, MaxLogBytes, "api", "--hostname", w.Repo.Host, "--allow-escape-sequences",
		fmt.Sprintf("repos/%s/actions/jobs/%d/logs", w.Repo.Slug(), job))
	if err != nil {
		var ee *ExitError
		if errors.As(err, &ee) && (strings.Contains(ee.Stderr, "404") || strings.Contains(ee.Stderr, "BlobNotFound")) {
			return nil, ErrNoLogYet
		}
		return nil, err
	}
	return out, nil
}

// ErrNoLogYet is a job log GitHub has not made yet.
var ErrNoLogYet = errors.New("no log yet")

// ErrInProgress is a failed log asked of a run still going.
var ErrInProgress = errors.New("the run is still in progress")

// FailedLog is the end of the failed steps' logs: the last MaxLogLines
// lines, each without its timestamp and colour.
func (a *Actions) FailedLog(ctx context.Context, w Where, id, job int64) ([]LogLine, bool, error) {
	args := []string{"run", "view", fmt.Sprint(id), "-R", w.Repo.Arg(), "--log-failed"}
	if job != 0 {
		args = append(args, "--job", fmt.Sprint(job))
	}
	out, err := a.gho(ctx, w, MaxOutput, args...)
	if err != nil {
		var ee *ExitError
		if errors.As(err, &ee) && strings.Contains(strings.ToLower(ee.Stderr), "in progress") {
			return nil, false, ErrInProgress
		}
		return nil, false, err
	}
	lines, cut := ParseLog(out, MaxLogLines)
	return lines, cut || len(out) >= MaxOutput, nil
}

var (
	ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	tsRE   = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z ?`)
)

// ParseLog reads gh's log ("job<TAB>step<TAB>time text" a line) and keeps
// the last max lines; true when it dropped some.
func ParseLog(b []byte, max int) ([]LogLine, bool) {
	text := strings.ReplaceAll(string(b), "\r\n", "\n")
	raw := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(raw) == 1 && raw[0] == "" {
		return []LogLine{}, false
	}
	cut := false
	if len(raw) > max {
		raw, cut = raw[len(raw)-max:], true
	}
	lines := make([]LogLine, 0, len(raw))
	for _, l := range raw {
		var ln LogLine
		parts := strings.SplitN(l, "\t", 3)
		if len(parts) == 3 {
			ln.Job, ln.Step, l = parts[0], parts[1], parts[2]
		}
		l = strings.TrimPrefix(l, "\ufeff")
		l = tsRE.ReplaceAllString(l, "")
		l = ansiRE.ReplaceAllString(l, "")
		l = strings.ReplaceAll(l, "\r", "")
		if len(l) > MaxLogLineSize {
			l = strings.ToValidUTF8(l[:MaxLogLineSize], "") + "…"
		}
		ln.Text = l
		lines = append(lines, ln)
	}
	return lines, cut
}

// Rerun starts a run again, only its failed jobs when failed.
func (a *Actions) Rerun(ctx context.Context, w Where, id int64, failed bool) error {
	args := []string{"run", "rerun", fmt.Sprint(id), "-R", w.Repo.Arg()}
	if failed {
		args = append(args, "--failed")
	}
	_, err := a.gho(ctx, w, 64<<10, args...)
	a.Forget()
	return err
}

// Cancel stops a run in progress.
func (a *Actions) Cancel(ctx context.Context, w Where, id int64) error {
	_, err := a.gho(ctx, w, 64<<10, "run", "cancel", fmt.Sprint(id), "-R", w.Repo.Arg())
	a.Forget()
	return err
}

// At is a repository named the way gh's -R takes it (OWNER/REPO, or
// HOST/OWNER/REPO), with gh found and asked whether it is signed in there.
// dir is where gh runs.
func (a *Actions) At(ctx context.Context, arg, dir string) Where {
	parts := strings.Split(strings.Trim(arg, "/"), "/")
	var r Repo
	switch {
	case len(parts) == 2 && parts[0] != "" && parts[1] != "":
		r = Repo{Host: "github.com", Owner: parts[0], Name: parts[1]}
	case len(parts) == 3 && parts[0] != "" && parts[1] != "" && parts[2] != "":
		r = Repo{Host: strings.ToLower(parts[0]), Owner: parts[1], Name: parts[2]}
	default:
		return Where{State: StateNotGitHub, Root: dir}
	}
	w := Where{Root: dir, Repo: &r}
	gh := a.gh()
	if gh == "" {
		w.State = StateNoGh
		return w
	}
	w.gh = gh
	if ok, detail := a.authed(ctx, gh, r.Host); !ok {
		w.State, w.Detail = StateNoAuth, detail
		return w
	}
	w.State = StateOK
	return w
}

// CommitRuns is the runs of one commit, newest first.
func (a *Actions) CommitRuns(ctx context.Context, w Where, sha string) ([]Run, error) {
	var g restRuns
	if err := a.api(ctx, w, &g, fmt.Sprintf("repos/%s/actions/runs", w.Repo.Slug()), "-f", "head_sha="+sha, "-F", "per_page=50"); err != nil {
		return nil, err
	}
	runs := make([]Run, 0, len(g.WorkflowRuns))
	for _, r := range g.WorkflowRuns {
		runs = append(runs, r.run())
	}
	return runs, nil
}

// HeadOf is the commit the checkout at root has out, and whether a remote
// branch has it.
func (a *Actions) HeadOf(ctx context.Context, root string) Head { return a.head(ctx, root) }
