package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// settingsSet is an agent that keeps its hooks in a settings.json shaped
// like Claude Code's: {"hooks": {"<Event>": [{"hooks": [{"type":
// "command", "command": "..."}]}]}}. Claude Code and Gemini CLI both do,
// and the only differences are the path, the event names, and what unit the
// timeout is in.
type settingsSet struct {
	// path is the settings.json to write.
	path string
	// events are the hooks stui wants to run it.
	events []string
	// command is `stui hook <agent> --hv <n>`.
	command string
	// timeout goes into each entry as the agent reads it: Claude Code takes
	// seconds, Gemini CLI milliseconds.
	timeout json.Number
	// version is the set this stui installs, and sets is every version's
	// events, for telling which version an unmarked set is.
	version int
	sets    map[int][]string
}

// hooksObject is the "hooks" object in doc, made when create and absent.
func hooksObject(doc *object, create bool) (*object, error) {
	v, ok := doc.get("hooks")
	if !ok {
		if !create {
			return nil, nil
		}
		h := &object{}
		doc.set("hooks", h)
		return h, nil
	}
	h, ok := v.(*object)
	if !ok {
		return nil, errors.New(`"hooks" in settings.json is not an object; it was left alone`)
	}
	return h, nil
}

// install makes sure every event in s.events runs s.command, once, at
// s.version. An entry stui wrote before -- another path to stui, an older
// version -- is updated in place; a second one for the same event, or one
// for an event stui no longer listens to, is taken out. Everything that is not
// stui's stays as it was.
func (s settingsSet) install(e Env) (Result, error) {
	doc, raw, err := loadSettings(s.path)
	if err != nil {
		return Result{File: s.path}, err
	}
	hooks, err := hooksObject(doc, true)
	if err != nil {
		return Result{File: s.path}, err
	}
	for _, ev := range s.events {
		if v, ok := hooks.get(ev); ok {
			if _, list := v.([]any); !list {
				return Result{File: s.path}, fmt.Errorf(`"hooks.%s" in settings.json is not a list; it was left alone`, ev)
			}
		}
	}
	wanted := map[string]bool{}
	for _, ev := range s.events {
		wanted[ev] = true
	}
	found := map[string]bool{}
	for _, m := range append([]member{}, hooks.members...) {
		groups, ok := m.val.([]any)
		if !ok {
			continue
		}
		kept := []any{}
		dropped := false
		for _, g := range groups {
			grp, ok := g.(*object)
			if !ok {
				kept = append(kept, g)
				continue
			}
			hv, _ := grp.get("hooks")
			list, ok := hv.([]any)
			if !ok {
				kept = append(kept, g)
				continue
			}
			left := []any{}
			for _, h := range list {
				if !isOurs(h) {
					left = append(left, h)
					continue
				}
				if !wanted[m.key] || found[m.key] {
					dropped = true
					continue
				}
				found[m.key] = true
				h.(*object).set("command", s.command)
				left = append(left, h)
			}
			if len(left) == 0 && len(list) > 0 {
				continue
			}
			if len(left) != len(list) {
				grp.set("hooks", left)
			}
			kept = append(kept, grp)
		}
		switch {
		case !dropped:
		case len(kept) == 0:
			hooks.del(m.key)
		default:
			hooks.set(m.key, kept)
		}
	}
	for _, ev := range s.events {
		if found[ev] {
			continue
		}
		v, _ := hooks.get(ev)
		groups, _ := v.([]any)
		entry := &object{}
		entry.set("type", "command")
		entry.set("command", s.command)
		entry.set("timeout", s.timeout)
		group := &object{}
		group.set("hooks", []any{entry})
		hooks.set(ev, append(groups, group))
	}
	return saveSettings(e, s.path, doc, raw)
}

// uninstall removes every hook stui wrote, and any group, event or the hooks
// object itself that only held stui's.
func (s settingsSet) uninstall(e Env) (Result, error) {
	if _, err := os.Stat(s.path); errors.Is(err, os.ErrNotExist) {
		return Result{File: s.path}, nil
	}
	doc, raw, err := loadSettings(s.path)
	if err != nil {
		return Result{File: s.path}, err
	}
	hooks, err := hooksObject(doc, false)
	if err != nil || hooks == nil {
		return Result{File: s.path}, err
	}
	for _, m := range append([]member{}, hooks.members...) {
		groups, ok := m.val.([]any)
		if !ok {
			continue
		}
		kept := []any{}
		removed := false
		for _, g := range groups {
			grp, ok := g.(*object)
			if !ok {
				kept = append(kept, g)
				continue
			}
			hv, _ := grp.get("hooks")
			list, ok := hv.([]any)
			if !ok {
				kept = append(kept, g)
				continue
			}
			left := []any{}
			for _, h := range list {
				if isOurs(h) {
					removed = true
				} else {
					left = append(left, h)
				}
			}
			if len(left) == 0 && len(list) > 0 {
				continue
			}
			grp.set("hooks", left)
			kept = append(kept, grp)
		}
		if !removed {
			continue
		}
		if len(kept) == 0 {
			hooks.del(m.key)
		} else {
			hooks.set(m.key, kept)
		}
	}
	if len(hooks.members) == 0 {
		doc.del("hooks")
	}
	return saveSettings(e, s.path, doc, raw)
}

// info reads the set in the file.
//
// A set carries its version in each command; one from before that is the
// newest version whose events it all has. The lowest of its entries counts:
// a set is as old as the oldest thing in it.
func (s settingsSet) info(e Env) (SetInfo, error) {
	info := SetInfo{State: "none", Required: s.version}
	doc, _, err := loadSettings(s.path)
	if err != nil {
		return info, err
	}
	hooks, _ := hooksObject(doc, false)
	if hooks == nil {
		info.Missing = append([]string{}, s.events...)
		return info, nil
	}
	present := map[string]bool{}
	marked, unmarked := 0, false
	for _, m := range hooks.members {
		groups, _ := m.val.([]any)
		for _, g := range groups {
			grp, ok := g.(*object)
			if !ok {
				continue
			}
			hv, _ := grp.get("hooks")
			list, _ := hv.([]any)
			for _, h := range list {
				ok, v := ourVersion(h)
				if !ok {
					continue
				}
				present[m.key] = true
				switch {
				case v == 0:
					unmarked = true
				case marked == 0 || v < marked:
					marked = v
				}
			}
		}
	}
	for _, ev := range s.events {
		if !present[ev] {
			info.Missing = append(info.Missing, ev)
		}
	}
	if len(present) == 0 {
		return info, nil
	}
	info.Version = marked
	if unmarked {
		inferred := 0
		for v, events := range s.sets {
			all := true
			for _, ev := range events {
				all = all && present[ev]
			}
			if all && v > inferred {
				inferred = v
			}
		}
		if info.Version == 0 || inferred < info.Version {
			info.Version = inferred
		}
	}
	switch {
	case info.Version < s.version:
		info.State = "outdated"
	case len(info.Missing) > 0:
		info.State = "partial"
	default:
		info.State = "installed"
	}
	return info, nil
}

// status lists which of s.events run stui's command.
func (s settingsSet) status(e Env) (installed []string, err error) {
	doc, _, err := loadSettings(s.path)
	if err != nil {
		return nil, err
	}
	hooks, _ := hooksObject(doc, false)
	if hooks == nil {
		return nil, nil
	}
	for _, ev := range s.events {
		v, _ := hooks.get(ev)
		groups, _ := v.([]any)
	scan:
		for _, g := range groups {
			grp, ok := g.(*object)
			if !ok {
				continue
			}
			hv, _ := grp.get("hooks")
			list, _ := hv.([]any)
			for _, h := range list {
				if isOurs(h) {
					installed = append(installed, ev)
					break scan
				}
			}
		}
	}
	return installed, nil
}
