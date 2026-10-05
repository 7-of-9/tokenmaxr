package tray

// The Settings section: the basic settings as rows at the bottom of the
// click popup and the main window, one sheet for everything (owner
// direction 2026-10-05: "the settings dialog standalone is ugly; move it
// into the main window as a right expand out or bottom expand out section",
// "basic settings part of the main ui", "advanced in webpage - ok"). A
// "Settings ▸" row opens it; open, it holds the display mode, Stop
// publishing (a second click confirms it, as Quit's does) or Connect
// GitHub…, and Advanced…, which opens the settings page in the browser
// for everything else. Settings rows keep the popup open (Action.Stays).

import (
	"strings"
	"unicode/utf8"
)

// SettingsState is the section as the app knows it.
type SettingsState struct {
	// Open: the section is expanded (one state for the popup and the main
	// window).
	Open bool
	// TrayOnly is config.json's trayOnly; CanSwitch: this app can restart
	// in the other mode (the desktop app with a UI).
	TrayOnly  bool
	CanSwitch bool
	// Tray is "menu bar" (macOS) or "system tray".
	Tray string
	// Error is the last settings action's failure ("" when none).
	Error string
	// SharesWithFleet: this machine's GitHub sign-in is shared with the
	// fleet, so stopping withdraws it and every machine stops publishing.
	// Adopted: it publishes with the fleet's sign-in, and stopping opts it
	// out of it.
	SharesWithFleet bool
	Adopted         bool
}

// The section's texts, shared by the rows and their tests.
const (
	SettingsText        = "Settings"
	StopPublishingText  = "Stop publishing"
	StopArmedText       = "Click again to stop publishing"
	StopFleetArmedText  = "Click again: stops publishing for all your machines"
	StopAdoptArmedText  = "Click again: stops using your fleet's sign-in"
	ConnectGitHubText   = "Connect GitHub…"
	AdvancedText        = "Advanced…"
	SettingsRestartNote = "Changing this restarts tokenmaxr."
	// The disclosure marks (closed, open) and the checkbox (off, on), from
	// WGL4, so Consolas draws them on Windows as SF Mono does on macOS.
	settingsClosed = "►"
	settingsOpen   = "▼"
	checkOff       = "[ ]"
	checkOn        = "[x]"
	// settingsIndent sets the section's rows under its heading.
	settingsIndent = "  "
	// settingsErrMax clips a long error, so the sheet keeps its width.
	settingsErrMax = 64
)

// SettingsRows are the section: its heading row and, open, its rows.
func SettingsRows(v View) []PanelLine {
	s := v.Settings
	head := PanelLine{Text: SettingsText + " " + settingsClosed, Kind: LineAction, Action: ActSettingsToggle}
	if !s.Open {
		return []PanelLine{head}
	}
	head.Text = SettingsText + " " + settingsOpen
	tray := s.Tray
	if tray == "" {
		tray = "menu bar"
	}
	box := checkOff
	if s.TrayOnly {
		box = checkOn
	}
	mode := PanelLine{Text: settingsIndent + box + " Run only in the " + tray, Kind: LineAction, Action: ActTrayOnly}
	if !s.CanSwitch {
		mode.Kind = LineActionOff
	}
	pub := PanelLine{Text: settingsIndent + ConnectGitHubText, Kind: LineAction, Action: ActConnectGitHub}
	if v.Publishing {
		pub = PanelLine{Text: settingsIndent + StopPublishingText, Kind: LineArmed, Action: ActStopPublishing, Armed: StopArmedText}
		switch {
		case s.SharesWithFleet:
			pub.Armed = StopFleetArmedText
		case s.Adopted:
			pub.Armed = StopAdoptArmedText
		}
	}
	out := []PanelLine{
		head,
		mode,
		{Text: settingsIndent + strings.Repeat(" ", utf8.RuneCountInString(checkOff)+1) + SettingsRestartNote, Kind: LineDim},
		pub,
		{Text: settingsIndent + AdvancedText, Kind: LineAction, Action: ActAdvanced},
	}
	if s.Error != "" {
		// Red and inert: the armed row's colour, with nothing to click.
		out = append(out, PanelLine{Text: settingsIndent + clip(strings.Join(strings.Fields(s.Error), " "), settingsErrMax), Kind: LineArmed})
	}
	return out
}

// Stays reports whether a row's action keeps the popup open: the Settings
// section works in place (its heading, the display mode, Stop publishing).
// Rows that open something else (the browser, the log) close it.
func (a Action) Stays() bool {
	switch a {
	case ActSettings, ActSettingsToggle, ActTrayOnly, ActStopPublishing, ActStopPublishingNow:
		return true
	}
	return false
}
