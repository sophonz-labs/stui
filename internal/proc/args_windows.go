package proc

// Args is not needed on Windows: children are in a job closed with the agent.
func Args(pid int) []string { return nil }
