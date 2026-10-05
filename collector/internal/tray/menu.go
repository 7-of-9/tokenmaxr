package tray

// Action is what clicking a menu item does.
type Action int

const (
	ActNone Action = iota
	ActCopyFleet
	ActDashboard
	ActSyncNow
	ActOpenLog
	ActQuit
	// ActPin shows the live panel, ActUnpin closes it (also its ×).
	ActPin
	ActUnpin
	// ActQuitNow quits without asking: the popup's Quit row was clicked
	// twice, which is its confirmation.
	ActQuitNow
	// ActSettings opens the settings page (destinations, label).
	ActSettings
	// ActGitHubDashboard opens the GitHub Pages dashboard of a machine that
	// also sends to a server (ActDashboard opens the server's).
	ActGitHubDashboard
)

// Item is one menu entry. Stable keys identify providers as recency changes their order.
type Item struct {
	Key   string
	Title string
	// Right is a second column (the provider totals): Windows lines it up
	// after a tab, like a shortcut column; elsewhere it follows " │ ".
	Right     string
	Action    Action
	Disabled  bool
	Hidden    bool
	Separator bool
}

// Label is the item's text with the renderer's column separator.
func (it Item) Label(sep string) string {
	if it.Right == "" {
		return it.Title
	}
	return it.Title + sep + it.Right
}

// QuitPrompt is the Quit confirmation.
const QuitPrompt = "Collection stops until next login. Quit?"

// Menu is the machine heading, providers, and actions.
func Menu(v View) []Item {
	items := []Item{
		{Key: "machine", Title: "● " + v.Machine},
		{Key: "sep-providers", Separator: true, Hidden: len(v.Providers) == 0},
	}
	seen := map[string]bool{}
	for _, l := range v.Providers {
		items = append(items, Item{Key: "provider:" + l.Provider, Title: l.Left(), Right: l.Right()})
		seen[l.Provider] = true
	}
	for _, p := range Providers {
		if !seen[p.ID] {
			items = append(items, Item{Key: "provider:" + p.ID, Hidden: true})
		}
	}

	// No Sync or Pin item: as in the popup (Popup).
	return append(items,
		Item{Key: "sep-actions", Separator: true},
		Item{Key: "dashboard", Title: "Open dashboard", Action: ActDashboard, Hidden: v.Dashboard == ""},
		Item{Key: "github-dashboard", Title: "Open GitHub dashboard", Action: ActGitHubDashboard, Hidden: v.GitHubDashboard == ""},
		Item{Key: "settings", Title: "Settings…", Action: ActSettings},
		Item{Key: "log", Title: "Open log", Action: ActOpenLog},
		Item{Key: "quit", Title: "Quit", Action: ActQuit},
	)
}

// UI is what the app drives: internal/tray/ui implements it with
// fyne.io/systray, tests with a recorder. Every method may be called from
// any goroutine.
type UI interface {
	SetIcon(c Color)
	SetTooltip(tip string)
	SetMenu(items []Item)
	// SetPanel shows, updates or closes the pinned live panel.
	SetPanel(p PanelState)
	// SetPopup is the click popup's rows (Popup); an open popup redraws.
	SetPopup(lines []PanelLine)
	// SetWindow is the main window's rows (Window); the window redraws.
	// Tray-only mode has no window and ignores it.
	SetWindow(lines []PanelLine)
	// ShowWindow restores the main window and brings it to the front (a
	// second launch, the Start-menu entry). Tray-only mode has none.
	ShowWindow()
	// Debug describes the UI's own state for app.dump (the popup's window
	// and rows); display text only.
	Debug() string
	// Confirm asks a native yes/no question.
	Confirm(question string) bool
	Quit()
}
