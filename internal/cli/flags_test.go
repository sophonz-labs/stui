package cli

import (
	"strings"
	"testing"
)

func TestCheckFlags(t *testing.T) {
	for _, c := range []struct {
		rest []string
		spec flagSpec
		hint map[string]string
		want string // in the refusal; "" for none
	}{
		{[]string{"--agent", "claude", "--fan", "3", "--machine", "ci-mac", "--task", "t.md", "--owns", "a/**", "--split", "--json"}, workerStartFlags, workerStartHints, ""},
		// A value that looks like a flag is a value.
		{[]string{"--name", "-x", "--task", "-"}, workerStartFlags, workerStartHints, ""},
		{[]string{"--agent=claude", "--task=t.md"}, workerStartFlags, workerStartHints, ""},
		{[]string{"--agent", "claude", "--deps", "build"}, workerStartFlags, workerStartHints, "stui task create"},
		{[]string{"--agent", "claude", "--machnie", "ci-mac"}, workerStartFlags, workerStartHints, "--machnie is not a flag"},
		{[]string{"build", "--machine", "ci-mac"}, taskStartFlags, taskStartHints, "runs its worker on this machine"},
		{[]string{"build", "--fan", "3"}, taskStartFlags, taskStartHints, "stui worker start --fan"},
		{[]string{"build", "--agent", "codex", "--split", "--cwd", "/x", "--worktree"}, taskStartFlags, taskStartHints, ""},
		{[]string{"build", "--task", "t.md", "--deps", "a,b", "--owns", "x/**", "--worktree"}, taskCreateFlags, nil, ""},
		{[]string{"build", "--task", "t.md", "--machine", "ci-mac"}, taskCreateFlags, nil, "--machine is not a flag"},
	} {
		got := checkFlags(c.rest, c.spec, c.hint)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%v: %q", c.rest, got)
		}
	}
}
