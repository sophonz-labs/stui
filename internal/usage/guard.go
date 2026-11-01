package usage

// The guard: a loop that has burned too much of the
// weekly limit is stopped at the end of its turn, by the Stop hook stui
// already answers (hookclient/stop.go). The rule is usage-coach's (MIT,
// github.com/netwaif/usage-coach, coach.py guard_check): with left the
// share of the weekly window not used and timeLeft the share of its time
// still to run, stop when
//
//	left < timeLeft - m   (burning faster than the week allows)
//	or left < floor       (nearly out)
//
// Only the weekly window is judged, as upstream does. The 5-hour window is
// not: stui does not own the loop and cannot start it again when that window
// resets, so it is only named in the reason, for the person to know when to.
// No reading, or an old one, lets the loop go on (fail-open) -- and says so
// in stui usage guard status.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// GuardLimits are one agent's m and floor, shares of 1.
type GuardLimits struct {
	M     float64 `json:"m"`
	Floor float64 `json:"floor"`
}

// DefaultGuard is usage-coach's default for every provider.
var DefaultGuard = GuardLimits{M: 0.10, Floor: 0.10}

// GuardConfig is ~/.stui/usage/guard.json.
type GuardConfig struct {
	On     bool                   `json:"on"`
	Agents map[string]GuardLimits `json:"agents,omitempty"`
}

// A reading older than this is no reading: the poller asks every five
// minutes and backs off to an hour at most, and Claude's status line says
// something every turn.
const guardStale = time.Hour

func guardPath(e Env) string { return filepath.Join(e.Dir(), "guard.json") }

// ReadGuard is the guard's settings; off when there are none or they do not
// parse.
func ReadGuard(e Env) GuardConfig {
	var c GuardConfig
	if b, err := os.ReadFile(guardPath(e)); err == nil {
		json.Unmarshal(b, &c)
	}
	return c
}

// Limits are agent's m and floor: the file's, or the default for what it
// leaves out.
func (c GuardConfig) Limits(agent string) GuardLimits {
	l, ok := c.Agents[agent]
	if !ok {
		return DefaultGuard
	}
	if l.M < 0 || l.M > 1 {
		l.M = DefaultGuard.M
	}
	if l.Floor <= 0 || l.Floor > 1 {
		l.Floor = DefaultGuard.Floor
	}
	return l
}

// SetGuard turns the guard on or off, keeping the limits in the file.
func SetGuard(e Env, on bool) error {
	c := ReadGuard(e)
	c.On = on
	if err := os.MkdirAll(e.Dir(), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return writeFile(guardPath(e), append(b, '\n'))
}

// GuardPath is where the settings are, for stui usage guard status.
func GuardPath(e Env) string { return guardPath(e) }

// Verdict is the guard's word on one agent.
type Verdict struct {
	Agent   string
	Profile string
	// Stop: the loop is to stop, for Reason. NoData: there was nothing to
	// judge, for Reason, and the loop goes on.
	Stop   bool
	NoData bool
	Reason string
	// Left and TimeLeft, shares of 1, when there was a weekly window.
	Left, TimeLeft float64
	Limits         GuardLimits
}

// Judge is the guard's verdict on a, now.
func Judge(a Agent, l GuardLimits, now time.Time) Verdict {
	v := Verdict{Agent: a.Agent, Profile: a.Profile, Limits: l}
	nodata := func(why string) Verdict {
		v.NoData, v.Reason = true, why
		return v
	}
	if a.State != "ok" || a.ObservedAt == 0 {
		return nodata("no reading of its limits")
	}
	if age := now.Sub(time.UnixMilli(a.ObservedAt)); age > guardStale {
		return nodata(fmt.Sprintf("the last reading is %s old", age.Round(time.Minute)))
	}
	// The tightest weekly window, as upstream picks among windows of one
	// length (all models, or one model's).
	var w *Window
	for i := range a.Windows {
		x := &a.Windows[i]
		if x.Kind != "weekly" {
			continue
		}
		if w == nil || x.UsedPercent > w.UsedPercent {
			w = x
		}
	}
	if w == nil {
		return nodata("no weekly window")
	}
	if w.Reset {
		v.Left, v.TimeLeft = 1, 1
		return v
	}
	if w.ResetsAt == 0 || w.Minutes == 0 {
		return nodata("the weekly window has no reset time")
	}
	v.Left = (100 - w.UsedPercent) / 100
	v.TimeLeft = float64(time.UnixMilli(w.ResetsAt).Sub(now)) / float64(time.Duration(w.Minutes)*time.Minute)
	v.TimeLeft = max(0, min(1, v.TimeLeft))
	name := AgentName(a.Agent)
	if a.Profile != "" {
		name += " (" + a.Profile + ")"
	}
	resets := "resets " + time.UnixMilli(w.ResetsAt).Local().Format("Mon 15:04")
	switch {
	case v.Left < v.TimeLeft-l.M:
		v.Stop = true
		v.Reason = fmt.Sprintf("%s's weekly limit is going faster than the week: %.0f%% left with %.0f%% of the week to go (stops under %.0f%%; %s)",
			name, v.Left*100, v.TimeLeft*100, (v.TimeLeft-l.M)*100, resets)
	case v.Left < l.Floor:
		v.Stop = true
		v.Reason = fmt.Sprintf("%s's weekly limit is nearly out: %.0f%% left, under the floor of %.0f%% (%s)",
			name, v.Left*100, l.Floor*100, resets)
	}
	if v.Stop {
		for _, s := range a.Windows {
			if s.Kind == "session" && s.ResetsAt > 0 && !s.Reset {
				v.Reason += fmt.Sprintf("; the 5-hour window is %.0f%% used and resets %s", s.UsedPercent, time.UnixMilli(s.ResetsAt).Local().Format("15:04"))
				break
			}
		}
	}
	return v
}

// GuardFor is the verdict for the agent a hook came from: agent is the
// hook's kind, profile CLAUDE_CONFIG_DIR's last element ("" the default).
// An agent stui reads no limits for is no data.
func GuardFor(e Env, c GuardConfig, agent, profile string) Verdict {
	now := e.now()
	var list []Agent
	switch agent {
	case "claude":
		list = readClaude(e)
	case "codex":
		if a, ok := readCodex(e); ok {
			list = []Agent{a}
		}
	}
	markReset(list, now.UnixMilli())
	for _, a := range list {
		if agent != "claude" || a.Profile == profile {
			return Judge(a, c.Limits(agent), now)
		}
	}
	return Verdict{Agent: agent, Profile: profile, NoData: true, Reason: "no reading of its limits", Limits: c.Limits(agent)}
}

// GuardAgent is one agent's line of the guard: what stui usage guard status
// prints and GET /api/usage carries as guard.agents.
type GuardAgent struct {
	Agent   string `json:"agent"`
	Profile string `json:"profile,omitempty"`
	// go, stop or nodata.
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	// Shares of 1 of the weekly window not used and of its time to run;
	// absent with nodata.
	Left     *float64 `json:"left,omitempty"`
	TimeLeft *float64 `json:"timeLeft,omitempty"`
	M        float64  `json:"m"`
	Floor    float64  `json:"floor"`
	// Whether the agent stops for the guard at all: Codex's /goal does not
	// stop for a Stop hook, so Codex is judged, not guarded.
	Guarded bool `json:"guarded"`
}

// GuardReport is the guard as it stands: GET /api/usage's guard.
type GuardReport struct {
	On     bool         `json:"on"`
	Agents []GuardAgent `json:"agents"`
}

// GuardStatus is the guard's word on every agent in rep, now. The CLI and
// the API both ask this, so they cannot disagree.
func GuardStatus(c GuardConfig, rep Report, now time.Time) GuardReport {
	out := GuardReport{On: c.On, Agents: []GuardAgent{}}
	for _, a := range rep.Agents {
		l := c.Limits(a.Agent)
		v := Judge(a, l, now)
		g := GuardAgent{Agent: a.Agent, Profile: a.Profile, State: "go", Reason: v.Reason, M: l.M, Floor: l.Floor, Guarded: a.Agent == "claude"}
		switch {
		case v.NoData:
			g.State = "nodata"
		case v.Stop:
			g.State = "stop"
		}
		if !v.NoData {
			left, tl := v.Left, v.TimeLeft
			g.Left, g.TimeLeft = &left, &tl
		}
		out.Agents = append(out.Agents, g)
	}
	return out
}
