//go:build windows

package ui

import (
	"golang.org/x/sys/windows"

	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
)

// setActivation is a macOS concern (the Dock icon, or none); on Windows
// the main window's WS_EX_APPWINDOW gives it its taskbar button.
func setActivation(window bool) {}

// confirm is a native OK/Cancel message box, brought to the front.
func confirm(question string) bool {
	text, err := windows.UTF16PtrFromString(question)
	if err != nil {
		return false
	}
	caption, _ := windows.UTF16PtrFromString(buildinfo.Product)
	const style = windows.MB_OKCANCEL | windows.MB_ICONQUESTION | windows.MB_DEFBUTTON2 | windows.MB_SETFOREGROUND | windows.MB_TOPMOST
	r, _ := windows.MessageBox(0, text, caption, style)
	const idOK = 1
	return r == idOK
}
