// stui is tmux, the coding agents in it and their CI, in one terminal UI,
// on one machine. The UI, the server it leaves running and the commands the
// agents call are the same executable.
package main

import (
	"fmt"
	"os"

	"github.com/sophonz-labs/stui/internal/cli"
	"github.com/sophonz-labs/stui/internal/home"
	"github.com/sophonz-labs/stui/internal/hookclient"
	"github.com/sophonz-labs/stui/internal/mux"
	"github.com/sophonz-labs/stui/internal/server"
	"github.com/sophonz-labs/stui/internal/usage"
)

// Stamped in at build time; a development build has no release to name.
var version = "dev"

func main() {
	// Before anything runs tmux (mux/locale.go).
	mux.EnsureUTF8()
	if len(os.Args) > 2 && (os.Args[1] == "hook" || os.Args[1] == "statusline") && (os.Args[2] == "--help" || os.Args[2] == "-h") {
		fmt.Printf("stui %s is run by coding agents (stui hooks install puts it there), not by hand\n", os.Args[1])
		return
	}
	// stui hook runs on every hook of a coding agent, which waits for it.
	if len(os.Args) > 1 && os.Args[1] == "hook" {
		hookclient.Run(os.Args[2:], os.Stdin)
		os.Exit(0)
	}
	// Claude Code's status line, after stui hooks install --usage.
	if len(os.Args) > 1 && os.Args[1] == "statusline" {
		os.Exit(usage.RunStatusline(home.Dir(), os.Stdin, os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "__serve" {
		server.Version = version
		os.Exit(server.Run(os.Args[2:]))
	}
	cli.Run(version, os.Args[1:])
}
