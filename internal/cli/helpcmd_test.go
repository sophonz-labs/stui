package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// runCommands is every command Run's switch takes, read from cli.go: a
// command added there is checked here without anyone remembering to.
func runCommands(t *testing.T) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "cli.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var cmds []string
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Run" {
			return true
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if cc, ok := n.(*ast.CaseClause); ok {
				for _, e := range cc.List {
					if lit, ok := e.(*ast.BasicLit); ok {
						s, _ := strconv.Unquote(lit.Value)
						cmds = append(cmds, s)
					}
				}
			}
			return true
		})
		return false
	})
	if len(cmds) < 15 {
		t.Fatalf("read only %d commands from Run", len(cmds))
	}
	return cmds
}

// Every command answers --help and -h with help and runs nothing; stui's own
// (__…) are left to run.
func TestEveryCommandTakesHelp(t *testing.T) {
	for _, cmd := range runCommands(t) {
		for _, h := range []string{"--help", "-h"} {
			a := args{h}
			if cmd != "" {
				a = args{cmd, h}
			}
			text, ok := helpFor(a)
			if strings.HasPrefix(cmd, "__") {
				if ok {
					t.Errorf("%s %s: stui's own command answered help", cmd, h)
				}
				continue
			}
			if !ok || strings.TrimSpace(text) == "" {
				t.Errorf("stui %s %s would run the command", cmd, h)
			}
		}
	}
}

func TestHelpFor(t *testing.T) {
	for _, c := range []struct {
		argv []string
		want string // in the text; "" for no help at all
	}{
		{[]string{"stop", "--help"}, "stui stop"},
		{[]string{"exit", "--help"}, "stui stop"},
		{[]string{"worker", "stop", "%3", "--yes", "--help"}, "stui worker"},
		{[]string{"hooks", "uninstall", "-h"}, "stui hooks uninstall"},
		{[]string{"--session", "x", "--help"}, "stui — tmux"},
		{[]string{"help", "ci"}, "stui ci"},
		{[]string{"help"}, "stui — tmux"},
		// Their own, not the orchestration help.
		{[]string{"plugin", "--help"}, "stui plugin uninstall"},
		{[]string{"skills", "-h"}, "stui skills get worker"},
		{[]string{"hooks", "--help"}, "stui hooks uninstall --usage"},
		{[]string{"usage", "--help"}, "stui usage guard"},
		// --help as a flag's value, or not there: the command runs.
		{[]string{"worker", "done", "--summary", "--help"}, ""},
		{[]string{"worker", "send", "%3", "read the --help output"}, ""},
		{[]string{"status"}, ""},
	} {
		text, ok := helpFor(args(c.argv))
		switch {
		case c.want == "" && ok:
			t.Errorf("%v: help instead of running", c.argv)
		case c.want != "" && (!ok || !strings.Contains(text, c.want)):
			t.Errorf("%v: %v %q", c.argv, ok, text)
		}
	}
	// One command's lines, not the whole help.
	if text, _ := helpFor(args{"stop", "--help"}); strings.Contains(text, "stui hooks") {
		t.Errorf("stop's help is everything:\n%s", text)
	}
}

// stui's help names what the product is for, and fits an 80-column terminal.
func TestTopHelp(t *testing.T) {
	for _, want := range []string{"stui orch start", "stui worker", "stui plugin install",
		"stui skills get", "stui hooks install", "stui ci"} {
		if !strings.Contains(help, want) {
			t.Errorf("stui --help does not say %q", want)
		}
	}
	for _, l := range strings.Split(help, "\n") {
		if n := len([]rune(l)); n > 80 {
			t.Errorf("%d columns: %s", n, l)
		}
	}
	for _, h := range []string{pluginHelp, skillsHelp} {
		if strings.Contains(h, "stui orchestration --") {
			t.Error("plugin or skills help is the orchestration help")
		}
	}
}
