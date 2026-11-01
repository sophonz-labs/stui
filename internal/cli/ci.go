package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	ghactions "github.com/sophonz-labs/stui/internal/actions"
)

// stui ci: the GitHub Actions runs of the checkout you are in, read with the
// machine's gh. The board's "Open the log window" runs
// `stui ci log --follow --job <id> -R <repo>` in a window of its own.

const ciHelp = `stui ci — the GitHub Actions runs of this checkout's commit, read with gh

  stui ci                             this commit's runs, one line each
  stui ci log                         the log of a job of this commit's runs
      --job <id>                    that job (any repository's with -R)
      --failed                      a failed job, when no --job is given
      --follow                      while it runs, its steps as they go;
                                    the log once it ends
      --all                         every step's lines, not only the failed ones'
      -R <owner/repo>               the repository (HOST/OWNER/REPO off github.com)

A job still running has no log yet: GitHub makes it when the job ends.
`

func ciCommand(a args) {
	rest := a.after("ci")
	sub := ""
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		sub = rest[0]
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	switch sub {
	case "":
		ciRuns(ctx, a)
	case "log":
		ciLog(ctx, a)
	default:
		fmt.Fprint(os.Stderr, ciHelp)
		os.Exit(2)
	}
}

// ciWhere is the repository: -R's, or the one this directory's checkout
// pushes to.
func ciWhere(ctx context.Context, gh *ghactions.Actions, a args) ghactions.Where {
	cwd, _ := os.Getwd()
	if r, ok := a.flag("-R"); ok {
		return gh.At(ctx, r, cwd)
	}
	return gh.Locate(ctx, cwd)
}

// ciState says what a state that is not ok means, and exits.
func ciState(w ghactions.Where) {
	switch w.State {
	case ghactions.StateOK:
		return
	case ghactions.StateNoGh:
		fmt.Fprintln(os.Stderr, "  gh is not installed on this machine (https://cli.github.com)")
	case ghactions.StateNoAuth:
		fmt.Fprintln(os.Stderr, "  gh is not signed in. Run gh auth login.")
		if w.Detail != "" {
			fmt.Fprintf(os.Stderr, "  %s%s%s\n", dim, w.Detail, reset)
		}
	case ghactions.StateNotGitHub:
		fmt.Fprintln(os.Stderr, "  not a GitHub repository")
	case ghactions.StateNotGit:
		fmt.Fprintln(os.Stderr, "  not a git checkout")
	}
	os.Exit(1)
}

func ciFail(err error) {
	msg := err.Error()
	var ee *ghactions.ExitError
	if errors.As(err, &ee) && ee.Stderr != "" {
		msg = strings.SplitN(ee.Stderr, "\n", 2)[0]
	}
	fmt.Fprintf(os.Stderr, "  %sgh: %s%s\n", red, msg, reset)
	os.Exit(1)
}

// ciRuns is stui ci: one line per run of the checked-out commit.
func ciRuns(ctx context.Context, a args) {
	gh := &ghactions.Actions{}
	w := ciWhere(ctx, gh, a)
	ciState(w)
	h := gh.HeadOf(ctx, w.Root)
	if h.Sha == "" {
		fmt.Fprintln(os.Stderr, "  no commit checked out")
		os.Exit(1)
	}
	runs, err := gh.CommitRuns(ctx, w, h.Sha)
	if err != nil {
		ciFail(err)
	}
	head := fmt.Sprintf("  %s%s%s  %s", bold, w.Repo.Slug(), reset, h.Sha[:7])
	if w.Branch != "" {
		head += "  " + w.Branch
	}
	fmt.Println(head)
	if len(runs) == 0 {
		if !h.Pushed {
			fmt.Printf("  %snot pushed yet: no remote branch has %s%s\n", dim, h.Sha[:7], reset)
		} else {
			fmt.Printf("  %sno runs for this commit yet%s\n", dim, reset)
		}
		return
	}
	for _, r := range runs {
		state := r.Status
		if r.Conclusion != "" {
			state = r.Conclusion
		}
		took := ghactions.Took(r.StartedAt, r.UpdatedAt)
		if !ghactions.Finished(r.Status) {
			if t, err := time.Parse(time.RFC3339, r.StartedAt); err == nil {
				took = ghactions.Duration(time.Since(t))
			}
		}
		fmt.Printf("  %s %s #%d  %s  %s%s%s\n    %s%s%s\n", ghactions.Mark(r.Status, r.Conclusion, yellow+"⟳"+reset),
			r.Workflow, r.Number, state, dim, took, reset, dim, r.URL, reset)
	}
}

// ciPick finds a job of the checked-out commit's runs: one going, then the
// first that failed (only those with --failed), then the last.
func ciPick(ctx context.Context, gh *ghactions.Actions, w ghactions.Where, failed bool) ghactions.Job {
	h := gh.HeadOf(ctx, w.Root)
	if h.Sha == "" {
		fmt.Fprintln(os.Stderr, "  no commit checked out; name the job with --job <id>")
		os.Exit(1)
	}
	runs, err := gh.CommitRuns(ctx, w, h.Sha)
	if err != nil {
		ciFail(err)
	}
	if len(runs) == 0 {
		fmt.Fprintf(os.Stderr, "  no runs for %s\n", h.Sha[:7])
		os.Exit(1)
	}
	var going, bad, last *ghactions.Job
	for _, r := range runs {
		if failed && !ghactions.Failed(r.Conclusion) {
			continue
		}
		jobs, err := gh.Jobs(ctx, w, r.ID)
		if err != nil {
			ciFail(err)
		}
		for i := range jobs {
			j := &jobs[i]
			switch {
			case going == nil && !failed && j.Status == "in_progress":
				going = j
			case bad == nil && ghactions.Failed(j.Conclusion):
				bad = j
			}
			if last == nil {
				last = j
			}
		}
		if going != nil {
			break
		}
	}
	for _, j := range []*ghactions.Job{going, bad} {
		if j != nil {
			return *j
		}
	}
	if failed || last == nil {
		fmt.Fprintf(os.Stderr, "  no failed job for %s\n", h.Sha[:7])
		os.Exit(1)
	}
	return *last
}

// ciLog is stui ci log.
func ciLog(ctx context.Context, a args) {
	gh := &ghactions.Actions{}
	w := ciWhere(ctx, gh, a)
	ciState(w)
	var job ghactions.Job
	if v, ok := a.flag("--job"); ok {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			fmt.Fprintln(os.Stderr, "  --job takes a job id")
			os.Exit(2)
		}
		if job, err = gh.Job(ctx, w, id); err != nil {
			ciFail(err)
		}
	} else {
		job = ciPick(ctx, gh, w, a.has("--failed"))
	}
	fmt.Printf("%s%s%s  %s#%d%s\n", bold, job.Name, reset, dim, job.ID, reset)
	fmt.Printf("%s%s%s\n", dim, job.URL, reset)
	if !ghactions.Finished(job.Status) {
		if !a.has("--follow") {
			ciSteps(job, "⟳")
			fmt.Printf("%sThe log comes when the job ends (stui ci log --follow waits for it).%s\n", dim, reset)
			return
		}
		var err error
		if job, err = ciFollow(ctx, gh, w, job); err != nil {
			if ctx.Err() != nil {
				return
			}
			ciFail(err)
		}
	}
	ciPrintLog(ctx, gh, w, job, a.has("--all"))
}

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// ciSteps prints a job's steps, one line each; it returns how many lines.
func ciSteps(j ghactions.Job, spin string) int {
	of := len(j.Steps)
	for i, s := range j.Steps {
		line := fmt.Sprintf("  %s %d/%d %s", ghactions.Mark(s.Status, s.Conclusion, yellow+spin+reset), i+1, of, s.Name)
		t := ghactions.Took(s.StartedAt, s.CompletedAt)
		if s.Status == "in_progress" {
			// How long it has been going: a spinner alone looks the same
			// stuck.
			if at, err := time.Parse(time.RFC3339, s.StartedAt); err == nil {
				t = ghactions.Duration(time.Since(at))
			}
		}
		if t != "" {
			line += " " + dim + t + reset
		}
		fmt.Println(line)
	}
	return of
}

// ciFollow draws the steps of a running job in place until it ends, asking
// GitHub every three seconds; the spinner turns in between.
func ciFollow(ctx context.Context, gh *ghactions.Actions, w ghactions.Where, j ghactions.Job) (ghactions.Job, error) {
	const ask = 3 * time.Second
	drawn := 0
	frame := 0
	next := time.Now().Add(ask)
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	for {
		if drawn > 0 {
			fmt.Printf("\x1b[%dF\x1b[J", drawn)
		}
		drawn = ciSteps(j, spinner[frame%len(spinner)]) + 1
		fmt.Printf("%sThe log comes when the job ends.%s\n", dim, reset)
		frame++
		if ghactions.Finished(j.Status) {
			return j, nil
		}
		select {
		case <-ctx.Done():
			return j, ctx.Err()
		case <-tick.C:
		}
		if time.Now().After(next) {
			next = time.Now().Add(ask)
			nj, err := gh.Job(ctx, w, j.ID)
			if err != nil {
				// One failed ask (the network) does not end the watch.
				continue
			}
			j = nj
		}
	}
}

// ciPrintLog fetches a finished job's log and prints it by step: GitHub
// makes the log as the job ends, so a 404 is asked again for 30 seconds.
func ciPrintLog(ctx context.Context, gh *ghactions.Actions, w ghactions.Where, j ghactions.Job, all bool) {
	fmt.Printf("%sFetching the log…%s\n", dim, reset)
	var raw []byte
	deadline := time.Now().Add(30 * time.Second)
	for {
		var err error
		raw, err = gh.JobLog(ctx, w, j.ID)
		if err == nil {
			break
		}
		if !errors.Is(err, ghactions.ErrNoLogYet) || time.Now().After(deadline) {
			if errors.Is(err, ghactions.ErrNoLogYet) {
				fmt.Fprintf(os.Stderr, "  GitHub has no log for this job (it may have been skipped or deleted)\n")
				os.Exit(1)
			}
			ciFail(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
	// Up over "Fetching the log…".
	fmt.Print("\x1b[1F\x1b[J")
	for _, l := range ghactions.RenderLog(ghactions.SplitLog(raw, j.Steps), all) {
		fmt.Println(l)
	}
	if !all {
		fmt.Printf("%sOnly the failed steps are open: stui ci log --job %d --all for every line.%s\n", dim, j.ID, reset)
	}
}
