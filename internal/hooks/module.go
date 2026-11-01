package hooks

// Some agents have no place to register a command against their events:
// they load JavaScript modules from a directory on start (opencode's
// plugins, Pi's extensions). For those stui writes one module of its own, and
// the module runs `stui hook <agent>` on each event. This is what installing,
// removing and reading such a file have in common; opencode.go and pi.go say
// where the file goes and what is in it.

import (
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// module is one agent's stui-written module file.
type module struct {
	// What the agent calls such a file: "plugin", "extension".
	noun string
	// The file stui writes.
	path func(Env) string
	// The module's source, with __VERSION__ and __COMMAND__ to fill in.
	source string
	// The version of the module this stui writes. A file has no command line
	// to carry `--hv`, so it is a constant in the file (STUI_HOOK_VERSION),
	// read back to tell an older stui's module from this one. It goes up
	// whenever the module changes what it sends.
	version int
	// The stui events the module sends, for stui hooks status: a module of
	// this version without one of them was changed by hand.
	events []string
}

var (
	moduleMark    = regexp.MustCompile(`(?m)^// stui-hook\.js: written by .stui hooks install.`)
	moduleVersion = regexp.MustCompile(`(?m)^const STUI_HOOK_VERSION = ([0-9]{1,4});`)
	// Where stui's path is: another path to stui is still this version.
	moduleCommand = regexp.MustCompile(`(?m)^const STUI_COMMAND = .*$`)
)

// text is the module as this stui writes it.
func (m module) text(e Env) string {
	cmd, _ := json.Marshal(e.St)
	t := strings.Replace(m.source, "__VERSION__", strconv.Itoa(m.version), 1)
	return strings.Replace(t, "__COMMAND__", string(cmd), 1)
}

// read is the file, whether stui wrote it, and its version.
func (m module) read(e Env) (text string, ours bool, version int, err error) {
	b, err := os.ReadFile(m.path(e))
	if err != nil {
		return "", false, 0, err
	}
	text = string(b)
	if !moduleMark.MatchString(text) {
		return text, false, 0, nil
	}
	if v := moduleVersion.FindStringSubmatch(text); v != nil {
		version, _ = strconv.Atoi(v[1])
	}
	return text, true, version, nil
}

// install writes stui's module, or brings one an older stui wrote up to date.
// A file of that name stui did not write is left alone.
func (m module) install(e Env) (Result, error) {
	path := m.path(e)
	res := Result{File: path}
	want := m.text(e)
	text, ours, _, err := m.read(e)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return res, err
	case !ours:
		res.Note = "a " + m.noun + " stui did not write is at " + path + "; left alone"
		return res, nil
	case text == want:
		return res, nil
	default:
		b, err := backup(e, path, []byte(text))
		if err != nil {
			return res, err
		}
		res.Backup = b
	}
	res.Changed = true
	return res, writeKeepingMode(path, []byte(want))
}

// uninstall removes stui's module, and only stui's.
func (m module) uninstall(e Env) (Result, error) {
	path := m.path(e)
	res := Result{File: path}
	_, ours, _, err := m.read(e)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return res, nil
	case err != nil:
		return res, err
	case !ours:
		res.Note = "a " + m.noun + " stui did not write is at " + path + "; left alone"
		return res, nil
	}
	res.Changed = true
	return res, os.Remove(path)
}

// info reads stui's module against what this stui writes: outdated when an
// older stui wrote it, partial when it is this version but not as stui wrote it
// (changed by hand; Missing names the events it no longer sends). Another
// path to stui is not a change.
func (m module) info(e Env) SetInfo {
	info := SetInfo{State: "none", Required: m.version}
	text, ours, v, err := m.read(e)
	if err != nil || !ours {
		return info
	}
	info.Version = v
	switch {
	case v < m.version:
		info.State = "outdated"
	case moduleCommand.ReplaceAllString(text, "") != moduleCommand.ReplaceAllString(m.text(e), ""):
		info.State = "partial"
		for _, ev := range m.events {
			if !strings.Contains(text, strconv.Quote(ev)) {
				info.Missing = append(info.Missing, ev)
			}
		}
	default:
		info.State = "installed"
	}
	return info
}
