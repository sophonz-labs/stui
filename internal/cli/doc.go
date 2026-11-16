package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/sophonz-labs/stui/internal/proc"
	"github.com/sophonz-labs/stui/internal/vault"
)

const docHelp = `stui doc -- documents kept on this machine, for the workspace's Documents tab

  stui doc <file>       keep a copy of a markdown file, in the folder of this
                        pane's session and window (outside tmux: the top)
  stui doc ls           the documents: path, title, when they changed
  stui doc cat <path>   print one
  stui doc rm <path>    delete one

They are files under ~/.stui/vault/<session>/<window>/. Nothing is sent
anywhere. The same text kept again is the same document.
`

func docCommand(rest []string) {
	if len(rest) == 0 {
		fmt.Print(docHelp)
		return
	}
	switch rest[0] {
	case "ls", "list":
		docs, err := vault.List()
		if err != nil {
			fail(err)
		}
		if len(docs) == 0 {
			fmt.Println("no documents (stui doc <file>)")
			return
		}
		for _, d := range docs {
			fmt.Printf("%-40s  %-30s  %s\n", d.Path, d.Title, d.Mod.Format("2006-01-02 15:04"))
		}
	case "cat", "show":
		if len(rest) < 2 {
			fail(fmt.Errorf("stui doc cat <path> (stui doc ls lists them)"))
		}
		b, err := vault.Read(rest[1])
		if err != nil {
			fail(err)
		}
		os.Stdout.Write(b)
	case "rm", "delete":
		if len(rest) < 2 {
			fail(fmt.Errorf("stui doc rm <path> (stui doc ls lists them)"))
		}
		if err := vault.Remove(rest[1]); err != nil {
			fail(err)
		}
		fmt.Println("deleted", rest[1])
	default:
		session, window := paneFolder()
		p, err := vault.Keep(rest[0], session, window)
		if err != nil {
			fail(err)
		}
		fmt.Println("kept", p)
	}
}

// paneFolder is the session and window of the pane this runs in, "" outside
// tmux.
func paneFolder() (session, window string) {
	pane := os.Getenv("TMUX_PANE")
	if pane == "" || os.Getenv("TMUX") == "" {
		return "", ""
	}
	c := exec.Command("tmux", "display-message", "-p", "-t", pane, "#{session_name}\t#{window_name}")
	proc.Hide(c)
	out, err := c.Output()
	if err != nil {
		return "", ""
	}
	session, window, _ = strings.Cut(strings.TrimRight(string(out), "\n"), "\t")
	return session, window
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "stui doc:", err)
	os.Exit(1)
}
