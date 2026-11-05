package proc_test

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// Every console child stui starts on Windows must be hidden.
//
// The agent runs without a console of its own, and Windows gives a console
// program started from such a process a window of its own. Anything stui starts
// on a timer therefore flashes a window open and shut, over and over, for as
// long as stui runs. It is a bug made of an absence, which is why it has come
// back twice: once through a tmux query that needed its own environment and
// built its own exec.Command, and once through a new package that wrote its
// own runner. Neither diff showed anything missing.
//
// So this walks what the Windows build would actually compile and insists
// that a function starting a process also hides it. Anything that genuinely
// does not need it is listed below with the reason.
func TestEveryConsoleChildIsHiddenOnWindows(t *testing.T) {
	// path:function -> why it is allowed to start a process bare.
	allowed := map[string]string{
		"main.go:main": "stui tmux forwards the user terminal to an explicitly invoked tmux client, never a background timer",
		// Run by a person, from a terminal that already exists. A child
		// inherits that console and opens no window of its own.
		"internal/cli/cli.go:attachTo":        "the user's own terminal",
		"internal/cli/cli.go:upgradeCommand":  "the user's own terminal",
		"internal/cli/keep.go:fallBack":       "the user's own terminal",
		"internal/cli/service.go:run":         "the user's own terminal",
		"internal/ui/ui.go:stCmd":             "stui ui runs in the user's own terminal",
		"internal/ui/attach.go:serverSocket":  "stui ui runs in the user's own terminal",
		"internal/ui/attach.go:locate":        "stui ui runs in the user's own terminal",
		"internal/ui/attach.go:attachTo":      "stui ui runs in the user's own terminal",
		"internal/ui/ci.go:open":              "stui ui runs in the user's own terminal",
		"internal/ui/plugins.go:runExec":      "stui ui runs in the user's own terminal",
		"internal/ui/plugins.go:popup":        "stui ui runs in the user's own terminal",
		"internal/config/config.go:editorCmd": "stui config edit runs in the user's own terminal",

		// stui service is launchd and systemd only; there is no Windows service,
		// so keepalive never runs there.
		"internal/cli/service.go:keepalive": "launchd/systemd only",

		// Unix-only programs. On Windows the exec fails before a process
		// exists, so there is no window to hide.
		"internal/agent/ttydreap.go:psList":         "/bin/ps",
		"internal/cmdicon/argv.go:ForegroundArgs":   "ps",
		"internal/restore/snapshot.go:psForeground": "ps",
		"internal/agent/watchdog.go:runShort":       "lsof; and muxServerPID returns before it on Windows",
		"internal/health/read.go:run":               "read_windows.go measures nothing, so it never calls this",

		// proc.NeedsGuard() is false on Windows (parent_other.go), so this
		// never starts anything there.
		"internal/proc/guardchild.go:GuardChild": "no guard is started on Windows",

		// osinfo asks the OS directly on Windows (RtlGetVersion), so
		// command() is only ever reached on Unix.
		"internal/osinfo/osinfo.go:command": "Windows goes through RtlGetVersion",
	}

	root := filepath.Join("..", "..")
	win := build.Context{GOOS: "windows", GOARCH: "amd64", Compiler: build.Default.Compiler}
	var bare []string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			// tools/ are developer programs run by hand, never by the agent.
			if name == "tools" || name == "vendor" || name == "node_modules" ||
				(strings.HasPrefix(name, ".") && name != "." && name != "..") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		// Build tags and filename suffixes both: this is what Windows compiles.
		if ok, err := win.MatchFile(filepath.Dir(path), name); err != nil || !ok {
			return nil
		}

		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			starts, hides := false, false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, _ := sel.X.(*ast.Ident)
				switch {
				case pkg != nil && pkg.Name == "exec" &&
					(sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext"):
					starts = true
				case sel.Sel.Name == "Hide" || sel.Sel.Name == "Detach":
					hides = true
				}
				return true
			})
			if starts && !hides {
				if _, ok := allowed[rel+":"+fn.Name.Name]; !ok {
					bare = append(bare, rel+":"+fn.Name.Name)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, b := range bare {
		t.Errorf("%s starts a process without proc.Hide or proc.Detach.\n"+
			"    On Windows that flashes a console window. Hide it, or add it to the\n"+
			"    list in this test with the reason it cannot.", b)
	}
}
