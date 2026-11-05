package server

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/sophonz-labs/stui/internal/orch"
	"github.com/sophonz-labs/stui/internal/proc"
)

// A new binary repairs installed skills without delaying server startup.
func refreshSkills(parent context.Context) {
	if Version == "dev" {
		return // A source build does not speak for the installed stui binary.
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	var run func(...string) (string, error)
	if claude, err := exec.LookPath("claude"); err == nil {
		run = func(args ...string) (string, error) {
			cmd := exec.CommandContext(ctx, claude, args...)
			proc.Hide(cmd)
			out, err := cmd.CombinedOutput()
			return strings.TrimSpace(string(out)), err
		}
	}
	if changed, err := orch.RefreshPlugin(Version, run); err != nil {
		logf("skills: could not refresh installed skills: %v", err)
	} else if changed {
		logf("skills: refreshed installed skills for %s", Version)
	}
}
