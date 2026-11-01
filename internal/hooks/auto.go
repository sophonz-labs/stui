package hooks

// Keeping stui's hooks up to date without being asked each time.
//
// A set an older stui wrote (an outdated one) misses what a newer stui listens
// to, and nothing about it says so to the person using it: the phone just
// follows that agent a little less well. So once somebody has installed stui's
// hooks -- by stui hooks install, by answering yes when stui first offered, by
// pressing Install in an app, or by having stui's entries in the file already,
// which they did not get any other way -- that is taken as agreeing to stui
// keeping them current. stui then updates an outdated set when an agent
// starts: after an upgrade, that is the new version's first start. Only
// stui's own entries change, and the file is backed up first, exactly as stui
// hooks install does it.
//
// `stui hooks auto off` (or the switch in the apps) stops it. A set at this
// version with events taken out (partial) is never put back on its own:
// that is somebody's edit, and only stui hooks install undoes it.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Prefs is hooks.json.
type Prefs struct {
	// Somebody installed stui's hooks on this machine.
	Consent   bool   `json:"consent,omitempty"`
	ConsentAt string `json:"consentAt,omitempty"`
	// Keep them up to date on their own; nil is on.
	Auto *bool `json:"auto,omitempty"`
	// The last time an outdated set was updated on its own.
	LastAuto *AutoRun `json:"lastAuto,omitempty"`
	// The [hooks.state] keys stui wrote into Codex's config.toml, to take out
	// again when stui's hooks move or go.
	CodexTrust []string `json:"codexTrust,omitempty"`
}

// AutoRun is one round of updating outdated sets on their own.
type AutoRun struct {
	// Unix milliseconds.
	At      int64        `json:"at"`
	Changes []AutoChange `json:"changes"`
}

// AutoChange is one agent's set brought up to date.
type AutoChange struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	From   int    `json:"from"`
	To     int    `json:"to"`
	File   string `json:"file"`
	Backup string `json:"backup,omitempty"`
}

func (e Env) prefsPath() string {
	dir := e.StateDir
	if dir == "" {
		dir = filepath.Join(e.Home, ".stui")
	}
	return filepath.Join(dir, "hooks.json")
}

func (e Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// ReadPrefs is hooks.json; the zero value when there is none.
func ReadPrefs(e Env) Prefs {
	var p Prefs
	if b, err := os.ReadFile(e.prefsPath()); err == nil {
		json.Unmarshal(b, &p)
	}
	return p
}

func writePrefs(e Env, p Prefs) error {
	b, _ := json.MarshalIndent(p, "", "  ")
	if err := os.MkdirAll(filepath.Dir(e.prefsPath()), 0o755); err != nil {
		return err
	}
	tmp := e.prefsPath() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, e.prefsPath())
}

// RecordConsent notes that somebody installed stui's hooks here.
func RecordConsent(e Env) error {
	p := ReadPrefs(e)
	if p.Consent {
		return nil
	}
	p.Consent, p.ConsentAt = true, e.now().UTC().Format(time.RFC3339)
	return writePrefs(e, p)
}

// SetAuto turns keeping the hooks up to date on or off.
func SetAuto(e Env, on bool) error {
	p := ReadPrefs(e)
	p.Auto = &on
	return writePrefs(e, p)
}

// AutoWanted is whether the person has not turned it off; it says nothing
// about consent.
func AutoWanted(e Env) bool {
	p := ReadPrefs(e)
	return p.Auto == nil || *p.Auto
}

// Consented is whether stui's hooks were ever installed here: recorded, or
// stui's entries in an agent's file.
func Consented(e Env) bool {
	if ReadPrefs(e).Consent {
		return true
	}
	for _, info := range Sets(e) {
		if info.State != "none" {
			return true
		}
	}
	return false
}

// AutoOn is whether stui updates outdated sets on its own here.
func AutoOn(e Env) bool { return AutoWanted(e) && Consented(e) }

// AutoUpdate brings every outdated set up to date when AutoOn, and says
// what it changed; nothing when it is off or nothing is outdated.
func AutoUpdate(e Env) ([]AutoChange, error) {
	if !AutoWanted(e) {
		return nil, nil
	}
	var stale []Agent
	sets := map[string]SetInfo{}
	consent := ReadPrefs(e).Consent
	for _, a := range Agents() {
		if !a.Hookable() || a.Info == nil {
			continue
		}
		info := a.Info(e)
		sets[a.ID] = info
		if info.State != "none" {
			consent = true
		}
		if info.State == "outdated" {
			stale = append(stale, a)
		}
	}
	if !consent || len(stale) == 0 {
		return nil, nil
	}
	var changes []AutoChange
	var firstErr error
	for _, a := range stale {
		res, err := a.Install(e)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !res.Changed {
			continue
		}
		changes = append(changes, AutoChange{
			ID: a.ID, Name: a.Name, From: sets[a.ID].Version, To: a.Info(e).Version,
			File: res.File, Backup: res.Backup,
		})
	}
	p := ReadPrefs(e)
	if !p.Consent {
		p.Consent, p.ConsentAt = true, e.now().UTC().Format(time.RFC3339)
	}
	if len(changes) > 0 {
		p.LastAuto = &AutoRun{At: e.now().UnixMilli(), Changes: changes}
	}
	writePrefs(e, p)
	return changes, firstErr
}
