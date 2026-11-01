package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/sophonz-labs/stui/internal/state"
)

// client talks to the stui share running on this machine, as stui ls does.
type client struct {
	base, token string
	hc          *http.Client
}

func newClient(s *state.Agent) *client {
	return &client{base: fmt.Sprintf("http://127.0.0.1:%d", s.Port), token: s.Token, hc: &http.Client{Timeout: 8 * time.Second}}
}

// do sends a request; a reply that is not 2xx is an error with the
// share's own words when it gave some.
func (c *client) do(method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	req.Header.Set(state.TokenHeader, c.token)
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	res, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return fmt.Errorf("%s", e.Error)
		}
		return fmt.Errorf("%s", res.Status)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (c *client) get(path string, out any) error { return c.do("GET", path, nil, out) }

type session struct {
	Name          string `json:"name"`
	Windows       int    `json:"windows"`
	Attached      int    `json:"attached"`
	Created       int64  `json:"created"`
	CurrentWindow string `json:"currentWindow"`
	Command       string `json:"command"`
	Agents        int    `json:"agents"`
}

type window struct {
	Index   int    `json:"index"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	Active  bool   `json:"active"`
	Panes   int    `json:"panes"`
	Command string `json:"command"`
	Repo    string `json:"repo"`
}

type pane struct {
	Window     int    `json:"window"`
	WindowID   string `json:"windowId"`
	WindowName string `json:"windowName"`
	Index      int    `json:"index"`
	ID         string `json:"id"`
	Active     bool   `json:"active"`
	Command    string `json:"command"`
}

type orchInfo struct {
	Run     string `json:"run"`
	Role    string `json:"role"`
	Parent  string `json:"parent"`
	Name    string `json:"name"`
	Done    bool   `json:"done"`
	Adopted bool   `json:"adopted"`
	Summary string `json:"summary"`
	Brief   string `json:"brief"`
}

type agentPane struct {
	ID         string `json:"id"`
	Session    string `json:"session"`
	Window     int    `json:"window"`
	WindowName string `json:"windowName"`
	Pane       int    `json:"pane"`
	Agent      string `json:"agent"`
	Command    string `json:"command"`
	State      string `json:"state"`
	Since      int64  `json:"since"`
	Turn       *struct {
		StartedAt int64 `json:"startedAt"`
	} `json:"turn"`
	Subagents struct {
		Running int `json:"running"`
	} `json:"subagents"`
	Title string    `json:"title"`
	Orch  *orchInfo `json:"orch"`
}

type agentSession struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	Working   int    `json:"working"`
	Waiting   int    `json:"waiting"`
	Delegated int    `json:"delegated"`
	Error     int    `json:"error"`
	Idle      int    `json:"idle"`
	Done      int    `json:"done"`
	TeamEnded int    `json:"teamEnded"`
}

type agents struct {
	Sessions []agentSession `json:"sessions"`
	Panes    []agentPane    `json:"panes"`
}

type usageWindow struct {
	ID          string  `json:"id"`
	Kind        string  `json:"kind"`
	Minutes     int     `json:"minutes"`
	UsedPercent float64 `json:"usedPercent"`
	ResetsAt    int64   `json:"resetsAt"`
}

type usageAgent struct {
	Agent   string        `json:"agent"`
	State   string        `json:"state"`
	Plan    string        `json:"plan"`
	Windows []usageWindow `json:"windows"`
	Account *struct {
		Label string `json:"label"`
	} `json:"account"`
}

type usage struct {
	Agents []usageAgent `json:"agents"`
	Guard  struct {
		On     bool `json:"on"`
		Agents []struct {
			Agent  string `json:"agent"`
			State  string `json:"state"`
			Reason string `json:"reason"`
		} `json:"agents"`
	} `json:"guard"`
}

// ciRun is a GitHub Actions run as /api/actions lists it.
type ciRun struct {
	ID         int64  `json:"id"`
	Workflow   string `json:"workflow"`
	Title      string `json:"title"`
	Branch     string `json:"branch"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	UpdatedAt  string `json:"updatedAt"`
	URL        string `json:"url"`
}

// ciInfo is a pane's repository and its latest runs on the checked-out
// branch, newest first.
type ciInfo struct {
	State  string  `json:"state"`
	Repo   string  `json:"repo"`
	Root   string  `json:"root"`
	Branch string  `json:"branch"`
	URL    string  `json:"url"`
	Detail string  `json:"detail"`
	Runs   []ciRun `json:"runs"`
	err    string
}

// latest is the newest run per workflow on the branch: what "is CI green"
// means for it.
func (c *ciInfo) latest() []ciRun {
	seen := map[string]bool{}
	var out []ciRun
	for _, r := range c.Runs {
		if (c.Branch != "" && r.Branch != c.Branch) || seen[r.Workflow] {
			continue
		}
		seen[r.Workflow] = true
		out = append(out, r)
	}
	return out
}

// ciState is one word for runs: running, failed, passed, or "".
func ciState(runs []ciRun) string {
	state := ""
	for _, r := range runs {
		switch {
		case r.Status != "completed":
			if state != "failed" {
				state = "running"
			}
		case r.Conclusion == "failure" || r.Conclusion == "timed_out" || r.Conclusion == "startup_failure":
			state = "failed"
		case r.Conclusion == "success" && state == "":
			state = "passed"
		}
	}
	return state
}

type version struct {
	Mux        string `json:"mux"`
	MuxVersion string `json:"muxVersion"`
	OS         string `json:"os"`
}

// snap is everything the screens show, read at one time.
type snap struct {
	at       time.Time
	version  version
	sessions []session
	windows  map[string][]window // by session
	panes    map[string][]pane   // by session
	agents   agents
	usage    usage
	ci       map[string]*ciInfo // by pane id
	err      error
}

// maxCIPanes caps how many panes a snapshot asks the runs of.
const maxCIPanes = 40

// fetch reads a snapshot. CI comes from gh and changes slowly: withCI says
// to read it this time, else it is carried over from prev.
func (c *client) fetch(prev *snap, withCI bool) *snap {
	s := &snap{at: time.Now(), windows: map[string][]window{}, panes: map[string][]pane{}, ci: map[string]*ciInfo{}}
	if prev != nil {
		s.ci = prev.ci
	}
	s.err = c.get("/api/sessions", &s.sessions)
	var wg sync.WaitGroup
	var mu sync.Mutex
	run := func(f func()) { wg.Add(1); go func() { defer wg.Done(); f() }() }
	run(func() { c.get("/api/version", &s.version) })
	run(func() { c.get("/api/agents", &s.agents) })
	run(func() { c.get("/api/usage", &s.usage) })
	for _, x := range s.sessions {
		q := "?session=" + url.QueryEscape(x.Name)
		run(func() {
			var ws []window
			var ps []pane
			c.get("/api/windows"+q, &ws)
			c.get("/api/panes"+q, &ps)
			mu.Lock()
			s.windows[x.Name], s.panes[x.Name] = ws, ps
			mu.Unlock()
		})
	}
	wg.Wait()
	if withCI {
		s.ci = c.fetchCI(s)
	}
	return s
}

// fetchCI reads the runs of every pane's repository, one request per pane
// (the server reads each repository from gh once every few seconds).
func (c *client) fetchCI(s *snap) map[string]*ciInfo {
	var ids []string
	for _, x := range s.sessions {
		for _, p := range s.panes[x.Name] {
			if len(ids) < maxCIPanes {
				ids = append(ids, p.ID)
			}
		}
	}
	out := map[string]*ciInfo{}
	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, 4)
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ci := &ciInfo{}
			if err := c.get("/api/actions?limit=20&pane="+url.QueryEscape(id), ci); err != nil {
				ci.err = err.Error()
			}
			mu.Lock()
			out[id] = ci
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

// agentOf is the agent in a pane, by its id, or nil.
func (s *snap) agentOf(id string) *agentPane {
	for i := range s.agents.Panes {
		if s.agents.Panes[i].ID == id {
			return &s.agents.Panes[i]
		}
	}
	return nil
}

func (s *snap) agentSession(name string) *agentSession {
	for i := range s.agents.Sessions {
		if s.agents.Sessions[i].Name == name {
			return &s.agents.Sessions[i]
		}
	}
	return nil
}
