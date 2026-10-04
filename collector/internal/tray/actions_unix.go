//go:build !windows

package tray

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
)

// Open opens a URL, file or folder with its default handler.
func Open(target string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := exec.Command(name, target)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// OpenText opens a plain-text file (the log) in the default text editor
// (TextEdit on macOS), whatever app owns the file's extension.
func OpenText(path string) error {
	if runtime.GOOS != "darwin" {
		return Open(path)
	}
	cmd := exec.Command("open", "-t", path)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// Copy puts s on the clipboard.
func Copy(s string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("clipboard is supported on Windows and macOS")
	}
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(s)
	return cmd.Run()
}

// detach puts a child in its own session so it outlives the app.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
