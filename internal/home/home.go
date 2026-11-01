// Package home locates ~/.stui, where every piece of state stui keeps lives.
package home

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// Dir is overridable so a test run cannot reach into a real one.
//
// In a test binary that did not say where (STUI_HOME), the home of the
// person running the tests is never it: a temporary one is. Otherwise the
// keeper's tests, which give it a tmux server of its own but no stui home,
// would retire every live run of the machine they ran on -- their
// coordinators are in no tmux server the keeper can see. A test that sets HOME to a directory of
// its own still gets HOME/.stui.
func Dir() string {
	if d := os.Getenv("STUI_HOME"); d != "" {
		return d
	}
	h, err := os.UserHomeDir()
	if err != nil {
		h = "."
	}
	if testing.Testing() && h == startHome {
		return testHome()
	}
	return filepath.Join(h, ".stui")
}

// startHome is the home the process started with.
var startHome, _ = os.UserHomeDir()

var (
	testOnce sync.Once
	testDir  string
)

func testHome() string {
	testOnce.Do(func() {
		d, err := os.MkdirTemp("", "stui-test-home-")
		if err != nil {
			d = filepath.Join(os.TempDir(), "stui-test-home")
		}
		testDir = d
	})
	return testDir
}

func Exe() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}
