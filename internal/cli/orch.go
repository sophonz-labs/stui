package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sophonz-labs/stui/internal/chat"
	"github.com/sophonz-labs/stui/internal/mux"
	"github.com/sophonz-labs/stui/internal/orch"
	"github.com/sophonz-labs/stui/internal/proc"
	"github.com/sophonz-labs/stui/internal/state"
	"github.com/sophonz-labs/stui/internal/watch"
)

// stui orch, stui worker, stui skills, stui plugin: coding agents handing work to
// other coding agents in tmux. Run from inside an
// agent's tmux pane ($TMUX_PANE); the agent stui shares must be running.

// slow is for calls that wait on the agent (typing into a pane).
var slow = &http.Client{Timeout: 40 * time.Second}

// apiDo calls the agent's API with an optional JSON body; the status code,
// and out decoded when given.
func apiDo(s *state.Agent, method, path string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", s.Port, path), rd)
	req.Header.Set(state.TokenHeader, s.Token)
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	res, err := slow.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if out != nil {
		json.NewDecoder(res.Body).Decode(out)
	}
	return res.StatusCode, nil
}

// tx runs tmux against the server of the pane this runs in ($TMUX).
// On Windows that is psmux, which a pane's $TMUX routes to its own server
// for a bare %N (measured, psmux 3.4.0).
func tx(args ...string) (string, error) {
	bin := mux.NewTmux().Command()
	if runtime.GOOS == "windows" {
		bin = mux.NewPsmux().Command()
		args = watch.PsmuxArgs(args)
	}
	cmd := exec.Command(bin, args...)
	proc.Hide(cmd)
	out, err := cmd.Output()
	return strings.TrimRight(string(out), "\n"), err
}

func paneOpt(pane, name string) string {
	v, _ := tx("show-options", "-pqv", "-t", pane, name)
	return strings.TrimSpace(v)
}

// workRun is the run pane works in: a team's lead adopted as a worker
// works in @stui-in-run, everyone else in @stui-run.
func workRun(pane string) string {
	if id := paneOpt(pane, orch.OptInRun); id != "" {
		return id
	}
	return paneOpt(pane, "@stui-run")
}

// levels is how deep pane is within its run and in the whole chain above
// it (orch.Levels). Where the count goes up to another machine, the
// pane's STUI_DEPTH and STUI_CHAIN (from that machine) say the rest.
func levels(pane string) (depth, chainLen int) {
	envDepth, _ := strconv.Atoi(os.Getenv("STUI_DEPTH"))
	envChain, _ := strconv.Atoi(os.Getenv("STUI_CHAIN"))
	envChain = max(envChain, envDepth)
	out, err := tx("list-panes", "-a", "-F", orch.PaneFormat)
	if err != nil {
		return envDepth, envChain
	}
	d, c, dRemote, cRemote := orch.Levels(out, pane)
	if d < 0 || c < 0 {
		return orch.MaxDepth, orch.MaxChain
	}
	if dRemote {
		d = max(d, envDepth)
	}
	if cRemote {
		c = max(c, envChain)
	}
	return d, c
}

// tooDeep dies when pane may not start a worker.
func tooDeep(pane string, receipt map[string]any, asJSON bool) (depth, chainLen int) {
	d, c := levels(pane)
	if d >= orch.MaxDepth {
		die(asJSON, receipt, "too deep: within a team workers start workers at most %d levels below its coordinator", orch.MaxDepth)
	}
	if c >= orch.MaxChain {
		die(asJSON, receipt, "too deep: the whole chain of agents handing work down is at most %d long", orch.MaxChain)
	}
	return d, c
}

// paneAlive is whether pane (%N) is in this tmux server.
func paneAlive(pane string) bool {
	if !strings.HasPrefix(pane, "%") {
		return false
	}
	out, err := tx("display-message", "-p", "-t", pane, "#{pane_id}")
	return err == nil && strings.TrimSpace(out) == pane
}

func setPaneOpts(pane string, kv ...string) error {
	for i := 0; i+1 < len(kv); i += 2 {
		if _, err := tx("set-option", "-p", "-t", pane, kv[i], kv[i+1]); err != nil {
			return fmt.Errorf("set %s on %s: %w", kv[i], pane, err)
		}
	}
	return nil
}

func die(asJSON bool, receipt map[string]any, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if asJSON {
		receipt["ok"] = false
		receipt["error"] = msg
		b, _ := json.Marshal(receipt)
		fmt.Println(string(b))
	} else {
		fmt.Fprintln(os.Stderr, "  "+msg)
	}
	os.Exit(1)
}

func printJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

// here is the pane and the running agent, or a reason there are none.
func here(asJSON bool) (string, *state.Agent) {
	pane := os.Getenv("TMUX_PANE")
	if pane == "" || os.Getenv("TMUX") == "" {
		die(asJSON, map[string]any{}, "run this inside a tmux pane")
	}
	return pane, ensureServer()
}

// baseSession is the session a pane is in, not an app's view of it.
func baseSession(pane string) string {
	out, _ := tx("display-message", "-p", "-t", pane, "#{session_group}\t#{session_name}")
	g, n, _ := strings.Cut(out, "\t")
	if g != "" {
		return g
	}
	return n
}

// ensureRun is the run the coordinator pane is in, made when it has none.
func ensureRun(pane, objective string) (*orch.Run, error) {
	if id := paneOpt(pane, "@stui-run"); id != "" {
		if r, err := orch.Load(id); err == nil {
			return r, nil
		}
	}
	// Keyed, so Rebind finds its coordinator however that pane's options
	// read: a worker's say its own run, or one that was retired.
	key := paneOpt(pane, orch.OptKey)
	if key == "" {
		key = orch.NewKey()
		setPaneOpts(pane, orch.OptKey, key)
	}
	r := &orch.Run{ID: orch.NewID(), Objective: objective, Coordinator: pane, CoordinatorKey: key, CoordinatorLive: true, Session: baseSession(pane), Created: orch.Now()}
	if err := r.Save(); err != nil {
		return nil, err
	}
	// A worker that starts workers stays a worker; its run is its own.
	if paneOpt(pane, "@stui-role") == "" {
		if err := setPaneOpts(pane, "@stui-run", r.ID, "@stui-role", "coordinator"); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func orchCommand(a args) {
	sub := a.after("orch")
	asJSON := a.has("--json")
	if len(sub) == 0 {
		fmt.Print(orchHelp)
		return
	}
	switch sub[0] {
	case "start":
		pane, _ := here(asJSON)
		objective, _ := a.flag("--objective")
		r, err := ensureRun(pane, objective)
		if err != nil {
			die(asJSON, map[string]any{}, "%v", err)
		}
		if objective != "" && r.Objective == "" {
			r.Objective = objective
			r.Save()
		}
		if asJSON {
			printJSON(map[string]any{"ok": true, "run": r.ID, "coordinator": pane, "session": r.Session})
		} else {
			fmt.Printf("  run %s, coordinator %s in %s\n", r.ID, pane, r.Session)
		}
	case "status":
		workerList(a, asJSON)
	case "history":
		orchHistory(a, asJSON)
	case "unarchive":
		if len(sub) < 2 {
			die(asJSON, map[string]any{}, "stui orch unarchive <run>")
		}
		if err := orch.Unarchive(sub[1]); err != nil {
			die(asJSON, map[string]any{}, "%v", err)
		}
		if asJSON {
			printJSON(map[string]any{"ok": true, "run": sub[1]})
		} else {
			fmt.Printf("  %s is back: stui finds its agents by their keys within a few seconds (stui orch status)\n", sub[1])
		}
	default:
		fmt.Print(orchHelp)
		os.Exit(1)
	}
}

const orchHelp = `stui orchestration -- agents handing work to other agents in tmux

  stui orch start --objective "<goal>"         this pane coordinates a run
  stui orch status                             the run's workers
  stui orch history [--run r-…] [--all] [--limit N]  what was handed to whom and when, who finished,
                                             who was told
  stui orch unarchive <run>                    put a retired run back (~/.stui/orch/archive); its agents
                                             are found again by their keys
  stui worker start --agent claude|codex|opencode|pi --task <file|-> [--name n] [--split] [--cwd dir] [--owns "src/x/**"] [--worktree [--any-size]]
  stui worker wait [ids…] [--any] [--for done,waiting,idle] [--brief] [--timeout 15m] [--check-in 3m]
                                             done: it ran stui worker done; idle: it ended its turn
                                             without it, and why (its usage limit, an error, an
                                             interrupt) (--for done alone may wait out the timeout).
                                             --check-in: come back after that long with each one's
                                             state, how long it has been so and its title, leaving
                                             them running (exit 2, as for a timeout; 0 done, 3 asks)
  stui worker read <id> [--brief]              its last answer and summary (--brief: and the brief
                                             it gave when it was adopted)
  stui worker send <id> "<text>"               type into it, checked
  stui worker send <id> --file <file|-> ["<note>"] [--title T]
                                             more than a line: stui keeps a copy and types the
                                             worker where it is (not typed now -- someone typing
                                             there -- it waits in the worker's inbox)
  stui worker question [<id>] "<question>" [--timeout 10m]
                                             ask a pane (default: your coordinator) and wait for
                                             the answer; exit 2 when it does not come in time
  stui worker question --resume <question id>  wait again for that answer
  stui worker reply <question id> "<answer>"   answer a question stui typed to you
  stui worker inbox [--peek] [--ack <id>|--ack-all] [--wait [--timeout 10m]]
                                             mail owed to this pane: work not typed, news not
                                             seen to go in. Showing it takes it; --peek only
                                             shows. --wait: until some comes (exit 2 if none)
  stui worker done --summary "<line>" [--outcome succeeded|failed]
                                             a worker reports it is done; its parent is told in one
                                             line between its turns (also when a worker ends its
                                             turn without done)
  stui worker list [--all] [--depth N]         this pane's workers and theirs, as a tree; --depth 1: only
                                             its own ("team N" where more are under); --all: every worker
  stui worker stop <id> --yes                  close a worker's pane
  stui worker adopt <id> [--role "<why>"] [--no-brief] [--parent <id>] [--owns …]
                                             an agent already running becomes a worker
                                             (of this pane, or of --parent); it keeps its conversation.
                                             It is asked for a brief once its turn ends
  stui worker about <id> "<role>" | --clear    an adopted worker's role, shown under its name
  stui worker rename <id> <name>               a worker's name (letters, digits, dashes; 24 at most);
                                             one another worker has is refused. The tmux window
                                             keeps its title
  stui worker brief --summary "<lines>"        an adopted worker says what it has done (asked by stui)
  stui worker own <id> "<paths>" | --clear     what a worker may change (comma separated, ** and *);
                                             a Claude worker's edit outside them is refused
  stui worker release <id>                     an adopted worker is no one's again
  stui worker trees                            every worktree on this machine, by its id (<run>/<worker>),
                                             its worker done or gone too
  stui worker diff <id|tree> [--stat]          what a worker changed in its worktree, and from what:
                                             a commit, or stui's snapshot of a folder with no git
  stui worker ask <id|tree> ["<question>"] [--agent claude]
                                             a reading agent in its own window in the worktree, with
                                             the changes in its first prompt; never the worker itself.
                                             Asked again, the same window
  stui worker merge <id|tree> [--commit] [--squash]  its worktree's branch into this checkout; a conflict merges nothing
  stui worker clean <id|tree> [--force] [--keep-branch]  remove its worktree (and branch)
  stui worker start --fan 3 --agent claude,codex --task <file> [--owns …] [--split]
                                             one task to several workers, each in its own worktree
                                             from the same commit
  stui worker fan [<id>]                       a fan's workers side by side (what each changed)
  stui worker pick <id> [--commit] [--keep]    merge that one; the others' worktrees and windows go
  stui task create|list|start|wait             tasks in order: --deps a,b (stui task for more)
  stui gate ask "<question>" --option A --option B   a person decides; stui gate for more
  stui --skill                               common guide, plus obligations for STUI_RUN workers
  stui skills get <topic>|all                 the guides (stui skills --help)
  stui plugin install | status | uninstall     the Claude Code plugin and Codex skills

Ids: %15, or a name from stui worker list (api-tests).
A name is looked for among this pane's workers and theirs first, then every
worker on this machine; one that fits two workers does nothing and lists them.

Every command takes --json. The guide: stui skills get orchestrate
`

func workerCommand(a args) {
	sub := a.after("worker")
	asJSON := a.has("--json")
	if len(sub) == 0 {
		fmt.Print(orchHelp)
		return
	}
	switch sub[0] {
	case "start":
		workerStart(a, asJSON)
	case "wait":
		workerWait(a, sub[1:], asJSON)
	case "read":
		workerRead(sub[1:], asJSON)
	case "send":
		workerSend(sub[1:], asJSON)
	case "inbox":
		workerInbox(a, asJSON)
	case "question":
		workerQuestion(a, sub[1:], asJSON)
	case "reply":
		workerReply(sub[1:], asJSON)
	case "done":
		workerDone(a, asJSON)
	case "list":
		workerList(a, asJSON)
	case "stop":
		workerStop(a, sub[1:], asJSON)
	case "adopt", "release":
		workerAdopt(a, sub[0], sub[1:], asJSON)
	case "own":
		workerOwn(a, sub[1:], asJSON)
	case "about":
		workerAbout(a, sub[1:], asJSON)
	case "rename":
		workerRename(sub[1:], asJSON)
	case "brief":
		workerBrief(a, asJSON)
	case "diff", "merge", "clean":
		workerTree(a, sub[0], sub[1:], asJSON)
	case "trees":
		workerTrees(asJSON)
	case "ask":
		workerAsk(a, sub[1:], asJSON)
	case "fan":
		workerFanList(sub[1:], asJSON)
	case "pick":
		workerPick(a, sub[1:], asJSON)
	default:
		fmt.Print(orchHelp)
		os.Exit(1)
	}
}

// readTask is --task's text: a file, or stdin for "-".
func readTask(a args, asJSON bool, receipt map[string]any) string {
	taskArg, ok := a.flag("--task")
	if !ok {
		die(asJSON, receipt, "--task <file>, or --task - to read it from stdin")
	}
	var task []byte
	var err error
	if taskArg == "-" {
		task, err = io.ReadAll(os.Stdin)
	} else {
		task, err = os.ReadFile(taskArg)
	}
	if err != nil || strings.TrimSpace(string(task)) == "" {
		die(asJSON, receipt, "no task: %v", err)
	}
	return string(task)
}

// startSpec is what starting a worker takes, from stui worker start's flags
// or from a task (stui task start).
type startSpec struct {
	agent, text, name string
	owns              []string
	split, worktree   bool
	// Snapshot a folder git does not know however big (--any-size).
	anySize bool
	// The fan it is one of (stui worker start --fan).
	fan    string
	cwd    string
	hasCwd bool
}

func workerStart(a args, asJSON bool) {
	receipt := map[string]any{"stage": "check"}
	if why := checkFlags(a.after("start"), workerStartFlags, workerStartHints); why != "" {
		die(asJSON, receipt, "%s", why)
	}
	if _, remote := a.flag("--machine"); remote && a.has("--split") {
		die(asJSON, receipt, "--split opens the worker beside this pane; with --machine it opens in a window on that machine: leave out --split")
	}
	pane, s := here(asJSON)
	agent, _ := a.flag("--agent")
	if f, ok := a.flag("--fan"); ok {
		workerFanStart(a, pane, s, agent, f, asJSON)
		return
	}
	if !chat.CanSend(agent) {
		die(asJSON, receipt, "--agent %s (stui types the task into its input box, which it reads off the screen)", chat.TypedWords())
	}
	sp := startSpec{agent: agent, text: readTask(a, asJSON, receipt), split: a.has("--split"), worktree: a.has("--worktree"), anySize: a.has("--any-size"), fan: ""}
	sp.name, _ = a.flag("--name")
	if o, ok := a.flag("--owns"); ok {
		sp.owns = orch.ParseOwns(o)
	}
	sp.cwd, sp.hasCwd = a.flag("--cwd")
	if m, ok := a.flag("--machine"); ok {
		workerStartRemote(pane, sp, m, receipt, asJSON)
		return
	}
	id := startWorker(pane, s, sp, receipt, asJSON)
	_ = id
}

// startWorker opens the worker's window, types its task and sees it start;
// its pane id. It dies, with the receipt, on any failure.
func startWorker(pane string, s *state.Agent, sp startSpec, receipt map[string]any, asJSON bool) string {
	agent, text := sp.agent, sp.text
	depth, chainLen := tooDeep(pane, receipt, asJSON)
	name := orch.Name(sp.name)
	if name == "" {
		name = orch.Name(orch.FirstLine(text))
	}
	run, err := ensureRun(pane, orch.FirstLine(text))
	if err != nil {
		die(asJSON, receipt, "%v", err)
	}
	if name == "" || run.Worker(name) != nil {
		name = fmt.Sprintf("worker-%d", len(run.Workers)+1)
	}
	for _, w := range run.Workers {
		if w.Name == name {
			name = fmt.Sprintf("%s-%d", name, len(run.Workers)+1)
		}
	}
	receipt["run"], receipt["name"] = run.ID, name
	file, err := orch.WriteTask(run.ID, name, text)
	if err != nil {
		die(asJSON, receipt, "write the task: %v", err)
	}
	receipt["task"] = file
	cwd := sp.cwd
	if !sp.hasCwd {
		cwd, _ = tx("display-message", "-p", "-t", pane, "#{pane_current_path}")
	}
	// Its own worktree: a branch from where this checkout is, in a folder
	// of its own, and the worker works there (orch/worktree.go).
	var wt *orch.Worktree
	if sp.worktree {
		receipt["stage"] = "worktree"
		// A folder git does not know is snapshotted into a shadow
		// repository in stui's home (orch/shadow.go); too big, it says so.
		w, err := orch.MakeWorktreeWith(cwd, run.ID, name, orch.TreeOptions{AnySize: sp.anySize})
		var big *orch.TooBigError
		if errors.As(err, &big) {
			receipt["files"], receipt["bytes"] = big.Files, big.Bytes
			die(asJSON, receipt, "%v", err)
		}
		if err != nil {
			die(asJSON, receipt, "could not make the worktree: %v", err)
		}
		wt, cwd = w, w.Dir
		receipt["worktree"], receipt["branch"], receipt["tree"] = w.Path, w.Branch, w.TreeID()
		if w.Shadow != "" {
			receipt["snapshot"] = w.Snapshot
		}
	}
	// What the worker reads: a copy inside its project (orch/projectcopy.go),
	// or the run's own file. The run's file stays the record.
	read := file
	if b, err := os.ReadFile(file); err == nil {
		if c := orch.ProjectCopy(cwd, run.ID, filepath.Base(file), b); c != "" {
			receipt["copy"], read = c, c
		}
	}
	envs := []string{"-e", "STUI_RUN=" + run.ID, "-e", "STUI_PARENT_PANE=" + pane, "-e", "STUI_DEPTH=" + strconv.Itoa(depth+1), "-e", "STUI_CHAIN=" + strconv.Itoa(chainLen+1), "-e", "STUI_TASK_FILE=" + read}

	// The window (or a pane beside the coordinator with --split).
	receipt["stage"] = "window"
	var args_ []string
	if sp.split {
		args_ = append([]string{"split-window", "-d", "-P", "-F", "#{pane_id}\t#{window_index}", "-t", pane, "-c", cwd}, envs...)
	} else {
		args_ = append([]string{"new-window", "-d", "-P", "-F", "#{pane_id}\t#{window_index}", "-t", baseSession(pane) + ":", "-n", name, "-c", cwd}, envs...)
	}
	out, err := tx(append(args_, agent)...)
	if err != nil {
		die(asJSON, receipt, "could not open a window for the worker: %v", err)
	}
	id, win, _ := strings.Cut(strings.TrimSpace(out), "\t")
	window, _ := strconv.Atoi(win)
	receipt["id"], receipt["window"] = id, window
	if err := setPaneOpts(id, "@stui-run", run.ID, "@stui-role", "worker", "@stui-parent", pane, "@stui-name", name, "@stui-task", orch.FirstLine(text)); err != nil {
		die(asJSON, receipt, "%v (the worker's window is open: stui worker stop %s --yes)", err, id)
	}
	// Before the task is typed: its first edit is held to them already.
	if len(sp.owns) > 0 {
		if err := setPaneOpts(id, orch.OptOwns, strings.Join(sp.owns, ","), orch.OptRoot, cwd); err != nil {
			die(asJSON, receipt, "%v (the worker's window is open: stui worker stop %s --yes)", err, id)
		}
		receipt["owns"] = sp.owns
	}
	if wt != nil {
		setPaneOpts(id, "@stui-worktree", wt.Branch)
		if sp.fan != "" {
			setPaneOpts(id, "@stui-fan", sp.fan)
		}
	}
	run.Workers = append(run.Workers, orch.Worker{ID: id, Name: name, Agent: agent, Task: file, Window: window, Started: orch.Now(), Owns: sp.owns, Worktree: wt, Fan: sp.fan})
	run.Save()

	// The task, typed once the agent's input is there (stui's chat send:
	// it refuses while a prompt or a screen is up, and says whether the
	// prompt landed).
	receipt["stage"] = "deliver"
	msg := fmt.Sprintf("Your task is in %s . Read it and do it. When you are done, run: stui worker done --summary \"<what you did, and how you checked it>\"", read)
	deadline := time.Now().Add(90 * time.Second)
	var last string
	delivered := false
	for time.Now().Before(deadline) {
		var res map[string]any
		code, err := apiDo(s, "POST", "/api/chat/send?pane="+url.QueryEscape(id), map[string]any{"text": msg}, &res)
		if err == nil && code == 200 && res["delivered"] != nil {
			delivered = true
			receipt["delivered"] = res["delivered"]
			break
		}
		if err != nil {
			last = err.Error()
		} else if e, ok := res["error"].(string); ok {
			last = fmt.Sprintf("%d %s", code, e)
		} else {
			last = strconv.Itoa(code)
		}
		if _, gone := tx("display-message", "-p", "-t", id, "#{pane_id}"); gone != nil {
			die(asJSON, receipt, "the worker's agent exited before taking the task (%s)", last)
		}
		time.Sleep(time.Second)
	}
	startEv := orch.Event{Run: run.ID, Kind: orch.EvStart, From: pane, FromName: paneName(pane), To: id, ToName: name,
		File: file, Title: orch.FirstLine(text), Bytes: len(text), SHA256: sha(text), OK: &delivered}
	if !delivered {
		startEv.Why = last
		logEvent(startEv)
		die(asJSON, receipt, "the task was not typed into %s in 90 s (%s): look at window %d -- a trust or login screen? Its window is left open (stui worker stop %s --yes)", id, last, window, id)
	}
	logEvent(startEv)

	// Started: stui sees the turn begin (the agent's hooks).
	receipt["stage"] = "start"
	started := false
	for end := time.Now().Add(30 * time.Second); time.Now().Before(end); time.Sleep(500 * time.Millisecond) {
		if st := paneState(s, id); st == "working" || st == "waiting" {
			started = true
			break
		}
	}
	if !started {
		die(asJSON, receipt, "the task was typed into %s but its agent has not started a turn in 30 s (are stui's hooks installed? stui hooks install)", id)
	}
	receipt["stage"], receipt["ok"] = "started", true
	if asJSON {
		printJSON(receipt)
	} else {
		fmt.Printf("  worker %s (%s, %s) started in window %d\n  task: %s\n", id, name, agent, window, file)
		if len(sp.owns) > 0 {
			fmt.Printf("  may change only: %s (in %s)\n", strings.Join(sp.owns, ", "), cwd)
		}
		if wt != nil {
			fmt.Printf("  in its worktree %s on %s, %s (stui worker diff %s, stui worker ask %s, stui worker merge %s)\n", wt.Path, wt.Branch, wt.BaseLine(), wt.TreeID(), wt.TreeID(), wt.TreeID())
		}
	}
	return id
}

type agentPane struct {
	ID    string `json:"id"`
	State string `json:"state"`
	// When it came to that state (ms).
	Since int64 `json:"since"`
	// When its agent last took a prompt (ms), 0 for not heard.
	Submitted int64 `json:"submitted"`
	// tmux's pane title: Claude Code keeps its task summary there.
	Title string `json:"title"`
}

func agentPanes(s *state.Agent) map[string]agentPane {
	var snap struct {
		Panes []agentPane `json:"panes"`
	}
	apiDo(s, "GET", "/api/agents", nil, &snap)
	m := map[string]agentPane{}
	for _, p := range snap.Panes {
		m[p.ID] = p
	}
	return m
}

func paneState(s *state.Agent, id string) string { return agentPanes(s)[id].State }

// orchPane is a pane's run options as tmux lists them.
type orchPane struct {
	ID, Run, Role, Parent, Name, Done, Task, Summary string
	Window                                           int
	// When stui worker send last gave it something (ms; @stui-sent).
	Sent int64
	// The run a team's lead works in (@stui-in-run); Run is its team's.
	InRun string
	// A worker on another machine that leads a team there.
	Team *orch.TeamInfo
	// Adopted (@stui-adopted), its role and its brief (orch/brief.go); Brief
	// with its newlines back.
	Adopted                  bool
	About, Brief, BriefState string
	BriefAt                  int64
}

func orchPanes() []orchPane {
	out, err := tx("list-panes", "-a", "-F", "#{pane_id}\t#{@stui-run}\t#{@stui-role}\t#{@stui-parent}\t#{@stui-name}\t#{@stui-done}\t#{window_index}\t#{@stui-task}\t#{@stui-sent}\t#{@stui-in-run}\t#{@stui-adopted}\t#{@stui-about}\t#{@stui-brief-state}\t#{@stui-brief-at}\t#{@stui-brief}\t#{@stui-summary}")
	if err != nil {
		return nil
	}
	return parseOrchPanes(out)
}

func parseOrchPanes(out string) []orchPane {
	seen := map[string]bool{}
	var ps []orchPane
	for _, l := range strings.Split(out, "\n") {
		f := strings.SplitN(l, "\t", 16)
		if len(f) < 16 || f[1] == "" || seen[f[0]] {
			continue
		}
		seen[f[0]] = true
		w, _ := strconv.Atoi(f[6])
		sent, _ := strconv.ParseInt(f[8], 10, 64)
		at, _ := strconv.ParseInt(f[13], 10, 64)
		ps = append(ps, orchPane{ID: f[0], Run: f[1], Role: f[2], Parent: f[3], Name: f[4], Done: f[5], Window: w, Task: f[7], Sent: sent, InRun: f[9],
			Adopted: f[10] == "1", About: f[11], BriefState: f[12], BriefAt: at, Brief: orch.BriefText(f[14]), Summary: f[15]})
	}
	return ps
}

// teamOf is the team p leads: how many panes here have it as their parent
// (a worker that started or adopted workers of its own, or a lead adopted
// with its team), or what its machine says of one on another machine.
func teamOf(p orchPane, ps []orchPane) *orch.TeamInfo {
	if p.Team != nil {
		return p.Team
	}
	n := 0
	for _, q := range ps {
		if q.Parent == p.ID && q.ID != p.ID {
			n++
		}
	}
	if n == 0 {
		return nil
	}
	return &orch.TeamInfo{Run: p.Run, Members: n}
}

// status of a worker: exited, done (it ran stui worker done), idle (its turn
// ended without that), waiting (a question), working.
// sentGrace is how long a worker that was just sent something may stay
// idle before stui takes it that it is not going to start.
var sentGrace = 2 * time.Minute

func workerStatus(p *orchPane, a agentPane) string {
	switch {
	case p == nil:
		return "exited"
	case p.Done == "1":
		return "done"
	case a.State == "waiting":
		return "waiting"
	case a.State == "done" || a.State == "error" || a.State == "idle":
		// Sent something and still idle since before it: it has not
		// started on it yet, so it is not done with it.
		if p.Sent > 0 && a.Since < p.Sent && time.Since(time.UnixMilli(p.Sent)) < sentGrace {
			return "working"
		}
		return "idle"
	case a.State == "":
		return "exited"
	}
	return "working"
}

// stopRows puts on each idle row why its turn ended (stopRow).
func stopRows(rows []map[string]any) {
	for _, r := range rows {
		if r["status"] != "idle" {
			continue
		}
		run, _ := r["run"].(string)
		in, _ := r["_inRun"].(string)
		stopRow(r, fmt.Sprint(r["id"]), in, run)
	}
}

// stopRow puts on row why worker id's turn ended, from the idle event stui
// wrote for it in one of runs: stopReason, and for a limit resetsAt and
// the agent's line. Nothing when there is none (an older stui, or not told
// yet).
func stopRow(row map[string]any, id string, runs ...string) {
	seen := map[string]bool{}
	for _, run := range runs {
		if run == "" || seen[run] {
			continue
		}
		seen[run] = true
		ev := orch.LastStop(orch.ReadEvents(run), id)
		if ev == nil || ev.StopReason == "" {
			continue
		}
		row["stopReason"] = ev.StopReason
		if ev.Why != "" {
			row["stopWhy"] = ev.Why
		}
		if ev.ResetsAt > 0 {
			row["resetsAt"] = ev.ResetsAt
		}
		return
	}
}

// idleNote is what stui worker wait says after an idle worker: why its turn
// ended, and whether reading it is worth it.
func idleNote(r map[string]any) string {
	reason, _ := r["stopReason"].(string)
	switch reason {
	case orch.StopLimit:
		at, _ := r["resetsAt"].(int64)
		if when := orch.ResetsLine(at, time.Now()); when != "" {
			return "  (at its usage limit until " + when + ": nothing to read)"
		}
		return "  (at its usage limit: nothing to read)"
	case orch.StopError:
		return fmt.Sprintf("  (stopped on an error: stui worker read %v)", r["id"])
	case orch.StopCancelled:
		return fmt.Sprintf("  (interrupted at its pane: stui worker read %v)", r["id"])
	}
	return fmt.Sprintf("  (ended its turn without stui worker done: stui worker read %v)", r["id"])
}

func workerWait(a args, ids []string, asJSON bool) {
	pane, s := here(asJSON)
	// An id that means no worker stops it: waiting for every worker
	// instead would look like waiting for that one.
	want := resolveArgs(ids, asJSON)
	if len(want) == 0 {
		for _, p := range orchPanes() {
			if p.Parent == pane {
				want = append(want, p.ID)
			}
		}
		// And its workers on other machines.
		if r, err := orch.Load(paneOpt(pane, "@stui-run")); err == nil {
			for _, w := range r.Workers {
				if w.Machine != "" {
					if p, _, err := remoteWorker(w.ID, asJSON); err == nil && p != nil && strings.HasSuffix(p.Parent, "/"+pane) {
						want = append(want, w.ID)
					}
				}
			}
		}
	}
	if len(want) == 0 {
		die(asJSON, map[string]any{}, "no workers of %s to wait for", pane)
	}
	forSet := map[string]bool{"done": true, "waiting": true, "idle": true, "exited": true}
	if f, ok := a.flag("--for"); ok {
		forSet = map[string]bool{"exited": true}
		for _, x := range strings.Split(f, ",") {
			forSet[strings.TrimSpace(x)] = true
		}
	}
	timeout := 15 * time.Minute
	if t, ok := a.flag("--timeout"); ok {
		if d, err := time.ParseDuration(t); err == nil {
			timeout = d
		}
	}
	first := a.has("--any")
	briefs := a.has("--brief")
	if briefs {
		forSet = map[string]bool{"brief": true, "no-brief": true, "exited": true}
	}
	cfg := waitCfg{forSet: forSet, first: first, end: time.Now().Add(timeout), tick: time.Second}
	if c, ok := a.flag("--check-in"); ok {
		d, err := time.ParseDuration(c)
		if err != nil || d <= 0 {
			die(asJSON, map[string]any{}, "--check-in takes a duration (3m)")
		}
		cfg.checkIn = time.Now().Add(d)
	}
	// Its workers' news wakes it here; the carrier does not type it in
	// as well while this waits (waiter first, orch/inbox.go).
	defer orch.Waiting(pane)()
	look := func() []map[string]any {
		states := agentPanes(s)
		byID := map[string]*orchPane{}
		ps := orchPanes()
		for i := range ps {
			byID[ps[i].ID] = &ps[i]
		}
		rows := []map[string]any{}
		for _, id := range want {
			p, a := byID[id], states[id]
			if !strings.HasPrefix(id, "%") {
				var err error
				if p, a, err = remoteWorker(id, asJSON); err != nil {
					// Unreachable for now: still working, as far as we know.
					p, a = &orchPane{ID: id}, agentPane{State: "working"}
				}
			}
			st := workerStatus(p, a)
			row := map[string]any{"id": id, "status": st}
			if a.Since > 0 {
				row["forMs"] = time.Since(time.UnixMilli(a.Since)).Milliseconds()
			}
			if a.Title != "" {
				row["title"] = a.Title
			}
			if p != nil {
				row["name"], row["summary"] = p.Name, p.Summary
				if st == "idle" {
					stopRow(row, id, p.InRun, p.Run)
				}
			}
			if briefs {
				// Waiting for its brief, not its work: done when it has
				// given one, or none was asked.
				if st != "exited" {
					switch {
					case p != nil && p.BriefState == orch.BriefAnswered:
						st = "brief"
					case p == nil || p.BriefState != orch.BriefAsked:
						st = "no-brief"
					}
				}
				row["status"] = st
				if p != nil {
					row["summary"] = p.Brief
				}
			}
			rows = append(rows, row)
		}
		return rows
	}
	rows, out := waitLoop(cfg, look)
	if asJSON {
		printJSON(map[string]any{"workers": rows, "timeout": out == waitTimedOut, "checkedIn": out == waitCheckedIn})
	} else {
		for _, r := range rows {
			note := ""
			if r["status"] == "idle" {
				note = idleNote(r)
			}
			if out == waitCheckedIn {
				note += checkInNote(r)
			}
			fmt.Printf("  %-6v %-8v %v %v%s\n", r["id"], r["status"], r["name"], r["summary"], note)
		}
		switch out {
		case waitTimedOut:
			fmt.Println("  timed out: a checkpoint, not a failure. stui worker list, then wait again")
		case waitCheckedIn:
			fmt.Println("  checked in: the workers go on. Wait again, or look at the one that worries you")
		}
	}
	if code := waitExit(out, rows); code != 0 {
		os.Exit(code)
	}
}

// waitCfg is what stui worker wait waits for: workers whose status is in
// forSet (all of them, or the first with first), until end; and with
// checkIn, coming back then with how they are, leaving them running.
type waitCfg struct {
	forSet  map[string]bool
	first   bool
	end     time.Time
	checkIn time.Time
	tick    time.Duration
}

type waitOutcome int

const (
	waitDone waitOutcome = iota
	waitTimedOut
	waitCheckedIn
)

// waitLoop looks at the workers every tick until cfg is met. It only
// looks: a check-in sends nothing and stops no one.
func waitLoop(cfg waitCfg, look func() []map[string]any) ([]map[string]any, waitOutcome) {
	for {
		rows := look()
		matched := 0
		for _, r := range rows {
			if st, _ := r["status"].(string); cfg.forSet[st] {
				matched++
			}
		}
		now := time.Now()
		switch {
		case (cfg.first && matched > 0) || matched == len(rows):
			return rows, waitDone
		case now.After(cfg.end):
			return rows, waitTimedOut
		case !cfg.checkIn.IsZero() && !now.Before(cfg.checkIn):
			return rows, waitCheckedIn
		}
		time.Sleep(cfg.tick)
	}
}

// waitExit is stui worker wait's exit code: 0 done, 3 done with one asking,
// 2 timed out or checked in (a checkpoint, not a failure).
func waitExit(out waitOutcome, rows []map[string]any) int {
	if out != waitDone {
		return 2
	}
	for _, r := range rows {
		if r["status"] == "waiting" {
			return 3
		}
	}
	return 0
}

// checkInNote is how long a worker has been as it is, and what it says it
// is on (its pane title).
func checkInNote(r map[string]any) string {
	note := ""
	if ms, ok := r["forMs"].(int64); ok {
		note = "  (for " + (time.Duration(ms) * time.Millisecond).Round(time.Second).String() + ")"
	}
	if t, ok := r["title"].(string); ok && t != "" {
		note += "  " + t
	}
	return note
}

// chatSnap is GET /api/chat, what stui worker read takes from it.
type chatSnap struct {
	Messages []struct {
		Role   string `json:"role"`
		Blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"blocks"`
	} `json:"messages"`
	Unavailable string `json:"unavailable"`
	// Since when a transcript that stopped growing has not been written,
	// and what explains it.
	Since int64  `json:"since,omitempty"`
	Cause string `json:"cause,omitempty"`
}

func workerRead(rest []string, asJSON bool) {
	if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
		die(asJSON, map[string]any{}, "%s", "stui worker read <id>   ("+idForms+")")
	}
	_, s := here(asJSON)
	id := resolveWorker(rest[0], asJSON)
	if !strings.HasPrefix(id, "%") {
		remoteRead(id, slices.Contains(rest, "--brief"), asJSON)
		return
	}
	// Read all the same, but said: it may be another agent's conversation.
	if why, now, mine := checkTarget(id); why != "" {
		fmt.Fprintf(os.Stderr, "  warning: %s\n", targetMessage(why, now, mine))
	}
	var snap chatSnap
	code, err := apiDo(s, "GET", "/api/chat?tail=40&pane="+url.QueryEscape(id), nil, &snap)
	if err != nil || code != 200 {
		die(asJSON, map[string]any{"id": id}, "could not read %s's conversation (%d %v)", id, code, err)
	}
	printAnswer(id, paneOpt(id, "@stui-summary"), orch.BriefText(paneOpt(id, orch.OptBrief)), snap, slices.Contains(rest, "--brief"), asJSON)
}

// printAnswer is the last answer -- the assistant's text after the last
// prompt -- and the worker's summary. The brief it gave when it was adopted
// only with showBrief (--brief): it was true then, and days later it names
// work long done, in every read.
func printAnswer(id, summary, brief string, snap chatSnap, showBrief, asJSON bool) {
	var parts []string
	for i := len(snap.Messages) - 1; i >= 0; i-- {
		m := snap.Messages[i]
		if m.Role == "user" {
			break
		}
		if m.Role != "assistant" {
			continue
		}
		var t []string
		for _, b := range m.Blocks {
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				t = append(t, b.Text)
			}
		}
		if len(t) > 0 {
			parts = append([]string{strings.Join(t, "\n")}, parts...)
		}
	}
	answer := strings.Join(parts, "\n\n")
	if asJSON {
		printJSON(map[string]any{"id": id, "summary": summary, "brief": brief, "answer": answer, "unavailable": snap.Unavailable, "since": snap.Since, "cause": snap.Cause})
		return
	}
	if brief != "" && showBrief {
		fmt.Printf("  brief (when it was adopted):\n")
		for _, l := range strings.Split(brief, "\n") {
			fmt.Printf("    %s\n", l)
		}
		fmt.Println()
	}
	if summary != "" {
		fmt.Printf("  summary: %s\n\n", summary)
	}
	if why := unavailableLine(snap.Unavailable, snap.Since, snap.Cause); why != "" {
		fmt.Printf("  %s\n\n", why)
	}
	if answer == "" {
		fmt.Println("  (no answer yet)")
		return
	}
	fmt.Println(answer)
}

func workerSend(rest []string, asJSON bool) {
	// stui's own flags are not part of the message.
	force, hasFile := false, false
	file, docRef, title := "", "", ""
	var words []string
	for i := 0; i < len(rest); i++ {
		w := rest[i]
		next := func() string {
			if i+1 < len(rest) {
				i++
				return rest[i]
			}
			return ""
		}
		switch w {
		case "--force":
			force = true
		case "--json":
		case "--file":
			hasFile, file = true, next()
		case "--doc":
			docRef = next()
			if docRef == "" {
				die(asJSON, map[string]any{}, "--doc takes a document: its id, title or folder/title (stui doc ls)")
			}
		case "--title":
			title = next()
		default:
			words = append(words, w)
		}
	}
	withBrief := hasFile || docRef != ""
	if len(words) < 1 || !withBrief && len(words) < 2 {
		die(asJSON, map[string]any{}, "%s", `stui worker send <id> "<text>" | --file <file|-> ["<note>"] | --doc <doc> [--force]   (`+idForms+`)`)
	}
	me, s := here(asJSON)
	id := resolveWorker(words[0], asJSON)
	note := strings.Join(words[1:], " ")
	var b *sendFile
	if withBrief {
		b = readSendFile(s, file, hasFile, docRef, title, asJSON)
	}
	if !strings.HasPrefix(id, "%") {
		remoteSend(me, id, note, b, asJSON)
		return
	}
	// The agent in that pane must be the worker meant (target.go).
	if why, now, mine := checkTarget(id); why != "" {
		if !force {
			receipt := map[string]any{"id": id, "reason": "not-your-worker"}
			if now != "" {
				receipt["now"] = now
			}
			die(asJSON, receipt, "not sent: %s (--force sends it all the same)", targetMessage(why, now, mine))
		}
		fmt.Fprintf(os.Stderr, "  warning: %s; sending anyway (--force)\n", targetMessage(why, now, mine))
	}
	run := firstRun(id, me)
	text := note
	ev := orch.Event{Run: run, Kind: orch.EvSend, From: me, FromName: paneName(me), To: id, ToName: paneOpt(id, "@stui-name"),
		Title: orch.FirstLine(text), Bytes: len(text)}
	if b != nil {
		worker := paneOpt(id, "@stui-name")
		if worker == "" {
			worker = id
		}
		b.keep(s, run, me, worker, asJSON)
		// The worker reads a copy inside its project when it has one.
		cwd, _ := tx("display-message", "-p", "-t", id, "#{pane_current_path}")
		read := orch.Readable(orch.ProjectCopy(strings.TrimSpace(cwd), run, "sent/"+filepath.Base(b.file), []byte(b.content)), b.file)
		text = orch.SentLine(paneName(me), read, b.title, len(b.content), note)
		ev.Doc, ev.File, ev.Title, ev.Bytes, ev.SHA256 = b.doc, b.file, b.title, len(b.content), sha(b.content)
	}
	receipt := map[string]any{"id": id}
	if b != nil {
		receipt["doc"], receipt["file"] = b.doc, b.file
	}
	// The next work, whether it goes in now or waits in the inbox: a done
	// for an older one is told from it (workerDone).
	gen := nextGen(paneOpt(id, "@stui-gen"))
	at, code, res, err := deliverWork(s, id, text)
	// The send is when it was typed, not when stui had written it down.
	ev.TS = at
	ok := err == nil && code == 200
	ev.OK = &ok
	receipt["result"] = res
	if !ok {
		why := ""
		if err != nil {
			ev.Why, why = err.Error(), err.Error()
		} else {
			ev.Why, _ = res["reason"].(string)
			why = chat.RefusalWords(code, res)
		}
		// Not now, for a while: someone typing there, a question up, another
		// line going in, no box on screen. It is owed to the worker's inbox,
		// and stui types a pointer there between its turns (orch/inbox.go).
		if code == 409 && owedLater(ev.Why) && run != "" {
			ev.Owed = true
			ev.Text = text
			ev.Gen = gen
			logEvent(ev)
			// Given, if not yet typed: stui worker wait waits for it.
			gave(id, gen, at)
			receipt["ok"], receipt["owed"] = true, true
			if asJSON {
				printJSON(receipt)
			} else {
				fmt.Printf("  not typed now: %s; it waits in %s's inbox -- stui types a pointer there between its turns%s\n", why, named(id), b.said())
			}
			return
		}
		logEvent(ev)
		die(asJSON, receipt, "not sent: %s%s", why, resendHint(id, b))
	}
	// Taken now (the prompt went in), or owed until it is: a line left in
	// the box is the carrier's to see go in, or to point to.
	taken := res["delivered"] == true
	if !taken && run != "" {
		ev.Owed = true
		ev.Text = text
	}
	ev.Gen = gen
	if run != "" {
		var err error
		if ev, err = orch.AppendEvent(ev); err != nil {
			fmt.Fprintf(os.Stderr, "  (not written to the run's history: %v)\n", err)
		} else if taken {
			logEvent(orch.Event{Run: run, Kind: orch.EvDelivered, Ref: ev.ID, From: me, To: id, ToName: ev.ToName, Text: text})
		}
	}
	gave(id, gen, at)
	if asJSON {
		receipt["ok"] = true
		printJSON(receipt)
	} else {
		fmt.Printf("  sent to %s (delivered: %v)%s\n", named(id), res["delivered"], b.said())
	}
}

// deliverWork types text into pane id through stui (POST /api/chat/send)
// and says when it began: before the keys, so before the agent could
// record taking the prompt. That is what @stui-sent holds; lateDone compares
// the agent's record with it, and a time taken after the send (the answer
// waits for the prompt to be seen going in, then the history is written)
// would make every done for this work look like one for older work.
func deliverWork(s *state.Agent, id, text string) (at int64, code int, res map[string]any, err error) {
	at = time.Now().UnixMilli()
	code, err = apiDo(s, "POST", "/api/chat/send?pane="+url.QueryEscape(id), map[string]any{"text": text}, &res)
	return at, code, res, err
}

// gave marks a worker as given work gen, sent at at (deliverWork): no
// longer done with the last thing (nor its outcome), and stui worker wait
// waits for this one (workerStatus). Called only once the send went
// through, or was owed; the time is the send's, not this call's.
func gave(id string, gen int, at int64) {
	tx("set-option", "-p", "-u", "-t", id, "@stui-done")
	tx("set-option", "-p", "-u", "-t", id, "@stui-outcome")
	setPaneOpts(id, "@stui-sent", strconv.FormatInt(at, 10), "@stui-gen", strconv.Itoa(gen))
}

// said is the receipt's end for a brief: the document it is.
func (b *sendFile) said() string {
	if b == nil {
		return ""
	}
	if b.doc == "" {
		return "  copy: " + b.file
	}
	return "  doc: " + b.title
}

// resendHint says how to send a refused brief again without adding it
// twice.
func resendHint(id string, b *sendFile) string {
	if b == nil {
		return ""
	}
	if b.doc == "" {
		return "; the copy is kept: " + b.file
	}
	return fmt.Sprintf("; kept as a document -- stui worker send %s --doc %s sends it again", id, b.doc)
}

// nextGen is the work after the one a pane's @stui-gen names: 1 for a
// worker given nothing by stui worker send yet (its first work is 1, so its
// first send is 2).
func nextGen(opt string) int {
	n, err := strconv.Atoi(opt)
	if err != nil || n < 1 {
		n = 1
	}
	return n + 1
}

// paneGen is the work a pane is on: @stui-gen, 1 before any send.
func paneGen(opt string) int {
	if n, err := strconv.Atoi(opt); err == nil && n > 0 {
		return n
	}
	return 1
}

// doneOutcome is what stui worker done says of the work: --outcome, or with
// none "failed" for a summary that starts with "blocked" (how a worker
// said it before there was --outcome), else "succeeded".
func doneOutcome(flag string, given bool, summary string) (string, error) {
	if given {
		switch flag {
		case orch.OutcomeSucceeded, orch.OutcomeFailed:
			return flag, nil
		}
		return "", fmt.Errorf("--outcome is succeeded or failed, not %q", flag)
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(summary)), "blocked") {
		return orch.OutcomeFailed, nil
	}
	return orch.OutcomeSucceeded, nil
}

// lateDone is whether a done is for work given before the last: the
// pane's agent last took a prompt before that work was sent, so the turn
// it reports from had not seen it. Unknown (no prompt heard, nothing
// sent) is not late: a report is never dropped on a guess.
func lateDone(sent, submitted int64) bool {
	return sent > 0 && submitted > 0 && submitted < sent
}

func workerDone(a args, asJSON bool) {
	pane, s := here(asJSON)
	summary, _ := a.flag("--summary")
	of, given := a.flag("--outcome")
	outcome, err := doneOutcome(of, given, summary)
	if err != nil {
		die(asJSON, map[string]any{}, "%v", err)
	}
	gen := paneGen(paneOpt(pane, "@stui-gen"))
	sent, _ := strconv.ParseInt(paneOpt(pane, "@stui-sent"), 10, 64)
	if lateDone(sent, agentPanes(s)[pane].Submitted) {
		// Written down for the record, not counted: the work sent since is
		// still to do, and its parent is not told.
		if id := workRun(pane); id != "" {
			logEvent(orch.Event{Run: id, Kind: orch.EvLateDone, From: pane, FromName: paneOpt(pane, "@stui-name"), Title: orch.FirstLine(summary), Gen: gen - 1, Outcome: outcome})
		}
		at := time.UnixMilli(sent).Local().Format("15:04:05")
		if asJSON {
			printJSON(map[string]any{"ok": true, "id": pane, "late": true, "gen": gen - 1, "outcome": outcome})
		} else {
			fmt.Printf("  written down, not counted: this turn took its prompt before the work sent at %s (work %d); stui worker done again when that is done\n", at, gen)
		}
		return
	}
	if err := setPaneOpts(pane, "@stui-done", "1", "@stui-summary", strings.ReplaceAll(summary, "\n", " "), "@stui-outcome", outcome); err != nil {
		die(asJSON, map[string]any{}, "%v", err)
	}
	told := ""
	if id := workRun(pane); id != "" {
		if r, err := orch.Load(id); err == nil {
			if w := r.Worker(pane); w != nil {
				w.Done, w.Summary, w.Ended = true, summary, orch.Now()
				r.Save()
			}
		}
		// The parent's mail (orch/mail.go): stui types it into the nearest
		// parent still at work, between its turns.
		ev := orch.Event{Run: id, Kind: orch.EvDone, From: pane, FromName: paneOpt(pane, "@stui-name"), Title: orch.FirstLine(summary), Gen: gen, Outcome: outcome}
		if out, err := tx("list-panes", "-a", "-F", orch.PaneFormat); err == nil {
			ev.Up = orch.ChainUpFrom(out, pane)
		}
		if len(ev.Up) == 0 {
			if p := paneOpt(pane, "@stui-parent"); p != "" {
				ev.Up = []string{p}
			}
		}
		if len(ev.Up) > 0 {
			ev.To = ev.Up[0]
			if strings.HasPrefix(ev.To, "%") {
				ev.ToName = paneName(ev.To)
			}
			// Who stui will tell: the nearest parent still at work (one that
			// is done itself passes it on), or one on another machine.
			told = orch.Recipient(ev,
				func(x string) (string, bool) { return paneOpt(x, "@stui-parent"), paneAlive(x) },
				paneAlive,
				func(x string) bool { return paneOpt(x, "@stui-done") == "1" })
			if told == "" {
				for _, x := range ev.Up {
					if strings.Contains(x, "/") {
						told = x
						break
					}
				}
			}
		}
		logEvent(ev)
	}
	if asJSON {
		printJSON(map[string]any{"ok": true, "id": pane, "told": told, "gen": gen, "outcome": outcome})
	} else if told != "" {
		name := paneName(told)
		if name != "" {
			name += " "
		}
		fmt.Printf("  reported done; %s%s is told between its turns\n", name, told)
	} else {
		fmt.Println("  reported done")
	}
}

// workerList is this pane's workers and theirs, as a tree (a worker that
// leads a team on another machine says how many it has), or with --all
// every worker. --depth N stops N levels down; a row with more under it
// says "team N".
func workerList(a args, asJSON bool) {
	pane, s := here(asJSON)
	run := paneOpt(pane, "@stui-run")
	all := a.has("--all")
	limit := 0
	if d, ok := a.flag("--depth"); ok {
		n, err := strconv.Atoi(d)
		if err != nil || n < 1 {
			die(asJSON, map[string]any{}, "--depth takes a number from 1")
		}
		limit = n
	}
	if run == "" {
		// A coordinator whose pane lost its options (a tmux server that
		// died, before stui put them back) still has its run's record.
		if r := orch.RunOf(pane, paneOpt(pane, orch.OptKey)); r != nil {
			run = r.ID
		}
	}
	states := agentPanes(s)
	rows := []map[string]any{}
	ps := orchPanes()
	byPane := map[string]orchPane{}
	for _, p := range ps {
		byPane[p.ID] = p
	}
	parentOf := map[string]string{}
	for _, p := range ps {
		if p.Role == "worker" {
			parentOf[p.ID] = p.Parent
		}
	}
	// The runs whose workers on other machines may be in the tree: this
	// pane's, and those of the workers under it.
	runSet := map[string]bool{}
	if run != "" {
		runSet[run] = true
	}
	for _, p := range ps {
		if p.Role != "worker" {
			continue
		}
		mine := under(candidate{Parent: p.Parent}, pane, parentOf)
		if !all && !mine {
			continue
		}
		if mine {
			runSet[p.Run] = true
			if p.InRun != "" {
				runSet[p.InRun] = true
			}
		}
		p := p
		row := map[string]any{"id": p.ID, "name": p.Name, "window": p.Window, "status": workerStatus(&p, states[p.ID]), "task": p.Task, "summary": p.Summary, "parent": p.Parent, "run": p.Run, "_up": p.Parent}
		briefRow(row, &p)
		if p.InRun != "" {
			row["_inRun"] = p.InRun
		}
		if t := teamOf(p, ps); t != nil {
			row["team"] = t
		}
		if _, _, remoteParent := splitRemote(p.Parent); remoteParent {
			// A worker of a coordinator on another machine: ask it.
			if gone, now, known := parentHere(p.Parent, p.Run); known && gone {
				row["parentGone"] = true
				if now != "" {
					row["parentNow"] = now
				}
			}
		} else if !parentInRun(p, byPane) {
			row["parentGone"] = true
		}
		if row["status"] == "exited" {
			// Its pane is here and a worker's, with no agent in it (a
			// restore brought the pane back, not the agent).
			row["noAgent"] = true
			if w := recordOf(p.Run, p.ID); w != nil && w.Conv != "" {
				row["conv"] = w.Conv
			}
		}
		rows = append(rows, row)
	}
	var runs []string
	if all {
		for _, r := range orch.Runs() {
			runs = append(runs, r.ID)
		}
	} else {
		for id := range runSet {
			runs = append(runs, id)
		}
		sort.Strings(runs)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		seen[fmt.Sprint(r["id"])] = true
	}
	anchors := paneAnchors()
	for _, id := range runs {
		rows = append(rows, lostRows(id, anchors, seen)...)
	}
	for _, id := range runs {
		for _, r := range remoteRows(id, asJSON) {
			if id, _ := r["id"].(string); !seen[id] {
				seen[id] = true
				rows = append(rows, r)
			}
		}
	}
	stopRows(rows)
	rows = treeRows(rows, pane, all, limit)
	shown := map[string]int{}
	for _, r := range rows {
		if up, _ := r["_up"].(string); up != "" {
			shown[up]++
		}
		delete(r, "_up")
		delete(r, "_inRun")
	}
	if asJSON {
		out := map[string]any{"run": run, "workers": rows}
		if r, err := orch.Load(run); err == nil {
			out["objective"] = r.Objective
		}
		printJSON(out)
		return
	}
	if len(rows) == 0 {
		fmt.Println("  no workers")
		return
	}
	w := 6
	for _, r := range rows {
		d, _ := r["depth"].(int)
		if n := len(fmt.Sprint(r["id"])) + 2*(d-1); n > w {
			w = n
		}
	}
	for _, r := range rows {
		where := ""
		m, _ := r["machine"].(string)
		if m != "" {
			where = " on " + machineLabel(m)
		}
		// Its team, when the rows under it are not here to show it.
		if t, ok := r["team"].(*orch.TeamInfo); ok && t != nil && shown[fmt.Sprint(r["id"])] == 0 {
			if m != "" {
				where += fmt.Sprintf(" (team %d on %s)", t.Members, machineLabel(m))
			} else {
				where += fmt.Sprintf(" (team %d)", t.Members)
			}
		}
		if r["parentGone"] == true {
			where += "  (parent gone"
			if now, ok := r["parentNow"].(string); ok {
				where += "; its coordinator is " + now + " now"
			}
			where += ")"
		}
		switch {
		case r["status"] == "lost":
			where += "  (its pane is gone: no pane has its key; stui worker adopt it again where it runs"
			if c, ok := r["conv"].(string); ok {
				where += ", its conversation " + c
			}
			where += ")"
		case r["noAgent"] == true:
			where += "  (no agent runs in its pane"
			if c, ok := r["conv"].(string); ok {
				where += "; its conversation " + c
			}
			where += ")"
		}
		status := fmt.Sprint(r["status"])
		if reason, _ := r["stopReason"].(string); status == "idle" && orch.StopWord(reason) != "" {
			// Why its turn ended, in the status column: a limit is not
			// worth reading.
			status = orch.StopWord(reason)
			if at, _ := r["resetsAt"].(int64); reason == orch.StopLimit {
				if when := orch.ResetsLine(at, time.Now()); when != "" {
					where += "  (resets " + when + ")"
				}
			}
		}
		d, _ := r["depth"].(int)
		id := strings.Repeat("  ", max(d-1, 0)) + fmt.Sprint(r["id"])
		fmt.Printf("  %-*v %-16v window %-3v %-9v %v%s\n", w, id, r["name"], r["window"], status, r["task"], where)
		for _, l := range briefLines(r) {
			fmt.Printf("  %*s %s\n", w, "", l)
		}
	}
}

// treeRows is rows in tree order, each with its "depth" (1 for this pane's
// own workers). A row is under the row whose id is its "_up". Without all,
// the tree starts at self, and a row out of it is kept only when its parent
// is gone (that is when it most needs finding); with all, a row whose
// parent is not listed starts a tree of its own. limit > 0 stops that many
// levels down.
func treeRows(rows []map[string]any, self string, all bool, limit int) []map[string]any {
	ids := map[string]bool{}
	kids := map[string][]map[string]any{}
	for _, r := range rows {
		ids[fmt.Sprint(r["id"])] = true
		up, _ := r["_up"].(string)
		kids[up] = append(kids[up], r)
	}
	var out []map[string]any
	done := map[string]bool{}
	var walk func(r map[string]any, depth int)
	walk = func(r map[string]any, depth int) {
		id := fmt.Sprint(r["id"])
		if done[id] {
			return
		}
		done[id] = true
		r["depth"] = depth
		out = append(out, r)
		if limit > 0 && depth >= limit {
			return
		}
		for _, k := range kids[id] {
			walk(k, depth+1)
		}
	}
	if all {
		for _, r := range rows {
			if up, _ := r["_up"].(string); !ids[up] {
				walk(r, 1)
			}
		}
	} else {
		for _, r := range kids[self] {
			walk(r, 1)
		}
		for _, r := range rows {
			if r["parentGone"] == true && !done[fmt.Sprint(r["id"])] {
				walk(r, 1)
			}
		}
	}
	return out
}

func workerStop(a args, rest []string, asJSON bool) {
	if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
		die(asJSON, map[string]any{}, "%s", "stui worker stop <id> --yes   ("+idForms+")")
	}
	here(asJSON)
	rest[0] = resolveWorker(rest[0], asJSON)
	if !a.has("--yes") {
		die(asJSON, map[string]any{}, "this closes %s and stops what runs in it: add --yes", rest[0])
	}
	if !strings.HasPrefix(rest[0], "%") {
		remoteStop(rest[0], asJSON)
		return
	}
	if paneOpt(rest[0], "@stui-role") != "worker" {
		die(asJSON, map[string]any{}, "%s is not a worker stui started", rest[0])
	}
	// Closing a pane is not undone: it must be one of this pane's workers,
	// not another run's that got the id after a restore (target.go).
	if why, now, mine := checkTarget(rest[0]); why != "" {
		die(asJSON, map[string]any{"id": rest[0], "reason": "not-your-worker"}, "not closed: %s", targetMessage(why, now, mine))
	}
	if paneOpt(rest[0], "@stui-adopted") == "1" {
		die(asJSON, map[string]any{}, "%s was running before it was adopted: stui worker release %s lets it go, and its pane stays", rest[0], rest[0])
	}
	stopEv := orch.Event{Run: workRun(rest[0]), Kind: orch.EvStop, From: os.Getenv("TMUX_PANE"), To: rest[0], ToName: paneOpt(rest[0], "@stui-name")}
	stopEv.FromName = paneName(stopEv.From)
	if _, err := tx("kill-pane", "-t", rest[0]); err != nil {
		die(asJSON, map[string]any{}, "%v", err)
	}
	logEvent(stopEv)
	if asJSON {
		printJSON(map[string]any{"ok": true, "id": rest[0]})
	} else {
		fmt.Printf("  closed %s\n", rest[0])
	}
}

// workerAdopt is stui worker adopt and release, through stui's API.
func workerAdopt(a args, verb string, rest []string, asJSON bool) {
	if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
		die(asJSON, map[string]any{}, "stui worker %s <id>   (%s)", verb, idForms)
	}
	pane, s := here(asJSON)
	rest[0] = resolveWorker(rest[0], asJSON)
	// Another machine's pane: by its slug (a name works too).
	if m, p, ok := splitRemote(rest[0]); ok {
		mm, err := resolveMachine(m)
		if err != nil {
			die(asJSON, map[string]any{}, "%s: %v", m, err)
		}
		rest[0] = mm.Slug + "/" + p
		if _, ok := a.flag("--owns"); ok {
			die(asJSON, map[string]any{}, "--owns is for workers on this machine")
		}
	}
	body := map[string]any{"pane": rest[0]}
	if verb == "adopt" {
		parent, ok := a.flag("--parent")
		if !ok {
			parent = pane
		} else {
			parent = resolveWorker(parent, asJSON)
		}
		body = map[string]any{"parent": parent, "child": rest[0]}
		// Its role: why it is brought in. Asked on a terminal, skipped with
		// Enter; a script without --role goes on without one.
		role, ok := a.flag("--role")
		if !ok && !asJSON {
			role = askRole(rest[0])
		}
		body["about"] = role
		if a.has("--no-brief") {
			body["noBrief"] = true
		}
	}
	// Who asked, for the run's history of its tree (orch/tree.go).
	body["by"] = pane
	var res map[string]any
	code, err := apiDo(s, "POST", "/api/orch/"+verb, body, &res)
	if err != nil || code != 200 {
		why := fmt.Sprint(res["error"])
		if code == 404 && res["error"] == nil {
			why = "this stui does not adopt yet: a stui server older than this binary is running; stui stop, then try again"
		}
		die(asJSON, map[string]any{"result": res}, "not done (%d %v): %s", code, err, why)
	}
	if asJSON {
		printJSON(res)
		return
	}
	if verb == "adopt" {
		printAdopted(rest[0], res)
		if o, ok := a.flag("--owns"); ok {
			setOwns(rest[0], orch.ParseOwns(o), "", asJSON)
		}
	} else {
		fmt.Printf("  %s is no one's worker now\n", rest[0])
	}
}

// workerOwn is stui worker own <id> <paths> | --clear: what a worker may change.
func workerOwn(a args, rest []string, asJSON bool) {
	if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
		die(asJSON, map[string]any{}, "%s", `stui worker own <id> "src/auth/**,docs/auth.md" [--root dir] | --clear   (`+idForms+`)`)
	}
	here(asJSON)
	rest[0] = resolveWorker(rest[0], asJSON)
	if !strings.HasPrefix(rest[0], "%") {
		die(asJSON, map[string]any{"id": rest[0]}, "%s is on another machine: what it may change is set there", rest[0])
	}
	if a.has("--clear") {
		tx("set-option", "-p", "-u", "-t", rest[0], orch.OptOwns)
		tx("set-option", "-p", "-u", "-t", rest[0], orch.OptRoot)
		if asJSON {
			printJSON(map[string]any{"ok": true, "id": rest[0], "owns": []string{}})
		} else {
			fmt.Printf("  %s may change anything again\n", rest[0])
		}
		return
	}
	var owns []string
	for _, x := range rest[1:] {
		if !strings.HasPrefix(x, "--") {
			owns = append(owns, orch.ParseOwns(x)...)
		}
	}
	if len(owns) == 0 {
		die(asJSON, map[string]any{}, "which paths? stui worker own %s \"src/auth/**\"", rest[0])
	}
	root, _ := a.flag("--root")
	setOwns(rest[0], owns, root, asJSON)
}

// setOwns puts a worker's owned paths on its pane, relative to root (its
// folder when empty), and says so.
func setOwns(pane string, owns []string, root string, asJSON bool) {
	if root == "" {
		root, _ = tx("display-message", "-p", "-t", pane, "#{pane_current_path}")
	}
	if err := setPaneOpts(pane, orch.OptOwns, strings.Join(owns, ","), orch.OptRoot, root); err != nil {
		die(asJSON, map[string]any{}, "%v", err)
	}
	if id := workRun(pane); id != "" {
		if r, err := orch.Load(id); err == nil {
			if w := r.Worker(pane); w != nil {
				w.Owns = owns
				r.Save()
			}
		}
	}
	if asJSON {
		printJSON(map[string]any{"ok": true, "id": pane, "owns": owns, "root": root})
	} else {
		fmt.Printf("  %s may change only: %s (in %s)\n", pane, strings.Join(owns, ", "), root)
	}
}

func skillsCommand(a args) {
	sub := a.after("skills")
	if len(sub) == 0 {
		fmt.Print(skillHere())
		return
	}
	if len(sub) < 2 || sub[0] != "get" {
		fmt.Println("  stui skills get " + strings.Join(orch.GuideNames(), " | ") + " | all")
		os.Exit(1)
	}
	if sub[1] == "all" {
		for i, name := range orch.GuideNames() {
			g, _ := orch.Guide(name)
			if i > 0 {
				fmt.Println()
			}
			fmt.Print(g)
		}
		return
	}
	g, err := orch.Guide(sub[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "  "+err.Error())
		os.Exit(1)
	}
	fmt.Print(g)
}

// skillHere starts at the common entrypoint and retains worker obligations.
func skillHere() string {
	g, _ := orch.Guide("common")
	if os.Getenv("STUI_RUN") != "" {
		worker, _ := orch.Guide("worker")
		g += "\n" + worker
	}
	return g
}

func pluginCommand(a args) {
	sub := a.after("plugin")
	if len(sub) == 0 {
		fmt.Println("  stui plugin install | status | uninstall")
		os.Exit(1)
	}
	claude, _ := exec.LookPath("claude")
	run := func(args ...string) (string, error) {
		cmd := exec.Command(claude, args...)
		proc.Hide(cmd)
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	switch sub[0] {
	case "install":
		root, err := orch.WritePlugin(version)
		if err != nil {
			fmt.Fprintln(os.Stderr, "  "+err.Error())
			os.Exit(1)
		}
		fmt.Printf("  plugin files: %s\n", root)
		d, err := orch.WriteCodexSkills()
		if err != nil {
			fmt.Fprintln(os.Stderr, "  Codex skills: "+err.Error())
			os.Exit(1)
		}
		fmt.Printf("  Codex skills: %s\n", d)
		if claude == "" {
			fmt.Printf("  claude is not on PATH: start Claude Code with --plugin-dir %s/stui, or run stui plugin install again later\n", root)
			return
		}
		if out, err := run("plugin", "marketplace", "add", root); err != nil && !strings.Contains(strings.ToLower(out), "already") {
			fmt.Fprintf(os.Stderr, "  marketplace: %s\n", out)
		}
		run("plugin", "marketplace", "update", "stui")
		if out, err := run("plugin", "install", "stui@stui", "--scope", "user"); err != nil && !strings.Contains(strings.ToLower(out), "already") {
			fmt.Fprintf(os.Stderr, "  install: %s\n", out)
			os.Exit(1)
		}
		if err := orch.UpdateClaudePlugin(run); err != nil {
			fmt.Fprintln(os.Stderr, "  Claude Code: "+err.Error())
			os.Exit(1)
		}
		fmt.Println("  installed in Claude Code (new sessions get /stui:orchestrate)")
	case "status":
		_, errP := os.Stat(orch.PluginRoot())
		_, errC := os.Stat(orch.CodexSkillsDir() + "/stui-orchestrate")
		fmt.Printf("  plugin files: %v\n  Codex skills: %v\n", errP == nil, errC == nil)
		if claude != "" {
			out, _ := run("plugin", "list")
			fmt.Printf("  Claude Code: %v\n", strings.Contains(out, "stui"))
		}
	case "uninstall":
		if claude != "" {
			run("plugin", "uninstall", "stui@stui")
			run("plugin", "marketplace", "remove", "stui")
		}
		orch.RemoveCodexSkills()
		os.RemoveAll(orch.PluginRoot())
		fmt.Println("  removed")
	default:
		fmt.Println("  stui plugin install | status | uninstall")
		os.Exit(1)
	}
}

// parentInRun is whether p's parent is a pane here in the run p works in:
// not gone, and not a pane that took its number after a restart (another
// run's, or none's).
func parentInRun(p orchPane, byPane map[string]orchPane) bool {
	if p.Parent == "" {
		return false
	}
	// Not listed: gone, or a pane in no run at all.
	q, ok := byPane[p.Parent]
	if !ok {
		return false
	}
	run := p.Run
	if p.InRun != "" {
		run = p.InRun
	}
	return q.Run == run || q.InRun == run || q.Run == p.Run
}

// recordOf is worker id of run as its record has it.
func recordOf(run, id string) *orch.Worker {
	r, err := orch.Load(run)
	if err != nil {
		return nil
	}
	return r.Worker(id)
}

// lostRows is the workers of run on this machine that its record keeps as
// live but no pane here has the key of, and no row is listed for: their
// pane went with a tmux server and nothing took its place. Listed, not
// left out, so a person can bring them back.
func lostRows(run string, anchors map[string]orch.PaneAnchor, seen map[string]bool) []map[string]any {
	r, err := orch.Load(run)
	if err != nil {
		return nil
	}
	keys := map[string]string{}
	for id, p := range anchors {
		if p.Key != "" {
			keys[p.Key] = id
		}
	}
	var rows []map[string]any
	for _, w := range r.Workers {
		if w.Machine != "" || !w.Live || w.Ended != 0 || seen[w.ID] {
			continue
		}
		if _, here := keys[w.Key]; here && w.Key != "" {
			continue
		}
		up := r.Coordinator
		if id, ok := keys[w.ParentKey]; ok && w.ParentKey != "" {
			up = id
		} else if id, ok := keys[r.CoordinatorKey]; ok && r.CoordinatorKey != "" {
			up = id
		}
		row := map[string]any{"id": w.ID, "name": w.Name, "window": w.Window, "status": "lost", "task": orch.FirstLine(w.Task), "run": r.ID, "parent": up, "_up": up, "lost": true}
		if w.Conv != "" {
			row["conv"] = w.Conv
		}
		if w.Key != "" {
			row["key"] = w.Key
		}
		seen[w.ID] = true
		rows = append(rows, row)
	}
	return rows
}

// paneAnchors is every pane's lasting key and runs (orch/anchor.go).
func paneAnchors() map[string]orch.PaneAnchor {
	out, err := tx("list-panes", "-a", "-F", orch.AnchorFormat)
	if err != nil {
		return nil
	}
	return orch.ParseAnchors(out)
}
