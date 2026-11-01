// Package proc is what stui needs from processes that are not its children:
// asking whether one is there, stopping it, and starting one that outlives us.
package proc

// State is the answer to "is this pid running?".
type State int

const (
	Gone State = iota
	Running
	// Denied means the process is there but not ours to signal -- an agent
	// started with administrator rights refuses even the question.
	Denied
)

// Alive is true only for a process we can plainly see and signal.
func Alive(pid int) bool { return pid > 0 && Probe(pid) == Running }

// Present counts a process we may not signal as running: refusing the
// question is not the same as answering no.
func Present(pid int) bool { return pid > 0 && Probe(pid) != Gone }
