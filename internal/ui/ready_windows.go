//go:build windows

package ui

import "golang.org/x/sys/windows"

// ready is whether the console has input within ms.
// shortcut: focus and mouse events also wake it, so a read can then wait
// for a key while the reader is paused; one key may go to stui ui instead of
// the program it handed the terminal to. Read the console's events if that bites.
func ready(fd uintptr, ms int) bool {
	ev, err := windows.WaitForSingleObject(windows.Handle(fd), uint32(ms))
	return err == nil && ev == windows.WAIT_OBJECT_0
}
