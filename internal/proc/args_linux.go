package proc

import (
	"os"
	"strconv"
	"strings"
)

// Args is pid's command line, or nil when it cannot be read.
func Args(pid int) []string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil || len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
}
