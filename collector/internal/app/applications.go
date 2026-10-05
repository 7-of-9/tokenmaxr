package app

import (
	"runtime"

	"github.com/7-of-9/tokenmaxr/collector/internal/appbundle"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
)

// The macOS Applications entry (app mode, window or tray only): install
// adds /Applications/tokenmaxr.app (~/Applications when /Applications is
// not writable), whose executable runs <home>/bin/tokenmaxr app, so
// Spotlight, Launchpad and `open -a tokenmaxr` find the app by name.
// Opened while the app runs, it brings the window to the front (a second
// launch). `install --no-app` and uninstall remove it.

// applicationsDirs is where this install keeps its entry, or nil: a test's
// folders when it set them, else the Applications folders, for the default
// home on macOS only (an install in another directory never replaces the
// main install's entry).
func (a *App) applicationsDirs() []string {
	if a.ApplicationsDirs != nil {
		return a.ApplicationsDirs
	}
	if runtime.GOOS != "darwin" || !paths.IsDefaultHome(a.Home) {
		return nil
	}
	return appbundle.Dirs()
}

// addApplications creates (or repairs) the Applications entry. It returns
// the bundle's path, "" when this install has none, and whether a file was
// written.
func (a *App) addApplications() (string, bool, error) {
	dirs := a.applicationsDirs()
	if dirs == nil {
		return "", false, nil
	}
	return appbundle.Ensure(dirs, a.binPath(false), a.Version)
}

// removeApplications deletes the Applications entry, if any. It reports
// whether there was one.
func (a *App) removeApplications() (bool, error) {
	dirs := a.applicationsDirs()
	if dirs == nil {
		return false, nil
	}
	removed, err := appbundle.Remove(dirs)
	return len(removed) > 0, err
}
