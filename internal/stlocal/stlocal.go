// Package stlocal finds st (the program stui was cut from) when it runs on
// this machine: its record says where its server answers on 127.0.0.1 and
// with which token. stui only reads from it; nothing here needs st or an
// account to build or run.
package stlocal

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/sophonz-labs/stui/internal/proc"
)

// TokenHeader is how st's server takes its token.
const TokenHeader = "x-sophonz-token"

// Home is st's state directory: $STUI_ST_HOME, $SOPHONZ_HOME (st's own
// override), or ~/.sophonz.
func Home() string {
	for _, k := range []string{"STUI_ST_HOME", "SOPHONZ_HOME"} {
		if d := os.Getenv(k); d != "" {
			return d
		}
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".sophonz")
}

// Record is the part of st's agent.json stui reads.
type Record struct {
	PID   int    `json:"pid"`
	Port  int    `json:"port"`
	Token string `json:"token"`
}

// Base is the server's address.
func (r *Record) Base() string { return fmt.Sprintf("http://127.0.0.1:%d", r.Port) }

// Find is st's record when it names a live process, or nil.
func Find() *Record {
	b, err := os.ReadFile(filepath.Join(Home(), "agent.json"))
	if err != nil {
		return nil
	}
	var r Record
	if json.Unmarshal(b, &r) != nil || r.Port < 1 || r.Port > 65535 || r.Token == "" || !proc.Alive(r.PID) {
		return nil
	}
	return &r
}

// Answers is whether st's server takes the token (GET /api/version within
// a second).
func (r *Record) Answers() bool {
	req, _ := http.NewRequest("GET", r.Base()+"/api/version", nil)
	req.Header.Set(TokenHeader, r.Token)
	res, err := (&http.Client{Timeout: time.Second}).Do(req)
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode >= 200 && res.StatusCode < 300
}

// Live is whether st runs here and answers: its record, a live pid and the
// token taken.
func Live() bool {
	r := Find()
	return r != nil && r.Answers()
}
