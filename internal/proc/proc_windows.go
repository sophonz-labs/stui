//go:build windows

package proc

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const stillActive = 259

func Probe(pid int) State {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return Denied
		}
		return Gone
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return Denied
	}
	if code == stillActive {
		return Running
	}
	return Gone
}

// Windows has no SIGTERM to give; what node did for one was TerminateProcess.
func Terminate(pid int) error { return Kill(pid) }

func Kill(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}

// Detach gives the agent no console of its own and a process group apart
// from the terminal that started it. HideWindow is there because Windows
// hands a console program with no console an empty window of its own.
func Detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
}

// Hide keeps a console child of a console-less agent from opening a window
// nobody asked for. Inherited handles are unaffected, so its output still
// lands in the log.
func Hide(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
}

var (
	jobOnce sync.Once
	job     windows.Handle
)

// newKillJob is a job object whose processes are killed when its last
// handle closes.
func newKillJob() windows.Handle {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK |
				windows.JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK |
				windows.JOB_OBJECT_LIMIT_DIE_ON_UNHANDLED_EXCEPTION |
				windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(h)
		return 0
	}
	return h
}

// assign puts cmd's process into j, and says whether it is in.
func assign(j windows.Handle, cmd *exec.Cmd) bool {
	if j == 0 || cmd.Process == nil {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	return windows.AssignProcessToJobObject(j, h) == nil
}

// Contain ties cmd's life to this process's. Windows has no signal for the
// agent to catch on its way out -- stui exit is TerminateProcess -- so without
// this a child outlives it. libuv does the same: every child it spawns goes into a job that is killed when the job's last handle
// closes, and the handle closes with this process.
//
// Only cmd itself, though, with the flags libuv uses. SILENT_BREAKAWAY_OK
// leaves whatever cmd starts outside the job: zellij's server is started by
// its client, a grandchild of the agent, and it is the session. Held in the
// job it was killed with the agent, with no chance to clean up, and the next
// agent could neither see the session nor make a new one in its place.
func Contain(cmd *exec.Cmd) {
	jobOnce.Do(func() { job = newKillJob() })
	assign(job, cmd)
}

// EnableVT turns on escape-sequence processing for the console, which the
// colours and the QR code need. conhost leaves it off; Bun turned it on.
func EnableVT() {
	for _, f := range []*os.File{os.Stdout, os.Stderr} {
		h := windows.Handle(f.Fd())
		var mode uint32
		if windows.GetConsoleMode(h, &mode) == nil {
			windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
		}
	}
}
