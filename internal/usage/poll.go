package usage

// Asking the providers themselves, so the limits are current without an
// agent turn: the status line and the session files only say something
// when Claude or Codex answers, and a quiet machine showed numbers hours
// old. Every five minutes or so stui reads the login each CLI
// already keeps on this machine and asks the same endpoint that CLI's own
// /usage asks. The token goes to its own provider and nowhere else, is
// never written anywhere by stui, and is never refreshed by stui: expired
// is skipped, and the last reading stands until the CLI signs in again.
//
// Off with `stui usage poll off`. The status line and session files are
// still read (usage.go), and the newest reading of either kind wins.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sophonz-labs/stui/internal/proc"
)

// What Claude Code's /usage asks (the claude binary carries the path and
// the beta header), and what codex's status screen asks.
const (
	claudeUsageURL = "https://api.anthropic.com/api/oauth/usage"
	codexUsageURL  = "https://chatgpt.com/backend-api/wham/usage"
	claudeBeta     = "oauth-2025-04-20"
)

// PollEvery is the ordinary interval, with PollJitter either way.
const (
	PollEvery  = 5 * time.Minute
	PollJitter = 30 * time.Second
	pollMax    = time.Hour
)

// ---- the switch

type pollPrefs struct {
	Off bool `json:"off,omitempty"`
}

func pollPrefsPath(e Env) string { return filepath.Join(e.Dir(), "poll.json") }

// PollOff is whether `stui usage poll off` was said.
func PollOff(e Env) bool {
	b, err := os.ReadFile(pollPrefsPath(e))
	if err != nil {
		return false
	}
	var p pollPrefs
	return json.Unmarshal(b, &p) == nil && p.Off
}

// SetPoll turns polling on or off for good.
func SetPoll(e Env, on bool) error {
	if on {
		err := os.Remove(pollPrefsPath(e))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(e.Dir(), 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(pollPrefs{Off: true})
	return writeFile(pollPrefsPath(e), b)
}

// ---- what a poll kept

// pollRecord is ~/.stui/usage/poll-<agent>[-<key>].json.
type pollRecord struct {
	Agent      string       `json:"agent"`
	ConfigDir  string       `json:"configDir,omitempty"`
	ObservedAt int64        `json:"observedAt"`
	Plan       string       `json:"plan,omitempty"`
	Windows    []Window     `json:"windows"`
	Account    *AccountInfo `json:"account,omitempty"`
}

func pollFile(e Env, agent, configDir string) string {
	name := "poll-" + agent
	if agent == "claude" {
		name += "-" + strings.TrimPrefix(strings.TrimSuffix(filepath.Base(claudeFile(e.StateDir, configDir)), ".json"), "claude-")
	}
	return filepath.Join(e.Dir(), name+".json")
}

func readPoll(path string) (pollRecord, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return pollRecord{}, false
	}
	var r pollRecord
	if json.Unmarshal(b, &r) != nil || len(r.Windows) == 0 {
		return pollRecord{}, false
	}
	return r, true
}

// polledClaude is every Claude poll record, keyed by config directory.
func polledClaude(e Env) map[string]pollRecord {
	out := map[string]pollRecord{}
	files, _ := filepath.Glob(filepath.Join(e.Dir(), "poll-claude-*.json"))
	sort.Strings(files)
	for _, f := range files {
		if r, ok := readPoll(f); ok {
			out[r.ConfigDir] = r
		}
	}
	return out
}

// Newer says whether a poll record is the fresher word than a reading
// observed at the given time (0: none). Equal goes to the other reading:
// what the agent itself said is as good, and already shown.
func (r pollRecord) newer(observedAt int64) bool { return r.ObservedAt > observedAt }

// ---- the logins

// claudeLogin is Claude Code's OAuth access token, from the macOS keychain
// item Claude Code keeps ("Claude Code-credentials") or, elsewhere,
// .credentials.json in its config directory. Only the token and its expiry
// are read; nothing is written back.
// Set when the keychain did not answer in time (it was asking the person):
// then the file is the only place looked for the rest of the run.
var keychainGaveUp atomic.Bool

type claudeLogin struct {
	token     string
	expiresAt int64 // unix ms; 0 unknown
}

func (e Env) claudeLogin() (claudeLogin, bool) {
	var raw []byte
	if runtime.GOOS == "darwin" && !keychainGaveUp.Load() {
		// A few seconds: the keychain answers at once or is asking the
		// person something, and it is not asked again by this process.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "security", "find-generic-password", "-s", "Claude Code-credentials", "-w")
		proc.Hide(cmd)
		cmd.Stderr = nil
		if out, err := cmd.Output(); err == nil {
			raw = out
		} else if ctx.Err() != nil {
			keychainGaveUp.Store(true)
		}
	}
	if len(raw) == 0 {
		b, err := os.ReadFile(filepath.Join(e.ClaudeDir, ".credentials.json"))
		if err != nil {
			return claudeLogin{}, false
		}
		raw = b
	}
	return parseClaudeLogin(raw)
}

func parseClaudeLogin(raw []byte) (claudeLogin, bool) {
	var f struct {
		OAuth *struct {
			AccessToken string `json:"accessToken"`
			ExpiresAt   int64  `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &f) != nil || f.OAuth == nil || f.OAuth.AccessToken == "" {
		return claudeLogin{}, false
	}
	return claudeLogin{token: f.OAuth.AccessToken, expiresAt: f.OAuth.ExpiresAt}, true
}

// codexLogin is codex's ChatGPT access token and account id from auth.json.
// An API-key login has neither, and no plan limits to ask about.
type codexLogin struct {
	token     string
	accountID string
	expiresAt int64 // from the JWT's exp; 0 unknown
}

func (e Env) codexLogin() (codexLogin, bool) {
	b, err := os.ReadFile(filepath.Join(e.CodexDir, "auth.json"))
	if err != nil {
		return codexLogin{}, false
	}
	return parseCodexLogin(b)
}

func parseCodexLogin(raw []byte) (codexLogin, bool) {
	var f struct {
		Tokens *struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(raw, &f) != nil || f.Tokens == nil || f.Tokens.AccessToken == "" {
		return codexLogin{}, false
	}
	l := codexLogin{token: f.Tokens.AccessToken, accountID: f.Tokens.AccountID}
	if parts := strings.Split(l.token, "."); len(parts) == 3 {
		if p, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "=")); err == nil {
			var claims struct {
				Exp  float64 `json:"exp"`
				Auth struct {
					AccountID string `json:"chatgpt_account_id"`
				} `json:"https://api.openai.com/auth"`
			}
			if json.Unmarshal(p, &claims) == nil {
				if claims.Exp > 0 {
					l.expiresAt = int64(claims.Exp * 1000)
				}
				if l.accountID == "" {
					l.accountID = claims.Auth.AccountID
				}
			}
		}
	}
	if l.accountID == "" {
		return codexLogin{}, false
	}
	return l, true
}

// expired is whether a token's expiry has passed, or is about to: a minute
// is enough for the request to be answered.
func expired(expiresAt int64, now time.Time) bool {
	return expiresAt > 0 && now.Add(time.Minute).UnixMilli() >= expiresAt
}

// ---- the answers

// ParseClaudeUsage reads /api/oauth/usage: five_hour, seven_day and the
// per-model weekly windows, each a utilization (0-100) and an RFC 3339
// resets_at -- the same keys the status line's rate_limits carries, so the
// same reader. Dollar fields and the experiments beside them are left.
func ParseClaudeUsage(body []byte) []Window {
	body = []byte(strings.TrimSpace(string(body)))
	if len(body) == 0 || body[0] != '{' {
		return nil
	}
	return ParseStatusline(append(append([]byte(`{"rate_limits":`), body...), '}'))
}

// ParseCodexUsage reads /backend-api/wham/usage: the plan and
// rate_limit.primary_window / secondary_window, each a used_percent,
// limit_window_seconds, reset_at (unix seconds) and reset_after_seconds.
func ParseCodexUsage(body []byte, now time.Time) ([]Window, string) {
	var in struct {
		PlanType  string `json:"plan_type"`
		RateLimit *struct {
			Primary   *codexPollWindow `json:"primary_window"`
			Secondary *codexPollWindow `json:"secondary_window"`
		} `json:"rate_limit"`
	}
	if json.Unmarshal(body, &in) != nil || in.RateLimit == nil {
		return nil, codexPlan(in.PlanType)
	}
	var out []Window
	for _, w := range []struct {
		id string
		w  *codexPollWindow
	}{{"primary", in.RateLimit.Primary}, {"secondary", in.RateLimit.Secondary}} {
		if w.w == nil || w.w.UsedPercent == nil {
			continue
		}
		c := codexWindow{UsedPercent: w.w.UsedPercent, ResetsAt: w.w.ResetAt, ResetsInSeconds: w.w.ResetAfter}
		if w.w.WindowSeconds != nil {
			m := *w.w.WindowSeconds / 60
			c.WindowMinutes = &m
		}
		if !usable(&c) {
			continue
		}
		out = append(out, codexWindowOut(w.id, &c, now))
	}
	return out, codexPlan(in.PlanType)
}

type codexPollWindow struct {
	UsedPercent   *float64 `json:"used_percent"`
	WindowSeconds *float64 `json:"limit_window_seconds"`
	ResetAt       *float64 `json:"reset_at"`
	ResetAfter    *float64 `json:"reset_after_seconds"`
}

// ---- the poller

// Poller asks each provider on its own clock.
type Poller struct {
	Env Env
	// "stui/0.1.0".
	UserAgent string
	Client    *http.Client
	// Told what each round did; nil says nothing.
	Logf func(format string, a ...any)
}

// errRateLimited carries the provider's Retry-After.
type errRateLimited struct{ after time.Duration }

func (e errRateLimited) Error() string { return "rate limited" }

// Run polls until ctx ends: every PollEvery (jittered) when the last round
// went well, doubling up to pollMax after a failure, and at least what a
// 429 asked. The first round is a few seconds in, so a machine that just
// started says something soon.
func (p *Poller) Run(ctx context.Context) {
	if p.Client == nil {
		p.Client = &http.Client{Timeout: 20 * time.Second}
	}
	wait := 5*time.Second + time.Duration(rand.Int64N(int64(10*time.Second)))
	backoff := PollEvery
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if PollOff(p.Env) {
			wait = PollEvery
			continue
		}
		err := p.Once(ctx)
		switch {
		case err == nil:
			backoff = PollEvery
			wait = PollEvery + time.Duration(rand.Int64N(int64(2*PollJitter))) - PollJitter
		default:
			backoff = min(backoff*2, pollMax)
			wait = backoff
			var rl errRateLimited
			if errors.As(err, &rl) && rl.after > wait {
				wait = min(rl.after, pollMax)
			}
			if p.Logf != nil {
				p.Logf("usage poll: %v; next in %s", err, wait.Round(time.Second))
			}
		}
	}
}

// Once asks every provider with a login here and keeps what each said. The
// error is the first provider's that failed; the others are still kept.
func (p *Poller) Once(ctx context.Context) error {
	if p.Client == nil {
		p.Client = &http.Client{Timeout: 20 * time.Second}
	}
	now := p.Env.now()
	var first error
	keep := func(err error) {
		if err != nil && first == nil {
			first = err
		}
	}
	if l, ok := p.Env.claudeLogin(); ok && !expired(l.expiresAt, now) {
		ws, err := p.fetchClaude(ctx, l.token)
		keep(err)
		if err == nil && len(ws) > 0 {
			configDir := os.Getenv("CLAUDE_CONFIG_DIR")
			rec := pollRecord{Agent: "claude", ConfigDir: configDir, ObservedAt: now.UnixMilli(), Windows: ws, Account: ClaudeAccount(p.Env.Home, configDir)}
			if rec.Account != nil {
				rec.Plan = rec.Account.Plan
			}
			keep(writePoll(pollFile(p.Env, "claude", configDir), rec))
		}
	}
	if l, ok := p.Env.codexLogin(); ok && !expired(l.expiresAt, now) {
		ws, plan, err := p.fetchCodex(ctx, l.token, l.accountID)
		keep(err)
		if err == nil && len(ws) > 0 {
			rec := pollRecord{Agent: "codex", ObservedAt: now.UnixMilli(), Windows: ws, Plan: plan, Account: CodexAccount(p.Env.CodexDir)}
			if rec.Plan == "" && rec.Account != nil {
				rec.Plan = rec.Account.Plan
			}
			keep(writePoll(pollFile(p.Env, "codex", ""), rec))
		}
	}
	return first
}

func writePoll(path string, rec pollRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(rec)
	return writeFile(path, b)
}

func (p *Poller) get(ctx context.Context, url string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if p.UserAgent != "" {
		req.Header.Set("User-Agent", p.UserAgent)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := p.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	switch {
	case res.StatusCode == 429:
		after := 15 * time.Minute
		if s, err := strconv.Atoi(strings.TrimSpace(res.Header.Get("Retry-After"))); err == nil && s > 0 {
			after = time.Duration(s) * time.Second
		}
		return nil, errRateLimited{after: after}
	case res.StatusCode != 200:
		// Never the body: a provider's error page is not stui's to log.
		return nil, errors.New(url + ": " + res.Status)
	}
	return body, nil
}

func (p *Poller) fetchClaude(ctx context.Context, token string) ([]Window, error) {
	body, err := p.get(ctx, claudeUsageURL, map[string]string{"Authorization": "Bearer " + token, "anthropic-beta": claudeBeta})
	if err != nil {
		return nil, err
	}
	ws := ParseClaudeUsage(body)
	if len(ws) == 0 {
		return nil, errors.New("claude: no windows in the answer")
	}
	return ws, nil
}

func (p *Poller) fetchCodex(ctx context.Context, token, accountID string) ([]Window, string, error) {
	body, err := p.get(ctx, codexUsageURL, map[string]string{"Authorization": "Bearer " + token, "ChatGPT-Account-Id": accountID})
	if err != nil {
		return nil, "", err
	}
	ws, plan := ParseCodexUsage(body, p.Env.now())
	if len(ws) == 0 {
		return nil, plan, errors.New("codex: no windows in the answer")
	}
	return ws, plan, nil
}
