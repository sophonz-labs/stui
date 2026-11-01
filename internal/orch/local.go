package orch

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// What is left here of stui's cross-machine half: stui runs on one machine.

// remoteStart is how far a question typed into a reading agent got.
type remoteStart struct {
	stage, err string
}

var sessionName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

func (a *Adopter) tmuxOpt(ctx context.Context, id, name string) string {
	v, _ := a.o.Tmux(ctx, "show-options", "-pqv", "-t", id, name)
	return strings.TrimSpace(v)
}

func firstNonEmptyStr(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// RemoteWorker is GET /api/orch/workers/<pane>.
type RemoteWorker struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Run    string `json:"run"`
	Parent string `json:"parent"`
	Agent  string `json:"agent"`
	Window int    `json:"window"`
	// Its pane's lasting key (anchor.go): A takes a pane with another key
	// for another agent, not this worker.
	Key string `json:"key,omitempty"`
	// Starting it: deliver, start, started, failed.
	Stage string `json:"stage"`
	Error string `json:"error,omitempty"`
	// The agent's state as agent watch has it, and since when (ms).
	State string `json:"state"`
	Since int64  `json:"since"`
	Done  bool   `json:"done"`
	// What its done said of the work: succeeded or failed (--outcome); an
	// older stui leaves it out.
	Outcome string `json:"outcome,omitempty"`
	// Why its last turn ended, as this machine read it (stopreason.go),
	// when State is a turn end; an older stui leaves them out.
	StopReason string `json:"stopReason,omitempty"`
	StopWhy    string `json:"stopWhy,omitempty"`
	ResetsAt   int64  `json:"resetsAt,omitempty"`
	Summary    string `json:"summary,omitempty"`
	Sent       int64  `json:"sent,omitempty"`
	Task       string `json:"task,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Exited     bool   `json:"exited,omitempty"`
	// It leads a team here, adopted as one worker.
	Team *TeamInfo `json:"team,omitempty"`
	// An adopted worker's role and its brief (adopt-brief.md); the brief
	// with its newlines back.
	About      string `json:"about,omitempty"`
	Brief      string `json:"brief,omitempty"`
	BriefState string `json:"briefState,omitempty"`
	BriefAt    int64  `json:"briefAt,omitempty"`
}

// FromHeader names the stui of another machine; stui never has one.
const FromHeader = "X-St-From"

var errOneMachine = fmt.Errorf("stui runs on one machine: no worker is on another")

// remoteChild splits "<machine>/%<pane>": stui has no other machine.
func remoteChild(id string) (slug, pane string, ok bool) { return "", "", false }

func (a *Adopter) remoteCall(ctx context.Context, slug, method, path string, body, out any) error {
	return errOneMachine
}

func (a *Adopter) remotePane(ctx context.Context, id string) (pane, error) {
	return pane{}, errOneMachine
}

func askedByParent(r *http.Request, p pane) bool { return false }
