//go:build !windows

package termfmt

import (
	"os"

	"golang.org/x/sys/unix"
)

func termWidth(f *os.File) (int, bool) {
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 {
		return 0, false
	}
	return int(ws.Col), true
}

func enableColor(*os.File) bool { return true }
