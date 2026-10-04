//go:build !(windows || (darwin && cgo))

package main

import (
	"runtime"

	"github.com/7-of-9/tokenmaxr/collector/internal/app"
)

// desktopUI: this build has no GUI (a CGO_ENABLED=0 macOS build, or another
// OS), so the app runs its collection loop headless.
func desktopUI() (func(*app.Desktop), string) {
	if runtime.GOOS == "darwin" {
		return nil, "the menu-bar icon needs the macOS release build (cgo); running headless"
	}
	return nil, "no desktop UI on " + runtime.GOOS + "; running headless"
}
