//go:build windows

package tray

import (
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// Open opens a URL, file or folder with its default handler.
func Open(target string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

// OpenText opens a plain-text file (the log) in Notepad. ShellExecute would
// show Windows' "Select an app" picker when .log has no default app.
func OpenText(path string) error {
	cmd := exec.Command("notepad.exe", path)
	// Visible window, but its own process group so it outlives the app.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// Copy puts s on the clipboard (clip.exe reads its stdin).
func Copy(s string) error {
	cmd := exec.Command("clip.exe")
	cmd.Stdin = strings.NewReader(s)
	detach(cmd)
	return cmd.Run()
}

// detach starts a child with no console window, outside the app's
// console group, so it outlives the app.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP,
	}
}
