package orch

// A role and a brief for an adopted worker.
//
// The role is one line the one adopting writes: why it was brought in. The
// brief is the adopted agent's own account of what it has done and is doing,
// asked of it once its turn has ended, so that asking interrupts nothing: a
// message typed while an agent works lands in its turn (Codex steers with
// Enter). Both are pane options, like every other relation, and copied into
// the run's record for Rebind.
//
// The brief has keys of its own, apart from @stui-summary: everything that
// reads @stui-summary reads it as a finished worker's report (stui worker read
// prints it whatever @stui-done says, stui worker done overwrites it).

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sophonz-labs/stui/internal/proc"
)

// The options.
const (
	OptAbout      = "@stui-about"
	OptBrief      = "@stui-brief"
	OptBriefState = "@stui-brief-state"
	OptBriefAt    = "@stui-brief-at"
	// When stui typed the request, so a restarted stui does not ask twice.
	OptBriefSent = "@stui-brief-sent"
)

// Brief states.
const (
	BriefAsked    = "asked"
	BriefAnswered = "answered"
	BriefSkipped  = "skipped"
)

const (
	maxAbout      = 120
	maxBrief      = 600
	maxBriefLines = 5
)

// cut is s in at most n characters, the last an ellipsis when it was cut.
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

// packSafe takes out what agent watch packs pane options with ("|^|").
func packSafe(s string) string { return strings.ReplaceAll(s, "|^|", "| ^ |") }

// CleanAbout is a role as it is kept: one line, at most 120 characters.
func CleanAbout(s string) string {
	return cut(packSafe(strings.Join(strings.Fields(s), " ")), maxAbout)
}

// CleanBrief is a brief as it is kept: at most five lines and 600
// characters, each newline as the two characters `\n` (a pane option is one
// line).
func CleanBrief(s string) string {
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(s, `\n`, "\n"), "\n") {
		if l = strings.Join(strings.Fields(l), " "); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > maxBriefLines {
		lines = lines[:maxBriefLines]
	}
	return strings.ReplaceAll(cut(packSafe(strings.Join(lines, "\n")), maxBrief), "\n", `\n`)
}

// BriefText is a kept brief with its newlines back.
func BriefText(kept string) string { return strings.ReplaceAll(kept, `\n`, "\n") }

// BriefRequest is what stui types to an adopted agent once its turn ends.
func BriefRequest(lead bool) string {
	team := ""
	if lead {
		team = " Your team's work goes in it too."
	}
	return "[stui] Not a new task. Before you go on, leave what you have done so far and what you are doing now, in five lines or fewer." + team + " Run: stui worker brief --summary \"<the five lines>\". Then carry on with what you were doing."
}

// briefOptions are the options adopting sets for a role and a brief.
func briefOptions(about string, ask bool) (set []string, unset []string) {
	state := BriefSkipped
	if ask {
		state = BriefAsked
	}
	set = []string{OptBriefState, state, OptBriefAt, strconv.FormatInt(time.Now().Unix(), 10)}
	unset = []string{OptBrief, OptBriefSent}
	if about = CleanAbout(about); about != "" {
		set = append(set, OptAbout, about)
	} else {
		unset = append(unset, OptAbout)
	}
	return set, unset
}

// briefKeys are every option of a role and a brief, for letting a worker go.
var briefKeys = []string{OptAbout, OptBrief, OptBriefState, OptBriefAt, OptBriefSent, "@stui-tell-later"}

// OptTellLater holds the message telling an adopted agent it is a worker,
// when it was working at the time: typed with the brief request once its
// turn ends, not into the middle of it.
const OptTellLater = "@stui-tell-later"

// briefFormat is what the request loop lists of each pane.
const briefFormat = "#{pane_id}\t#{" + OptBriefState + "}\t#{" + OptBriefSent + "}\t#{@stui-in-run}\t#{" + OptTellLater + "}"

// briefMoment is whether a pane's agent is between turns: done or idle,
// with no question up. Working and waiting (a question) are not.
func briefMoment(state string) bool { return state == "done" || state == "idle" }

// AskBriefs types what waits for an adopted agent's turn to end -- the
// message saying it is a worker, held while it worked, and the brief
// request -- to each whose turn has ended. It runs every few seconds
// (StartBriefs); what waits lives in the pane's options, so a restarted stui
// picks it up.
func (a *Adopter) AskBriefs(ctx context.Context) {
	if a.o.Tmux == nil || a.o.Deliver == nil || a.o.State == nil {
		return
	}
	out, err := a.o.Tmux(ctx, "list-panes", "-a", "-F", briefFormat)
	if err != nil {
		return
	}
	seen := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.SplitN(l, "\t", 5)
		if len(f) < 5 || !paneID.MatchString(f[0]) || seen[f[0]] {
			continue
		}
		seen[f[0]] = true
		ask := f[1] == BriefAsked && f[2] == ""
		held := strings.TrimSpace(f[4])
		if !ask && held == "" {
			continue
		}
		if state, _ := a.o.State(f[0]); !briefMoment(state) {
			continue
		}
		var parts []string
		if held != "" {
			parts = append(parts, strings.TrimSuffix(held, " Nothing to do now."))
		}
		if ask {
			parts = append(parts, BriefRequest(f[3] != ""))
		}
		code, _ := a.o.Deliver(ctx, f[0], strings.Join(parts, " "))
		if code != 200 {
			continue
		}
		if held != "" {
			a.unset(ctx, f[0], OptTellLater)
		}
		if ask {
			a.set(ctx, f[0], OptBriefSent, strconv.FormatInt(time.Now().Unix(), 10))
		}
	}
}

// tellChild types the message to an adopted agent now when it is between
// turns, and otherwise holds it for AskBriefs: "ok", why not, or "at turn
// end".
func (a *Adopter) tellChild(ctx context.Context, child, msg string) string {
	if a.o.State != nil {
		if state, _ := a.o.State(child); state != "" && !briefMoment(state) {
			if a.set(ctx, child, OptTellLater, msg) == nil {
				return TurnEnd
			}
		}
	}
	return a.tellBoth(ctx, child, msg, "", "")[child]
}

// TurnEnd is what Told says of a message held until that agent's turn ends.
const TurnEnd = "at turn end"

// StartBriefs runs AskBriefs until ctx ends.
func (a *Adopter) StartBriefs(ctx context.Context) {
	go func() {
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				c, cancel := context.WithTimeout(ctx, 60*time.Second)
				a.AskBriefs(c)
				cancel()
			}
		}
	}()
}

// facts fills in what stui knows of res.Child at once: the workers it
// directs, its state, the last thing it was asked, where it works, and its
// role and brief state from its options. ps is the panes after adopting.
func (a *Adopter) facts(ctx context.Context, ps map[string]pane, res *AdoptResult) {
	child := res.Child
	for id, p := range ps {
		if p.Parent == child && id != child {
			res.Members = append(res.Members, id)
		}
	}
	if r := ps[child].Run; r != "" {
		if run, err := Load(r); err == nil && run.Coordinator == child {
			for _, w := range run.Workers {
				if w.Machine != "" {
					res.Members = append(res.Members, w.ID)
				}
			}
		}
	}
	sort.Strings(res.Members)
	if a.o.State != nil {
		res.State, res.Since = a.o.State(child)
	}
	if a.o.LastAsked != nil {
		res.LastAsked = a.o.LastAsked(child)
	}
	opt := func(name string) string {
		v, _ := a.o.Tmux(ctx, "display-message", "-p", "-t", child, "#{"+name+"}")
		return strings.TrimSpace(v)
	}
	res.Cwd = opt("pane_current_path")
	res.About = opt(OptAbout)
	res.BriefState = opt(OptBriefState)
	if res.Cwd != "" {
		res.Branch = gitBranch(ctx, res.Cwd)
	}
}

// gitBranch is the branch checked out in dir, "" when it is not a git
// work tree or git does not answer at once.
func gitBranch(ctx context.Context, dir string) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "branch", "--show-current")
	proc.Hide(cmd)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// SetAbout sets (or with "" takes off) the role of worker id: a pane here,
// or "<machine>/%<pane>" whose machine keeps it. The run's record keeps a
// copy.
func (a *Adopter) SetAbout(ctx context.Context, id, about string) (string, error) {
	about = CleanAbout(about)
	ev := Event{Kind: EvAbout, To: id, Now: about}
	var ps map[string]pane
	if slug, pane, ok := remoteChild(id); ok {
		if err := a.remoteCall(ctx, slug, "POST", "/api/orch/workers/"+url.PathEscape(pane)+"/about", map[string]any{"about": about}, nil); err != nil {
			return "", err
		}
	} else {
		if !paneID.MatchString(id) {
			return "", refuse(400, "bad-pane", "bad pane id")
		}
		ps, err := a.panes(ctx)
		if err != nil {
			return "", refuse(502, "tmux", "tmux: %v", err)
		}
		p, ok := ps[id]
		if !ok || p.Role != RoleWorkerName {
			return "", refuse(409, "not-worker", "%s is no one's worker", id)
		}
		ev.Run, ev.ToName, ev.ToKey, ev.Was = workRunOf(p), p.Name, a.tmuxOpt(ctx, id, OptKey), a.tmuxOpt(ctx, id, OptAbout)
		if about == "" {
			a.unset(ctx, id, OptAbout)
		} else if err := a.set(ctx, id, OptAbout, about); err != nil {
			return "", refuse(502, "tmux", "%v", err)
		}
	}
	for _, r := range Runs() {
		if w := r.Worker(id); w != nil {
			if ev.Run == "" {
				ev.Run, ev.ToName, ev.ToKey, ev.Was, ev.Machine = r.ID, w.Name, w.Key, w.About, w.Machine
			}
			w.About = about
			r.Save()
		}
	}
	if ev.Was != about {
		if ps == nil {
			ps, _ = a.panes(ctx)
		}
		a.treeEvent(ctx, ps, ev)
	}
	return about, nil
}

// serveAbout answers POST /api/orch/about {"pane","about"}: what an app
// sends when a role is typed after a card was dropped.
func (a *Adopter) serveAbout(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
		return
	}
	var body struct {
		Pane  string `json:"pane"`
		About string `json:"about"`
		By    string `json:"by"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body) != nil {
		writeJSON(w, 400, map[string]any{"error": "bad body"})
		return
	}
	about, err := a.SetAbout(WithBy(r.Context(), body.By), body.Pane, body.About)
	var ae *AdoptError
	switch {
	case errors.As(err, &ae):
		writeJSON(w, ae.Status, map[string]any{"error": ae.Msg, "reason": ae.Reason})
	case err != nil:
		writeJSON(w, 502, map[string]any{"error": err.Error()})
	default:
		writeJSON(w, 200, map[string]any{"ok": true, "pane": body.Pane, "about": about})
	}
}
