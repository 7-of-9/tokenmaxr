package app

import (
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
	"github.com/7-of-9/tokenmaxr/collector/internal/tray"
)

// The Settings section of the popup and the main window (tray.SettingsRows):
// the basic settings in the app's own sheet (owner direction 2026-10-05:
// "basic settings part of the main ui", "advanced in webpage - ok"). Its
// open state is the app's, so the popup and the window show it alike.
// Connect GitHub… and Advanced… open the settings page in the browser; a
// headless app (no UI) has only the page.

// settingsAct runs a Settings section row (Desktop.Click). It reports
// whether act was one.
func (d *Desktop) settingsAct(act tray.Action) bool {
	var err error
	switch act {
	case tray.ActSettings:
		d.setSettingsOpen(true)
		return true
	case tray.ActSettingsToggle:
		d.mu.Lock()
		open := !d.settingsOpen
		d.mu.Unlock()
		d.setSettingsOpen(open)
		return true
	case tray.ActTrayOnly:
		var cfg store.Config
		if cfg, err = retryRead(store.LoadConfig, d.a.Home); err == nil {
			var restart bool
			if restart, err = d.settingsPage().saveAppOptions(!cfg.TrayOnly); err == nil && restart {
				d.restartFor("display mode changed in settings")
				return true
			}
		}
	case tray.ActStopPublishingNow:
		// The row's second click was the confirmation.
		if err = d.a.GitHubLogout(); err == nil {
			d.a.Log.Printf("settings: stopped publishing to GitHub")
			d.load(nil)
			d.Sync()
		}
	case tray.ActConnectGitHub:
		err = d.openSettingsAt("github")
	case tray.ActAdvanced:
		err = d.openSettingsAt("advanced")
	default:
		return false
	}
	msg := ""
	if err != nil {
		msg = err.Error()
		d.a.Log.Printf("app: settings: %v", err)
	}
	d.mu.Lock()
	d.settingsErr = msg
	d.mu.Unlock()
	d.Refresh()
	return true
}

// setSettingsOpen opens or closes the section (and drops its last error).
func (d *Desktop) setSettingsOpen(open bool) {
	d.mu.Lock()
	d.settingsOpen, d.settingsErr = open, ""
	d.mu.Unlock()
	d.Refresh()
}

// showSettings opens the section and shows it: the main window, or tray
// only the popup (Settings… from another process: `tokenmaxr settings`).
// Without a UI it opens the settings page.
func (d *Desktop) showSettings() {
	d.mu.Lock()
	hasUI, u := d.hasUI, d.ui
	d.mu.Unlock()
	if !hasUI {
		if err := d.openSettingsAt(""); err != nil {
			d.a.Log.Printf("app: settings: %v", err)
		}
		return
	}
	d.a.markFirstRunSettings()
	d.setSettingsOpen(true)
	d.draw() // open before it shows
	u.ShowWindow()
}

// settingsState is the section's state for the view: with what Stop
// publishing does to the fleet, as the settings page's confirm says it.
func (d *Desktop) settingsState(cfg *store.Config, sec store.Secrets) tray.SettingsState {
	s := tray.SettingsState{TrayOnly: cfg.TrayOnly, CanSwitch: d.hasUI, Tray: trayName()}
	if githubEnabled(cfg, sec) {
		s.Adopted = cfg.GitHub.Adopted
		s.SharesWithFleet = !s.Adopted && serverOn(cfg, sec) && cfg.GitHub.SharesWithFleet()
	}
	return s
}
