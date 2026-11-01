package hooks

// Codex's hooks: ~/.codex/hooks.json, and the trust Codex wants for each of
// them in ~/.codex/config.toml.
//
// Codex runs a hook from hooks.json only once it is trusted: a
// [hooks.state."<file>:<event>:<group>:<handler>"] table whose trusted_hash
// is the hash of that hook as Codex reads it (codex-rs hooks discovery.rs
// hook_hash, config fingerprint.rs version_for_toml). Without it the hook
// sits in Codex's /hooks review list and never runs. stui writes the hash
// itself, as Codex's own /hooks approval would.
//
// Other tools put their hooks in the same file (Orca does, at index 0 of
// every event). stui's entry is updated where it is and otherwise appended, so
// the indexes in their trust keys never move.
//
// Codex's notify line (hooks.go) stays alongside: a Codex from before hooks
// still says when a turn ends, and a turn ended twice ends once.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// CodexEvents are the hooks stui listens to in Codex. PermissionRequest is
// observed only: stui hook prints nothing, so Codex shows its own approval.
// Interrupt (Codex 0.150+) is the one hook an Esc-cancelled turn fires;
// SessionEnd closes the pane's agent. A Codex older than an event ignores it.
var CodexEvents = []string{
	"SessionStart", "UserPromptSubmit", "PreToolUse", "PermissionRequest", "PostToolUse",
	"SubagentStart", "SubagentStop", "Stop", "Interrupt", "SessionEnd",
}

// codexLabels is each event's name in Codex's trust keys and hashes
// (codex-rs hooks lib.rs hook_event_key_label).
var codexLabels = map[string]string{
	"SessionStart": "session_start", "UserPromptSubmit": "user_prompt_submit",
	"PreToolUse": "pre_tool_use", "PermissionRequest": "permission_request",
	"PostToolUse": "post_tool_use", "SubagentStart": "subagent_start",
	"SubagentStop": "subagent_stop", "Stop": "stop", "Interrupt": "interrupt",
	"SessionEnd": "session_end", "PreCompact": "pre_compact", "PostCompact": "post_compact",
}

// codexTimeout is what stui writes for each event: Codex caps Interrupt and
// SessionEnd at 3 s and warns at every start about a longer one.
func codexTimeout(event string) int {
	if event == "Interrupt" || event == "SessionEnd" {
		return 3
	}
	return 10
}

func (e Env) CodexHooksFile() string { return filepath.Join(e.codexDir(), "hooks.json") }

func (e Env) codexHookCommand() string {
	return codexQuote(e.St) + " hook codex --hv " + strconv.Itoa(CodexVersion)
}

// plainPath is a path every shell reads as one word, as it is.
var plainPath = regexp.MustCompile(`^[A-Za-z0-9_.:\\/-]+$`)

// codexQuote is stui's path as the shell Codex runs hooks with reads it.
//
// Codex runs a hook with its session's shell (codex-rs core session
// build_hooks_config, shell.rs derive_exec_args): on Windows that is
// PowerShell, `powershell -NoProfile -Command <hook>`, and cmd /c only when
// there is no PowerShell. PowerShell reads a line that starts with a quoted
// path as a string, and the word after it as an error ("Unexpected token
// 'hook'", exit 1) -- and every Windows path has a backslash, which
// quoteCommand quotes. So: a plain path as it is, which PowerShell and cmd
// both run, and any other with PowerShell's call operator.
func codexQuote(path string) string {
	if hostOS != "windows" {
		return quoteCommand(path)
	}
	if plainPath.MatchString(path) {
		return path
	}
	return "& '" + strings.ReplaceAll(path, "'", "''") + "'"
}

// codexKeySource is the hooks.json path as Codex puts it in a trust key:
// its directory with links resolved, the file name as it is.
func (e Env) codexKeySource() string {
	dir := e.codexDir()
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	return filepath.Join(dir, "hooks.json")
}

func codexTrustKey(source, event string, group, handler int) string {
	return fmt.Sprintf("%s:%s:%d:%d", source, codexLabels[event], group, handler)
}

// codexHookHash is Codex's hash of one command hook with no matcher: the
// sha256 of its normalized identity as canonical JSON (keys sorted, no HTML
// escaping, as serde_json writes it).
func codexHookHash(event, command string, timeout int) string {
	identity := map[string]any{
		"event_name": codexLabels[event],
		"hooks": []any{map[string]any{
			"type": "command", "command": command, "timeout": timeout, "async": false,
		}},
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.Encode(identity)
	sum := sha256.Sum256(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// codexHooksObject is hooks.json's "hooks", made if missing and create is set.
func codexHooksObject(doc *object, create bool) (*object, error) {
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
		return nil, errors.New(`"hooks" in Codex's hooks.json is not an object; it was left alone`)
	}
	return h, nil
}

// codexEntry is where stui's hook for one event is: its group's index.
type codexEntry struct {
	event string
	group int
}

// installCodexHooks makes sure every event in CodexEvents runs stui hook
// codex, once, and reports where each entry ended up.
func installCodexHooks(e Env) (Result, []codexEntry, error) {
	path := e.CodexHooksFile()
	doc, raw, err := loadSettings(path)
	if err != nil {
		return Result{File: path}, nil, err
	}
	hooks, err := codexHooksObject(doc, true)
	if err != nil {
		return Result{File: path}, nil, err
	}
	wanted := map[string]bool{}
	for _, ev := range CodexEvents {
		wanted[ev] = true
		if v, ok := hooks.get(ev); ok {
			if _, list := v.([]any); !list {
				return Result{File: path}, nil, fmt.Errorf(`"hooks.%s" in Codex's hooks.json is not a list; it was left alone`, ev)
			}
		}
	}
	cmd := e.codexHookCommand()
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
				h.(*object).set("command", cmd)
				h.(*object).set("timeout", json.Number(strconv.Itoa(codexTimeout(m.key))))
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
	for _, ev := range CodexEvents {
		if found[ev] {
			continue
		}
		v, _ := hooks.get(ev)
		groups, _ := v.([]any)
		entry := &object{}
		entry.set("type", "command")
		entry.set("command", cmd)
		entry.set("timeout", json.Number(strconv.Itoa(codexTimeout(ev))))
		group := &object{}
		group.set("hooks", []any{entry})
		hooks.set(ev, append(groups, group))
	}
	res, err := saveSettings(e, path, doc, raw)
	if err != nil {
		return res, nil, err
	}
	return res, codexEntries(hooks), nil
}

// codexEntries finds stui's hook in each event: the group that holds it. The
// handler index is always 0, since stui's group holds stui's hook alone -- one
// sharing a group with another tool's is not stui's to trust.
func codexEntries(hooks *object) []codexEntry {
	var out []codexEntry
	for _, m := range hooks.members {
		if codexLabels[m.key] == "" {
			continue
		}
		groups, _ := m.val.([]any)
		for i, g := range groups {
			grp, ok := g.(*object)
			if !ok {
				continue
			}
			hv, _ := grp.get("hooks")
			list, _ := hv.([]any)
			if len(list) > 0 && isOurs(list[0]) {
				out = append(out, codexEntry{m.key, i})
				break
			}
		}
	}
	return out
}

// ---- trust, in config.toml

var hookStateHeader = regexp.MustCompile(`^\s*\[\s*hooks\.state\.("(?:[^"\\]|\\.)*"|'[^']*')\s*\]\s*(?:#.*)?$`)

// hookStateKey is the key of a [hooks.state."..."] header line, or "".
func hookStateKey(line string) string {
	m := hookStateHeader.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	q := m[1]
	if q[0] == '\'' {
		return q[1 : len(q)-1]
	}
	k, err := strconv.Unquote(q)
	if err != nil {
		return ""
	}
	return k
}

func isTableHeader(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "[")
}

// removeTrust drops the [hooks.state."key"] tables whose key is in keys,
// each up to the next table header.
func removeTrust(text string, keys map[string]bool) (string, bool) {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	changed := false
	for i := 0; i < len(lines); i++ {
		if k := hookStateKey(lines[i]); k != "" && keys[k] {
			changed = true
			for i+1 < len(lines) && !isTableHeader(lines[i+1]) {
				i++
			}
			continue
		}
		out = append(out, lines[i])
	}
	return strings.Join(out, "\n"), changed
}

func trustBlock(key, hash string) string {
	return "[hooks.state." + strconv.Quote(key) + "]\ntrusted_hash = " + strconv.Quote(hash)
}

// writeCodexTrust replaces the trust stui wrote before (old) with trust for
// entries, and remembers the new keys.
func writeCodexTrust(e Env, entries []codexEntry, old []string) (Result, []string, error) {
	path := e.CodexConfig()
	res := Result{File: path}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return res, old, err
	}
	source := e.codexKeySource()
	cmd := e.codexHookCommand()
	drop := map[string]bool{}
	for _, k := range old {
		drop[k] = true
	}
	var keys, blocks []string
	for _, en := range entries {
		k := codexTrustKey(source, en.event, en.group, 0)
		// Whatever trusted this key before is about another hook now.
		drop[k] = true
		keys = append(keys, k)
		blocks = append(blocks, trustBlock(k, codexHookHash(en.event, cmd, codexTimeout(en.event))))
	}
	text, _ := removeTrust(string(raw), drop)
	text = strings.TrimRight(text, "\n")
	if len(blocks) > 0 {
		if text != "" {
			text += "\n\n"
		}
		text += strings.Join(blocks, "\n\n")
	}
	if text != "" {
		text += "\n"
	}
	if text == string(raw) {
		return res, keys, nil
	}
	if raw != nil {
		b, err := backup(e, path, raw)
		if err != nil {
			return res, old, err
		}
		res.Backup = b
	}
	res.Changed = true
	return res, keys, writeKeepingMode(path, []byte(text))
}

// removeCodexHooks takes stui's entries out of hooks.json, and the file
// itself when nothing else was in it.
func removeCodexHooks(e Env) (Result, error) {
	path := e.CodexHooksFile()
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return Result{File: path}, nil
	}
	doc, raw, err := loadSettings(path)
	if err != nil {
		return Result{File: path}, err
	}
	hooks, err := codexHooksObject(doc, false)
	if err != nil || hooks == nil {
		return Result{File: path}, err
	}
	removed := false
	for _, m := range append([]member{}, hooks.members...) {
		groups, ok := m.val.([]any)
		if !ok {
			continue
		}
		kept := []any{}
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
		if len(kept) == 0 {
			hooks.del(m.key)
		} else {
			hooks.set(m.key, kept)
		}
	}
	if !removed {
		return Result{File: path}, nil
	}
	if len(hooks.members) == 0 && len(doc.members) == 1 {
		b, err := backup(e, path, raw)
		if err != nil {
			return Result{File: path}, err
		}
		return Result{File: path, Changed: true, Backup: b}, os.Remove(path)
	}
	return saveSettings(e, path, doc, raw)
}

// codexHooksInfo reads stui's entries in hooks.json: which events have one,
// and the lowest version among them.
func codexHooksInfo(e Env) (present map[string]bool, version int) {
	present = map[string]bool{}
	doc, _, err := loadSettings(e.CodexHooksFile())
	if err != nil {
		return present, 0
	}
	hooks, _ := codexHooksObject(doc, false)
	if hooks == nil {
		return present, 0
	}
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
				if version == 0 || v < version {
					version = v
				}
			}
		}
	}
	return present, version
}
