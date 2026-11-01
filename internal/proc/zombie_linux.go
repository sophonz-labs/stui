package proc

import (
	"os"
	"strconv"
	"strings"
)

// zombie is whether pid has ended and waits to be reaped: state Z (or X)
// in /proc/<pid>/stat, read after the command's closing parenthesis.
func zombie(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	return zombieStat(string(b))
}

func zombieStat(stat string) bool {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return false
	}
	f := strings.Fields(stat[i+1:])
	return len(f) > 0 && (f[0] == "Z" || f[0] == "X")
}
