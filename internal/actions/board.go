package actions

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// The CI board: every workflow of the pane's checkout with its latest run on
// this branch, and the jobs of the few runs worth watching. One `gh api` for
// the runs and at most BoardJobRuns more for jobs, kept boardTTL for every
// client on the machine.

const (
	// Runs read for the board: enough to find each workflow's latest.
	BoardRuns = 50
	// Runs whose jobs come with the board; the rest come with their status
	// only, and a client asks /api/actions/run when one is opened.
	BoardJobRuns = 3
	boardTTL     = 5 * time.Second
)

// Board is the answer of GET /api/actions/board.
type Board struct {
	State    string `json:"state"`
	Provider string `json:"provider"`
	Repo     string `json:"repo,omitempty"`
	Host     string `json:"host,omitempty"`
	Root     string `json:"root,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Sha      string `json:"sha,omitempty"`
	// False when the checked-out commit is on no remote branch: it has no
	// runs because it has not been pushed.
	Pushed   bool   `json:"pushed"`
	URL      string `json:"url,omitempty"`
	Detail   string `json:"detail,omitempty"`
	PolledAt string `json:"polledAt,omitempty"`
	// An upstream's board (?remote=): the remote's name and the checkout
	// it is a remote of. Branch is the upstream's default branch.
	Remote     string          `json:"remote,omitempty"`
	UpstreamOf string          `json:"upstreamOf,omitempty"`
	Workflows  []BoardWorkflow `json:"workflows,omitempty"`
}

// BoardWorkflow is one workflow file and its latest run, nil when it has none.
type BoardWorkflow struct {
	Workflow
	Run *BoardRun `json:"run"`
}

// BoardRun is a run, with its jobs when it is one of the few the board
// reads them for: Jobs is nil (JSON null) for the others.
type BoardRun struct {
	Run
	Jobs []Job `json:"jobs"`
}

// Head is the commit a checkout has out.
type Head struct {
	Sha    string // full
	Pushed bool
}

// head reads the checked-out commit and whether a remote branch has it.
func (a *Actions) head(ctx context.Context, root string) Head {
	r := a.runner()
	git := func(args ...string) (string, error) {
		out, err := r.Run(ctx, root, a.git(), 4<<10, append([]string{"-c", "core.fsmonitor=false", "--no-pager"}, args...)...)
		return strings.TrimSpace(string(out)), err
	}
	sha, err := git("rev-parse", "HEAD")
	if err != nil {
		return Head{}
	}
	// Empty: every commit up to HEAD is on some remote-tracking branch.
	ahead, err := git("rev-list", "-1", "HEAD", "--not", "--remotes")
	return Head{Sha: sha, Pushed: err == nil && ahead == ""}
}

type boardKey struct{ repo, root, branch, sha string }

type boardEntry struct {
	at        time.Time
	runs      []Run
	jobs      map[int64][]Job
	err       error
	done      chan struct{}
	workflows []Workflow
}

// Board builds the board for a located checkout.
func (a *Actions) Board(ctx context.Context, w Where) (Board, error) {
	b := Board{State: w.State, Provider: "github", Root: w.Root, Branch: w.Branch, Detail: w.Detail}
	if w.Repo != nil {
		b.Repo, b.Host, b.URL = w.Repo.Slug(), w.Repo.Host, w.Repo.URL()+"/actions"
	}
	b.Remote, b.UpstreamOf = w.remote, w.upstreamOf
	if w.State != StateOK {
		return b, nil
	}
	var h Head
	if w.remote == "" {
		h = a.head(ctx, w.Root)
		b.Sha, b.Pushed = short(h.Sha), h.Pushed
	} else {
		// Nothing of ours to push: its runs are the upstream's.
		b.Pushed = true
	}
	e, err := a.boardData(ctx, w, h)
	if err != nil {
		return b, err
	}
	b.PolledAt = e.at.UTC().Format(time.RFC3339)
	b.Workflows = latestRuns(e.workflows, e.runs, e.jobs)
	return b, nil
}

// boardData is the runs and jobs, from the cache or from gh; one request
// at a time per board, so clients polling together share one set of calls.
func (a *Actions) boardData(ctx context.Context, w Where, h Head) (*boardEntry, error) {
	k := boardKey{w.Repo.Arg(), w.Root, w.Branch, h.Sha}
	a.mu.Lock()
	if a.boards == nil {
		a.boards = map[boardKey]*boardEntry{}
	}
	for {
		e, ok := a.boards[k]
		if !ok {
			break
		}
		if e.done != nil {
			done := e.done
			a.mu.Unlock()
			select {
			case <-done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			a.mu.Lock()
			continue
		}
		if e.err == nil && time.Since(e.at) < boardTTL {
			a.mu.Unlock()
			return e, nil
		}
		break
	}
	for key, e := range a.boards {
		if e.done == nil && time.Since(e.at) > boardTTL {
			delete(a.boards, key)
		}
	}
	e := &boardEntry{done: make(chan struct{})}
	a.boards[k] = e
	a.mu.Unlock()

	if w.remote != "" {
		e.workflows = w.wfs
	} else {
		e.workflows = ReadWorkflows(w.Root)
	}
	e.runs, e.jobs, e.err = a.fetchBoard(ctx, w, h)
	e.at = time.Now()
	a.mu.Lock()
	close(e.done)
	e.done = nil
	if e.err != nil {
		delete(a.boards, k)
	}
	a.mu.Unlock()
	return e, e.err
}

func (a *Actions) fetchBoard(ctx context.Context, w Where, h Head) ([]Run, map[int64][]Job, error) {
	path := fmt.Sprintf("repos/%s/actions/runs", w.Repo.Slug())
	fields := []string{"-F", fmt.Sprintf("per_page=%d", BoardRuns)}
	if w.Branch != "" {
		fields = append(fields, "-f", "branch="+w.Branch)
	} else if h.Sha != "" {
		// Detached: the runs of this commit.
		fields = append(fields, "-f", "head_sha="+h.Sha)
	}
	var g restRuns
	if err := a.api(ctx, w, &g, path, fields...); err != nil {
		return nil, nil, err
	}
	runs := make([]Run, 0, len(g.WorkflowRuns))
	for _, r := range g.WorkflowRuns {
		runs = append(runs, r.run())
	}
	// At once: each is a network round trip, and the machine-wide limit of
	// four gh commands still holds.
	ids := watchRuns(runs, BoardJobRuns)
	finished := map[int64]bool{}
	for _, r := range runs {
		finished[r.ID] = Finished(r.Status)
	}
	got := make([][]Job, len(ids))
	errs := make([]error, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		if j, ok := a.keptJobs(w, id); ok && finished[id] {
			got[i] = j
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], errs[i] = a.Jobs(ctx, w, id)
			if errs[i] == nil && finished[id] {
				a.keepJobs(w, id, got[i])
			}
		}()
	}
	wg.Wait()
	jobs := map[int64][]Job{}
	for i, id := range ids {
		if errs[i] != nil {
			return nil, nil, errs[i]
		}
		jobs[id] = got[i]
	}
	return runs, jobs, nil
}

// watchRuns picks, among each workflow's latest run, the ones whose jobs
// the board reads: those not finished, newest first, then the failed ones,
// newest first; at most n.
func watchRuns(runs []Run, n int) []int64 {
	seen := map[string]bool{}
	var latest []Run
	for _, r := range runs {
		if !seen[r.file] {
			seen[r.file] = true
			latest = append(latest, r)
		}
	}
	sort.SliceStable(latest, func(i, j int) bool { return latest[i].CreatedAt > latest[j].CreatedAt })
	var out []int64
	for _, pass := range []func(Run) bool{
		func(r Run) bool { return !Finished(r.Status) },
		func(r Run) bool { return Finished(r.Status) && Failed(r.Conclusion) },
	} {
		for _, r := range latest {
			if len(out) < n && pass(r) {
				out = append(out, r.ID)
			}
		}
	}
	return out
}

// keptJobs is a finished run's jobs, read in the last doneJobsTTL: they no
// longer change (a rerun is a run that is not finished).
func (a *Actions) keptJobs(w Where, run int64) ([]Job, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.doneJobs[fmt.Sprintf("%s|%d", w.Repo.Arg(), run)]
	return c.v, ok && time.Since(c.at) < doneJobsTTL
}

func (a *Actions) keepJobs(w Where, run int64, jobs []Job) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.doneJobs == nil {
		a.doneJobs = map[string]cached[[]Job]{}
	}
	for k, c := range a.doneJobs {
		if time.Since(c.at) > doneJobsTTL {
			delete(a.doneJobs, k)
		}
	}
	a.doneJobs[fmt.Sprintf("%s|%d", w.Repo.Arg(), run)] = cached[[]Job]{v: jobs, at: time.Now()}
}
