package watch

import (
	"regexp"
	"strings"
)

// What each agent draws while it is running a turn, as captured from the
// real programs (testdata/screens) or read from their source.
var (
	// Claude Code's spinner line: a glyph from its spinner set, a verb and
	// an ellipsis, then "(3s · ↓ 12 tokens · thinking)" once it has a
	// count: "✽ Baking… (1s · …)", "· Crafting…". With a custom statusLine
	// the footer's "esc to interrupt" is not drawn, so this line is the one
	// that is always there. "✻ Waiting for 1 background agent to finish"
	// (main idle, a background agent running) and "✻ Baked for 11s ·
	// done" have no ellipsis.
	claudeSpinnerRE = regexp.MustCompile(`^ ?[·✢✳✶✻✽*] [A-Z][^…]{0,60}…(\s|$)`)
	// Claude's footer while a turn runs, when no statusLine replaces it:
	// "⏸ manual mode on · esc to interrupt · ← for agents". Read only below
	// the input box: the words anywhere in the conversation are not it.
	escInterruptRE = regexp.MustCompile(`(?i)^\s*(\S.*· )?esc to interrupt( ·|$)`)
	// Claude, right after Stop: "✳ Newspapering… (running Stop hooks… 1/2 ·
	// 1s …)". The turn is ending unless a Stop hook blocks it, in which case
	// the spinner goes on without this.
	stopHooksRE = regexp.MustCompile(`(?i)running stop hooks`)
	// Codex's status indicator: "• Working (0s • esc to interrupt)". Only
	// with the elapsed time: its question UI lists "esc to interrupt" among
	// its key hints too.
	codexBusyRE = regexp.MustCompile(`(?i)\((\d+[hms]\s*)+•\s*esc to interrupt\)`)
	// Gemini CLI's loading indicator: "(esc to cancel, 5s)", "(esc to
	// cancel, 1m 5s)".
	geminiBusyRE = regexp.MustCompile(`\(esc to cancel, [^)]*\)`)
	// Qwen Code's loading indicator: "(5s · esc to cancel)", "(1m 5s · ↓ 1.2k
	// tokens · esc to cancel)" (LoadingIndicator.tsx; in no locale file, so
	// English in every language). Not Gemini's, which it was forked from.
	qwenBusyRE = regexp.MustCompile(`\((?:\d+[hms]\s*)+[^()]*·\s*esc to cancel\)`)
	// opencode's footer while it works (1.19): its blocks, then "esc
	// interrupt", or "esc again to interrupt" after one Esc. In a narrow
	// pane the label folds and only "esc" stays on the blocks' line, so
	// that is all it asks for; the blocks keep prose and bar charts out.
	opencodeBusyRE = regexp.MustCompile(`^\s*[⬝■]{4,}\s+esc\b`)
	// Pi's while it works (1.4.0): the rule over its input box carries a
	// spinner and a label, "── ⠇ Working ───…" ("Compacting context", or
	// an extension's own words). Idle, the rule is bare. The rule on both
	// sides is what makes it the box's, not a line of the conversation.
	piBusyRE = regexp.MustCompile(`^─{2,} [\x{2801}-\x{28FF}] \S.* ─{3,}$`)
	// Claude's input box: a rule, the ❯ line, a rule.
	ruleRE = regexp.MustCompile(`^─{20,}$`)
)

// busyTitle is Claude Code's terminal title while it animates: "◐ topic",
// "◑ topic", and "✳ topic" otherwise. Under tmux Claude keeps the title
// static ("✳") unless its tengu_static_title_under_mux flag is off, so the
// title can say busy but never idle.
func busyTitle(title string) bool {
	return strings.HasPrefix(title, "◐") || strings.HasPrefix(title, "◑")
}

// screenState is what the bottom of a pane says about the turn.
type screenState struct {
	// A turn is in progress on screen.
	busy bool
	// Claude is running its Stop hooks: the turn that just ended, still
	// drawing its spinner.
	stopping bool
	// Claude's empty input box is on screen, for an idle Claude to be told
	// from a screen stui cannot read (a full-screen dialog).
	inputBox bool
}

// readScreen reads what agent's screen (and title) say about the turn.
func readScreen(screen, title, agent string) screenState {
	lines := screenLines(screen)
	if len(lines) > 15 {
		lines = lines[len(lines)-15:]
	}
	var s screenState
	claude := agent == "claude" || agent == "" || agent == "other"
	// Where Claude's footer starts: below its input box, or the last lines.
	footer := max(len(lines)-3, 0)
	for i := range lines {
		if i+2 < len(lines) && ruleRE.MatchString(strings.TrimSpace(lines[i])) &&
			strings.HasPrefix(lines[i+1], "❯") && ruleRE.MatchString(strings.TrimSpace(lines[i+2])) {
			s.inputBox = claude
			footer = i + 3
		}
	}
	for i, l := range lines {
		if claude {
			if claudeSpinnerRE.MatchString(l) {
				s.busy = true
				if stopHooksRE.MatchString(l) {
					s.stopping = true
				}
			}
			if i >= footer && escInterruptRE.MatchString(l) {
				s.busy = true
			}
		}
		if agent != "claude" && agent != "gemini" && codexBusyRE.MatchString(l) {
			s.busy = true
		}
		if agent != "claude" && agent != "codex" && geminiBusyRE.MatchString(l) {
			s.busy = true
		}
		if agent == "qwen" && qwenBusyRE.MatchString(l) {
			s.busy = true
		}
		if agent == "opencode" && opencodeBusyRE.MatchString(l) {
			s.busy = true
		}
		if agent == "pi" && piBusyRE.MatchString(strings.TrimRight(l, " ")) {
			s.busy = true
		}
	}
	if claude && busyTitle(title) {
		s.busy = true
	}
	if s.stopping {
		// Nothing else on screen says busy but the ending turn.
		s.busy = true
	}
	return s
}

// isWorking is whether the bottom of the screen shows a turn in progress,
// for any agent.
func isWorking(screen string) bool {
	return readScreen(screen, "", "").busy
}
