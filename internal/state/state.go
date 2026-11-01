// Package state is where the CLI keeps track of the agent it started.
//
// stui returns to the prompt and leaves the agent running, so every later
// command has to find it again. A file is enough: one agent per machine.
package state

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/sophonz-labs/stui/internal/home"
	"github.com/sophonz-labs/stui/internal/proc"
)

type Agent struct {
	PID       int    `json:"pid"`
	Port      int    `json:"port"`
	Token     string `json:"token"`
	Session   string `json:"session"`
	StartedAt int64  `json:"startedAt"`
	// The stui version the agent runs, so stui upgrade can tell whether it is
	// already on the one just installed. Absent in a record from before it
	// was kept, which is older than anything that asks.
	Version string `json:"version,omitempty"`
}

// TokenHeader is how stui authenticates: it has the token and wants no redirects.
const TokenHeader = "x-stui-token"

func file() string { return filepath.Join(home.Dir(), "agent.json") }

func Read() *Agent {
	b, err := os.ReadFile(file())
	if err != nil {
		return nil
	}
	var a Agent
	if json.Unmarshal(b, &a) != nil {
		return nil
	}
	return &a
}

func Write(a *Agent) error {
	if err := os.MkdirAll(filepath.Dir(file()), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(a, "", "  ")
	return os.WriteFile(file(), b, 0o644)
}

func Clear() { _ = os.Remove(file()) }

// Running is the agent as it actually is, not as the file remembers it.
//
// A stale file is the normal case after a reboot or a kill, so the check is
// whether the process is there *and* answering -- a recycled pid would pass
// the first test alone.
func Running() *Agent {
	a := Read()
	if a == nil {
		return nil
	}
	if !proc.Alive(a.PID) {
		Clear()
		return nil
	}
	if !Answers(a.Port, a.Token, 3*time.Second) {
		Clear()
		return nil
	}
	return a
}

// Answers is true when an agent on port accepts token.
func Answers(port int, token string, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/api/version", port), nil)
	req.Header.Set(TokenHeader, token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode == 200
}
