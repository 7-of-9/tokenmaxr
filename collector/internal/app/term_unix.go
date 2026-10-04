//go:build !windows

package app

import (
	"bufio"
	"os"
)

// openConsole opens the controlling terminal. Under `curl | sh` stdin is
// the script itself, so questions go to /dev/tty, which fails to open when
// there is no terminal (launchd, cron, CI).
func openConsole() (*terminal, bool) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, false
	}
	return &terminal{in: bufio.NewReader(f), out: f, closers: []func() error{f.Close}}, true
}
