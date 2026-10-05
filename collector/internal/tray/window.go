package tray

import "github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"

// The main window (window mode, the default; config.json "trayOnly" turns
// it off): a normal window with a taskbar button on Windows and a Dock
// icon on macOS, so the app is easy to find. Its content is the click
// popup's: the same rows (the pinned panel's heading, status, account and
// provider lines, then the action rows) drawn by the same sheet, and the
// action rows work as the popup's do. The one difference is that the
// window stays open after an action: it is closed (minimized on Windows,
// hidden on macOS) by its own close button, and Quit exits.

// WindowTitle is the main window's title (and the Start-menu entry's name).
const WindowTitle = buildinfo.Product

// Window is the main window's rows: the popup's, row for row.
func Window(v View) []PanelLine { return Popup(v) }

// ClickWindow applies a row click in the main window: as ClickPopup (the
// identity row copies, Quit asks for a second click), but the window never
// closes for an action.
func ClickWindow(st *PopupState, act Action) PopupClick {
	res := ClickPopup(st, act)
	res.Close = false
	return res
}
