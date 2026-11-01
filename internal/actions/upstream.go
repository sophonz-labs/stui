package actions

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// A checkout's upstreams: its other GitHub remotes that are other
// repositories (the one a fork follows), shown under it on the
// coordinator's board on their default branch. Their workflows are read
// from GitHub, not from the checkout's files: a fork adds, drops and
// renames workflows, and the upstream's runs go with the upstream's files.

const (
	// Upstreams a checkout shows, in remoteRank order.
	MaxUpstreams = 2
	// How long an upstream's runs are kept. Nothing we push changes them;
	// the 30 s of a checkout is for seeing what was just pushed.
	UpstreamTTL = 2 * time.Minute
	// How long an upstream's default branch and workflow list are kept.
	upMetaTTL = 10 * time.Minute
	// How long a failed read of them is kept before trying again.
	upMetaRetry = BoardsTTL
)

// More group states.
const (
	StateArchived = "archived" // an upstream GitHub has archived: no runs to come
)

// Remote is a git remote of a checkout that is a GitHub repository.
type Remote struct {
	Name string
	Repo Repo
}

// sameRepo is whether a and b are one repository. GitHub does not tell
// owners or names apart by case.
func sameRepo(a, b Repo) bool {
	return strings.EqualFold(a.Host, b.Host) && strings.EqualFold(a.Owner, b.Owner) && strings.EqualFold(a.Name, b.Name)
}

// others is cands without chosen and without any repository named twice,
// the first of each kept, at most MaxUpstreams. cands is in the order
// Locate tried them: the branch's remote, then remoteRank.
func others(cands []Remote, chosen Repo) []Remote {
	var out []Remote
	seen := []Repo{chosen}
next:
	for _, c := range cands {
		for _, s := range seen {
			if sameRepo(s, c.Repo) {
				continue next
			}
		}
		seen = append(seen, c.Repo)
		out = append(out, c)
		if len(out) == MaxUpstreams {
			break
		}
	}
	return out
}

// upMeta is what is kept of an upstream: its default branch, whether it
// is archived, its name now (GitHub follows a rename), its workflows.
type upMeta struct {
	at        time.Time
	branch    string
	archived  bool
	repo      Repo
	workflows []Workflow
	err       error
	reading   chan struct{}
}

func metaKey(r Repo) string { return strings.ToLower(r.Arg()) }

// startMeta starts reading an upstream's default branch and workflows
// unless what is kept is fresh, a read is under way, or its host is
// backing off. The channel closes when the read is done; nil when there is
// none.
func (a *Actions) startMeta(w Where) chan struct{} {
	k := metaKey(*w.Repo)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.metas == nil {
		a.metas = map[string]*upMeta{}
	}
	m := a.metas[k]
	if m == nil {
		m = &upMeta{}
		a.metas[k] = m
	}
	if m.reading != nil {
		return m.reading
	}
	ttl := upMetaTTL
	if m.err != nil {
		ttl = upMetaRetry
	}
	if !m.at.IsZero() && time.Since(m.at) < ttl {
		return nil
	}
	if b := a.limited[w.Repo.Host]; b != nil && time.Now().Before(b.until) {
		return nil
	}
	ch := make(chan struct{})
	m.reading = ch
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		got, err := a.fetchMeta(ctx, w)
		a.mu.Lock()
		defer a.mu.Unlock()
		m.reading = nil
		close(ch)
		if err != nil && isRateLimit(err.Error()) {
			a.backOff(w.Repo.Host, 0, time.Time{})
			return
		}
		m.at, m.err = time.Now(), err
		if err == nil {
			m.branch, m.archived, m.repo, m.workflows = got.branch, got.archived, got.repo, got.workflows
		}
	}()
	return ch
}

// keptMeta is what is kept of an upstream, nil when nothing is yet.
// a.mu is held.
func (a *Actions) keptMeta(r Repo) *upMeta {
	m := a.metas[metaKey(r)]
	if m == nil || m.at.IsZero() {
		return nil
	}
	return m
}

type restRepo struct {
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Archived      bool   `json:"archived"`
}

type restWorkflows struct {
	Workflows []struct {
		Name  string `json:"name"`
		Path  string `json:"path"`
		State string `json:"state"`
	} `json:"workflows"`
}

// fetchMeta reads an upstream with REST (GraphQL has no ETag and its own
// points): the repository, then its workflows.
func (a *Actions) fetchMeta(ctx context.Context, w Where) (upMeta, error) {
	var r restRepo
	if err := a.api(ctx, w, &r, "repos/"+w.Repo.Slug()); err != nil {
		return upMeta{}, err
	}
	if r.DefaultBranch == "" {
		return upMeta{}, fmt.Errorf("GitHub gave %s no default branch", w.Repo.Slug())
	}
	m := upMeta{branch: r.DefaultBranch, archived: r.Archived, repo: *w.Repo}
	// A renamed repository answers under its new name: read it as that.
	if o, n, ok := strings.Cut(r.FullName, "/"); ok && o != "" && n != "" {
		m.repo.Owner, m.repo.Name = o, n
	}
	var wf restWorkflows
	if err := a.api(ctx, w, &wf, "repos/"+w.Repo.Slug()+"/actions/workflows", "-F", "per_page=100"); err != nil {
		return upMeta{}, err
	}
	m.workflows = apiWorkflows(wf)
	return m, nil
}

// apiWorkflows is the workflow list as the board lists a checkout's: one
// per file of .github/workflows, in file name order, named as the file
// names itself (its file name when it has no name:). Workflows turned off
// are left out, and so are GitHub's own (dynamic/..., Copilot and
// Dependabot): they are no file of the repository. No runners: the
// upstream's jobs are opened on GitHub.
func apiWorkflows(g restWorkflows) []Workflow {
	out := []Workflow{}
	for _, w := range g.Workflows {
		if !strings.HasPrefix(w.Path, ".github/workflows/") || strings.HasPrefix(w.State, "disabled") {
			continue
		}
		file := workflowFile(w.Path)
		name := w.Name
		// GitHub names a workflow without name: by its path.
		if name == "" || name == w.Path {
			name = file
		}
		out = append(out, Workflow{File: file, Name: name, Runners: []string{}})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].File < out[j].File })
	if len(out) > MaxWorkflows {
		out = out[:MaxWorkflows]
	}
	return out
}

// upWhere is the upstream r of the checkout w, as its board is read: on
// its default branch, with the checkout's gh, in the checkout's folder.
// State is ok, loading, limited, error, archived or no-auth.
func (a *Actions) upWhere(ctx context.Context, w Where, r Remote, wait <-chan time.Time) Where {
	repo := r.Repo
	u := Where{State: StateOK, Root: w.Root, Repo: &repo, gh: w.gh, remote: r.Name, upstreamOf: w.Root}
	if ok, detail := a.authed(ctx, w.gh, repo.Host); !ok {
		u.State, u.Detail = StateNoAuth, detail
		return u
	}
	if ch := a.startMeta(u); ch != nil {
		select {
		case <-ch:
		case <-wait:
		case <-ctx.Done():
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	m := a.keptMeta(repo)
	b := a.limited[repo.Host]
	switch {
	case m == nil && b != nil && time.Now().Before(b.until):
		u.State = StateLimited
	case m == nil:
		u.State = StateLoading
	case m.err != nil:
		u.State, u.Detail = StateError, errLine(m.err)
	default:
		canon := m.repo
		u.Repo, u.Branch, u.wfs = &canon, m.branch, m.workflows
		if m.archived {
			u.State = StateArchived
		}
	}
	return u
}

// Upstream is the checkout w's remote called name as an upstream board
// reads it; false when the checkout has no such upstream (the name is
// another repository's than one of w.others, or none).
func (a *Actions) Upstream(ctx context.Context, w Where, name string) (Where, bool) {
	for _, r := range w.others {
		if r.Name == name {
			return a.upWhere(ctx, w, r, nil), true
		}
	}
	return Where{}, false
}
