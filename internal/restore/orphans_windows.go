package restore

import "context"

// psmux has no control clients left behind to end (orphans_unix.go).

func Stuck(err error) bool                           { return false }
func ReapOrphans(ctx context.Context, t Tmux) int    { return 0 }
func DefaultSocket(env []string) string              { return "" }
func Unstick(ctx context.Context, socket string) int { return 0 }

func SocketFor(tmpdir, label string) string { return "" }
func Canonical(path string) string          { return path }

// EnvOf: Windows gives no way to read another process's environment here.
func EnvOf(pid int, key string) string { return "" }
