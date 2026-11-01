package watch

// Reading what an agent is asking from its screen: the question and the
// numbered choices under it, and which keys pick each one. Claude Code's
// permission prompts, AskUserQuestion and plan approval, Codex approvals,
// Gemini confirmations and a plain [y/N] all come down to the same shape.

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Option is one choice of a prompt.
type Option struct {
	Label       string   `json:"label"`
	Description string   `json:"description,omitempty"`
	Keys        []string `json:"keys"`
	Input       bool     `json:"input,omitempty"`
}

// Parsed is a prompt as read off the screen, before it has an id.
type Parsed struct {
	Kind      string
	Title     string
	Question  string
	Detail    string
	Options   []Option
	AllowText bool
	// Fingerprint identifies the prompt, not where the cursor is in it
	// (except for a menu without numbers, where the cursor decides the keys).
	Fingerprint string
	// Everything above the options, and the options, with all whitespace
	// taken out: the same however the screen's width wrapped them. See Same.
	Body, Choices string
}

// minSuffix is how much of a question has to be left for a screen that cut
// its top off to still count as showing it.
const minSuffix = 16

// Same says whether two reads of a screen show the same question: what an
// agent redrawing it (a resize, a re-render, a narrower window wrapping it
// differently, the top scrolled away on a short one) leaves the same. Where
// the cursor is does not count.
func Same(a, b *Parsed) bool {
	if a == nil || b == nil || a.Kind != b.Kind || a.Choices != b.Choices {
		return false
	}
	if a.Body == b.Body {
		return true
	}
	short, long := a.Body, b.Body
	if len(short) > len(long) {
		short, long = long, short
	}
	return len(short) >= minSuffix && strings.HasSuffix(long, short)
}

// squeeze drops every whitespace character.
func squeeze(ss ...string) string {
	var b strings.Builder
	for _, s := range ss {
		for _, r := range s {
			if !unicode.IsSpace(r) {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// choices is the options for Same: labels and descriptions, in order.
func choices(opts []Option) string {
	var b strings.Builder
	for _, o := range opts {
		b.WriteString(squeeze(o.Label, o.Description))
		b.WriteByte(0)
	}
	return b.String()
}

var (
	// A selectable line: an optional cursor, a number, a dot, the label.
	optionRE = regexp.MustCompile(`^(\s*)([❯›▶●>]\s*)?(\d{1,2})\.\s+(\S.*?)\s*$`)
	cursorRE = regexp.MustCompile(`^\s*[❯›▶●]`)
	// A trailing single-key shortcut, as Codex prints them: "(y)", "(esc)".
	shortcutRE = regexp.MustCompile(`\s*\((esc|[a-z])\)$`)
	// The hint line under a menu.
	footerRE = regexp.MustCompile(`(?i)esc to (cancel|interrupt|exit)|enter to (select|confirm)|to navigate|press enter|ctrl\+g to edit|tab to amend`)
	ynRE     = regexp.MustCompile(`(?i)(\[y/n\]|\(y/n\)|\[yes/no\]|\(yes/no\))\s*[:?]?\s*$`)
	inputRE  = regexp.MustCompile(`(?i)type something|tell (claude|codex|gemini)|what to (do|change)|suggest changes|^other\b`)
	checkRE  = regexp.MustCompile(`^[☐☒☑✔✓□■]\s*`)
)

// isRule is a line drawn across the screen: a separator, not text.
func isRule(s string) bool {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) < 8 {
		return false
	}
	for _, r := range s {
		switch r {
		case '─', '━', '╌', '╍', '┄', '┈', '▔', '▁', '═', '-', '=':
		default:
			return false
		}
	}
	return true
}

// isHardRule is a rule that closes a region; ╌ lines only frame a detail.
func isHardRule(s string) bool {
	return isRule(s) && !strings.ContainsAny(s, "╌╍┄┈")
}

func indentOf(s string) int {
	return utf8.RuneCountInString(s) - utf8.RuneCountInString(strings.TrimLeftFunc(s, unicode.IsSpace))
}

// trimGutter drops a box-drawing gutter at the start of a detail line.
func trimGutter(s string) string {
	t := strings.TrimSpace(s)
	for _, g := range []string{"│ ", "┃ ", "| ", "│", "┃"} {
		if strings.HasPrefix(t, g) {
			return strings.TrimSpace(strings.TrimPrefix(t, g))
		}
	}
	return t
}

func screenLines(screen string) []string {
	lines := strings.Split(strings.ReplaceAll(screen, "\r", ""), "\n")
	for i := range lines {
		l := strings.TrimRightFunc(lines[i], unicode.IsSpace)
		// Inside a box (Gemini draws its prompts in one): the text only.
		if t := strings.TrimLeftFunc(l, unicode.IsSpace); strings.HasPrefix(t, "│") && strings.HasSuffix(t, "│") && len(t) > 6 {
			l = strings.TrimRightFunc(strings.TrimSuffix(strings.TrimPrefix(t, "│"), "│"), unicode.IsSpace)
		}
		lines[i] = l
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// ParseScreen reads a prompt off a captured pane, or returns nil when the
// screen is not asking anything stui knows how to answer.
func ParseScreen(screen, agent string) *Parsed {
	lines := screenLines(screen)
	if p := parseNumbered(lines, agent); p != nil {
		return p
	}
	if p := parseMenu(lines); p != nil {
		return p
	}
	return parseYN(lines)
}

type rawOption struct {
	num   int
	label string
	desc  []string
	line  int
}

func parseNumbered(lines []string, agent string) *Parsed {
	// The cursor line nearest the bottom.
	sel := -1
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-60; i-- {
		if m := optionRE.FindStringSubmatch(lines[i]); m != nil && m[2] != "" && cursorRE.MatchString(lines[i]) {
			sel = i
			break
		}
	}
	if sel < 0 {
		return nil
	}
	m := optionRE.FindStringSubmatch(lines[sel])
	numCol := indentOf(lines[sel]) + utf8.RuneCountInString(m[2])

	isOpt := func(i int) (rawOption, bool) {
		m := optionRE.FindStringSubmatch(lines[i])
		if m == nil {
			return rawOption{}, false
		}
		// Every option's number sits in the same column, cursor or not.
		col := utf8.RuneCountInString(m[1]) + utf8.RuneCountInString(m[2])
		if col != numCol && !(m[2] == "" && col == numCol) {
			return rawOption{}, false
		}
		n, _ := strconv.Atoi(m[3])
		return rawOption{num: n, label: m[4], line: i}, true
	}

	// Up to the first line that is neither an option, a continuation nor a
	// rule between options.
	start := sel
	for i := sel - 1; i >= 0; i-- {
		if _, ok := isOpt(i); ok {
			start = i
			continue
		}
		if lines[i] != "" && !isRule(lines[i]) && indentOf(lines[i]) > numCol {
			continue // a description or a wrapped label of the option above
		}
		break
	}
	// Collect downwards from start.
	var opts []rawOption
	end := start
	for i := start; i < len(lines); i++ {
		if o, ok := isOpt(i); ok {
			opts = append(opts, o)
			end = i
			continue
		}
		if len(opts) > 0 && lines[i] != "" && !isRule(lines[i]) && indentOf(lines[i]) > numCol {
			last := &opts[len(opts)-1]
			last.desc = append(last.desc, strings.TrimSpace(lines[i]))
			end = i
			continue
		}
		if isRule(lines[i]) {
			// A rule inside the list (Claude puts one before "Chat about
			// this") -- only if another option follows.
			if i+1 < len(lines) {
				if _, ok := isOpt(i + 1); ok {
					continue
				}
			}
		}
		break
	}
	if len(opts) < 2 {
		return nil
	}
	for i, o := range opts {
		if o.num != i+1 {
			return nil
		}
	}
	// Below the list: blank lines, then a hint line or the bottom of the
	// screen. Anything else means this is a numbered list in the output.
	footer := false
	for i := end + 1; i < len(lines); i++ {
		if lines[i] == "" {
			continue
		}
		if footerRE.MatchString(lines[i]) || indentOf(lines[i]) > numCol || strings.HasPrefix(strings.TrimSpace(lines[i]), "╰") {
			footer = true
		}
		break
	}
	if !footer && end < len(lines)-1 {
		trailing := 0
		for i := end + 1; i < len(lines); i++ {
			if lines[i] != "" {
				trailing++
			}
		}
		if trailing > 0 {
			return nil
		}
	}

	p := &Parsed{}
	p.Title, p.Question, p.Detail, p.Body = header(lines, start)
	for _, o := range opts {
		label := o.label
		keys := []string{strconv.Itoa(o.num)}
		if sc := shortcutRE.FindStringSubmatch(label); sc != nil {
			label = strings.TrimSpace(strings.TrimSuffix(label, sc[0]))
			if agent == "codex" {
				keys = []string{sc[1]}
				if sc[1] == "esc" {
					keys = []string{"Escape"}
				}
			}
		}
		opt := Option{Label: label, Keys: keys, Input: inputRE.MatchString(label)}
		if len(o.desc) > 0 {
			opt.Description = strings.Join(o.desc, " ")
		}
		p.Options = append(p.Options, opt)
		if opt.Input {
			p.AllowText = true
		}
	}
	p.Kind = classify(p, agent)
	// A permission's long label that wrapped is a label, not a description.
	if p.Kind == "permission" {
		for i := range p.Options {
			if d := p.Options[i].Description; d != "" {
				p.Options[i].Label += " " + d
				p.Options[i].Description = ""
			}
		}
	}
	p.Title, p.Question, p.Detail = clipText(p.Title, 120), clipText(p.Question, 500), clipText(p.Detail, 4000)
	p.Fingerprint = fingerprint(p, "")
	p.Choices = choices(p.Options)
	return p
}

// header reads what is above a list of options: a title, the question and
// the detail between them (the command, the file, the plan).
//
// With a rule above it (Claude Code draws one across the screen) the header
// is everything from that rule down; a region holding only the question --
// plan approval draws a rule right above it -- reaches to the rule before.
// Without one it is the question and whatever sits between it and the list,
// up to a blank line.
func header(lines []string, start int) (title, question, detail, body string) {
	ruleAt := -1
	for i, n := start-1, 0; i >= 0 && i >= start-60; i-- {
		if isHardRule(lines[i]) {
			has := false
			for j := i + 1; j < start; j++ {
				if strings.TrimSpace(lines[j]) != "" && !isRule(lines[j]) {
					has = true
				}
			}
			n++
			ruleAt = i
			if has && (n > 1 || !onlyQuestion(lines[i+1:start])) {
				break
			}
		}
	}
	var block []string
	if ruleAt >= 0 {
		for _, l := range lines[ruleAt+1 : start] {
			if strings.TrimSpace(l) != "" && !isRule(l) {
				block = append(block, l)
			}
		}
	} else {
		// Up to the question, then up to the blank line above it.
		i := start - 1
		for i >= 0 && strings.TrimSpace(lines[i]) == "" {
			i--
		}
		q := -1
		for j := i; j >= 0 && j >= start-12; j-- {
			if strings.HasSuffix(strings.TrimSpace(lines[j]), "?") {
				q = j
				break
			}
		}
		if q < 0 {
			q = i
		}
		from := q
		for from > 0 && strings.TrimSpace(lines[from-1]) != "" && !isRule(lines[from-1]) {
			from--
		}
		for j := from; j >= 0 && j < start; j++ {
			if strings.TrimSpace(lines[j]) != "" && !isRule(lines[j]) {
				block = append(block, lines[j])
			}
		}
	}
	if len(block) == 0 {
		return "", "", "", ""
	}
	for _, l := range block {
		body += squeeze(trimGutter(l))
	}
	qi := len(block) - 1
	for i := len(block) - 1; i >= 0; i-- {
		if strings.HasSuffix(strings.TrimSpace(block[i]), "?") {
			qi = i
			break
		}
	}
	question = strings.TrimSpace(block[qi])
	var d []string
	for i, l := range block {
		switch {
		case i == qi:
		case i == 0 && qi > 0 && ruleAt >= 0:
			title = checkRE.ReplaceAllString(strings.TrimSpace(l), "")
		default:
			d = append(d, trimGutter(l))
		}
	}
	if title == "" {
		title = question
	}
	return title, question, strings.Join(d, "\n"), body
}

// onlyQuestion is a region with one line of text in it.
func onlyQuestion(ls []string) bool {
	n := 0
	for _, l := range ls {
		if strings.TrimSpace(l) != "" && !isRule(l) {
			n++
		}
	}
	return n <= 1
}

var permissionRE = regexp.MustCompile(`(?i)^(do you want|would you like to (run|make|apply|allow)|allow |approve|apply this|proceed\?)`)

func classify(p *Parsed, agent string) string {
	text := p.Title + "\n" + p.Question + "\n" + p.Detail
	switch {
	case strings.Contains(text, "Ready to code") || strings.Contains(text, "written up a plan") || strings.Contains(p.Detail, "Here is Claude's plan"):
		return "plan"
	case permissionRE.MatchString(p.Question) || strings.Contains(p.Question, "Allow execution") || strings.Contains(p.Question, "run the following command"):
		return "permission"
	}
	for _, o := range p.Options {
		if strings.HasPrefix(o.Label, "Type something") {
			return "question"
		}
	}
	if len(p.Options) > 0 && strings.HasPrefix(p.Options[0].Label, "Yes") {
		return "confirm"
	}
	return "question"
}

// parseMenu reads a cursor menu without numbers, like Claude Code's
// "trust this folder" dialog. The keys depend on where the cursor is.
func parseMenu(lines []string) *Parsed {
	sel := -1
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-30; i-- {
		if cursorRE.MatchString(lines[i]) {
			sel = i
			break
		}
	}
	if sel < 0 {
		return nil
	}
	cur := strings.TrimLeftFunc(lines[sel], unicode.IsSpace)
	_, size := utf8.DecodeRuneInString(cur)
	text := strings.TrimSpace(cur[size:])
	col := indentOf(lines[sel]) + 1 + (len(cur[size:]) - len(strings.TrimLeft(cur[size:], " ")))
	sibling := func(i int) bool {
		return lines[i] != "" && !isRule(lines[i]) && !cursorRE.MatchString(lines[i]) && indentOf(lines[i]) == col
	}
	start, end := sel, sel
	for start > 0 && sibling(start-1) {
		start--
	}
	for end+1 < len(lines) && sibling(end+1) {
		end++
	}
	if end-start < 1 {
		return nil
	}
	// Needs its hint line, or the prompt box of an idle agent passes too.
	hint := false
	for i := end + 1; i < len(lines) && i <= end+3; i++ {
		if lines[i] != "" && footerRE.MatchString(lines[i]) {
			hint = true
			break
		}
	}
	if !hint {
		return nil
	}
	p := &Parsed{Kind: "confirm"}
	for i := start; i <= end; i++ {
		label := strings.TrimSpace(lines[i])
		if i == sel {
			label = text
		}
		var keys []string
		for j := i; j < sel; j++ {
			keys = append(keys, "Up")
		}
		for j := sel; j < i; j++ {
			keys = append(keys, "Down")
		}
		keys = append(keys, "Enter")
		p.Options = append(p.Options, Option{Label: label, Keys: keys})
	}
	var above []string
	for i := start - 1; i >= 0 && len(above) < 8; i-- {
		if isHardRule(lines[i]) {
			break
		}
		if t := strings.TrimSpace(lines[i]); t != "" {
			above = append([]string{t}, above...)
		}
	}
	for _, l := range above {
		if strings.Contains(l, "?") && p.Question == "" {
			p.Question = l
		}
	}
	if len(above) > 0 {
		p.Title = above[0]
	}
	if p.Question == "" && len(above) > 0 {
		p.Question = above[len(above)-1]
	}
	p.Detail = strings.Join(above, "\n")
	p.Body = squeeze(above...)
	p.Title, p.Question, p.Detail = clipText(p.Title, 120), clipText(p.Question, 500), clipText(p.Detail, 4000)
	p.Fingerprint = fingerprint(p, strconv.Itoa(sel-start))
	// The labels without the cursor: the same menu wherever the cursor is.
	p.Choices = choices(p.Options)
	return p
}

// parseYN reads a plain [y/N] question on the cursor line.
func parseYN(lines []string) *Parsed {
	if len(lines) == 0 {
		return nil
	}
	last := strings.TrimSpace(lines[len(lines)-1])
	m := ynRE.FindStringSubmatch(last)
	if m == nil {
		return nil
	}
	yes, no := "y", "n"
	if strings.Contains(strings.ToLower(m[1]), "yes") {
		yes, no = "yes", "no"
	}
	p := &Parsed{
		Kind:     "confirm",
		Title:    clipText(last, 120),
		Question: clipText(last, 500),
		Options: []Option{
			{Label: "Yes", Keys: []string{yes, "Enter"}},
			{Label: "No", Keys: []string{no, "Enter"}},
		},
	}
	p.Fingerprint = fingerprint(p, "")
	p.Body, p.Choices = squeeze(last), choices(p.Options)
	return p
}

func clipText(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func fingerprint(p *Parsed, extra string) string {
	h := sha256.New()
	for _, s := range []string{p.Kind, p.Title, p.Question, extra} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	for _, o := range p.Options {
		h.Write([]byte(o.Label))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:12])
}
