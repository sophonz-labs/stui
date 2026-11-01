package orch

// TreeMember is one agent of a subtree: its name, its pane, and the machine it
// is on when that is another one ("" here).
type TreeMember struct {
	Name    string
	Pane    string
	Machine string
}

// Subtree is pane and every worker below it, depth first: the workers of
// the run it coordinates (the newest, as RunOf), and in the run it is a
// worker of, the ones whose parent it is. A team lead's own run is a run it
// coordinates, so a team comes with it. A worker on another machine is a
// member, and nothing is read below it. key is the pane's lasting key, ""
// when it has none.
func Subtree(pane, key string) []TreeMember {
	runs := Runs()
	name := ""
	if RunOf(pane, key) != nil {
		name = RoleCoordinatorName
	}
	if r, w := workerOf(runs, pane, key); r != nil {
		name = w.Name
	}
	out := []TreeMember{{Name: name, Pane: pane}}
	seen := map[string]bool{pane: true}
	var walk func(pane, key string, depth int)
	walk = func(pane, key string, depth int) {
		if depth > MaxChain {
			return
		}
		var kids []Worker
		if r := RunOf(pane, key); r != nil {
			for _, w := range r.Workers {
				if w.ParentKey == "" {
					kids = append(kids, w)
				}
			}
		}
		if r, _ := workerOf(runs, pane, key); r != nil && key != "" {
			for _, w := range r.Workers {
				if w.ParentKey == key {
					kids = append(kids, w)
				}
			}
		}
		for _, w := range kids {
			if w.ID == "" || seen[w.ID] {
				continue
			}
			seen[w.ID] = true
			out = append(out, TreeMember{Name: w.Name, Pane: w.ID, Machine: w.Machine})
			if w.Machine == "" {
				walk(w.ID, w.Key, depth+1)
			}
		}
	}
	walk(pane, key, 1)
	return out
}

// workerOf is the newest run pane is a worker of, by its key or, for a
// record without one, its pane id; and its record there.
func workerOf(runs []*Run, pane, key string) (*Run, *Worker) {
	for _, r := range runs {
		for i := range r.Workers {
			w := &r.Workers[i]
			if w.Machine != "" {
				continue
			}
			if (key != "" && w.Key == key) || (w.Key == "" && w.ID == pane) {
				return r, w
			}
		}
	}
	return nil, nil
}
