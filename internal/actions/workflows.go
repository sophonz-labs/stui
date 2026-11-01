package actions

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The workflows a checkout has, read off .github/workflows: what the board
// lists, and the runners each one's jobs ask for -- the only way to know a
// queued job's runner, since GitHub names it only once the job starts.

// Caps on what is read: a strange repository should not make stui read
// hundreds of files on every poll.
const (
	MaxWorkflows     = 64
	MaxWorkflowBytes = 256 << 10
)

// Runner kinds.
const (
	KindHosted     = "hosted"      // a GitHub-hosted runner (ubuntu-latest, macos-14, ...)
	KindSelfHosted = "self-hosted" // ours: `self-hosted`, or a label GitHub does not host
	KindUnknown    = "unknown"     // runs-on is an expression, or only a runner group
)

// Workflow is one file of .github/workflows.
type Workflow struct {
	File    string   `json:"file"`
	Name    string   `json:"name"`
	Runners []string `json:"runners"`
}

// ReadWorkflows reads root's .github/workflows/*.yml and *.yaml, in file
// name order. Nil when there are none.
func ReadWorkflows(root string) []Workflow {
	dir := filepath.Join(root, ".github", "workflows")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !(strings.HasSuffix(n, ".yml") || strings.HasSuffix(n, ".yaml")) {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) > MaxWorkflows {
		names = names[:MaxWorkflows]
	}
	out := make([]Workflow, 0, len(names))
	for _, n := range names {
		w := Workflow{File: n, Name: n, Runners: []string{}}
		if b, ok := readCapped(filepath.Join(dir, n)); ok {
			name, labels := parseWorkflow(string(b))
			if name != "" {
				w.Name = name
			}
			w.Runners = runnerKinds(labels)
		} else {
			w.Runners = []string{KindUnknown}
		}
		out = append(out, w)
	}
	return out
}

// readCapped reads a regular file of at most MaxWorkflowBytes.
func readCapped(path string) ([]byte, bool) {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > MaxWorkflowBytes {
		return nil, false
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxWorkflowBytes+1))
	return b, err == nil && len(b) <= MaxWorkflowBytes
}

// A job's runs-on, as read: its labels, or unknown.
type runsOn struct {
	labels  []string
	unknown bool
}

// parseWorkflow reads a workflow's top-level name and every job's runs-on.
// Not a YAML parser: the two keys a workflow file writes in a handful of
// shapes (a string, a flow list, a block list, a group/labels mapping),
// which is all the board needs. Anything else reads as unknown.
func parseWorkflow(src string) (string, []runsOn) {
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	name := ""
	var jobs []runsOn
	inJobs := false
	jobIndent, childIndent := -1, -1
	for i := 0; i < len(lines); i++ {
		raw := stripComment(lines[i])
		if strings.TrimSpace(raw) == "" {
			continue
		}
		ind := indentOf(raw)
		key, val, isKey := cutKey(strings.TrimSpace(raw))
		if ind == 0 {
			inJobs = isKey && key == "jobs"
			jobIndent, childIndent = -1, -1
			if isKey && key == "name" && name == "" {
				name = unquote(val)
			}
			continue
		}
		if !inJobs || !isKey {
			continue
		}
		if jobIndent < 0 {
			jobIndent = ind
		}
		if ind == jobIndent {
			childIndent = -1
			continue
		}
		if ind < jobIndent {
			continue
		}
		if childIndent < 0 {
			childIndent = ind
		}
		if ind != childIndent || key != "runs-on" {
			continue
		}
		if val != "" {
			jobs = append(jobs, inlineRunsOn(val))
			continue
		}
		// A block: list items, or group:/labels:.
		var ro runsOn
		got := false
		for i+1 < len(lines) {
			next := stripComment(lines[i+1])
			if strings.TrimSpace(next) == "" {
				i++
				continue
			}
			if indentOf(next) <= ind {
				break
			}
			i++
			t := strings.TrimSpace(next)
			if item, ok := strings.CutPrefix(t, "- "); ok {
				ro.add(unquote(item))
				got = true
				continue
			}
			k, v, ok := cutKey(t)
			switch {
			case ok && k == "labels" && v != "":
				in := inlineRunsOn(v)
				ro.labels = append(ro.labels, in.labels...)
				ro.unknown = ro.unknown || in.unknown
				got = true
			case ok && k == "labels":
				got = true // its items follow as "- x"
			case ok && k == "group":
				got = true
				if len(ro.labels) == 0 {
					ro.unknown = true
				}
			}
		}
		if len(ro.labels) > 0 && !strings.Contains(strings.Join(ro.labels, " "), "${{") {
			ro.unknown = false
		}
		if !got {
			ro.unknown = true
		}
		jobs = append(jobs, ro)
	}
	return name, jobs
}

func (r *runsOn) add(l string) {
	if strings.Contains(l, "${{") {
		r.unknown = true
		return
	}
	if l != "" {
		r.labels = append(r.labels, l)
	}
}

// inlineRunsOn reads `ubuntu-latest`, `[self-hosted, macOS]`, or a flow
// mapping, which only counts when it names labels.
func inlineRunsOn(v string) runsOn {
	v = strings.TrimSpace(v)
	if strings.Contains(v, "${{") {
		return runsOn{unknown: true}
	}
	if strings.HasPrefix(v, "{") {
		m := flowLabelsRE.FindStringSubmatch(v)
		if m == nil {
			return runsOn{unknown: true}
		}
		v = m[1]
	}
	var r runsOn
	if strings.HasPrefix(v, "[") {
		for _, p := range strings.Split(strings.Trim(v, "[]"), ",") {
			r.add(unquote(p))
		}
	} else {
		r.add(unquote(v))
	}
	if len(r.labels) == 0 {
		r.unknown = true
	}
	return r
}

var flowLabelsRE = regexp.MustCompile(`labels\s*:\s*(\[[^\]]*\]|[^,}]+)`)

// GitHub's hosted runner labels: ubuntu-latest, ubuntu-24.04, windows-2022,
// macos-14, macos-15-xlarge, ubuntu-24.04-arm ...
var hostedLabelRE = regexp.MustCompile(`^(?i)(ubuntu|windows|macos)-[a-z0-9.-]+$`)

// runnerKinds is the set of kinds a workflow's jobs ask for, in a fixed
// order.
func runnerKinds(jobs []runsOn) []string {
	seen := map[string]bool{}
	for _, j := range jobs {
		seen[labelsKind(j)] = true
	}
	out := []string{}
	for _, k := range []string{KindHosted, KindSelfHosted, KindUnknown} {
		if seen[k] {
			out = append(out, k)
		}
	}
	return out
}

func labelsKind(j runsOn) string {
	if j.unknown || len(j.labels) == 0 {
		return KindUnknown
	}
	for _, l := range j.labels {
		if strings.EqualFold(l, "self-hosted") {
			return KindSelfHosted
		}
	}
	for _, l := range j.labels {
		if !hostedLabelRE.MatchString(l) {
			return KindSelfHosted
		}
	}
	return KindHosted
}

func indentOf(s string) int { return len(s) - len(strings.TrimLeft(s, " \t")) }

// cutKey splits `key: value`; false for a line that is not a mapping key
// (a list item, a bare scalar).
func cutKey(t string) (string, string, bool) {
	if strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "-\t") {
		return "", "", false
	}
	k, v, ok := strings.Cut(t, ":")
	if !ok || !keyRE.MatchString(k) {
		return "", "", false
	}
	if v != "" && v[0] != ' ' && v[0] != '\t' {
		return "", "", false // a:b is a scalar, not a key
	}
	return unquote(k), strings.TrimSpace(v), true
}

var keyRE = regexp.MustCompile(`^(?:[A-Za-z0-9_.-]+|"[^"]+"|'[^']+')$`)

// stripComment drops a # comment outside quotes.
func stripComment(s string) string {
	var q byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			q = c
		case c == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t'):
			return strings.TrimRight(s[:i], " \t")
		}
	}
	return s
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}
