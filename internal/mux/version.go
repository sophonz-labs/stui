package mux

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/sophonz-labs/stui/internal/proc"
)

// Version is the version m's binary says it is ("3.5a", "3.4.0"), or ""
// when it cannot be asked or does not say: never one made up. It runs the
// binary, so it is asked once and kept (/api/version's muxVersion).
func Version(ctx context.Context, m Multiplexer) string {
	bin := m.Command()
	if bin == "" || m.ID() == string(NoneChoice) {
		return ""
	}
	flag := "-V"
	if m.ID() == string(ZellijMux) {
		flag = "--version"
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, flag)
	proc.Hide(cmd)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return parseVersion(m.ID(), string(out))
}

// parseVersion reads "<name> <version>" from what -V printed. psmux prints
// "tmux 3.4.0" (what it stands in for) and then "psmux 3.4.0 (<commit>
// <date>)": its own line is the one.
func parseVersion(id, out string) string {
	var first string
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		if first == "" {
			first = f[1]
		}
		if f[0] == id {
			return f[1]
		}
	}
	if id == "psmux" {
		// A psmux that says only "tmux …" does not say its own version.
		return ""
	}
	return first
}
