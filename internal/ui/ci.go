package ui

import (
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"
)

// ciMark is a pane's CI in a cell: the branch's latest runs in one word.
func ciMark(c *ciInfo) string {
	if c == nil {
		return ""
	}
	switch ciState(c.latest()) {
	case "running":
		return "● running"
	case "failed":
		return "✗ failed"
	case "passed":
		return "✓ passed"
	}
	return ""
}

func ciColor(state string) string {
	switch state {
	case "running":
		return cTeal
	case "failed":
		return cRed
	case "passed":
		return cGreen
	}
	return cGray
}

// ciView is the latest run of each workflow on the checked-out branch of
// every repository a pane is in, or of one pane's.
type ciView struct{ pane string }

func (ciView) title() string { return "CI" }
func (v ciView) scope() string {
	if v.pane != "" {
		return v.pane
	}
	return "GitHub Actions"
}
func (ciView) header() []string {
	return []string{"REPO", "BRANCH", "WORKFLOW", "STATE", "UPDATED", "TITLE", "PANES"}
}

// repos is each repository (root and branch) once, with the panes in it.
func (v ciView) repos(a *app) ([]*ciInfo, map[*ciInfo][]string) {
	byKey := map[string]*ciInfo{}
	panes := map[*ciInfo][]string{}
	var keys []string
	for id, c := range a.snap.ci {
		if v.pane != "" && id != v.pane {
			continue
		}
		key := c.Root + "\x00" + c.Branch
		if c.Root == "" {
			key = "\x00" + id // not a repository: its own row says why
		}
		if byKey[key] == nil {
			byKey[key] = c
			keys = append(keys, key)
		}
		panes[byKey[key]] = append(panes[byKey[key]], id)
	}
	sort.Strings(keys)
	out := make([]*ciInfo, 0, len(keys))
	for _, k := range keys {
		sort.Strings(panes[byKey[k]])
		out = append(out, byKey[k])
	}
	return out, panes
}

func (v ciView) rows(a *app) []row {
	repos, panes := v.repos(a)
	var out []row
	for _, c := range repos {
		ps := strings.Join(panes[c], " ")
		if c.Root == "" && v.pane == "" {
			continue // a pane outside any repository is no CI to show
		}
		name := c.Repo
		if name == "" {
			name = c.Root
		}
		runs := c.latest()
		if len(runs) == 0 {
			why := c.err
			if why == "" {
				why = c.Detail
			}
			if why == "" {
				why = map[string]string{"ok": "no runs on this branch", "not-git": "not in a git checkout", "not-github": "no GitHub remote", "no-gh": "gh is not installed", "no-auth": "gh is not logged in (gh auth login)"}[c.State]
			}
			if why == "" {
				why = c.State
			}
			out = append(out, row{id: c.URL, color: cGray, cells: []string{name, c.Branch, "", "", "", why, ps}})
			continue
		}
		for _, r := range runs {
			st := ciState([]ciRun{r})
			label := st
			if label == "" {
				label = r.Conclusion
			}
			out = append(out, row{id: r.URL, color: ciColor(st), cells: []string{name, c.Branch, r.Workflow, label, since(r.UpdatedAt), r.Title, ps}})
		}
	}
	return out
}
func (ciView) hints() []hint { return []hint{{"enter/o", "open in browser"}} }
func (ciView) key(a *app, k string, r *row) bool {
	if k != "enter" && k != "o" {
		return false
	}
	if r == nil || r.id == "" {
		a.say("no run to open", true)
		return true
	}
	a.open(r.id)
	return true
}

// since is how long ago an RFC 3339 time was.
func since(t string) string {
	at, err := time.Parse(time.RFC3339, t)
	if err != nil {
		return ""
	}
	return ago(at.UnixMilli())
}

// openCI opens the run that matters most for a pane: a failed one, else a
// running one, else the newest.
func (a *app) openCI(pane string) {
	c := a.snap.ci[pane]
	if c == nil {
		a.say("no CI read for "+pane+" yet", true)
		return
	}
	runs := c.latest()
	if len(runs) == 0 {
		if c.URL != "" {
			a.open(c.URL)
			return
		}
		a.say("no runs for "+pane, true)
		return
	}
	for _, want := range []string{"failed", "running"} {
		for _, r := range runs {
			if ciState([]ciRun{r}) == want {
				a.open(r.URL)
				return
			}
		}
	}
	a.open(runs[0].URL)
}

// open shows a GitHub page in the browser: open on macOS, xdg-open elsewhere.
func (a *app) open(u string) {
	if !strings.HasPrefix(u, "https://") {
		a.say("not a web address: "+u, true)
		return
	}
	cmd := "xdg-open"
	if runtime.GOOS == "darwin" {
		cmd = "open"
	}
	if err := exec.Command(cmd, u).Start(); err != nil {
		a.say(cmd+": "+err.Error(), true)
		return
	}
	a.say("opened "+u, false)
}
