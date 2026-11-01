package chat

import (
	"os"
	"strings"
	"testing"
)

// The fixtures are the bottom of real screens, captured with
// `capture-pane -p -e` from Claude Code 2.1.294, 2.1.85 and Codex 0.159.3 in
// a sandbox tmux (nothing submitted), the folder and model names replaced.
// claude-2.1.85-suggestion is a worker's screen on a build machine: the
// prompt suggestion after a turn, which stui once read as typed and so
// refused to start the worker (409).
func TestReadInputRealScreens(t *testing.T) {
	cases := []struct {
		file, agent, want string
	}{
		{"claude-2.1.294-idle", "claude", ""},
		{"claude-2.1.294-typed", "claude", "hello world"},
		{"claude-2.1.294-multi", "claude", "hello world\nsecond line"},
		{"claude-2.1.294-cu1", "claude", "hello world"},
		{"claude-2.1.294-cuall", "claude", ""},
		{"claude-2.1.294-slash", "claude", "/comp"},
		// 2.1.85 draws its own cursor, a reverse-video cell (input.go).
		{"claude-2.1.85-idle", "claude", ""},
		{"claude-2.1.85-placeholder", "claude", ""},
		{"claude-2.1.85-suggestion", "claude", ""},
		{"claude-2.1.85-typed", "claude", "hello world"},
		{"claude-2.1.85-cu1", "claude", "hello world"},
		{"claude-2.1.85-cuhome", "claude", "hello world"},
		{"claude-2.1.85-one", "claude", "s"},
		// One letter typed and stepped back onto: the cursor is on it and
		// nothing follows. It is typed, though it looks like a placeholder's
		// first letter.
		{"claude-2.1.85-one-left", "claude", "s"},
		{"claude-2.1.85-stepback", "claude", "Tx"},
		{"codex-0.159.3-idle", "codex", ""},
		{"codex-0.159.3-typed", "codex", "hello world"},
		{"codex-0.159.3-multi", "codex", "hello world\nsecond line"},
		{"codex-0.159.3-cu1", "codex", "hello world"},
		{"codex-0.159.3-cuall", "codex", ""},
		{"codex-0.159.3-slash", "codex", "/stat"},
		// opencode 1.19.4, in a sandbox tmux: the home screen's box, and the
		// box under a conversation (whose messages are "┃" runs as well).
		{"opencode-1.19.4-idle", "opencode", ""},
		{"opencode-1.19.4-typed", "opencode", "hello world"},
		{"opencode-1.19.4-multi", "opencode", "hello world\nafter M-Enter\nafter C-j"},
		{"opencode-1.19.4-conv-idle", "opencode", ""},
		{"opencode-1.19.4-conv-typed", "opencode", "after S-Entersecond line here"},
		// Pi 1.4.0, in a sandbox tmux: the box between two rules, on its
		// start screen and under a conversation; typed while it works (the
		// spinner in the upper rule); a command's completions below it.
		{"pi-1.4.0-idle", "pi", ""},
		{"pi-1.4.0-typed", "pi", "hello world"},
		{"pi-1.4.0-conv-idle", "pi", ""},
		{"pi-1.4.0-conv-typed", "pi", "hello world"},
		{"pi-1.4.0-conv-multi", "pi", "hello world\nsecond line"},
		{"pi-1.4.0-busy-typed", "pi", "typed while busy"},
		{"pi-1.4.0-slash", "pi", "/mo"},
	}
	for _, c := range cases {
		b, err := os.ReadFile("testdata/input/" + c.file + ".ansi")
		if err != nil {
			t.Fatal(err)
		}
		got := ReadInput(string(b), c.agent)
		if !got.Found || got.Text != c.want {
			t.Errorf("%s: got %+v, want %q", c.file, got, c.want)
		}
	}
}

func TestReadInputSynthetic(t *testing.T) {
	rule := "\x1b[38;5;244m" + strings.Repeat("─", 40) + "\n"
	cases := []struct {
		name, agent, screen string
		want                InputBox
	}{
		{"no box", "claude", "just a shell $ \n", InputBox{}},
		// opencode's permission dialog has "┃" lines and no "╹▀" edge.
		{"opencode permission dialog", "opencode", "  ┃  △ Permission required\n  ┃  $ date\n  ┃   Allow once   Allow always   Reject\n", InputBox{}},
		{"box without its closing rule", "claude", rule + "\x1b[39m❯ hi\n", InputBox{}},
		{"claude grey placeholder", "claude", rule + "\x1b[39m❯ \x1b[38;2;153;153;153mTry something\x1b[39m\n" + rule, InputBox{Found: true}},
		// 2.1.303 in a tmux started without a config draws its own cursor
		// over the placeholder's first letter, as 2.1.85 does.
		{"claude 2.1.303 drawn cursor on the placeholder", "claude", rule + "\x1b[39m❯\u00a0\x1b[7mT\x1b[0;2mry \"edit <filepath> to...\"\x1b[0m\n" + rule, InputBox{Found: true}},
		{"claude coloured typed text is typed", "claude", rule + "\x1b[39m❯ \x1b[38;5;211m!ls\x1b[39m\n" + rule, InputBox{Found: true, Text: "!ls"}},
		{"claude quoted box higher up is not it", "claude",
			rule + "\x1b[39m❯ old\n" + rule + "conversation\n" + rule + "\x1b[39m❯ \n" + rule, InputBox{Found: true}},
		{"codex history line above the box", "codex",
			"› an earlier prompt\n\nreply\n\n\x1b[1m›\x1b[0m \x1b[2mAsk Codex\x1b[0m\n", InputBox{Found: true}},
		{"codex footer is not a line of the box", "codex",
			"\x1b[1m›\x1b[0m fix it\n  \x1b[38;2;246;226;183mmodel\x1b[39m · ~/p\n", InputBox{Found: true, Text: "fix it"}},
		{"unknown agent", "gemini", "> hi\n", InputBox{}},
		// Claude Code's shell mode, as 2.1.294 draws it: a command typed, or
		// the mode on with nothing typed yet. Either way a message is not
		// typed into it without asking.
		{"claude shell mode", "claude", "\x1b[38;5;211m" + strings.Repeat("─", 40) + "\n! \x1b[39mls\n\x1b[38;5;211m" + strings.Repeat("─", 40) + "\n",
			InputBox{Found: true, Text: "!ls"}},
		{"claude shell mode, empty", "claude", "\x1b[38;5;211m" + strings.Repeat("─", 40) + "\n! \x1b[2m\x1b[39mTry \"x\"\x1b[0m\n\x1b[38;5;211m" + strings.Repeat("─", 40) + "\n",
			InputBox{Found: true, Text: "!"}},
	}
	for _, c := range cases {
		if got := ReadInput(c.screen, c.agent); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

// A refusal says why in words a person can act on, not "409 refused".
func TestRefusalWords(t *testing.T) {
	cases := []struct {
		res  map[string]any
		want string
	}{
		{map[string]any{"error": "refused", "reason": BusyInput, "typed": "hello\nworld"}, `its input box has text in it ("hello world"), which stui does not type over`},
		{map[string]any{"error": "refused", "reason": NoInput}, "no input box on its screen: a trust, login or other full-screen prompt there?"},
		{map[string]any{"error": "refused", "reason": PromptPending}, "it is asking something first"},
		{map[string]any{"error": "refused", "reason": "something-new"}, "409 refused (something-new)"},
		{map[string]any{}, "409"},
	}
	for _, c := range cases {
		if got := RefusalWords(409, c.res); got != c.want {
			t.Errorf("%v: %q", c.res, got)
		}
	}
}

// A dialog Pi's extension opened sits where the box was, between the same
// rules: no box, so nothing is typed into the dialog.
func TestReadInputPiDialog(t *testing.T) {
	b, err := os.ReadFile("testdata/input/pi-1.4.0-confirm.ansi")
	if err != nil {
		t.Fatal(err)
	}
	if got := ReadInput(string(b), "pi"); got.Found {
		t.Errorf("a box in the dialog: %+v", got)
	}
}
