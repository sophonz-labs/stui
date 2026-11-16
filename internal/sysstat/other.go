//go:build !darwin && !linux && !windows

package sysstat

// Nothing is read on the other systems stui builds for; the metrics say
// null rather than a guess.

func ReadCPU() (CPUTimes, bool)  { return CPUTimes{}, false }
func ReadLoad() (float64, bool)  { return 0, false }
func ReadMemory() (Memory, bool) { return Memory{}, false }
func NewWalk() Walk              { return noWalk{} }

type noWalk struct{}

func (noWalk) Children(int) []int     { return nil }
func (noWalk) RSS(int) (uint64, bool) { return 0, false }
