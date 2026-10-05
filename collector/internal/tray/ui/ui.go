//go:build windows || (darwin && cgo)

// Package ui draws the desktop app (internal/tray) with fyne.io/systray:
// the notification-area icon on Windows, the menu-bar item on macOS. It is
// the only package that touches the GUI and holds no logic of its own.
// macOS needs cgo (Cocoa), so a CGO_ENABLED=0 darwin build never links it.
//
// Clicking the icon opens an owner-drawn popup drawn by the pinned panel's
// sheet: on Windows menu_windows.go and popup_windows.go, on macOS
// menu_darwin.go and popup_darwin.go. Both paint with the one sheet their
// platform already uses for the pinned panel.
//
// In window mode (the default) the app also has a main window with the
// popup's rows and actions, so it is easy to find: a normal window with a
// taskbar button on Windows (window_windows.go), a Dock icon and window on
// macOS (window_darwin.go). Tray-only mode (config.json "trayOnly") has
// neither, as before.
package ui

import (
	"runtime"
	"strings"
	"sync"

	"fyne.io/systray"

	"github.com/7-of-9/tokenmaxr/collector/internal/tray"
)

// Handler is the app behind the icon.
type Handler struct {
	// Ready runs once the icon exists, on its own goroutine; the app drives
	// the UI from there.
	Ready func(u tray.UI)
	// Click runs a menu action, on its own goroutine.
	Click func(a tray.Action)
	// Opened runs when the native menu is about to open. Both desktops open
	// the owner-drawn popup instead, so this stays idle.
	Opened func()
	// Popup runs when the click popup opens (true) and closes.
	Popup func(open bool)
	// Moved runs when the pinned panel was dragged to x, y (its top-left
	// corner in the platform's screen coordinates), on its own goroutine.
	// Its × is Click(tray.ActUnpin).
	Moved func(x, y int)
	// Window gives the app its main window: a taskbar button on Windows, a
	// Dock icon on macOS (false: tray only). Minimized starts it minimized
	// on the taskbar (macOS: hidden, with its Dock icon).
	Window    bool
	Minimized bool
	// Shown runs when the main window comes on screen (true) or is
	// minimized or hidden, on its own goroutine.
	Shown func(visible bool)
}

// Run shows the icon until Quit. Call it from the main goroutine: systray
// keeps the main thread for the OS event loop.
func Run(h Handler) {
	r := &renderer{h: h, icons: map[tray.Color][]byte{}}
	setActivation(h.Window)
	setupClicks(r)
	systray.Run(func() {
		if h.Opened != nil {
			go func() {
				for range systray.TrayOpenedCh {
					h.Opened()
				}
			}()
		}
		if h.Window {
			startWindow(h)
		}
		go h.Ready(r)
	}, nil)
}

type renderer struct {
	h     Handler
	mu    sync.Mutex
	menu  nativeMenu
	icons map[tray.Color][]byte
	icon  tray.Color
	tip   string
	drawn bool
}

func (r *renderer) SetIcon(c tray.Color) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.drawn && c == r.icon {
		return
	}
	b := r.icons[c]
	if b == nil {
		if runtime.GOOS == "windows" {
			b = tray.IconICO(c)
		} else {
			b = tray.IconPNG(c, 32, tray.MenuBarInset)
		}
		r.icons[c] = b
	}
	systray.SetIcon(b)
	r.icon, r.drawn = c, true
	if r.h.Window {
		setWindowIcon(c)
	}
}

func (r *renderer) SetTooltip(tip string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if tip != r.tip {
		systray.SetTooltip(tip)
		r.tip = tip
	}
}

func (r *renderer) Quit() {
	closeWindows()
	systray.Quit()
}

// SetPanel shows, updates or closes the pinned panel (panel_*.go).
func (r *renderer) SetPanel(p tray.PanelState) { setPanel(p, r.h) }

// SetMenu updates a native menu. Both desktops draw the popup instead, so
// the call keeps the model and adds no items.
func (r *renderer) SetMenu(items []tray.Item) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.menu.set(r, items)
}

// SetPopup is the click popup's rows.
func (r *renderer) SetPopup(lines []tray.PanelLine) { setPopupLines(lines, r.h) }

// SetWindow is the main window's rows (window_*.go); tray-only mode has no
// window.
func (r *renderer) SetWindow(lines []tray.PanelLine) {
	if r.h.Window {
		setWindowLines(lines, r.h)
	}
}

// ShowWindow restores the main window and brings it to the front.
func (r *renderer) ShowWindow() {
	if r.h.Window {
		showMainWindow()
	}
}

func (r *renderer) Confirm(question string) bool { return confirm(question) }

// Debug is the windows' state for app.dump.
func (r *renderer) Debug() string {
	var b strings.Builder
	b.WriteString("click opens: " + clickOpens + "\n")
	b.WriteString(debugWindows())
	if r.h.Window {
		b.WriteString(windowDebug())
	} else {
		b.WriteString("main window: none (tray only)\n")
	}
	return b.String()
}
