//go:build windows

package app

import (
	"bufio"
	"os"

	"golang.org/x/sys/windows"
)

// openConsole opens the console (CONIN$/CONOUT$) when someone is at it:
// stdin must itself be the console, so an automated run with redirected
// input (or the windowsgui build, which has no console) never blocks on a
// question nobody will answer.
func openConsole() (*terminal, bool) {
	var mode uint32
	if windows.GetConsoleMode(windows.Handle(os.Stdin.Fd()), &mode) != nil {
		return nil, false
	}
	in, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
	if err != nil {
		return nil, false
	}
	out, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		in.Close()
		return nil, false
	}
	return &terminal{in: bufio.NewReader(in), out: out, closers: []func() error{in.Close, out.Close}}, true
}
