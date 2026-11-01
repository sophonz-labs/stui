package actions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The coordinator's CI board: the checkouts of a pane and of every worker
// below it, one group each, with each workflow's latest run on that branch
// and no jobs. A group that is opened is read with GET /api/actions/board
// and its root.

const (
	// How long a group's runs are kept: the overview is a glance at the
	// other projects, and every client shares one call per group per this.
	BoardsTTL = 30 * time.Second
	// Checkouts looked for one level below a folder that is in none.
	MaxBelow = 8
	// Groups in one answer; the rest are skipped, and said so.
	MaxGroups = 16
	// How long a request waits for groups being read; the rest come as
	// they were (stale) or loading, and are read on.
	boardsWait = 4 * time.Second
	// Groups read at once, machine-wide (under the four gh commands).
	boardsAtOnce = 2
	// A finished run's jobs no longer change.
	doneJobsTTL = 10 * time.Minute
	// Backing off a host that said "rate limit" without saying how long.
	backoffFirst = time.Minute
	backoffMax   = 15 * time.Minute
)

// More group states than a single board has.
const (
	StateRemote  = "remote"  // a worker on another machine: not read here
	StateLimited = "limited" // the host's rate limit, and nothing read before
	StateLoading = "loading" // being read for the first time
	StateError   = "error"   // gh failed for this group; detail says how
)

// How an owner holds a group's checkout.
const (
	HowCwd    = "cwd"    // its folder is in the checkout
	HowBelow  = "below"  // the checkout is one level below its folder
	HowRemote = "remote" // it is on another machine
)

// Member is an agent whose checkouts the board shows: the pane asked for
// first, then the workers below it (orch.Subtree).
type Member struct {
	Name, Pane, Machine string
}

// Owner is a member holding a group's checkout.
type Owner struct {
	Name string `json:"name"`
	Pane string `json:"pane"`
	How  string `json:"how"`
}

// Boards is the answer of GET /api/actions/boards.
type Boards struct {
	Pane     string       `json:"pane"`
	PolledAt string       `json:"polledAt"`
	Groups   []BoardGroup `json:"groups"`
	Skipped  []Skipped    `json:"skipped"`
}

// BoardGroup is one checkout (or one other machine) and its workflows.
type BoardGroup struct {
	Project      string  `json:"project"`
	State        string  `json:"state"`
	Provider     string  `json:"provider,omitempty"`
	Repo         string  `json:"repo,omitempty"`
	Host         string  `json:"host,omitempty"`
	Root         string  `json:"root,omitempty"`
	Branch       string  `json:"branch,omitempty"`
	Sha          string  `json:"sha,omitempty"`
	Pushed       *bool   `json:"pushed,omitempty"`
	URL          string  `json:"url,omitempty"`
	Pane         string  `json:"pane,omitempty"`
	Machine      string  `json:"machine,omitempty"`
	Detail       string  `json:"detail,omitempty"`
	Owners       []Owner `json:"owners"`
	PolledAt     string  `json:"polledAt,omitempty"`
	Stale        bool    `json:"stale"`
	LimitedUntil string  `json:"limitedUntil,omitempty"`
	// An upstream (upstream.go): the root of the checkout it is a remote
	// of, and that remote's name. Root and Pane are the checkout's, Branch
	// is the upstream's default branch, and it has no owners, sha or
	// pushed.
	UpstreamOf string          `json:"upstreamOf,omitempty"`
	Remote     string          `json:"remote,omitempty"`
	Workflows  []BoardWorkflow `json:"workflows,omitempty"`
}

// Skipped is a member whose checkouts are not on the board, and why:
// not-git, too-many, no-pane, too-many-checkouts.
type Skipped struct {
	Name string `json:"name"`
	Pane string `json:"pane"`
	Why  string `json:"why"`
}

// found is a checkout found for a folder, and how.
type found struct {
	w   Where
	how string
}

// checkouts are the checkouts of cwd: the one it is in, or else the main
// checkouts (.git a directory, not a worktree's file) one level below it,
// at most MaxBelow. why is "not-git" or "too-many" when there are none.
func (a *Actions) checkouts(ctx context.Context, cwd string) (out []found, why string) {
	if w := a.Locate(ctx, cwd); w.State != StateNotGit {
		return []found{{w, HowCwd}}, ""
	}
	ents, err := os.ReadDir(cwd)
	if err != nil {
		return nil, "not-git"
	}
	var dirs []string
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		d := filepath.Join(cwd, e.Name())
		if fi, err := os.Lstat(filepath.Join(d, ".git")); err == nil && fi.IsDir() {
			dirs = append(dirs, d)
		}
	}
	if len(dirs) > MaxBelow {
		return nil, "too-many"
	}
	for _, d := range dirs {
		if w := a.Locate(ctx, d); w.State != StateNotGit {
			out = append(out, found{w, HowBelow})
		}
	}
	if len(out) == 0 {
		return nil, "not-git"
	}
	return out, ""
}

// AtRoot is the checkout root names, when it is one of the checkouts found
// for cwd (the one cwd is in, or one a level below). It answers with the
// found checkout, matched by the same file (links and /private resolved by
// the file system, not by the path's text), never with root itself.
func (a *Actions) AtRoot(ctx context.Context, cwd, root string) (Where, bool) {
	if root == "" || !filepath.IsAbs(root) {
		return Where{}, false
	}
	asked, err := os.Stat(root)
	if err != nil || !asked.IsDir() {
		return Where{}, false
	}
	fs, _ := a.checkouts(ctx, cwd)
	for _, f := range fs {
		if f.w.Root == "" {
			continue
		}
		if fi, err := os.Stat(f.w.Root); err == nil && os.SameFile(fi, asked) {
			return f.w, true
		}
	}
	return Where{}, false
}

// ovEntry is a group's runs as last read, and the read under way.
type ovEntry struct {
	at        time.Time
	runs      []Run
	etag      string
	err       error
	workflows []Workflow
	reading   chan struct{}
}

type backoff struct {
	until time.Time
	step  time.Duration
}

type groupAt struct {
	g      *BoardGroup
	w      Where
	h      Head
	cwd    int // order it was first found in a member's own folder, -1
	first  int // order it was first found at all
	remote bool
	// Where its runs are kept, and for how long.
	key boardKey
	ttl time.Duration
	// Its upstreams, after it; up is whether it is one.
	ups []*groupAt
	up  bool
}

// Boards builds the board of members' checkouts. tree is what the pane
// asked for and the workers below it; cwd reads a member's folder.
func (a *Actions) Boards(ctx context.Context, pane string, tree []Member, cwd func(context.Context, string) (string, bool)) Boards {
	out := Boards{Pane: pane, Groups: []BoardGroup{}, Skipped: []Skipped{}}
	byKey := map[boardKey]*groupAt{}
	machines := map[string]*groupAt{}
	var all []*groupAt
	n := 0
	for _, m := range tree {
		if m.Machine != "" {
			ga := machines[m.Machine]
			if ga == nil {
				ga = &groupAt{g: &BoardGroup{State: StateRemote, Machine: m.Machine, Owners: []Owner{}}, cwd: -1, first: n, remote: true}
				machines[m.Machine] = ga
				all = append(all, ga)
				n++
			}
			ga.g.Owners = append(ga.g.Owners, Owner{m.Name, m.Pane, HowRemote})
			continue
		}
		dir, ok := cwd(ctx, m.Pane)
		if !ok || dir == "" || !filepath.IsAbs(dir) {
			out.Skipped = append(out.Skipped, Skipped{m.Name, m.Pane, "no-pane"})
			continue
		}
		fs, why := a.checkouts(ctx, dir)
		if len(fs) == 0 {
			out.Skipped = append(out.Skipped, Skipped{m.Name, m.Pane, why})
			continue
		}
		for _, f := range fs {
			var h Head
			if f.w.State == StateOK {
				h = a.head(ctx, f.w.Root)
			}
			k := boardKey{root: f.w.Root, branch: f.w.Branch, sha: h.Sha}
			if f.w.Repo != nil {
				k.repo = f.w.Repo.Arg()
			}
			ga := byKey[k]
			if ga == nil {
				ga = &groupAt{g: newGroup(f.w, h, m.Pane), w: f.w, h: h, cwd: -1, first: n, ttl: BoardsTTL}
				if f.w.State == StateOK {
					ga.key = groupKey(f.w, h)
				}
				byKey[k] = ga
				all = append(all, ga)
				n++
			}
			if f.how == HowCwd && ga.cwd < 0 {
				ga.cwd = n
				n++
			}
			ga.g.Owners = addOwner(ga.g.Owners, Owner{m.Name, m.Pane, f.how})
		}
	}
	// Found in someone's own folder first (in tree order), then found a
	// level below, then the other machines.
	sort.SliceStable(all, func(i, j int) bool {
		ri, rj := rank(all[i]), rank(all[j])
		if ri != rj {
			return ri < rj
		}
		return order(all[i]) < order(all[j])
	})
	if len(all) > MaxGroups {
		for _, ga := range all[MaxGroups:] {
			out.Skipped = append(out.Skipped, Skipped{ga.g.Project, ga.g.Pane, "too-many-checkouts"})
		}
		all = all[:MaxGroups]
	}
	deadline := time.NewTimer(boardsWait)
	defer deadline.Stop()
	read := a.upstreams(ctx, all, deadline.C)
	a.readGroups(ctx, read, deadline.C)
	for _, ga := range all {
		out.Groups = append(out.Groups, *ga.g)
		for _, u := range ga.ups {
			out.Groups = append(out.Groups, *u.g)
		}
	}
	out.PolledAt = time.Now().UTC().Format(time.RFC3339)
	return out
}

func rank(ga *groupAt) int {
	switch {
	case ga.remote:
		return 2
	case ga.cwd >= 0:
		return 0
	}
	return 1
}

func order(ga *groupAt) int {
	if ga.cwd >= 0 {
		return ga.cwd
	}
	return ga.first
}

// addOwner adds o, once per pane; found in its own folder wins over below.
func addOwner(owners []Owner, o Owner) []Owner {
	for i := range owners {
		if owners[i].Pane == o.Pane {
			if o.How == HowCwd {
				owners[i].How = HowCwd
			}
			return owners
		}
	}
	return append(owners, o)
}

func newGroup(w Where, h Head, pane string) *BoardGroup {
	g := &BoardGroup{State: w.State, Provider: "github", Root: w.Root, Branch: w.Branch, Detail: w.Detail, Pane: pane, Owners: []Owner{}}
	g.Project = filepath.Base(w.Root)
	if w.Repo != nil {
		g.Project, g.Repo, g.Host, g.URL = w.Repo.Name, w.Repo.Slug(), w.Repo.Host, w.Repo.URL()+"/actions"
	}
	if w.State == StateOK {
		pushed := h.Pushed
		g.Sha, g.Pushed = short(h.Sha), &pushed
	}
	return g
}

// upstreams gives each checkout gh reads its upstreams, after it, and is
// every group whose runs are read: the checkouts, then the upstreams. They
// are not counted in MaxGroups: an upstream does not push another
// checkout off the board. An upstream on the branch of a checkout of the
// same repository on the board is read once, with that checkout's group
// and its 30 s.
func (a *Actions) upstreams(ctx context.Context, all []*groupAt, wait <-chan time.Time) []*groupAt {
	read := append([]*groupAt(nil), all...)
	type pending struct {
		parent *groupAt
		r      Remote
	}
	var ps []pending
	for _, ga := range all {
		if ga.remote || ga.g.State != StateOK {
			continue
		}
		for _, r := range ga.w.others {
			ps = append(ps, pending{ga, r})
		}
	}
	if len(ps) == 0 {
		return read
	}
	// Their default branches at once; each waits at most until wait.
	ws := make([]Where, len(ps))
	done := make(chan int, len(ps))
	for i, p := range ps {
		go func() {
			ws[i] = a.upWhere(ctx, p.parent.w, p.r, wait)
			done <- i
		}()
	}
	for range ps {
		<-done
	}
	for i, p := range ps {
		u := ws[i]
		g := &BoardGroup{Project: u.Repo.Name, State: u.State, Provider: "github", Repo: u.Repo.Slug(), Host: u.Repo.Host,
			Root: p.parent.w.Root, Branch: u.Branch, URL: u.Repo.URL() + "/actions", Pane: p.parent.g.Pane, Detail: u.Detail,
			Owners: []Owner{}, UpstreamOf: p.parent.w.Root, Remote: p.r.Name}
		ga := &groupAt{g: g, w: u, cwd: -1, up: true, key: boardKey{repo: strings.ToLower(u.Repo.Arg()), branch: u.Branch}, ttl: UpstreamTTL}
		if u.State == StateOK {
			for _, c := range all {
				if !c.remote && !c.up && c.g.State == StateOK && c.w.Branch == u.Branch && sameRepo(*c.w.Repo, *u.Repo) {
					ga.key, ga.ttl, ga.h = c.key, c.ttl, c.h
					break
				}
			}
			read = append(read, ga)
		}
		p.parent.ups = append(p.parent.ups, ga)
	}
	return read
}

// readGroups reads the runs of the groups gh can read: what is kept, or
// read now -- until wait for this request, after which a group comes as
// it was and its read goes on.
func (a *Actions) readGroups(ctx context.Context, all []*groupAt, wait <-chan time.Time) {
	var waits []chan struct{}
	for _, ga := range all {
		if ga.g.State != StateOK {
			continue
		}
		// Only a group with nothing to show yet is waited for; one
		// with runs read before comes with them, stale, while it is read.
		if ch, had := a.startGroup(ga); ch != nil && !had {
			waits = append(waits, ch)
		}
	}
	for _, ch := range waits {
		select {
		case <-ch:
		case <-wait:
			goto fill
		case <-ctx.Done():
			goto fill
		}
	}
fill:
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, ga := range all {
		if ga.g.State != StateOK {
			continue
		}
		a.fillGroup(ga)
	}
}

func groupKey(w Where, h Head) boardKey {
	return boardKey{w.Repo.Arg(), w.Root, w.Branch, h.Sha}
}

// startGroup starts reading a group unless what is kept is fresh, the host
// is backing off, or a read is under way; the channel closes when the
// read is done, nil when there is no read. had is whether runs read before
// are kept.
func (a *Actions) startGroup(ga *groupAt) (ch chan struct{}, had bool) {
	w, h, k := ga.w, ga.h, ga.key
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.overview == nil {
		a.overview = map[boardKey]*ovEntry{}
	}
	e := a.overview[k]
	if e == nil {
		e = &ovEntry{}
		a.overview[k] = e
	}
	had = !e.at.IsZero() && e.err == nil
	if e.reading != nil {
		return e.reading, had
	}
	if !e.at.IsZero() && time.Since(e.at) < ga.ttl {
		return nil, had
	}
	if b := a.limited[w.Repo.Host]; b != nil && time.Now().Before(b.until) {
		return nil, had
	}
	for key, old := range a.overview {
		if old.reading == nil && !old.at.IsZero() && time.Since(old.at) > 10*BoardsTTL {
			delete(a.overview, key)
		}
	}
	ch = make(chan struct{})
	e.reading = ch
	etag := e.etag
	if e.err != nil {
		etag = ""
	}
	go func() {
		// Not the request's: a read that outlives it is kept for the next.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		a.mu.Lock()
		if a.ovSem == nil {
			a.ovSem = make(chan struct{}, boardsAtOnce)
		}
		sem := a.ovSem
		a.mu.Unlock()
		sem <- struct{}{}
		res := a.fetchGroup(ctx, w, h, etag)
		<-sem
		// A checkout's own files; an upstream's come with it (and one
		// read with a checkout's group leaves that group's be).
		var wfs []Workflow
		if !ga.up {
			wfs = ReadWorkflows(w.Root)
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		e.reading = nil
		close(ch)
		if res.limited {
			a.backOff(w.Repo.Host, res.retryAfter, res.until)
			return
		}
		delete(a.limited, w.Repo.Host)
		e.at = time.Now()
		if !ga.up {
			e.workflows = wfs
		}
		switch {
		case res.err != nil:
			e.err = res.err
		case res.notModified:
			e.err = nil
		default:
			e.runs, e.etag, e.err = res.runs, res.etag, nil
		}
	}()
	return ch, had
}

// backOff stops reading host's groups for a while: until the time it gave,
// or for as long as it said, or doubling from backoffFirst.
func (a *Actions) backOff(host string, retryAfter time.Duration, until time.Time) {
	if a.limited == nil {
		a.limited = map[string]*backoff{}
	}
	b := a.limited[host]
	if b == nil {
		b = &backoff{}
		a.limited[host] = b
	}
	switch {
	case !until.IsZero():
		b.until = until
	case retryAfter > 0:
		b.until = time.Now().Add(retryAfter)
	default:
		if b.step == 0 {
			b.step = backoffFirst
		} else {
			b.step = min(2*b.step, backoffMax)
		}
		b.until = time.Now().Add(b.step)
	}
}

// fillGroup puts what is kept for ga into its group. a.mu is held.
func (a *Actions) fillGroup(ga *groupAt) {
	g := ga.g
	e := a.overview[ga.key]
	b := a.limited[ga.w.Repo.Host]
	limited := b != nil && time.Now().Before(b.until)
	have := e != nil && !e.at.IsZero() && e.err == nil
	if have {
		wfs := e.workflows
		if ga.up {
			wfs = ga.w.wfs
		}
		g.PolledAt = e.at.UTC().Format(time.RFC3339)
		g.Workflows = latestRuns(wfs, e.runs, nil)
		g.Stale = time.Since(e.at) >= ga.ttl || e.reading != nil
	}
	switch {
	case limited:
		g.LimitedUntil = b.until.UTC().Format(time.RFC3339)
		if !have {
			g.State = StateLimited
		} else {
			g.Stale = true
		}
	case e != nil && e.err != nil && e.reading == nil:
		g.State, g.Detail = StateError, errLine(e.err)
	case !have:
		g.State = StateLoading
	}
}

func errLine(err error) string {
	if ee, ok := err.(*ExitError); ok {
		if l := firstLine(ee.Stderr); l != "" {
			return l
		}
	}
	return firstLine(err.Error())
}

// latestRuns is each workflow with its latest run, and its jobs from jobs
// (nil: none).
func latestRuns(wfs []Workflow, runs []Run, jobs map[int64][]Job) []BoardWorkflow {
	latest := map[string]Run{}
	for _, r := range runs {
		if _, ok := latest[r.file]; !ok {
			latest[r.file] = r
		}
	}
	out := make([]BoardWorkflow, 0, len(wfs))
	for _, wf := range wfs {
		bw := BoardWorkflow{Workflow: wf}
		if r, ok := latest[wf.File]; ok {
			bw.Run = &BoardRun{Run: r, Jobs: jobs[r.ID]}
		}
		out = append(out, bw)
	}
	return out
}

type groupRead struct {
	runs        []Run
	etag        string
	notModified bool
	// A rate limit, and how long it asked to wait or until when (both
	// zero: back off by doubling).
	limited    bool
	retryAfter time.Duration
	until      time.Time
	err        error
}

// fetchGroup lists a group's runs with `gh api -i`, sending etag so that
// an unchanged list is a 304 (which the primary rate limit does not
// count). gh exits 1 on a 304, so its status line is
// read here, not its exit code.
func (a *Actions) fetchGroup(ctx context.Context, w Where, h Head, etag string) groupRead {
	args := []string{"api", "--hostname", w.Repo.Host, "-i", "-X", "GET", fmt.Sprintf("repos/%s/actions/runs", w.Repo.Slug()),
		"-F", fmt.Sprintf("per_page=%d", BoardRuns)}
	if w.Branch != "" {
		args = append(args, "-f", "branch="+w.Branch)
	} else if h.Sha != "" {
		args = append(args, "-f", "head_sha="+h.Sha)
	}
	if etag != "" {
		args = append(args, "-H", "If-None-Match: "+etag)
	}
	out, err := a.gho(ctx, w, MaxOutput, args...)
	status, hdr, body := splitHTTP(out)
	switch {
	case status == 304:
		return groupRead{notModified: true}
	case status == 200 && err == nil:
		var g restRuns
		if jerr := json.Unmarshal(body, &g); jerr != nil {
			return groupRead{err: fmt.Errorf("gh printed something else: %w", jerr)}
		}
		runs := make([]Run, 0, len(g.WorkflowRuns))
		for _, r := range g.WorkflowRuns {
			runs = append(runs, r.run())
		}
		return groupRead{runs: runs, etag: hdr["etag"]}
	case status == 403 || status == 429:
		if d, until, ok := rateLimited(hdr, body); ok {
			return groupRead{limited: true, retryAfter: d, until: until}
		}
	case status == 0 && err != nil && isRateLimit(err.Error()):
		return groupRead{limited: true}
	}
	if err == nil {
		err = fmt.Errorf("HTTP %d", status)
	}
	return groupRead{err: err}
}

// splitHTTP reads what `gh api -i` printed: the status line, the headers
// (lower-cased names) and the body. Status 0 when it is not that.
func splitHTTP(out []byte) (int, map[string]string, []byte) {
	hdr := map[string]string{}
	if !bytes.HasPrefix(out, []byte("HTTP/")) {
		return 0, hdr, out
	}
	sc := bufio.NewReader(bytes.NewReader(out))
	line, _ := sc.ReadString('\n')
	f := strings.Fields(line)
	if len(f) < 2 {
		return 0, hdr, out
	}
	status, _ := strconv.Atoi(f[1])
	n := len(line)
	for {
		l, err := sc.ReadString('\n')
		n += len(l)
		t := strings.TrimRight(l, "\r\n")
		if t == "" {
			break
		}
		if k, v, ok := strings.Cut(t, ":"); ok {
			hdr[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
		if err != nil {
			break
		}
	}
	return status, hdr, out[min(n, len(out)):]
}

// rateLimited is a 403 or 429 that is GitHub's rate limit, primary or
// secondary, and how long to stay away: Retry-After, else the reset of an
// exhausted limit, else neither (the doubling backoff).
func rateLimited(hdr map[string]string, body []byte) (time.Duration, time.Time, bool) {
	if s, err := strconv.Atoi(hdr["retry-after"]); err == nil && s > 0 {
		return time.Duration(s) * time.Second, time.Time{}, true
	}
	if hdr["x-ratelimit-remaining"] == "0" {
		if r, err := strconv.ParseInt(hdr["x-ratelimit-reset"], 10, 64); err == nil {
			return 0, time.Unix(r, 0), true
		}
		return 0, time.Time{}, true
	}
	if isRateLimit(string(body)) {
		return 0, time.Time{}, true
	}
	return 0, time.Time{}, false
}

func isRateLimit(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "rate limit")
}
