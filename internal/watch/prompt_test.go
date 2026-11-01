package watch

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func screen(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "screens", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type wantPrompt struct {
	kind, title, question string
	detailHas             string
	labels                []string
	keys                  [][]string
	input                 []int
}

func check(t *testing.T, name string, p *Parsed, w wantPrompt) {
	t.Helper()
	if p == nil {
		t.Fatalf("%s: no prompt", name)
	}
	if p.Kind != w.kind || p.Title != w.title || p.Question != w.question {
		t.Errorf("%s: kind %q title %q question %q", name, p.Kind, p.Title, p.Question)
	}
	if !strings.Contains(p.Detail, w.detailHas) {
		t.Errorf("%s: detail %q", name, p.Detail)
	}
	var labels []string
	var keys [][]string
	var input []int
	for i, o := range p.Options {
		labels = append(labels, o.Label)
		keys = append(keys, o.Keys)
		if o.Input {
			input = append(input, i)
		}
	}
	if !reflect.DeepEqual(labels, w.labels) {
		t.Errorf("%s: labels %q", name, labels)
	}
	if !reflect.DeepEqual(keys, w.keys) {
		t.Errorf("%s: keys %q", name, keys)
	}
	if !reflect.DeepEqual(input, w.input) {
		t.Errorf("%s: input %v", name, input)
	}
	if p.AllowText != (len(w.input) > 0) {
		t.Errorf("%s: allowText %v", name, p.AllowText)
	}
}

func k(ks ...string) []string { return ks }

// Captured from Claude Code 2.1.283 in a sandbox tmux: a real permission
// prompt, a file write, a narrow window, AskUserQuestion, plan approval and
// the folder trust dialog.
func TestParseClaudeScreens(t *testing.T) {
	check(t, "bash", ParseScreen(screen(t, "claude-permission-bash.txt"), "claude"), wantPrompt{
		kind: "permission", title: "Bash command", question: "Do you want to proceed?",
		detailHas: "echo hello > greeting.txt && ls -la",
		labels:    []string{"Yes", "Yes, and always allow access to /private/tmp/aw/proj from this project", "No"},
		keys:      [][]string{k("1"), k("2"), k("3")},
	})
	check(t, "write", ParseScreen(screen(t, "claude-permission-write.txt"), "claude"), wantPrompt{
		kind: "permission", title: "Create file", question: "Do you want to create notes.md?",
		detailHas: "notes.md",
		labels:    []string{"Yes", "Yes, and switch to accept edits (auto-approve file edits and common file commands) for this session (shift+tab)", "No"},
		keys:      [][]string{k("1"), k("2"), k("3")},
	})
	check(t, "narrow", ParseScreen(screen(t, "claude-permission-narrow.txt"), "claude"), wantPrompt{
		kind: "permission", title: "Bash command", question: "Do you want to proceed?",
		detailHas: "for i in 1 2 3; do echo\nline-$i-with-a-rather-long-suffix-to-force-wrapping;\ndone | tee out.txt | wc -l",
		labels:    []string{"Yes", "No"},
		keys:      [][]string{k("1"), k("2")},
	})
	q := ParseScreen(screen(t, "claude-question.txt"), "claude")
	check(t, "question", q, wantPrompt{
		kind: "question", title: "Logo Color", question: "Which color do you prefer for the logo?",
		labels: []string{"Red", "Green", "Blue", "Type something.", "Chat about this"},
		keys:   [][]string{k("1"), k("2"), k("3"), k("4"), k("5")},
		input:  []int{3},
	})
	if q.Options[1].Description != "Fresh and natural, represents growth, balance, and trust" {
		t.Errorf("description %q", q.Options[1].Description)
	}
	check(t, "plan", ParseScreen(screen(t, "claude-plan.txt"), "claude"), wantPrompt{
		kind: "plan", title: "Ready to code?",
		question:  "Claude has written up a plan and is ready to execute. Would you like to proceed?",
		detailHas: "Plan: Add README.md to /private/tmp/aw/proj",
		labels:    []string{"Yes, auto-accept edits", "Yes, manually approve edits", "Tell Claude what to change"},
		keys:      [][]string{k("1"), k("2"), k("3")},
		input:     []int{2},
	})
	check(t, "trust", ParseScreen(screen(t, "claude-trust.txt"), "claude"), wantPrompt{
		kind: "confirm", title: "Accessing workspace:",
		question:  "Quick safety check: Is this a project you created or one you trust? (Like your own code, a well-known open source",
		detailHas: "/private/tmp/aw/proj",
		labels:    []string{"No, exit", "Yes, I trust this folder"},
		keys:      [][]string{k("Enter"), k("Down", "Enter")},
	})
}

func TestParseOtherAgents(t *testing.T) {
	check(t, "codex", ParseScreen(screen(t, "codex-approval.txt"), "codex"), wantPrompt{
		kind: "permission", title: "Would you like to run the following command?",
		question: "Would you like to run the following command?", detailHas: "$ echo hi > hello.txt",
		labels: []string{"Yes, proceed", "Yes, and don't ask again for this command", "No, and tell Codex what to do differently"},
		keys:   [][]string{k("y"), k("a"), k("Escape")},
		input:  []int{2},
	})
	check(t, "gemini", ParseScreen(screen(t, "gemini-confirm.txt"), "gemini"), wantPrompt{
		kind: "permission", title: "Allow execution of: 'rm'?", question: "Allow execution of: 'rm'?",
		detailHas: "rm -rf build",
		labels:    []string{"Yes, allow once", "Yes, allow always ...", "No, suggest changes"},
		keys:      [][]string{k("1"), k("2"), k("3")},
		input:     []int{2},
	})
	check(t, "yn", ParseScreen(screen(t, "shell-yn.txt"), "other"), wantPrompt{
		kind: "confirm", title: "Do you want to continue? [Y/n]", question: "Do you want to continue? [Y/n]",
		labels: []string{"Yes", "No"},
		keys:   [][]string{k("y", "Enter"), k("n", "Enter")},
	})
}

// Screens that ask nothing: an idle prompt box, a turn in progress, a
// finished turn, and a numbered list in the agent's own output.
func TestParseNothing(t *testing.T) {
	for _, name := range []string{"claude-idle.txt", "claude-working.txt", "claude-done.txt", "claude-output-list.txt"} {
		if p := ParseScreen(screen(t, name), "claude"); p != nil {
			t.Errorf("%s: %+v", name, p)
		}
	}
}

// Moving the cursor is not a new prompt; a different prompt is.
func TestFingerprint(t *testing.T) {
	s := screen(t, "claude-permission-bash.txt")
	moved := strings.Replace(strings.Replace(s, " ❯ 1. Yes", "   1. Yes", 1), "   2. Yes,", " ❯ 2. Yes,", 1)
	a, b := ParseScreen(s, "claude"), ParseScreen(moved, "claude")
	if b == nil || a.Fingerprint != b.Fingerprint {
		t.Fatal("cursor moved, fingerprint changed")
	}
	c := ParseScreen(screen(t, "claude-permission-write.txt"), "claude")
	if a.Fingerprint == c.Fingerprint {
		t.Fatal("different prompts, same fingerprint")
	}
}

// A question wrapped by a narrower window, or with its top scrolled off a
// short one, is the same question; another one is not.
func TestSameAcrossWidths(t *testing.T) {
	q := "Which of these should the new settings page use for storing the user's notification preferences on the server?"
	draw := func(width int, top bool) string {
		var b strings.Builder
		if top {
			b.WriteString(strings.Repeat("─", width) + "\n ☐ Storage\n\n")
		}
		line := " "
		for _, word := range strings.Fields(q) {
			if len(line)+len(word)+1 > width {
				b.WriteString(line + "\n")
				line = " "
			}
			if line != " " {
				line += " "
			}
			line += word
		}
		b.WriteString(line + "\n\n")
		b.WriteString(" ❯ 1. Postgres\n      A table per account, read on every send\n   2. Redis\n   3. Type something.\n\n Enter to select · ↑/↓ to navigate · Esc to cancel\n")
		return b.String()
	}
	wide, narrow, cut := ParseScreen(draw(160, true), "claude"), ParseScreen(draw(48, true), "claude"), ParseScreen(draw(48, false), "claude")
	if wide == nil || narrow == nil || cut == nil {
		t.Fatalf("not read: %v %v %v", wide, narrow, cut)
	}
	if wide.Fingerprint == narrow.Fingerprint {
		t.Log("(the fingerprint alone changes with the width)")
	}
	if !Same(wide, narrow) || !Same(narrow, cut) || !Same(wide, cut) {
		t.Fatalf("one question, read as different ones:\n%q\n%q\n%q", wide.Body, narrow.Body, cut.Body)
	}
	if other := ParseScreen(strings.Replace(draw(160, true), "2. Redis", "2. SQLite", 1), "claude"); Same(wide, other) {
		t.Fatal("different options, same question")
	}
	a, b := ParseScreen(screen(t, "claude-permission-bash.txt"), "claude"), ParseScreen(screen(t, "claude-permission-write.txt"), "claude")
	if Same(a, b) {
		t.Fatal("two permissions, same question")
	}
}
