package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// Crossing is a window that went over one of Levels: the usage-high push.
type Crossing struct {
	Agent   string
	Account *AccountInfo
	Profile string
	Window  Window
	Level   int
}

// Alerts remembers which crossings were told, in ~/.stui/usage/alerts.json,
// so each window says each level once per reset, across restarts.
type Alerts struct {
	path string
	told map[string]int64 // key -> when it may be forgotten (unix ms)
}

func NewAlerts(e Env) *Alerts {
	a := &Alerts{path: filepath.Join(e.Dir(), "alerts.json"), told: map[string]int64{}}
	if b, err := os.ReadFile(a.path); err == nil {
		json.Unmarshal(b, &a.told)
	}
	return a
}

// alertKey names a level of one window's period. The reset time is rounded
// to ten minutes: one that moves by a few seconds between readings is still
// the same period.
func alertKey(agent, account, id string, resetsAt int64, level int) string {
	resetsAt = (resetsAt + 300_000) / 600_000
	return agent + "|" + account + "|" + id + "|" + strconv.FormatInt(resetsAt, 10) + "|" + strconv.Itoa(level)
}

// Check is the crossings in rep not told yet, the highest level only when a
// window went past two at once; it records them as told.
func (a *Alerts) Check(rep Report, now time.Time) []Crossing {
	var out []Crossing
	changed := false
	for _, ag := range rep.Agents {
		who := ag.Profile
		if ag.Account != nil {
			who = ag.Account.ID
		}
		for _, w := range ag.Windows {
			if w.Reset {
				continue
			}
			top := 0
			for _, l := range Levels {
				if w.UsedPercent >= float64(l) {
					top = l
				}
			}
			if top == 0 {
				continue
			}
			k := alertKey(ag.Agent, who, w.ID, w.ResetsAt, top)
			if _, done := a.told[k]; done {
				continue
			}
			// The lower levels go with it: 96 % after nothing is one push,
			// not an 80 % one followed by a 95 % one.
			// Forgotten a day after the reset; a window with no reset time
			// a week from now, and may then say its level again.
			until := w.ResetsAt
			if until == 0 {
				until = now.Add(7 * 24 * time.Hour).UnixMilli()
			}
			for _, l := range Levels {
				if l <= top {
					a.told[alertKey(ag.Agent, who, w.ID, w.ResetsAt, l)] = until
				}
			}
			changed = true
			out = append(out, Crossing{Agent: ag.Agent, Account: ag.Account, Profile: ag.Profile, Window: w, Level: top})
		}
	}
	for k, until := range a.told {
		if now.Sub(time.UnixMilli(until)) > 24*time.Hour {
			delete(a.told, k)
			changed = true
		}
	}
	if changed {
		os.MkdirAll(filepath.Dir(a.path), 0o700)
		b, _ := json.Marshal(a.told)
		writeFile(a.path, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Level > out[j].Level })
	return out
}

// WindowName is a window's name in English: "5-hour", "weekly", or its
// length.
func WindowName(minutes int) string {
	switch {
	case minutes == 300:
		return "5-hour"
	case minutes == 10080:
		return "weekly"
	case minutes > 0 && minutes%1440 == 0:
		return fmt.Sprintf("%d-day", minutes/1440)
	case minutes > 0 && minutes%60 == 0:
		return fmt.Sprintf("%d-hour", minutes/60)
	case minutes > 0:
		return fmt.Sprintf("%d-minute", minutes)
	}
	return ""
}

// AgentName is how an agent is called in a notification.
func AgentName(id string) string {
	switch id {
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	}
	return id
}
