//go:build !linux && !darwin && !windows

package proc

// zombie is not looked for here.
func zombie(int) bool { return false }
