package restore

// A restore waiting on a person.
//
// When stui starts and the newest snapshot is of a tmux server that is gone --
// the machine restarted, or tmux died while no stui was running -- that
// snapshot is the one worth having, and the minute after a start is when it
// is most easily lost: the agent made `default`, the keeper took a record of
// that one empty session every ten seconds, and five of those pushed the
// real one out. So until somebody restores it or says to start
// fresh, stui leaves the server alone: it makes no session, takes no record,
// and does not restore by itself either -- a person is asked.

import (
	"context"
	"errors"
	"sort"
	"time"
)

// RestoreWithin is how old a snapshot may be and still be restored, or
// asked about, by itself: after a long time away, sessions coming back
// unasked would be a surprise. `stui restore` still reaches it.
const RestoreWithin = 72 * time.Hour

// FindPending is the snapshot waiting to be restored or declined: the
// newest one, when it is of a server that is not the one running (or none
// is), it is recent, and no restore, close or decline is recorded for it.
// sure is false when tmux could not be told apart from no tmux; then
// nothing is pending, and nothing should be saved either.
func FindPending(ctx context.Context, t Tmux, st Store, now time.Time) (snap *Snapshot, sure bool) {
	last := st.Latest()
	if last == nil || len(last.Sessions) == 0 || now.Sub(time.UnixMilli(last.Taken)) > RestoreWithin {
		return nil, true
	}
	if m := st.Marker(); m != nil && m.From == last.ID {
		return nil, true
	}
	srv, state := Probe(ctx, t)
	switch {
	case state == Unknown:
		return nil, false
	case state == Known && last.Server.Same(srv):
		return nil, true
	}
	return last, true
}

// Decline records that the snapshot is not to be restored: the person
// chose to start fresh. It stays on disk (prune keeps the marker's), and
// `stui restore --from <id>` still reaches it.
func (st Store) Decline(id string, now time.Time) error {
	snap, err := st.Load(id)
	if err != nil {
		return err
	}
	if m := st.Marker(); m != nil && m.From == id && !m.Declined {
		return errors.New("snapshot " + id + " was restored already")
	}
	return st.SetMarker(&Marker{At: now.UnixMilli(), StoppedAt: snap.Taken, From: id, Declined: true, Shown: true})
}

// PendingInfo is what a client shows when it asks.
type PendingInfo struct {
	ID string `json:"id"`
	// When the snapshot was last confirmed (the server was alive then) and
	// first taken, unix ms.
	Taken    int64    `json:"taken"`
	First    int64    `json:"first"`
	Sessions []string `json:"sessions"`
	Windows  int      `json:"windows"`
	Panes    int      `json:"panes"`
	// Agents by kind, and of them how many a restore resumes (their
	// conversation is known and on record here) and how many only get their
	// resume line typed.
	Agents  map[string]int `json:"agents"`
	Resumes int            `json:"resumes"`
	Typed   int            `json:"typed"`
	// Orchestration runs with a coordinator or worker in it.
	Runs []string `json:"runs"`
}

// Describe is p's PendingInfo. byKey and runsOf are as for Apply's Keys and
// the runs a pane's key belongs to; either may be nil.
func Describe(p *Snapshot, o Options, runsOf map[string][]string) PendingInfo {
	s := p.WithKeys(o.Keys, o.HasRecord)
	info := PendingInfo{ID: s.ID, Taken: s.Taken, First: s.First, Agents: map[string]int{}, Runs: []string{}}
	runs := map[string]bool{}
	for _, se := range s.Sessions {
		info.Sessions = append(info.Sessions, se.Name)
		for _, w := range se.Windows {
			info.Windows++
			for _, pn := range w.Panes {
				info.Panes++
				if pn.Agent != nil {
					info.Agents[pn.Agent.Kind]++
				}
				for _, r := range runsOf[pn.Key] {
					runs[r] = true
				}
			}
		}
	}
	c := CountPanes(s, nil, o)
	info.Resumes, info.Typed = c.Resumed, c.Typed
	for r := range runs {
		info.Runs = append(info.Runs, r)
	}
	sort.Strings(info.Runs)
	return info
}

// WithKeys is s with each agent pane's conversation id filled in from
// byKey (a pane's lasting key -> its agent) where the hooks gave none: the
// records of orchestration runs keep every coordinator's and worker's
// conversation by key, and hooks lose theirs with the server (else a
// restore would read "resumed 0 agents"). An agent whose kind
// the record does not say gets the kind whose record of that conversation
// is on this machine. s itself is not changed.
func (s *Snapshot) WithKeys(byKey map[string]Agent, has func(*Agent) bool) *Snapshot {
	if len(byKey) == 0 {
		return s
	}
	if has == nil {
		has = HasRecord
	}
	c := *s
	c.Sessions = append([]Session{}, s.Sessions...)
	for i := range c.Sessions {
		ws := append([]Window{}, c.Sessions[i].Windows...)
		for j := range ws {
			ps := append([]Pane{}, ws[j].Panes...)
			for k := range ps {
				p := &ps[k]
				// An agent was running there: one the hooks or the command
				// named, or a program that is no shell (Claude Code under
				// node).
				running := p.Agent != nil || (p.Command != "" && !IsShell(p.Command))
				if p.Key == "" || !running || (p.Agent != nil && p.Agent.ID != "") {
					continue
				}
				known, ok := byKey[p.Key]
				if !ok || known.ID == "" {
					continue
				}
				kind := ""
				if p.Agent != nil {
					kind = p.Agent.Kind
				}
				if kind == "" {
					kind = known.Kind
				}
				if kind == "" {
					for _, k := range []string{"claude", "codex"} {
						if has(&Agent{Kind: k, ID: known.ID}) {
							kind = k
							break
						}
					}
				}
				if kind == "" {
					continue
				}
				p.Agent = &Agent{Kind: kind, ID: known.ID}
			}
			ws[j].Panes = ps
		}
		c.Sessions[i].Windows = ws
	}
	return &c
}
