//go:build !windows

package app

import (
	"os"
	"os/exec"
	"syscall"
)

func hideWindow(cmd *exec.Cmd) {}

// LowPriority lowers CPU priority for scheduled ticks (launchd already sets
// Nice and LowPriorityIO; this covers manual runs). Raising a higher nice
// value back to 10 fails harmlessly for unprivileged users.
func LowPriority() { syscall.Setpriority(syscall.PRIO_PROCESS, 0, 10) }

// AppPriority: the desktop app's ticks run at the same nice level as a
// scheduled tick.
func AppPriority() { LowPriority() }

// reexec replaces this process with a fresh image of exe (same pid, so
// launchd keeps tracking it). app.lock is close-on-exec, so the new image
// takes it again.
func reexec(exe string, args []string) error {
	return syscall.Exec(exe, append([]string{exe}, args...), os.Environ())
}
