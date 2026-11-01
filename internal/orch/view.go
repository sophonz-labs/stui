package orch

// Who works for whom, read from the run records first and the pane options
// second.
//
// A pane option is a copy: tmux drops every one but @stui-key when its server
// dies, and psmux before 3.4.0 holds none. The record is what stui keeps.
// The pane's lasting key (@stui-key) is what joins the two: a pane with a key
// that a record has is what that record says, and its options only fill
// what the record leaves empty (a record from an older stui). A pane with no
// key, or a key no record has, is what its options say (an older stui that
// wrote options only). The record wins where the two differ: Rebind has not
// caught up with it yet.
//
// Nothing reads through View yet; the readers move to it one by one.

import (
	"context"
	"strconv"
	"strings"
)

// ViewMember is one coordinator or worker as View sees it.
type ViewMember struct {
	// Its pane here ("%7"), its id on another machine ("ci-mac/%7"), or ""
	// when its record is live but no pane has its key (Lost).
	Pane string
	Key  string
	// The run it works in, and its own when it leads a team taken in as one
	// worker.
	Run, Team string
	Role      string // RoleCoordinatorName or RoleWorkerName
	// Its parent's pane now: "" for a coordinator, and for a worker whose
	// parent is not found (ParentGone). On another machine for a worker
	// here of a coordinator there.
	Parent     string
	ParentGone bool
	Name       string
	Adopted    bool
	Done       bool
	Summary    string
	Task       string
	About      string
	Brief      string
	BriefState string
	BriefAt    int64
	Owns       []string
	Root       string
	// The machine of a worker on another machine.
	Machine string
	// Its record is live but its pane is gone.
	Lost bool
	// Where it was read from: "record" or "options".
	From string
	// Its pane's place, "<session>:<window name>", and window index.
	Place  string
	Window int
}

// ViewFormat is the list-panes format ReadView reads. The summary is last:
// it is the one free text that is not kept on one line by stui.
const ViewFormat = "#{pane_id}\t#{" + OptKey + "}\t#{@stui-run}\t#{" + OptInRun + "}\t#{@stui-role}\t#{@stui-parent}\t#{@stui-name}\t#{@stui-adopted}\t#{@stui-done}\t#{@stui-task}\t#{" + OptAbout + "}\t#{" + OptOwns + "}\t#{" + OptRoot + "}\t#{session_name}\t#{window_name}\t#{window_index}\t#{@stui-summary}"

type viewPane struct {
	id, key, run, inRun, role, parent, name, adopted, done, task, about, owns, root, place, summary string
	window                                                                                          int
}

func parseViewPanes(out string) []viewPane {
	var ps []viewPane
	seen := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.SplitN(l, "\t", 17)
		if len(f) < 17 || !paneID.MatchString(f[0]) || seen[f[0]] {
			continue
		}
		seen[f[0]] = true
		w, _ := strconv.Atoi(f[15])
		place := ""
		if f[13] != "" {
			place = f[13] + ":" + f[14]
		}
		ps = append(ps, viewPane{id: f[0], key: f[1], run: f[2], inRun: f[3], role: f[4], parent: f[5], name: f[6], adopted: f[7], done: f[8],
			task: f[9], about: f[10], owns: f[11], root: f[12], place: place, window: w, summary: f[16]})
	}
	return ps
}

// View is everyone stui knows of here: list-panes -a -F ViewFormat, and the
// run records (newest first, as Runs gives them).
type View struct {
	Members []ViewMember
}

// Of is the member in pane id, or nil.
func (v *View) Of(id string) *ViewMember {
	for i := range v.Members {
		if v.Members[i].Pane == id {
			return &v.Members[i]
		}
	}
	return nil
}

// In is the members of run, coordinator first.
func (v *View) In(run string) []ViewMember {
	var out []ViewMember
	for _, m := range v.Members {
		if m.Run == run && m.Role == RoleCoordinatorName {
			out = append(out, m)
		}
	}
	for _, m := range v.Members {
		if m.Run == run && m.Role != RoleCoordinatorName {
			out = append(out, m)
		}
	}
	return out
}

// keyed is where a lasting key is in the records: run r's coordinator
// (worker -1) or its worker i.
type keyed struct {
	r      *Run
	worker int
}

// ReadView joins panes and records.
func ReadView(listOut string, runs []*Run) *View {
	ps := parseViewPanes(listOut)
	paneOfKey := map[string]string{}
	for _, p := range ps {
		if p.key != "" {
			paneOfKey[p.key] = p.id
		}
	}
	// Each key's record: the one where it is live, the newest of those;
	// else the newest.
	byKey := map[string]keyed{}
	live := func(k keyed) bool {
		if k.worker < 0 {
			return k.r.CoordinatorLive
		}
		return k.r.Workers[k.worker].Live
	}
	take := func(key string, k keyed) {
		if key == "" {
			return
		}
		if old, ok := byKey[key]; !ok || (!live(old) && live(k)) {
			byKey[key] = k
		}
	}
	for _, r := range runs {
		take(r.CoordinatorKey, keyed{r, -1})
		for i, w := range r.Workers {
			if w.Machine == "" {
				take(w.Key, keyed{r, i})
			}
		}
	}

	v := &View{}
	coordPane := func(r *Run) (string, bool) {
		if strings.Contains(r.Coordinator, "/") {
			return r.Coordinator, true
		}
		if r.CoordinatorKey != "" {
			p, ok := paneOfKey[r.CoordinatorKey]
			return p, ok
		}
		for _, p := range ps {
			if p.id == r.Coordinator && p.run == r.ID {
				return p.id, true
			}
		}
		return "", false
	}
	placed := map[string]bool{} // "run/key" of records a pane was found for
	for _, p := range ps {
		k, ok := byKey[p.key]
		if p.key == "" || !ok {
			if p.run == "" {
				continue
			}
			v.Members = append(v.Members, fromOptions(p))
			continue
		}
		m := ViewMember{Pane: p.id, Key: p.key, Run: k.r.ID, From: "record", Place: p.place, Window: p.window}
		if k.worker < 0 {
			m.Role = RoleCoordinatorName
		} else {
			w := k.r.Workers[k.worker]
			m.Role, m.Name, m.Adopted, m.Done, m.Summary, m.Task = RoleWorkerName, w.Name, w.Adopted, w.Done, w.Summary, w.Task
			m.About, m.Brief, m.BriefState, m.BriefAt, m.Owns, m.Team = w.About, w.Brief, w.BriefState, w.BriefAt, w.Owns, w.Team
			if w.Worktree != nil {
				m.Root = w.Worktree.Dir
			}
			switch {
			case w.ParentKey != "" && w.ParentKey != k.r.CoordinatorKey:
				m.Parent, ok = paneOfKey[w.ParentKey]
				m.ParentGone = !ok
			default:
				m.Parent, ok = coordPane(k.r)
				m.ParentGone = !ok
			}
		}
		fill(&m, p)
		placed[k.r.ID+"/"+p.key] = true
		v.Members = append(v.Members, m)
	}
	// Live in a record, with no pane: lost. Workers on other machines.
	for _, r := range runs {
		for _, w := range r.Workers {
			switch {
			case w.Machine != "":
				m := ViewMember{Pane: w.ID, Run: r.ID, Role: RoleWorkerName, Name: w.Name, Adopted: w.Adopted, Done: w.Done, Summary: w.Summary,
					Task: w.Task, About: w.About, Brief: w.Brief, BriefState: w.BriefState, BriefAt: w.BriefAt, Team: w.Team, Machine: w.Machine,
					Key: w.Key, From: "record", Window: w.Window}
				anchors := map[string]PaneAnchor{}
				for _, p := range ps {
					anchors[p.id] = PaneAnchor{Key: p.key, Run: p.run, InRun: p.inRun}
				}
				var found bool
				m.Parent, found = r.ParentHere(&w, anchors)
				m.ParentGone = !found
				v.Members = append(v.Members, m)
			case w.Live && w.Ended == 0 && w.Key != "" && !placed[r.ID+"/"+w.Key] && byKey[w.Key].r == r:
				if _, here := paneOfKey[w.Key]; here {
					continue
				}
				v.Members = append(v.Members, ViewMember{Run: r.ID, Role: RoleWorkerName, Name: w.Name, Key: w.Key, Lost: true, From: "record",
					Adopted: w.Adopted, Done: w.Done, Summary: w.Summary, Task: w.Task, About: w.About, Place: w.Place, Window: w.Window})
			}
		}
	}
	return v
}

// fill takes from the pane's options what the record leaves empty.
func fill(m *ViewMember, p viewPane) {
	or := func(s *string, o string) {
		if *s == "" {
			*s = o
		}
	}
	or(&m.Name, p.name)
	or(&m.Task, p.task)
	or(&m.About, p.about)
	or(&m.Summary, p.summary)
	or(&m.Root, p.root)
	if len(m.Owns) == 0 && p.owns != "" {
		m.Owns = strings.Split(p.owns, ",")
	}
	if !m.Adopted && p.adopted == "1" {
		m.Adopted = true
	}
}

// fromOptions is a pane as its options say, with no record to go by.
func fromOptions(p viewPane) ViewMember {
	m := ViewMember{Pane: p.id, Key: p.key, Run: p.run, Role: p.role, Parent: p.parent, Name: p.name, Adopted: p.adopted == "1",
		Done: p.done == "1", Summary: p.summary, Task: p.task, About: p.about, Root: p.root, From: "options", Place: p.place, Window: p.window}
	if p.inRun != "" {
		// A team's lead: it works in the run above; its own is its team's.
		m.Run, m.Team = p.inRun, p.run
	}
	if p.owns != "" {
		m.Owns = strings.Split(p.owns, ",")
	}
	if m.Role == RoleWorkerName && m.Parent == "" {
		m.ParentGone = true
	}
	return m
}

// View reads everyone stui knows of here now.
func (a *Adopter) View(ctx context.Context) (*View, error) {
	out, err := a.o.Tmux(ctx, "list-panes", "-a", "-F", ViewFormat)
	if err != nil {
		return nil, err
	}
	return ReadView(out, Runs()), nil
}
