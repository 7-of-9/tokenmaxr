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

	pin := Item{Key: "pin", Title: "Pin to screen", Action: ActPin}
	if v.Pinned {
		pin.Title, pin.Action = "Unpin", ActUnpin
	}
	sync := Item{Key: "sync", Title: "Sync now", Action: ActSyncNow, Disabled: !v.CanSync}
	if v.Syncing {
		sync.Title, sync.Disabled = v.SyncLabel, true
	}
	return append(items,
		Item{Key: "sep-actions", Separator: true},
		Item{Key: "dashboard", Title: "Open dashboard", Action: ActDashboard, Hidden: v.Dashboard == ""},
		pin,
		sync,
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
	// Debug describes the UI's own state for app.dump (the popup's window
	// and rows); display text only.
	Debug() string
	// Confirm asks a native yes/no question.
	Confirm(question string) bool
	Quit()
}
