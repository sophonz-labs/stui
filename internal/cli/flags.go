package cli

// Flags a command does not take are refused, not dropped.
//
// stui worker start --fan 3 --machine ci-mac started all three on this
// machine: the fan's path never read --machine. A flag a command does not
// read is worse than an error -- the command runs, and does something else
// than was asked. So the commands that start agents say which flags they
// take, and anything else stops them before they start.

import (
	"fmt"
	"sort"
	"strings"
)

// flagSpec is a command's flags: true for one that takes a value.
type flagSpec map[string]bool

// common is taken by every command.
var common = flagSpec{"--json": false, "--help": false, "-h": false}

// checkFlags returns why rest has a flag spec does not take ("" when none):
// the first such flag, with hint[flag] when there is one.
func checkFlags(rest []string, spec flagSpec, hint map[string]string) string {
	for i := 0; i < len(rest); i++ {
		w := rest[i]
		if !strings.HasPrefix(w, "-") || w == "-" {
			continue
		}
		name, _, inline := strings.Cut(w, "=")
		if v, ok := spec[name]; ok {
			if v && !inline {
				i++
			}
			continue
		}
		if _, ok := common[name]; ok {
			continue
		}
		if h := hint[name]; h != "" {
			return fmt.Sprintf("%s: %s", name, h)
		}
		var names []string
		for n := range spec {
			names = append(names, n)
		}
		sort.Strings(names)
		return fmt.Sprintf("%s is not a flag of this command (it takes %s)", name, strings.Join(names, " "))
	}
	return ""
}

var workerStartFlags = flagSpec{
	"--agent": true, "--task": true, "--name": true, "--owns": true, "--cwd": true,
	"--machine": true, "--fan": true, "--split": false, "--worktree": false, "--any-size": false,
}

var workerStartHints = map[string]string{
	"--deps": "dependencies are stui task create's: stui task create <name> --deps a,b, then stui task start <name>",
}

var taskCreateFlags = flagSpec{"--task": true, "--deps": true, "--agent": true, "--owns": true, "--worktree": false}

var taskStartFlags = flagSpec{"--agent": true, "--split": false, "--cwd": true, "--worktree": false}

var taskStartHints = map[string]string{
	"--machine": "stui task start runs its worker on this machine; tasks on another machine are not done yet. Start it there with stui worker start --machine <machine> --task <the task's file>",
	"--fan":     "stui task start starts one worker; for several on the same task use stui worker start --fan <n> --task <the task's file>",
	"--deps":    "a task's dependencies are given when it is created: stui task create <name> --deps a,b",
	"--owns":    "a task's owned paths are given when it is created: stui task create <name> --owns …",
}
