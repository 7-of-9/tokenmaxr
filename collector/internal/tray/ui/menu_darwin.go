//go:build darwin && cgo

package ui

// Both clicks open the owner-drawn popup, the same sheet as the pinned
// panel (native_darwin.go draws it, popup_darwin.go runs it). fyne.io/systray
// shows its NSMenu only when no tap handler is set, so none is ever added.

import (
	"fyne.io/systray"

	"github.com/7-of-9/tokenmaxr/collector/internal/tray"
)

// clickOpens is what a click on the icon shows.
const clickOpens = "the owner-drawn popup (left or right click)"

// setupClicks takes both clicks from systray. With a tap handler set,
// fyne.io/systray (v1.12) calls it instead of showing its native menu.
func setupClicks(r *renderer) {
	tap := func() { popupTap(r.h) }
	systray.SetOnTapped(tap)
	systray.SetOnSecondaryTapped(tap)
}

// nativeMenu is unused: the popup draws the menu.
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

func debugWindows() string { return popupDebug() }
