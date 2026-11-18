package ui

import (
	"errors"
	"net/http"
	"time"

	"github.com/sophonz-labs/stui/internal/stlocal"
)

// st is the program stui was cut from. When it runs on this machine its
// server answers on 127.0.0.1 like stui's, with the same /api/agents, so
// stui shows st's runs and agents beside its own -- read-only: a change to
// one of st's runs belongs to st ui, which knows its coordinator.

// stTokenHeader is how st's server takes its token.
const stTokenHeader = stlocal.TokenHeader

// stRefusal is what a key that would change one of st's runs says.
const stRefusal = "this is st's run: use st ui"

var errReadOnly = errors.New("read-only")

// stClient is st's server when its agent.json names a live process, or nil.
// It only ever asks 127.0.0.1, only GETs (do refuses the rest), and gives
// up after a second, so a hung st costs stui one second, not its screen.
// The token is read here each time and kept nowhere else.
func stClient() *client {
	r := stlocal.Find()
	if r == nil {
		return nil
	}
	return &client{base: r.Base(), token: r.Token, header: stTokenHeader, readOnly: true, hc: &http.Client{Timeout: time.Second}}
}

// readST is st's agents, marked as st's, and its client; nil and nil when
// st does not run here or does not answer. Answering /api/agents is the
// liveness check: asking /api/version first would only double the wait.
func readST() (*client, []agentPane) {
	c := stClient()
	if c == nil {
		return nil, nil
	}
	var ag agents
	if c.get("/api/agents", &ag) != nil {
		return nil, nil
	}
	for i := range ag.Panes {
		ag.Panes[i].st = true
	}
	return c, ag.Panes
}

// mergeST puts st's agents among stui's. A pane both servers see -- the
// same tmux pane, so the same id in the same session -- is listed once, as
// st's record: st knows the runs its panes are in, and stui sees st's
// workers only as agents. The one exception keeps stui's record: a pane in
// one of stui's own runs that st sees as a plain agent.
func (s *snap) mergeST(st []agentPane) {
	at := map[string]int{}
	for i, p := range s.agents.Panes {
		at[p.Session+"\x00"+p.ID] = i
	}
	for _, p := range st {
		i, ok := at[p.Session+"\x00"+p.ID]
		switch {
		case !ok:
			s.agents.Panes = append(s.agents.Panes, p)
		case p.Orch == nil && s.agents.Panes[i].Orch != nil:
		default:
			s.agents.Panes[i] = p
		}
	}
}

// stTag marks st's in a column that names a run.
func stTag(p *agentPane) string {
	if p != nil && p.st && p.Orch != nil {
		return " (st)"
	}
	return ""
}

// String keeps the token out of anything that prints a client.
func (c *client) String() string { return c.base }

// ---- the account, through st

// The account's machines, as st's GET /api/machines lists them (copied from
// st's internal/ui/data.go: stui does not import st).
type remoteWindow struct {
	Name   string `json:"name"`
	Index  int    `json:"index"`
	Agents []struct {
		Agent string `json:"agent"`
		State string `json:"state"`
	} `json:"agents"`
}

type remoteSession struct {
	Name   string `json:"name"`
	Agents *struct {
		Working int `json:"working"`
		Waiting int `json:"waiting"`
		Idle    int `json:"idle"`
	} `json:"agents"`
	WindowList []remoteWindow `json:"windowList"`
}

type machine struct {
	Slug     string          `json:"slug"`
	Name     string          `json:"name"`
	Hostname string          `json:"hostname"`
	Online   bool            `json:"online"`
	Seen     int64           `json:"seen"`
	Sessions []remoteSession `json:"sessions"`
}

// stAccount is what st knows of the account: the other machines (nil when
// st is signed out) and the plan limits. stui has no login of its own.
type stAccount struct {
	at       time.Time // when the machines were read
	slug     string    // this machine's, so its row is skipped
	machines []machine
	usage    *usage // st's limits; nil when it gave none
}

// stMachinesEvery is how often the machines are read, as st ui does: they
// come from the control plane through st and change slowly.
const stMachinesEvery = 10 * time.Second

// readSTAccount reads st's limits every time and its machines every
// stMachinesEvery, else carried over from prev.
func readSTAccount(c *client, prev *stAccount) *stAccount {
	a := &stAccount{}
	if prev != nil {
		*a = *prev
	}
	a.usage = nil
	if u := new(usage); c.get("/api/usage", u) == nil && len(u.Agents) > 0 {
		a.usage = u
	}
	if prev != nil && time.Since(prev.at) < stMachinesEvery {
		return a
	}
	a.at = time.Now()
	var v struct {
		Slug     string `json:"slug"`
		Hostname string `json:"hostname"`
	}
	c.get("/api/version", &v)
	var ms []machine
	switch err := c.get("/api/machines", &ms); {
	case err == nil:
		a.machines = ms
	case err.Error() == "not signed in":
		a.machines = nil
	} // anything else (the control plane away): the last list stays
	a.slug = ownSlug(v.Slug, v.Hostname, a.slug, a.machines)
	return a
}

// ownSlug is this machine's slug: st's word, else the last one known, else
// the one machine with this hostname. st has no slug until it has announced
// itself to the account, and without it this machine would be listed as
// one of the others (st #467).
func ownSlug(slug, hostname, last string, ms []machine) string {
	if slug != "" {
		return slug
	}
	if last != "" {
		return last
	}
	found := ""
	for _, m := range ms {
		if hostname != "" && m.Hostname == hostname {
			if found != "" {
				return "" // two of that name: no guess
			}
			found = m.Slug
		}
	}
	return found
}
