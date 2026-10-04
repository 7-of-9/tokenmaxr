//go:build windows

package termfmt

import (
	"os"

	"golang.org/x/sys/windows"
)

func termWidth(f *os.File) (int, bool) {
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(windows.Handle(f.Fd()), &info); err != nil {
		return 0, false
	}
	return int(info.Window.Right-info.Window.Left) + 1, true
}

// enableColor turns on the console's ANSI escape handling (Windows 10+).
func enableColor(f *os.File) bool {
	h := windows.Handle(f.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) != nil {
		return false
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return true
	}
	return windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
