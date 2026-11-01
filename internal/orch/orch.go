// Package orch is stui's orchestration: coding agents
// handing work to other coding agents in tmux. The relation lives in tmux
// itself, as pane options (@stui-run, @stui-role, @stui-parent, @stui-name,
// @stui-task, @stui-done, @stui-summary) that agent watch reads for the graph; a
// run's record here keeps its objective and the workers' reports.
package orch

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sophonz-labs/stui/internal/home"
)

// MaxDepth is how deep workers may start workers below a coordinator,
// within one run: a team adopted as a worker counts from its own lead.
const MaxDepth = 2

// MaxChain is how long the whole chain of agents handing work down may be,
// teams in teams included: it keeps delegation from going round and round.
const MaxChain = 4

// The @stui-role values (agent watch reads the same words).
const (
	RoleCoordinatorName = "coordinator"
	RoleWorkerName      = "worker"
)

//go:embed guides/*.md
var guides embed.FS

// Guide is `stui skills get <name>`: orchestrate or worker.
func Guide(name string) (string, error) {
	b, err := guides.ReadFile("guides/" + name + ".md")
	if err != nil {
		return "", fmt.Errorf("no guide %q (orchestrate, worker)", name)
	}
	return string(b), nil
}

type Worker struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Agent   string `json:"agent"`
	Task    string `json:"task"`
	Window  int    `json:"window"`
	Started int64  `json:"started"`
	Done    bool   `json:"done,omitempty"`
	Summary string `json:"summary,omitempty"`
	Ended   int64  `json:"ended,omitempty"`
	// An agent that was already running, made a worker (adopt.go).
	Adopted bool `json:"adopted,omitempty"`
	// What it may change (owns.go); none: anything.
	Owns []string `json:"owns,omitempty"`
	// Its own git worktree (worktree.go), when it has one.
	Worktree *Worktree `json:"worktree,omitempty"`
	// The fan it is one of (fan.go): workers given the same task.
	Fan string `json:"fan,omitempty"`
	// The machine it runs on, by slug, when that is another machine of the
	// account (remote.go); its ID is then "<machine>/%<pane>".
	Machine string `json:"machine,omitempty"`
	// A team adopted as one worker: its lead's own run (adopt.go).
	Team string `json:"team,omitempty"`
	// What it is known by when its pane id no longer means it (rebind.go):
	// the pane's lasting key, its agent's conversation, and its parent's
	// key ("" for the coordinator). Live: it was a worker of this run when
	// last looked at, so a pane that takes its place is made one again.
	Key       string `json:"key,omitempty"`
	Conv      string `json:"conv,omitempty"`
	ParentKey string `json:"parentKey,omitempty"`
	Live      bool   `json:"live,omitempty"`
	// "<session>:<window name>" it was last seen in: both come back with a
	// restore, so a pane found there with neither key nor conversation is
	// taken for it when it is the only one (rebind.go).
	Place string `json:"place,omitempty"`
	// The tmux server it was last seen in (ServerID): gone from that same
	// server, its pane was closed; gone after the server changed, it is
	// lost, and stui orch status says so.
	Seen string `json:"seen,omitempty"`
	// For a worker on another machine: the pane here its machine was last
	// told is its parent, so a parent that moved to another pane can be
	// told again (rebind.go); "" in records from before, read as the
	// coordinator.
	Parent string `json:"parent,omitempty"`
	// An adopted worker's role, written by the one adopting, and its own
	// brief of what it had done (brief.go): copies of the pane's options,
	// for Rebind and for a worker on another machine.
	About      string `json:"about,omitempty"`
	Brief      string `json:"brief,omitempty"`
	BriefState string `json:"briefState,omitempty"`
	BriefAt    int64  `json:"briefAt,omitempty"`
}

// TeamInfo is a team's lead seen from above: its own run and how many
// workers it directs.
type TeamInfo struct {
	Run     string `json:"run"`
	Members int    `json:"members"`
}

type Run struct {
	ID          string   `json:"id"`
	Objective   string   `json:"objective"`
	Coordinator string   `json:"coordinator"`
	Session     string   `json:"session"`
	Created     int64    `json:"created"`
	Workers     []Worker `json:"workers"`
	// What to do and in what order (tasks.go).
	Tasks []Task `json:"tasks,omitempty"`
	// The coordinator's lasting key, conversation and liveness, as a
	// worker's (rebind.go); Server is the tmux server they were last seen
	// in.
	CoordinatorKey  string `json:"coordinatorKey,omitempty"`
	CoordinatorConv string `json:"coordinatorConv,omitempty"`
	CoordinatorLive bool   `json:"coordinatorLive,omitempty"`
	// The coordinator's place, as a worker's.
	CoordinatorPlace string `json:"coordinatorPlace,omitempty"`
	Server           string `json:"server,omitempty"`
	// When and why it was retired (rebind.go, Archive); its record is then
	// under orch/archive.
	Retired    int64  `json:"retired,omitempty"`
	RetiredWhy string `json:"retiredWhy,omitempty"`
}

var runID = regexp.MustCompile(`^r-[0-9a-f]{8}$`)

func Dir() string { return filepath.Join(home.Dir(), "orch") }

func NewID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return "r-" + hex.EncodeToString(b)
}

// RunDir is where a run's record and task files are.
func RunDir(id string) string { return filepath.Join(Dir(), id) }

func Load(id string) (*Run, error) {
	if !runID.MatchString(id) {
		return nil, errors.New("bad run id")
	}
	b, err := os.ReadFile(filepath.Join(RunDir(id), "run.json"))
	if err != nil {
		return nil, err
	}
	var r Run
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (r *Run) Save() error {
	d := RunDir(r.ID)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(r, "", "  ")
	// A temporary file of its own: two saves at once sharing one name could
	// put one's half-written file in place of the record.
	f, err := os.CreateTemp(d, "run.json.*.tmp")
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(f.Name(), 0o600)
	}
	if err != nil {
		os.Remove(f.Name())
		return err
	}
	return os.Rename(f.Name(), filepath.Join(d, "run.json"))
}

// Worker is the record of worker id, or nil.
func (r *Run) Worker(id string) *Worker {
	for i := range r.Workers {
		if r.Workers[i].ID == id {
			return &r.Workers[i]
		}
	}
	return nil
}

var nameRE = regexp.MustCompile(`[^a-z0-9-]+`)

// Name makes a worker name safe for a window name and a file: lower case,
// letters, digits and dashes, at most 24.
func Name(s string) string {
	n := strings.Trim(nameRE.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-"), "-")
	if len(n) > 24 {
		n = strings.TrimRight(n[:24], "-")
	}
	return n
}

// Obligations go at the end of every task file.
const Obligations = `

---
You are a worker started by stui orchestration. When you are done, check the
acceptance criteria above, then run:

    stui worker done --summary "<what you did, and how you checked it>"

If you cannot finish, start the summary with "blocked:". Need a decision?
Ask in your answer and stop; the coordinator reads it. More: stui skills get worker
`

// WriteTask writes a worker's task file and returns its path.
func WriteTask(run, name, text string) (string, error) {
	d := RunDir(run)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	p := filepath.Join(d, name+".md")
	return p, os.WriteFile(p, []byte(strings.TrimRight(text, "\n")+Obligations), 0o600)
}

// FirstLine is a task's first non-empty line, for the graph, at most 80.
func FirstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "#"))
		if l != "" {
			if r := []rune(l); len(r) > 80 {
				return string(r[:79]) + "…"
			}
			return l
		}
	}
	return ""
}

func Now() int64 { return time.Now().UnixMilli() }

// Runs is every run's record, newest first.
func Runs() []*Run {
	entries, _ := os.ReadDir(Dir())
	var out []*Run
	for _, e := range entries {
		if !e.IsDir() || !runID.MatchString(e.Name()) {
			continue
		}
		if r, err := Load(e.Name()); err == nil {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created > out[j].Created })
	return out
}

// RunOf is the newest run whose coordinator is pane, by its pane id or its
// lasting key; nil when there is none.
func RunOf(pane, key string) *Run {
	for _, r := range Runs() {
		if (key != "" && r.CoordinatorKey == key) || (key == "" && r.CoordinatorKey == "" && r.Coordinator == pane) {
			return r
		}
	}
	return nil
}
