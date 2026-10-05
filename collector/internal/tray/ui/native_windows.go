//go:build windows

package ui

import (
	"fyne.io/systray"
	"golang.org/x/sys/windows"

	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
	"github.com/7-of-9/tokenmaxr/collector/internal/tray"
)

// setActivation is a macOS concern (the Dock icon, or none); on Windows
// the main window's WS_EX_APPWINDOW gives it its taskbar button.
func setActivation(window bool) {}

// trayIcons are the notification-area icons by colour (the renderer's
// lock guards them).
var trayIcons = map[tray.Color][]byte{}

// setMenuBarIcon shows the notification-area icon in c: the green grid, a
// .ico with an image for every DPI.
func setMenuBarIcon(c tray.Color) {
	b := trayIcons[c]
	if b == nil {
		b = tray.IconICO(c)
		trayIcons[c] = b
	}
	systray.SetIcon(b)
}

// replyTerminate is a macOS concern (a termination the system asked for).
func replyTerminate() bool { return false }

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
