//go:build windows

package app

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// hideWindow keeps console children from flashing a window when started by
// the windowsgui build.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

// LowPriority puts this process in background mode (low CPU and I/O
// priority) for scheduled ticks.
func LowPriority() {
	windows.SetPriorityClass(windows.CurrentProcess(), windows.PROCESS_MODE_BACKGROUND_BEGIN)
}

// AppPriority runs the desktop app below normal CPU priority: its ticks
// yield to the user's work, and the UI does next to nothing. (Background
// mode would also starve the UI thread's I/O.)
func AppPriority() {
	windows.SetPriorityClass(windows.CurrentProcess(), windows.BELOW_NORMAL_PRIORITY_CLASS)
}

// reexec starts a fresh detached copy of the app; the caller has let go of
// app.lock and returns, so the new process takes it.
func reexec(exe string, args []string) error {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
