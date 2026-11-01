package actions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ServiceOptions configure the routes.
type ServiceOptions struct {
	// PaneCwd is a pane's working directory; nil when the multiplexer has
	// no panes, and the routes answer 404.
	PaneCwd func(ctx context.Context, pane string) (string, bool)
	Actions *Actions
	// nil lets every request the terminal's gate let in. A read-only link
	// gets 403: the runs and the login that reads them are the owner's.
	IsOwner func(*http.Request) bool
	// Open opens (or finds) the window a job's log runs in, for POST
	// /api/actions/open; nil, or ErrNoMux from it, answers 409 no-mux.
	Open func(ctx context.Context, o OpenWindow) (Opened, error)
	// Self is the stui binary the log window runs; "" is this one.
	Self string
	// Tree is a pane and every worker below it (orch.Subtree), for GET
	// /api/actions/boards; nil: the pane alone.
	Tree func(ctx context.Context, pane string) []Member
}

// OpenWindow is a window to open for a job's log: in pane's session,
// called Name, in Dir, running Argv. Key tells the window of this job from
// one of the same name left by an earlier attempt.
type OpenWindow struct {
	Pane, Name, Key, Dir string
	Argv                 []string
}

// Opened is where the window is.
type Opened struct {
	Session string `json:"session"`
	Window  int    `json:"window"`
	Name    string `json:"name"`
}

// ErrNoMux is a machine whose multiplexer cannot open windows.
var ErrNoMux = errors.New("no multiplexer")

// Service serves GET /api/actions, /api/actions/board, /api/actions/boards,
// /api/actions/run, /api/actions/log and POST /api/actions/rerun,
// /api/actions/cancel, /api/actions/open.
type Service struct{ o ServiceOptions }

func NewService(o ServiceOptions) *Service {
	if o.Actions == nil {
		o.Actions = &Actions{}
	}
	return &Service{o: o}
}

// Serves is whether the routes work here (a multiplexer with panes).
func (s *Service) Serves() bool { return s.o.PaneCwd != nil }

var (
	paneRE   = regexp.MustCompile(`^(?:[A-Za-z0-9_-]{1,64}:)?%[0-9]{1,9}$`) // %3, or psmux's work:%3
	branchRE = regexp.MustCompile(`^[^\x00-\x20~^:?*\[\\]{1,255}$`)
)

func id(s string) (int64, bool) {
	if s == "" || len(s) > 19 || strings.TrimLeft(s, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil && n > 0
}

// ServeHTTP answers its routes and is false for any other path.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) bool {
	method := "GET"
	switch r.URL.Path {
	case "/api/actions", "/api/actions/board", "/api/actions/boards", "/api/actions/run", "/api/actions/log":
	case "/api/actions/rerun", "/api/actions/cancel", "/api/actions/open":
		method = "POST"
	default:
		return false
	}
	if s.o.IsOwner != nil && !s.o.IsOwner(r) {
		writeJSON(w, 403, map[string]any{"error": "read-only"})
		return true
	}
	if r.Method != method {
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
		return true
	}
	if s.o.PaneCwd == nil {
		writeJSON(w, 404, map[string]any{"error": "this multiplexer has no panes"})
		return true
	}
	q := r.URL.Query()
	var body struct {
		Pane   string `json:"pane"`
		ID     int64  `json:"id"`
		Failed bool   `json:"failed"`
		Run    int64  `json:"run"`
		Job    int64  `json:"job"`
		Root   string `json:"root"`
		Remote string `json:"remote"`
	}
	if method == "POST" {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
			writeJSON(w, 400, map[string]any{"error": "bad body"})
			return true
		}
	} else {
		body.Pane, body.Root, body.Remote = q.Get("pane"), q.Get("root"), q.Get("remote")
	}
	if !paneRE.MatchString(body.Pane) {
		writeJSON(w, 400, map[string]any{"error": "bad pane"})
		return true
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	defer cancel()
	cwd, ok := s.o.PaneCwd(ctx, body.Pane)
	if !ok || cwd == "" || !filepath.IsAbs(cwd) {
		writeJSON(w, 404, map[string]any{"error": "no such pane"})
		return true
	}
	a := s.o.Actions
	if r.URL.Path == "/api/actions/boards" {
		tree := []Member{{Pane: body.Pane}}
		if s.o.Tree != nil {
			if t := s.o.Tree(ctx, body.Pane); len(t) > 0 {
				tree = t
			}
		}
		writeJSON(w, 200, a.Boards(ctx, body.Pane, tree, s.o.PaneCwd))
		return true
	}
	// A checkout of the pane's that is not the one its folder is in (a
	// group of the coordinator's board): only one found for that pane,
	// and the one found is used, never the path as it was sent.
	var where Where
	if body.Root != "" {
		var ok bool
		if where, ok = a.AtRoot(ctx, cwd, body.Root); !ok {
			writeJSON(w, 400, map[string]any{"error": "bad root"})
			return true
		}
	} else {
		where = a.Locate(ctx, cwd)
	}
	// An upstream of that checkout (a line under it on the coordinator's
	// board): only a remote it has, read as the checkout reads it, never
	// the name as a URL. It is read, never acted on: its jobs open on
	// GitHub, and its runs are not ours to rerun or cancel.
	if body.Remote != "" {
		up, ok := a.Upstream(ctx, where, body.Remote)
		if !ok {
			writeJSON(w, 400, map[string]any{"error": "bad remote"})
			return true
		}
		if method == "POST" {
			writeJSON(w, 409, map[string]any{"error": "an upstream is read only here", "state": "upstream"})
			return true
		}
		where = up
	}
	switch r.URL.Path {
	case "/api/actions":
		s.list(ctx, w, q, cwd, where)
		return true
	case "/api/actions/board":
		b, err := a.Board(ctx, where)
		if err != nil {
			ghError(w, err)
			return true
		}
		writeJSON(w, 200, b)
		return true
	}
	if where.State != StateOK {
		writeJSON(w, 409, map[string]any{"error": "no repository gh can read", "state": where.State})
		return true
	}
	if r.URL.Path == "/api/actions/open" {
		s.open(ctx, w, where, body.Pane, body.Run, body.Job)
		return true
	}
	if method == "GET" {
		var ok bool
		if body.ID, ok = id(q.Get("id")); !ok {
			writeJSON(w, 400, map[string]any{"error": "bad id"})
			return true
		}
	} else if body.ID <= 0 {
		writeJSON(w, 400, map[string]any{"error": "bad id"})
		return true
	}
	switch r.URL.Path {
	case "/api/actions/run":
		d, err := a.Detail(ctx, where, body.ID)
		if err != nil {
			ghError(w, err)
			return true
		}
		writeJSON(w, 200, map[string]any{"repo": where.Repo.Slug(), "run": d})
	case "/api/actions/log":
		var job int64
		if j := q.Get("job"); j != "" {
			var ok bool
			if job, ok = id(j); !ok {
				writeJSON(w, 400, map[string]any{"error": "bad job"})
				return true
			}
		}
		lines, cut, err := a.FailedLog(ctx, where, body.ID, job)
		if errors.Is(err, ErrInProgress) {
			writeJSON(w, 409, map[string]any{"error": err.Error(), "state": "in-progress"})
			return true
		}
		if err != nil {
			ghError(w, err)
			return true
		}
		writeJSON(w, 200, map[string]any{"id": body.ID, "lines": lines, "truncated": cut})
	case "/api/actions/rerun":
		if err := a.Rerun(ctx, where, body.ID, body.Failed); err != nil {
			ghError(w, err)
			return true
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	case "/api/actions/cancel":
		if err := a.Cancel(ctx, where, body.ID); err != nil {
			ghError(w, err)
			return true
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	}
	return true
}

// open is POST /api/actions/open: the window running `stui ci log --follow`
// for one job, made once and found again after.
func (s *Service) open(ctx context.Context, w http.ResponseWriter, where Where, pane string, run, job int64) {
	if run <= 0 || job <= 0 {
		writeJSON(w, 400, map[string]any{"error": "bad run or job"})
		return
	}
	if s.o.Open == nil {
		writeJSON(w, 409, map[string]any{"error": "this multiplexer cannot open windows", "state": "no-mux"})
		return
	}
	d, err := s.o.Actions.Detail(ctx, where, run)
	if err != nil {
		ghError(w, err)
		return
	}
	var j *Job
	for i := range d.Jobs {
		if d.Jobs[i].ID == job {
			j = &d.Jobs[i]
		}
	}
	if j == nil {
		writeJSON(w, 404, map[string]any{"error": "no such job in that run"})
		return
	}
	self := s.o.Self
	if self == "" {
		self, _ = os.Executable()
	}
	wf := strings.TrimSuffix(strings.TrimSuffix(d.file, ".yml"), ".yaml")
	if wf == "" {
		wf = d.Workflow
	}
	name := WindowName(wf, j.Name)
	// The log, then a shell in the checkout: the window stays when the log
	// is done, and is somewhere to rerun from.
	argv := []string{"/bin/sh", "-c", `"$0" "$@"; exec "${SHELL:-/bin/sh}" -l`,
		self, "ci", "log", "--follow", "--job", strconv.FormatInt(job, 10), "-R", where.Repo.Arg()}
	o, err := s.o.Open(ctx, OpenWindow{Pane: pane, Name: name, Key: strconv.FormatInt(job, 10), Dir: where.Root, Argv: argv})
	if errors.Is(err, ErrNoMux) {
		writeJSON(w, 409, map[string]any{"error": "this multiplexer cannot open windows", "state": "no-mux"})
		return
	}
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, o)
}

// WindowName is a job's log window: "ci:<workflow>/<job>". It is found
// again by its exact name, never used as a tmux target, so dots and colons
// in it are harmless; line breaks are not.
func WindowName(workflow, job string) string {
	clean := strings.NewReplacer("\n", " ", "\r", " ", "\t", " ")
	return "ci:" + clean.Replace(workflow) + "/" + clean.Replace(job)
}

func (s *Service) list(ctx context.Context, w http.ResponseWriter, q map[string][]string, cwd string, where Where) {
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	res := map[string]any{"state": where.State, "cwd": cwd}
	if where.Root != "" {
		res["root"] = where.Root
	}
	if where.Branch != "" {
		res["branch"] = where.Branch
	}
	if where.Repo != nil {
		res["repo"] = where.Repo.Slug()
		res["host"] = where.Repo.Host
		res["url"] = where.Repo.URL() + "/actions"
	}
	if where.Detail != "" {
		res["detail"] = where.Detail
	}
	if where.State != StateOK {
		writeJSON(w, 200, res)
		return
	}
	branch := get("branch")
	if branch != "" && (!branchRE.MatchString(branch) || strings.HasPrefix(branch, "-")) {
		writeJSON(w, 400, map[string]any{"error": "bad branch"})
		return
	}
	limit, _ := strconv.Atoi(get("limit"))
	runs, err := s.o.Actions.Runs(ctx, where, branch, limit)
	if err != nil {
		ghError(w, err)
		return
	}
	res["runs"] = runs
	writeJSON(w, 200, res)
}

// ghError is 502 with gh's own first line: the network, GitHub or a
// missing run, which the phone shows as it is.
func ghError(w http.ResponseWriter, err error) {
	msg := err.Error()
	var ee *ExitError
	if errors.As(err, &ee) {
		msg = firstLine(ee.Stderr)
		if msg == "" {
			msg = ee.Error()
		}
	}
	code := 502
	if strings.Contains(strings.ToLower(msg), "not found") {
		code = 404
	}
	writeJSON(w, code, map[string]any{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("content-type", "application/json;charset=utf-8")
	w.Header().Set("cache-control", "no-store")
	w.Header().Set("x-content-type-options", "nosniff")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
