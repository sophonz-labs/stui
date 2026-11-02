package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/sophonz-labs/stui/internal/proc"
)

// st is the program stui was cut from. When it runs on this machine its
// server answers on 127.0.0.1 like stui's, with the same /api/agents, so
// stui shows st's runs and agents beside its own -- read-only: a change to
// one of st's runs belongs to st ui, which knows its coordinator.

// stTokenHeader is how st's server takes its token.
const stTokenHeader = "x-sophonz-token"

// stRefusal is what a key that would change one of st's runs says.
const stRefusal = "this is st's run: use st ui"

var errReadOnly = errors.New("read-only")

// stHome is st's state directory: $STUI_ST_HOME, $SOPHONZ_HOME (st's own
// override), or ~/.sophonz.
func stHome() string {
	for _, k := range []string{"STUI_ST_HOME", "SOPHONZ_HOME"} {
		if d := os.Getenv(k); d != "" {
			return d
		}
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".sophonz")
}

// stClient is st's server when its agent.json names a live process, or nil.
// It only ever asks 127.0.0.1, only GETs (do refuses the rest), and gives
// up after a second, so a hung st costs stui one second, not its screen.
// The token is read here each time and kept nowhere else.
func stClient() *client {
	b, err := os.ReadFile(filepath.Join(stHome(), "agent.json"))
	if err != nil {
		return nil
	}
	var s struct {
		PID   int    `json:"pid"`
		Port  int    `json:"port"`
		Token string `json:"token"`
	}
	if json.Unmarshal(b, &s) != nil || s.Port < 1 || s.Port > 65535 || s.Token == "" || !proc.Alive(s.PID) {
		return nil
	}
	return &client{base: fmt.Sprintf("http://127.0.0.1:%d", s.Port), token: s.Token, header: stTokenHeader,
		readOnly: true, hc: &http.Client{Timeout: time.Second}}
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
