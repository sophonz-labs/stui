package actions

import (
	"strings"
)

// GitHub's REST answers, through `gh api`. The jobs answer is the one that
// names each job's runner (runner_name, runner_group_name, labels), which
// `gh run view --json jobs` leaves out; the runs answer has each run's
// workflow file (path), which `gh run list --json` leaves out.

type restRun struct {
	ID           int64  `json:"id"`
	RunNumber    int    `json:"run_number"`
	RunAttempt   int    `json:"run_attempt"`
	Name         string `json:"name"`
	Path         string `json:"path"`
	DisplayTitle string `json:"display_title"`
	HeadBranch   string `json:"head_branch"`
	HeadSha      string `json:"head_sha"`
	Event        string `json:"event"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion"`
	CreatedAt    string `json:"created_at"`
	RunStartedAt string `json:"run_started_at"`
	UpdatedAt    string `json:"updated_at"`
	HTMLURL      string `json:"html_url"`
}

type restRuns struct {
	WorkflowRuns []restRun `json:"workflow_runs"`
}

type restStep struct {
	Number      int     `json:"number"`
	Name        string  `json:"name"`
	Status      string  `json:"status"`
	Conclusion  *string `json:"conclusion"`
	StartedAt   *string `json:"started_at"`
	CompletedAt *string `json:"completed_at"`
}

type restJob struct {
	ID              int64      `json:"id"`
	RunID           int64      `json:"run_id"`
	Name            string     `json:"name"`
	WorkflowName    string     `json:"workflow_name"`
	Status          string     `json:"status"`
	Conclusion      *string    `json:"conclusion"`
	StartedAt       *string    `json:"started_at"`
	CompletedAt     *string    `json:"completed_at"`
	HTMLURL         string     `json:"html_url"`
	RunnerName      *string    `json:"runner_name"`
	RunnerGroupName *string    `json:"runner_group_name"`
	Labels          []string   `json:"labels"`
	Steps           []restStep `json:"steps"`
}

type restJobs struct {
	Jobs []restJob `json:"jobs"`
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// workflowFile is a run's workflow file name: ".github/workflows/ci.yml"
// is "ci.yml". A path with "@ref" (a reusable workflow's caller) loses it.
func workflowFile(path string) string {
	path, _, _ = strings.Cut(path, "@")
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

func (g restRun) run() Run {
	return Run{
		ID: g.ID, Number: g.RunNumber, Attempt: g.RunAttempt, Workflow: g.Name, Title: g.DisplayTitle,
		Branch: g.HeadBranch, Sha: short(g.HeadSha), Event: g.Event, Status: g.Status, Conclusion: g.Conclusion,
		CreatedAt: g.CreatedAt, StartedAt: g.RunStartedAt, UpdatedAt: g.UpdatedAt, URL: g.HTMLURL,
		file: workflowFile(g.Path),
	}
}

// RunnerInfo is the runner a job runs on, once it has started.
type RunnerInfo struct {
	// "self-hosted", "hosted", or "" before the job has a runner.
	Kind   string   `json:"kind"`
	Name   string   `json:"name"`
	Group  string   `json:"group"`
	Labels []string `json:"labels"`
}

// StepNow is the step a job is on.
type StepNow struct {
	// Its place in the job's steps, 1-based: GitHub's own step numbers
	// skip (8, then 15 for the post steps), so they do not count to Of.
	Number int    `json:"number"`
	Name   string `json:"name"`
	Of     int    `json:"of"`
}

// runnerOf tells GitHub's runners from ours by the group GitHub puts its
// own in. Labels cannot: `runs-on: my-runner` has no self-hosted label.
func runnerOf(j restJob) RunnerInfo {
	r := RunnerInfo{Name: str(j.RunnerName), Group: str(j.RunnerGroupName), Labels: j.Labels}
	if r.Labels == nil {
		r.Labels = []string{}
	}
	switch {
	case r.Group == "GitHub Actions" || strings.HasPrefix(r.Name, "GitHub Actions "):
		r.Kind = KindHosted
	case r.Group != "" || r.Name != "":
		r.Kind = KindSelfHosted
	}
	return r
}

func (j restJob) job() Job {
	steps := make([]Step, 0, len(j.Steps))
	for _, s := range j.Steps {
		steps = append(steps, Step{
			Number: s.Number, Name: s.Name, Status: s.Status, Conclusion: str(s.Conclusion),
			StartedAt: str(s.StartedAt), CompletedAt: str(s.CompletedAt),
		})
	}
	out := Job{
		ID: j.ID, Name: j.Name, Status: j.Status, Conclusion: str(j.Conclusion),
		StartedAt: str(j.StartedAt), CompletedAt: str(j.CompletedAt), URL: j.HTMLURL,
		Steps: steps, Runner: runnerOf(j), runID: j.RunID,
	}
	out.Step = stepNow(out)
	return out
}

// stepNow is the step in progress; while a job runs between two steps, the
// first one not finished.
func stepNow(j Job) *StepNow {
	if j.Status != "in_progress" {
		return nil
	}
	for i, s := range j.Steps {
		if s.Status == "in_progress" {
			return &StepNow{Number: i + 1, Name: s.Name, Of: len(j.Steps)}
		}
	}
	for i, s := range j.Steps {
		if s.Status != "completed" {
			return &StepNow{Number: i + 1, Name: s.Name, Of: len(j.Steps)}
		}
	}
	return nil
}

// Finished says a run or job is over.
func Finished(status string) bool { return status == "completed" }

// Failed says a conclusion is a red one.
func Failed(conclusion string) bool {
	switch conclusion {
	case "failure", "timed_out", "startup_failure":
		return true
	}
	return false
}
