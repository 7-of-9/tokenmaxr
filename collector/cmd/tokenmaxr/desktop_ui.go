//go:build windows || (darwin && cgo)

package main

import (
	"github.com/7-of-9/tokenmaxr/collector/internal/app"
	"github.com/7-of-9/tokenmaxr/collector/internal/tray/ui"
)

// desktopUI is the tray icon (Windows) or menu-bar item (macOS, cgo).
func desktopUI() (func(*app.Desktop), string) {
	return func(d *app.Desktop) {
		ui.Run(ui.Handler{Ready: d.Ready, Click: d.Click, Opened: d.Refresh, Popup: d.PopupShown, Moved: d.Moved})
	}, ""
}
