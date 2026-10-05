//go:build windows

package ui

import (
	"fyne.io/systray"

	"github.com/7-of-9/tokenmaxr/collector/internal/tray"
)

// clickOpens is what a click on the icon shows.
const clickOpens = "the owner-drawn popup (left or right click)"

// setupClicks takes both clicks from systray: with a tap handler set,
// fyne.io/systray (v1.12) calls it instead of showing its native menu, so
// no menu item is ever added and the popup is the only thing that opens.
// The tooltip, the icon and the re-add on an Explorer restart
// (TaskbarCreated) stay systray's.
func setupClicks(r *renderer) {
	tap := func() { popupTap(r.h) }
	systray.SetOnTapped(tap)
	systray.SetOnSecondaryTapped(tap)
}

// nativeMenu is unused on Windows: the popup draws the menu.
type nativeMenu struct{}

func (*nativeMenu) set(*renderer, []tray.Item) {}

func setPopupLines(lines []tray.PanelLine, h Handler) { setPopup(lines, h) }

// closeWindows closes the panel, the popup and the main window (the app
// quits).
func closeWindows() {
	closePopup()
	closePanel()
	closeMainWindow()
}

func debugWindows() string { return panelDebug() + popupDebug() }
