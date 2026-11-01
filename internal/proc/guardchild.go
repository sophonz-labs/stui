package proc

import (
	"os"
	"os/exec"
	"strconv"
)

// GuardBinary is the stui that runs `stui __ttyd-guard`: set by main as it
// starts, and empty anywhere else -- a test binary run with those arguments
// would run its own tests again, not guard anything.
var GuardBinary string

// GuardChild starts a guard (Guard) that stops child if this process is
// killed with no chance to stop it, where the kernel would not do it either
// (macOS; Linux has DieWithParent, Windows its jobs). Best effort: without
// one, the child outlives this process. The guard is named for ttyd, the
// first child it watched; it watches any.
func GuardChild(child int) {
	if !NeedsGuard() || GuardBinary == "" || child <= 0 {
		return
	}
	g := exec.Command(GuardBinary, "__ttyd-guard", strconv.Itoa(os.Getpid()), strconv.Itoa(child))
	if g.Start() != nil {
		return
	}
	go g.Wait()
}
