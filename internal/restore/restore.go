package restore

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sophonz-labs/stui/internal/tmuxlayout"
)

// What happens to one session of a snapshot.
const (
	Create = "create" // not there: made from the snapshot
	Fill   = "fill"   // there, but only the empty shell a reconnecting client just made: the windows go into it
	Skip   = "skip"   // there, and somebody's: left alone
	Group  = "group"  // a member of another session's group, made to share its windows again
)

type Step struct {
	Session string `json:"session"`
	Action  string `json:"action"`
	Windows int    `json:"windows"`
	Panes   int    `json:"panes"`
	Agents  int    `json:"agents"`
	// Why it could not be made; such a session was not restored.
	Err string `json:"err,omitempty"`
}

type Options struct {
	// Enter an agent's resume line, not only type it, when its conversation
	// is still on record (Decide).
	ResumeAgents bool
	// Whether an agent's conversation is on record; nil: HasRecord.
	HasRecord func(a *Agent) bool
	// When the server was last seen alive, for the note in each pane.
	StoppedAt time.Time
	Now       time.Time
	// Each agent's conversation by its pane's lasting key, from the
	// orchestration runs' records: what fills in an id the hooks lost
	// (WithKeys). nil: only what the snapshot says.
	Keys map[string]Agent
	// Where a pane whose directory is gone starts instead.
	Home   string
	DryRun bool
}

type Result struct {
	Steps   []Step `json:"steps"`
	Windows int    `json:"windows"`
	Panes   int    `json:"panes"`
	Agents  int    `json:"agents"`
	// How the restored (or, dry, the to-be-restored) panes came out.
	Resume Counts `json:"resume"`
}

// Restored is the names of the sessions that were made or filled.
func (r Result) Restored() []string {
	var out []string
	for _, s := range r.Steps {
		if s.Action != Skip && s.Err == "" {
			out = append(out, s.Session)
		}
	}
	return out
}

// Failed is the names of the sessions that should have been made and were
// not.
func (r Result) Failed() []string {
	var out []string
	for _, s := range r.Steps {
		if s.Err != "" {
			out = append(out, s.Session)
		}
	}
	return out
}

func (r Result) Skipped() []string {
	var out []string
	for _, s := range r.Steps {
		if s.Action == Skip {
			out = append(out, s.Session)
		}
	}
	return out
}

type liveSession struct {
	windows int
	created int64
	// The one pane of a one-window session, when there is only one.
	onlyCmd string
	// Its window was made by a restore.
	restored bool
}

// pristineFor is how long after it was made a lone empty shell still counts
// as one a client made on reconnecting, rather than one somebody is using.
const pristineFor = 15 * time.Minute

func live(ctx context.Context, t Tmux) (map[string]liveSession, Server) {
	out := map[string]liveSession{}
	srv, ok := ServerOf(ctx, t)
	if !ok {
		return out, srv
	}
	s, err := t.Run(ctx, "list-sessions", "-F", "#{session_name}"+sep+"#{session_windows}"+sep+"#{session_created}")
	if err != nil {
		return out, srv
	}
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		f := strings.Split(line, sep)
		if len(f) != 3 {
			continue
		}
		w, _ := strconv.Atoi(f[1])
		c, _ := strconv.ParseInt(f[2], 10, 64)
		ls := liveSession{windows: w, created: c}
		if w == 1 {
			if p, err := t.Run(ctx, "list-panes", "-s", "-t", "="+f[0], "-F", "#{pane_current_command}"+sep+"#{@stui_restored}"); err == nil {
				if rows := strings.Split(strings.TrimSpace(p), "\n"); len(rows) == 1 {
					cmd, mark, _ := strings.Cut(rows[0], sep)
					ls.onlyCmd, ls.restored = cmd, mark != ""
				}
			}
		}
		out[f[0]] = ls
	}
	return out, srv
}

// Plan says what Apply would do with snap against the server as it is.
func Plan(ctx context.Context, t Tmux, snap *Snapshot, now time.Time) []Step {
	have, srv := live(ctx, t)
	var steps []Step
	for _, se := range ordered(snap) {
		st := Step{Session: se.Name}
		for _, w := range se.Windows {
			st.Windows++
			st.Panes += len(w.Panes)
			for _, p := range w.Panes {
				if p.Agent != nil {
					st.Agents++
				}
			}
		}
		l, there := have[se.Name]
		switch {
		case !there && se.GroupOf != "":
			st.Action = Group
		case !there:
			st.Action = Create
		case se.GroupOf == "" && l.windows == 1 && IsShell(l.onlyCmd) && !l.restored &&
			l.created >= srv.Start && now.Unix()-l.created < int64(pristineFor.Seconds()) && !snap.Server.Same(srv):
			st.Action = Fill
		default:
			st.Action = Skip
		}
		steps = append(steps, st)
	}
	return steps
}

// ordered puts every group's windows-carrying session before its members.
func ordered(snap *Snapshot) []Session {
	out := append([]Session{}, snap.Sessions...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].GroupOf == "" && out[j].GroupOf != "" })
	return out
}

// Apply sets snap up again on the server t reaches -- starting one if none
// is running. Sessions already there are left alone, except the lone empty
// shell a reconnecting client made, which the windows are added to. Nothing
// is killed, and nothing a pane was running is started again.
func Apply(ctx context.Context, t Tmux, snap *Snapshot, o Options) (Result, error) {
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	if o.Home == "" {
		o.Home, _ = os.UserHomeDir()
	}
	snap = snap.WithKeys(o.Keys, o.HasRecord)
	steps := Plan(ctx, t, snap, o.Now)
	res := Result{Steps: steps}
	if o.DryRun {
		made := map[string]bool{}
		for _, s := range steps {
			if s.Action != Skip {
				res.Windows += s.Windows
				res.Panes += s.Panes
				res.Agents += s.Agents
				if s.Action != Group {
					made[s.Session] = true
				}
			}
		}
		res.Resume = CountPanes(snap, made, o)
		return res, nil
	}
	sessions := map[string]Session{}
	for _, se := range snap.Sessions {
		sessions[se.Name] = se
	}
	stamp := o.Now.Format(time.RFC3339)
	var firstErr error
	var typing []typed
	for i := range steps {
		step := &steps[i]
		se := sessions[step.Session]
		var err error
		switch step.Action {
		case Create:
			err = createSession(ctx, t, se, o, stamp, &typing)
		case Fill:
			err = fillSession(ctx, t, se, o, stamp, &typing)
		case Group:
			_, err = t.Run(ctx, "new-session", "-d", "-s", se.Name, "-t", "="+se.GroupOf)
		}
		if err != nil {
			step.Err = err.Error()
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", se.Name, err)
			}
			continue
		}
		if step.Action == Create || step.Action == Fill {
			res.Windows += step.Windows
			res.Panes += step.Panes
			res.Agents += step.Agents
		}
	}
	res.Steps = steps
	made := map[string]bool{}
	for _, s := range steps {
		if (s.Action == Create || s.Action == Fill) && s.Err == "" {
			made[s.Session] = true
		}
	}
	res.Resume = CountPanes(snap, made, o)
	typeWhenReady(ctx, t, typing)
	return res, firstErr
}

func createSession(ctx context.Context, t Tmux, se Session, o Options, stamp string, typing *[]typed) error {
	if len(se.Windows) == 0 {
		_, err := t.Run(ctx, "new-session", "-d", "-s", se.Name)
		return err
	}
	var activeWin string
	for i, w := range se.Windows {
		var id string
		var err error
		if i == 0 {
			id, err = firstWindow(ctx, t, se.Name, w, o, typing)
		} else {
			id, err = addWindow(ctx, t, se.Name, w, o, typing)
		}
		if err != nil {
			return err
		}
		finishWindow(ctx, t, id, w, stamp)
		if w.Active {
			activeWin = id
		}
	}
	if activeWin != "" {
		t.Run(ctx, "select-window", "-t", activeWin)
	}
	return nil
}

func fillSession(ctx context.Context, t Tmux, se Session, o Options, stamp string, typing *[]typed) error {
	var activeWin string
	for _, w := range se.Windows {
		id, err := addWindow(ctx, t, se.Name, w, o, typing)
		if err != nil {
			return err
		}
		finishWindow(ctx, t, id, w, stamp)
		if w.Active {
			activeWin = id
		}
	}
	if activeWin != "" {
		t.Run(ctx, "select-window", "-t", activeWin)
	}
	return nil
}

// layoutSize is the window size a layout was made at, in either of tmux's
// forms (internal/tmuxlayout).
func layoutSize(layout string) (w, h int) { return tmuxlayout.Size(layout) }

// firstWindow makes the session with the snapshot's first window, at the
// size its layout was made for, so the layout fits. "@id|%pane".
func firstWindow(ctx context.Context, t Tmux, session string, w Window, o Options, typing *[]typed) (string, error) {
	p0 := w.Panes[0]
	cwd, note := dir(p0.Cwd, o.Home)
	args := []string{"new-session", "-d", "-s", session, "-c", cwd, "-P", "-F", "#{window_id}" + sep + "#{pane_id}"}
	if w.Name != "" {
		args = append(args, "-n", w.Name)
	}
	if x, y := layoutSize(w.Layout); x > 0 && y > 0 {
		args = append(args, "-x", strconv.Itoa(x), "-y", strconv.Itoa(y))
	}
	args = append(args, paneCommand(p0, note, o))
	out, err := t.Run(ctx, args...)
	if err != nil {
		return "", err
	}
	return panesOf(ctx, t, w, strings.TrimSpace(out), o, typing)
}

func addWindow(ctx context.Context, t Tmux, session string, w Window, o Options, typing *[]typed) (string, error) {
	p0 := w.Panes[0]
	cwd, note := dir(p0.Cwd, o.Home)
	args := []string{"new-window", "-d", "-t", "=" + session + ":", "-c", cwd, "-P", "-F", "#{window_id}" + sep + "#{pane_id}"}
	if w.Name != "" {
		args = append(args, "-n", w.Name)
	}
	args = append(args, paneCommand(p0, note, o))
	out, err := t.Run(ctx, args...)
	if err != nil {
		return "", err
	}
	return panesOf(ctx, t, w, strings.TrimSpace(out), o, typing)
}

// panesOf makes the window's other panes, lines up each agent's resume line
// to be typed once its shell is at a prompt, and returns the window id.
func panesOf(ctx context.Context, t Tmux, w Window, made string, o Options, typing *[]typed) (string, error) {
	winID, firstPane, _ := strings.Cut(made, sep)
	ids := []string{firstPane}
	for _, p := range w.Panes[1:] {
		cwd, note := dir(p.Cwd, o.Home)
		out, err := t.Run(ctx, "split-window", "-d", "-t", winID, "-c", cwd, "-P", "-F", "#{pane_id}", paneCommand(p, note, o))
		if err != nil {
			// A window too small for another split: the panes so far stand.
			break
		}
		ids = append(ids, strings.TrimSpace(out))
		// Evenly while splitting, so the next split has room.
		t.Run(ctx, "select-layout", "-t", winID, "tiled")
	}
	if w.Layout != "" && len(ids) == len(w.Panes) {
		t.Run(ctx, "select-layout", "-t", winID, w.Layout)
	}
	agent := ""
	for i, id := range ids {
		p := w.Panes[i]
		if p.Key != "" {
			t.Run(ctx, "set-option", "-p", "-t", id, "@stui-key", p.Key)
		}
		if d := Decide(p, o); d.Line != "" {
			*typing = append(*typing, typed{pane: id, line: d.Line, enter: d.Action == Run})
			t.Run(ctx, "set-option", "-p", "-t", id, paneAgent, p.Agent.Kind)
			if agent == "" {
				agent = p.Agent.Kind
			}
		}
		if p.Active {
			t.Run(ctx, "select-pane", "-t", id)
		}
	}
	// Which agent's resume line waits in this window, for the window list
	// (`restoredAgent`): the phone says so on the window it belongs to.
	if agent != "" {
		t.Run(ctx, "set-option", "-w", "-t", winID, "@stui_restored_agent", agent)
	}
	return winID, nil
}

func finishWindow(ctx context.Context, t Tmux, id string, w Window, stamp string) {
	// Marked, for the web and the phone to say "restored", until it has been
	// dealt with (Settle).
	t.Run(ctx, "set-option", "-w", "-t", id, "@stui_restored", stamp)
}

// dir is cwd when it still exists, else home and a note saying so.
func dir(cwd, home string) (string, string) {
	if cwd == "" {
		return home, ""
	}
	if fi, err := os.Stat(cwd); err == nil && fi.IsDir() {
		return cwd, ""
	}
	return home, fmt.Sprintf("(%s is gone; this pane starts in %s)", cwd, home)
}

// resumeWith is the resume line with the options the agent was started with
// that it keeps (resumeargs.go), and the ones it left out.
func resumeWith(a *Agent, args string) (string, []string) {
	line := resumeLine(a)
	if line == "" {
		return "", nil
	}
	kept, left := resumeOptions(a.Kind, args)
	if len(kept) > 0 {
		line += " " + strings.Join(kept, " ")
	}
	return line, left
}

// ResumeLine is what continues an agent's conversation in its directory.
func resumeLine(a *Agent) string {
	if a == nil {
		return ""
	}
	switch a.Kind {
	case "claude":
		if a.ID != "" {
			return "claude --resume " + a.ID
		}
		return "claude --continue"
	case "codex":
		if a.ID != "" {
			return "codex resume " + a.ID
		}
		return "codex resume --last"
	}
	return ""
}

// ResumeLine is exported for the CLI's dry run.
func ResumeLine(a *Agent) string { return resumeLine(a) }

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// paneCommand is what a restored pane runs: a few lines saying what it was,
// then the user's own login shell. Only a note -- nothing it names is run.
func paneCommand(p Pane, note string, o Options) string {
	var lines []string
	when := ""
	if !o.StoppedAt.IsZero() {
		when = " — tmux stopped around " + o.StoppedAt.Local().Format("15:04")
	}
	lines = append(lines, "restored by stui"+when)
	if note != "" {
		lines = append(lines, note)
	}
	d := Decide(p, o)
	switch {
	case d.Action == Run:
		lines = append(lines, p.Agent.Kind+" was running here; resuming it: "+d.Line)
	case d.Action == TypeOnly:
		lines = append(lines, p.Agent.Kind+" was running here; its resume line is typed below — Enter runs it ("+d.Why+")")
	case p.Agent != nil:
		lines = append(lines, p.Agent.Kind+" was running here")
	case p.Args != "":
		lines = append(lines, "was running: "+p.Args+"  (not started again)")
	case p.Command != "" && !IsShell(p.Command):
		lines = append(lines, "was running: "+p.Command+"  (not started again)")
	}
	var b strings.Builder
	if p.Scrollback != "" {
		b.WriteString("cat -- " + shq(p.Scrollback) + " 2>/dev/null; ")
	}
	b.WriteString(`printf '\033[2m%s\033[0m\n'`)
	for _, l := range lines {
		b.WriteString(" " + shq(l))
	}
	b.WriteString(`; exec "${SHELL:-/bin/sh}" -l`)
	return b.String()
}
