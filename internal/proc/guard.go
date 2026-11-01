package proc

import "time"

// Guard is the whole life of `stui __ttyd-guard`: it waits for either parent
// or child to exit. When the parent goes first -- the agent was killed with
// no chance to stop its children -- the child is stopped too: SIGTERM, then
// SIGKILL if it is still there two seconds on. When the child goes first
// there is nothing to do. Never anything but child, and only while it has
// been watched since it started, so its pid cannot have been given to
// another process in between.
func Guard(parent, child int) {
	if parent <= 0 || child <= 0 {
		return
	}
	if !waitEither(parent, child) {
		return
	}
	Terminate(child)
	for i := 0; i < 20 && Alive(child); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if Alive(child) {
		Kill(child)
	}
}
