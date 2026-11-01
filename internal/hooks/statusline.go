package hooks

// stui hooks install --usage: stui as Claude Code's status line, so the 5-hour
// and weekly limits Claude hands it reach the phones (internal/usage).
//
// statusLine is one slot, not a list: whatever the user had there -- their
// own script, a HUD -- is moved aside into ~/.stui/statusline.json, and
// stui statusline runs it with the same input, so the line under Claude looks
// as it did. Only on request: stui hooks install alone, the phone's Install
// button and automatic updates never touch it. stui hooks uninstall --usage
// puts back exactly what was there, and settings.json is backed up on both
// ways as for the hooks.

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sophonz-labs/stui/internal/usage"
)

// ClaudeHome is the Claude config directory these settings are in.
func (e Env) ClaudeHome() string { return filepath.Clean(e.claudeDir()) }

var oursStatus = regexp.MustCompile(`(^|[/\\'"])stui(\.exe)?['"]? statusline$`)

func (e Env) statusCommand() string { return quoteCommand(e.St) + " statusline" }

func isOurStatus(v any) bool {
	o, ok := v.(*object)
	if !ok {
		return false
	}
	c, _ := o.get("command")
	s, _ := c.(string)
	return oursStatus.MatchString(strings.TrimSpace(s))
}

// StatuslineOn is whether stui's status line is in Claude's settings.
func StatuslineOn(e Env) bool {
	doc, _, err := loadSettings(e.ClaudeSettings())
	if err != nil {
		return false
	}
	v, _ := doc.get("statusLine")
	return isOurStatus(v)
}

// previousCommand is the command of a statusLine value, when it runs one.
func previousCommand(v any) string {
	o, ok := v.(*object)
	if !ok {
		return ""
	}
	if t, ok := o.get("type"); ok {
		if s, _ := t.(string); s != "command" {
			return ""
		}
	}
	c, _ := o.get("command")
	s, _ := c.(string)
	return strings.TrimSpace(s)
}

// InstallStatusline makes stui Claude's status line, keeping what was there.
func InstallStatusline(e Env) (Result, error) {
	path := e.ClaudeSettings()
	doc, raw, err := loadSettings(path)
	if err != nil {
		return Result{File: path}, err
	}
	key := e.ClaudeHome()
	chains := usage.ReadChains(e.StateDir)
	cur, has := doc.get("statusLine")
	if has && isOurStatus(cur) {
		o := cur.(*object)
		c, _ := o.get("command")
		if c == e.statusCommand() {
			if _, kept := chains[key]; !kept {
				// stui's line with nothing recorded behind it: there was none.
				chains[key] = usage.Chain{Previous: json.RawMessage("null"), InstalledAt: e.now().UnixMilli()}
				if err := usage.WriteChains(e.StateDir, chains); err != nil {
					return Result{File: path}, err
				}
			}
			return Result{File: path}, nil
		}
		// Another path to stui (moved, or an older install): updated in place,
		// with what it took the place of left as recorded.
		o.set("command", e.statusCommand())
		return saveSettings(e, path, doc, raw)
	}
	prev := json.RawMessage("null")
	next := &object{}
	if has {
		var buf bytes.Buffer
		writeValue(&buf, cur, "  ", 0)
		prev = json.RawMessage(buf.Bytes())
		// padding and anything else that is not what runs stays as it was.
		if o, ok := cur.(*object); ok {
			for _, m := range o.members {
				if m.key != "type" && m.key != "command" {
					next.set(m.key, m.val)
				}
			}
		}
	}
	next.members = append([]member{{"type", "command"}, {"command", e.statusCommand()}}, next.members...)
	// Recorded first: a settings file that points at stui with nothing to run
	// behind it would lose the user's line until uninstall.
	chains[key] = usage.Chain{Previous: prev, Command: previousCommand(cur), InstalledAt: e.now().UnixMilli()}
	if err := usage.WriteChains(e.StateDir, chains); err != nil {
		return Result{File: path}, err
	}
	doc.set("statusLine", next)
	res, err := saveSettings(e, path, doc, raw)
	if err == nil && has {
		if c := previousCommand(cur); c != "" {
			res.Note = "kept your status line (" + clipNote(c) + "); stui runs it"
		}
	}
	return res, err
}

// UninstallStatusline puts back the status line stui took the place of. One
// the user changed since is theirs and is left alone.
func UninstallStatusline(e Env) (Result, error) {
	path := e.ClaudeSettings()
	doc, raw, err := loadSettings(path)
	if err != nil {
		return Result{File: path}, err
	}
	key := e.ClaudeHome()
	chains := usage.ReadChains(e.StateDir)
	c, kept := chains[key]
	cur, has := doc.get("statusLine")
	if !has || !isOurStatus(cur) {
		if kept {
			delete(chains, key)
			usage.WriteChains(e.StateDir, chains)
		}
		return Result{File: path, Note: "stui is not Claude's status line"}, nil
	}
	restored := false
	if kept && len(c.Previous) > 0 && string(c.Previous) != "null" {
		v, err := parseAny(c.Previous)
		if err != nil {
			return Result{File: path}, errors.New("the status line stui kept in " + usage.ChainFile(e.StateDir) + " is not valid JSON; settings.json was left alone")
		}
		doc.set("statusLine", v)
		restored = true
	} else {
		doc.del("statusLine")
	}
	res, err := saveSettings(e, path, doc, raw)
	if err != nil {
		return res, err
	}
	delete(chains, key)
	if err := usage.WriteChains(e.StateDir, chains); err != nil {
		return res, err
	}
	if restored {
		res.Note = "put your status line back"
	}
	return res, nil
}

func parseAny(b []byte) (any, error) {
	if bytes.HasPrefix(bytes.TrimSpace(b), []byte("{")) {
		return parseDoc(b)
	}
	var v any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	return v, d.Decode(&v)
}

func clipNote(s string) string {
	if len(s) > 60 {
		return s[:57] + "..."
	}
	return s
}
