//go:build !windows

package main

import (
	"os"
	"runtime"
)

// desktopSession: a macOS login session, not an SSH shell (which cannot
// show a menu-bar item).
func desktopSession() bool {
	return runtime.GOOS == "darwin" && os.Getenv("SSH_CONNECTION") == "" && os.Getenv("SSH_TTY") == ""
}
