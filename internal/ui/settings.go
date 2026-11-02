package ui

import (
	"fmt"
	"strings"

	"github.com/sophonz-labs/stui/internal/config"
	"github.com/sophonz-labs/stui/internal/plugin"
)

// startViews are the views config.yaml's startView may name.
var startViews = []string{"sessions", "agents", "orch", "ci", "usage"}

// StartViews is startViews, for stui config.
func StartViews() []string { return startViews }

// conf is the settings, the defaults before any were read.
func (a *app) conf() *config.Config {
	if a.cfg == nil {
		a.cfg = config.Defaults(startViews)
	}
	return a.cfg
}

// applyConfig puts c in effect: the toggles go back to what it says.
func (a *app) applyConfig(c *config.Config) {
	a.cfg = c
	a.logoOn, a.headless = c.Logo, c.Headless
	a.rate.Store(int64(c.RefreshRate))
	if a.scr != nil {
		a.scr.mono = !c.Color
	}
	a.clear()
	a.loadPlugins(plugin.Dirs("stui"))
}

// reloadConfig reads config.yaml again and says what came of it.
func (a *app) reloadConfig() {
	if a.cfg == nil || a.cfg.Path == "" {
		return
	}
	c := config.Load(a.cfg.Path, startViews)
	if c.Same(a.cfg) { // touched, or nothing new: keep this session's toggles
		a.cfg.ModTime = c.ModTime
		return
	}
	a.applyConfig(c)
	if n := len(c.Warnings); n > 0 {
		a.say(fmt.Sprintf("config.yaml read again: %s (%d warnings; :config)", c.Warnings[0], n), true)
	} else {
		a.say("config.yaml read again", false)
	}
}

// clear wipes the screen before the next draw, when the layout moves.
func (a *app) clear() {
	if a.scr != nil {
		a.scr.w.WriteString("\x1b[2J")
	}
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// keepPlugins leaves out what config.yaml turns off.
func (a *app) keepPlugins(ps []*plugin.Plugin) []*plugin.Plugin {
	c := a.conf()
	if !c.PluginsEnabled {
		return nil
	}
	for _, p := range ps {
		if c.Disabled(p.Name) && p.M != nil {
			p.M = nil
			p.Problems = append(p.Problems, "disabled in config.yaml")
		}
	}
	return ps
}

// -- :config --

type configView struct{}

func (configView) title() string    { return "Config" }
func (configView) scope() string    { return "config.yaml" }
func (configView) header() []string { return []string{"KEY", "VALUE", "FROM"} }
func (configView) rows(a *app) []row {
	c := a.conf()
	var out []row
	for _, k := range config.Keys {
		v, from, color := c.Value(k), c.Source[k], cAqua
		switch {
		case k == "ui.logo" && a.logoOn != c.Logo:
			v, from = fmt.Sprint(a.logoOn), "this session (:logo)"
		case k == "ui.headless" && a.headless != c.Headless:
			v, from = fmt.Sprint(a.headless), "this session (ctrl-e)"
		}
		if from == "default" {
			color = cGray
		}
		out = append(out, row{id: k, color: color, cells: []string{k, v, from}})
	}
	file := tilde(c.Path)
	if c.ModTime.IsZero() {
		file += " (not there: stui config edit makes it)"
	}
	out = append(out, row{id: "file", color: cGray, cells: []string{"file", file, ""}})
	for i, w := range c.Warnings {
		out = append(out, row{id: fmt.Sprint("warning", i), color: cRed, cells: []string{"warning", w, ""}})
	}
	return out
}
func (configView) hints() []hint { return []hint{{"e", "edit"}} }
func (configView) key(a *app, k string, r *row) bool {
	if k != "e" {
		return false
	}
	a.handOver(false, "config", "edit")
	a.reloadConfig()
	return true
}

// configHelp is the settings in effect, for help.
func (a *app) configHelp() []string {
	c := a.conf()
	out := []string{"CONFIG (" + tilde(c.Path) + "; :config shows it, stui config edit changes it)"}
	for _, k := range config.Keys {
		out = append(out, fmt.Sprintf("  %-18s %s", k, c.Value(k)))
	}
	if len(c.Warnings) > 0 {
		out = append(out, "  warnings: "+strings.Join(c.Warnings, "; "))
	}
	return append(out, "")
}
