//go:build !linux && !windows

package proc

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Args is pid's command line as ps prints it, split on spaces, or nil when it
// cannot be read. Good enough for the flags stui gave ttyd, which have none.
func Args(pid int) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/bin/ps", "-ww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return nil
	}
	return strings.Fields(strings.TrimSpace(string(out)))
}
