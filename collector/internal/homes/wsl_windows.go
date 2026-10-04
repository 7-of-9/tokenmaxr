//go:build windows

package homes

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

// DefaultWSL runs the real wsl.exe from System32 (never a wsl.exe found on
// the PATH) with no console window, so the windowsgui build stays silent.
func DefaultWSL() *WSL {
	exe := filepath.Join(os.Getenv("SystemRoot"), "System32", "wsl.exe")
	if os.Getenv("SystemRoot") == "" {
		exe = `C:\Windows\System32\wsl.exe`
	}
	if _, err := os.Stat(exe); err != nil {
		return nil
	}
	return &WSL{
		Root: WSLRoot,
		List: func(ctx context.Context, runningOnly bool) ([]byte, error) {
			args := []string{"-l", "-q"}
			if runningOnly {
				args = []string{"-l", "--running", "-q"}
			}
			cmd := exec.CommandContext(ctx, exe, args...)
			cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
			out, err := cmd.Output()
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				// wsl.exe exits non-zero (with a message) when nothing is
				// installed or running; that is an empty list, not a failure.
				return nil, nil
			}
			return out, err
		},
	}
}
