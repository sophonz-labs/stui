package restore

// Which of an agent's first options its resume line keeps.
//
// A pane started as `claude --model opus --dangerously-skip-permissions`
// came back as `claude --resume <id>`: another model, and asking for every
// permission again. The snapshot has the whole command line (Pane.Args), so
// the resume line keeps the options that say how the agent runs -- model,
// effort, permissions, directories, plugins -- and leaves out what says what
// to do now (the first prompt, -p, the old --resume and its id, a new
// worktree). An option not in the tables is left out and named, so a dry run
// shows it.
//
// Pane.Args is `ps -o args=`: the arguments joined by spaces, their quoting
// gone. A value with a space in it (a folder under "Application Support")
// comes back in pieces. So a path is kept only when the pieces, joined again,
// name something on this machine; a value that is free text (a system prompt)
// is never kept, as it cannot be put back together.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// How an option's value is read.
const (
	argNone  = iota // a flag
	argWord         // one word: a model, a mode
	argPath         // a path, which may have held spaces
	argPaths        // paths, until the next option
	argWords        // words until the next option (tool lists)
	argConf         // a path or JSON (claude --settings, --mcp-config)
	argConfs        // the same, until the next option
	argKV           // codex -c key=value
)

// An option the resume line keeps.
type keep struct {
	name string // as written in the line (the long form)
	arg  int
}

// What is dropped without being named: it says what to do now, not how the
// agent runs. The value is how many words follow (-1: an optional one).
type drop map[string]int

var claudeKeep = map[string]keep{
	"--model":                              {"--model", argWord},
	"--effort":                             {"--effort", argWord},
	"--fallback-model":                     {"--fallback-model", argWord},
	"--permission-mode":                    {"--permission-mode", argWord},
	"--agent":                              {"--agent", argWord},
	"--setting-sources":                    {"--setting-sources", argWord},
	"--autocompact":                        {"--autocompact", argWord},
	"--add-dir":                            {"--add-dir", argPaths},
	"--plugin-dir":                         {"--plugin-dir", argPath},
	"--settings":                           {"--settings", argConf},
	"--mcp-config":                         {"--mcp-config", argConfs},
	"--allowedTools":                       {"--allowedTools", argWords},
	"--allowed-tools":                      {"--allowed-tools", argWords},
	"--disallowedTools":                    {"--disallowedTools", argWords},
	"--disallowed-tools":                   {"--disallowed-tools", argWords},
	"--tools":                              {"--tools", argWords},
	"--dangerously-skip-permissions":       {"--dangerously-skip-permissions", argNone},
	"--allow-dangerously-skip-permissions": {"--allow-dangerously-skip-permissions", argNone},
	"--strict-mcp-config":                  {"--strict-mcp-config", argNone},
	"--disable-slash-commands":             {"--disable-slash-commands", argNone},
	"--bare":                               {"--bare", argNone},
	"--safe-mode":                          {"--safe-mode", argNone},
	"--restricted":                         {"--restricted", argNone},
	"--brief":                              {"--brief", argNone},
	"--chrome":                             {"--chrome", argNone},
	"--no-chrome":                          {"--no-chrome", argNone},
	"--ide":                                {"--ide", argNone},
	"--verbose":                            {"--verbose", argNone},
}

var claudeDrop = drop{
	"-c": 0, "--continue": 0, "-r": -1, "--resume": -1, "--session-id": 1, "--fork-session": 0,
	"-p": 0, "--print": 0,
}

var codexKeep = map[string]keep{
	"-m":                 {"--model", argWord},
	"--model":            {"--model", argWord},
	"-s":                 {"--sandbox", argWord},
	"--sandbox":          {"--sandbox", argWord},
	"-a":                 {"--ask-for-approval", argWord},
	"--ask-for-approval": {"--ask-for-approval", argWord},
	"-p":                 {"--profile", argWord},
	"--profile":          {"--profile", argWord},
	"-c":                 {"--config", argKV},
	"--config":           {"--config", argKV},
	"--enable":           {"--enable", argWord},
	"--disable":          {"--disable", argWord},
	"--local-provider":   {"--local-provider", argWord},
	"--add-dir":          {"--add-dir", argPath},
	"--oss":              {"--oss", argNone},
	"--search":           {"--search", argNone},
	"--approve-for-me":   {"--approve-for-me", argNone},
	"--dangerously-bypass-approvals-and-sandbox": {"--dangerously-bypass-approvals-and-sandbox", argNone},
	"--dangerously-bypass-hook-trust":            {"--dangerously-bypass-hook-trust", argNone},
	"--no-alt-screen":                            {"--no-alt-screen", argNone},
	"--no-daemon":                                {"--no-daemon", argNone},
	"--strict-config":                            {"--strict-config", argNone},
}

var codexDrop = drop{"--last": 0, "--all": 0, "--include-non-interactive": 0}

// agentAt is where the agent's own arguments start in args: after its
// program (`/…/claude`, or `node /…/claude-code/cli.js`), -1 when it is not
// there.
func agentAt(kind string, words []string) int {
	for i, w := range words {
		base := strings.TrimSuffix(strings.ToLower(filepath.Base(w)), ".exe")
		if base == kind {
			return i + 1
		}
		switch {
		case kind == "claude" && strings.Contains(w, "claude-code/cli"):
			return i + 1
		case kind == "codex" && strings.Contains(w, "@openai/codex/") && strings.HasSuffix(base, ".js"):
			return i + 1
		}
	}
	return -1
}

// isOpt is whether a word is an option rather than a value.
func isOpt(w string) bool { return len(w) > 1 && w[0] == '-' }

// resumeOptions is what the resume line keeps of the agent's first command
// line, ready to append (each value quoted), and the options it left out.
func resumeOptions(kind, args string) (kept []string, left []string) {
	var keeps map[string]keep
	var drops drop
	switch kind {
	case "claude":
		keeps, drops = claudeKeep, claudeDrop
	case "codex":
		keeps, drops = codexKeep, codexDrop
	default:
		return nil, nil
	}
	words := strings.Fields(args)
	at := agentAt(kind, words)
	if at < 0 {
		return nil, nil
	}
	words = words[at:]
	// `codex resume <id> …`: the line is made again with its own id.
	if kind == "codex" && len(words) > 0 && words[0] == "resume" {
		words = words[1:]
	}
	seen := map[string]bool{}
	add := func(parts ...string) {
		k := strings.Join(parts, "\x00")
		if !seen[k] {
			seen[k] = true
			kept = append(kept, parts...)
		}
	}
	for i := 0; i < len(words); i++ {
		w := words[i]
		if !isOpt(w) {
			continue // the first prompt, a subcommand's id: what to do now
		}
		name, inline, hasInline := strings.Cut(w, "=")
		if n, ok := drops[name]; ok {
			if !hasInline {
				if n == -1 && i+1 < len(words) && !isOpt(words[i+1]) {
					i++
				}
				i += max(n, 0)
			}
			continue
		}
		k, ok := keeps[name]
		if !ok {
			left = append(left, name)
			// Its value, if it had one, is not an option: skipped as a word.
			continue
		}
		// The words after it, up to the next option.
		rest := []string{}
		if hasInline {
			rest = append(rest, inline)
		}
		j := i + 1
		for ; j < len(words) && !isOpt(words[j]); j++ {
			rest = append(rest, words[j])
		}
		switch k.arg {
		case argNone:
			add(k.name)
			if hasInline {
				left = append(left, name)
			}
			continue
		case argWord, argKV:
			if len(rest) == 0 || (k.arg == argKV && (!strings.Contains(rest[0], "=") || strings.ContainsAny(rest[0], `"'`))) {
				left = append(left, name)
			} else {
				add(k.name, shq(rest[0]))
			}
		case argWords:
			if len(rest) == 0 || !balanced(rest) {
				left = append(left, name)
			} else {
				parts := []string{k.name}
				for _, r := range rest {
					parts = append(parts, shq(r))
				}
				add(parts...)
			}
		case argPath, argPaths, argConf, argConfs:
			many := k.arg == argPaths || k.arg == argConfs
			conf := k.arg == argConf || k.arg == argConfs
			vals, ok := rejoin(rest, many, conf)
			if !ok {
				left = append(left, name)
			} else {
				parts := []string{k.name}
				for _, v := range vals {
					parts = append(parts, shq(v))
				}
				add(parts...)
			}
		}
		i = j - 1
	}
	return kept, left
}

// balanced is whether no word of a tool list opens a parenthesis it does not
// close: `Bash(git *)` was one word, which ps split in two.
func balanced(ws []string) bool {
	for _, w := range ws {
		if strings.Count(w, "(") != strings.Count(w, ")") {
			return false
		}
	}
	return true
}

// rejoin puts back the values ps split at their spaces: the shortest run of
// words that, joined by spaces, is an existing path (or, for conf, valid
// JSON). One value, or with many as many as there are; false when the
// first makes none.
func rejoin(ws []string, many, conf bool) ([]string, bool) {
	var out []string
	for len(ws) > 0 {
		n := 0
		for k := 1; k <= len(ws); k++ {
			v := strings.Join(ws[:k], " ")
			if exists(v) || (conf && strings.HasPrefix(v, "{") && json.Valid([]byte(v))) {
				n = k
				break
			}
		}
		if n == 0 {
			// With many, what is left was the first prompt.
			break
		}
		out = append(out, strings.Join(ws[:n], " "))
		ws = ws[n:]
		if !many {
			// What follows was the first prompt.
			break
		}
	}
	return out, len(out) > 0
}

func exists(p string) bool {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(h, p[2:])
		}
	}
	_, err := os.Stat(p)
	return err == nil
}
