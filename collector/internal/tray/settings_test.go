package tray

import (
	"strings"
	"testing"
)

// The Settings section: closed, one row; open, the display mode, Stop
// publishing (or Connect GitHub…) and Advanced….
func TestSettingsRows(t *testing.T) {
	for _, c := range []struct {
		name string
		v    View
		want string
	}{
		{"closed", View{Publishing: true}, "Settings ►   [action: settings-toggle]\n"},
		{"publishing", View{Publishing: true, Settings: SettingsState{Open: true, CanSwitch: true, Tray: "menu bar"}},
			"Settings ▼   [action: settings-toggle]\n" +
				"  [ ] Run only in the menu bar   [action: tray-only]\n" +
				"      Changing this restarts tokenmaxr.\n" +
				"  Stop publishing   [action: stop-publishing]\n" +
				"  Advanced…   [action: advanced]\n"},
		{"not publishing, tray only", View{Settings: SettingsState{Open: true, CanSwitch: true, TrayOnly: true, Tray: "system tray"}},
			"Settings ▼   [action: settings-toggle]\n" +
				"  [x] Run only in the system tray   [action: tray-only]\n" +
				"      Changing this restarts tokenmaxr.\n" +
				"  Connect GitHub…   [action: connect-github]\n" +
				"  Advanced…   [action: advanced]\n"},
		{"an error, no switch", View{Settings: SettingsState{Open: true, Error: "the collector is busy;\ntry again"}},
			"Settings ▼   [action: settings-toggle]\n" +
				"  [ ] Run only in the menu bar   [disabled]\n" +
				"      Changing this restarts tokenmaxr.\n" +
				"  Connect GitHub…   [action: connect-github]\n" +
				"  Advanced…   [action: advanced]\n" +
				"  the collector is busy; try again\n"},
	} {
		if got := SheetText(SettingsRows(c.v), ""); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
	long := SettingsRows(View{Settings: SettingsState{Open: true, Error: strings.Repeat("x", 200)}})
	if e := long[len(long)-1]; e.Kind != LineArmed || e.Hoverable() || e.Clickable() || len([]rune(e.Text)) > 2+settingsErrMax {
		t.Fatalf("error row %+v", e)
	}
}

// Settings rows work with the popup open; rows that open something else
// close it. Stop publishing takes a second click, like Quit.
func TestSettingsClicks(t *testing.T) {
	var st PopupState
	for _, act := range []Action{ActSettings, ActSettingsToggle, ActTrayOnly} {
		if res := ClickPopup(&st, act); res.Run != act || res.Close {
			t.Fatalf("%s: %+v", act, res)
		}
	}
	for _, act := range []Action{ActConnectGitHub, ActAdvanced, ActDashboard} {
		if res := ClickPopup(&st, act); res.Run != act || !res.Close {
			t.Fatalf("%s: %+v", act, res)
		}
	}
	v := View{Publishing: true, Settings: SettingsState{Open: true, CanSwitch: true}}
	res := ClickPopup(&st, ActStopPublishing)
	if res.Run != ActNone || res.Close || !res.ArmQuit || !res.Redraw || !st.StopArmed {
		t.Fatalf("first click: %+v %+v", res, st)
	}
	rows := PopupView(SettingsRows(v), st)
	armed := rows[3]
	if armed.Text != "  "+StopArmedText || armed.Kind != LineArmed || armed.Action != ActStopPublishingNow || !armed.Selected {
		t.Fatalf("armed row %+v", armed)
	}
	// Hovering the armed row keeps it armed; leaving disarms it.
	if HoverPopup(&st, ActStopPublishingNow); !st.StopArmed {
		t.Fatal("disarmed on its own row")
	}
	HoverPopup(&st, ActAdvanced)
	if st.StopArmed {
		t.Fatal("still armed after leaving")
	}
	ClickPopup(&st, ActStopPublishing)
	if !DisarmQuit(&st) || st.StopArmed {
		t.Fatal("the timer does not disarm it")
	}
	ClickPopup(&st, ActStopPublishing)
	if res := ClickPopup(&st, ActStopPublishingNow); res.Run != ActStopPublishingNow || res.Close || st.StopArmed {
		t.Fatalf("second click: %+v %+v", res, st)
	}
	// The keyboard: Return on the selected row arms, then stops.
	ClickPopup(&st, ActStopPublishing)
	if res := ClickPopup(&st, st.Sel); res.Run != ActStopPublishingNow {
		t.Fatalf("Return twice: %+v", res)
	}
	// An armed click with nothing armed (a stale row) only arms.
	if res := ClickPopup(&st, ActStopPublishingNow); res.Run != ActNone || !st.StopArmed {
		t.Fatalf("stale armed row: %+v", res)
	}
	// The arrows from the armed row go to its neighbours, not the ends.
	rows = PopupView(SettingsRows(v), st)
	if up, down := PopupNav(rows, ActStopPublishingNow, -1), PopupNav(rows, ActStopPublishingNow, 1); up != ActTrayOnly || down != ActAdvanced {
		t.Fatalf("arrows from the armed row: up %v down %v", up, down)
	}
	// The pointer resting on the armed row keeps its highlight after the
	// disarm.
	HoverPopup(&st, ActStopPublishingNow)
	DisarmQuit(&st)
	if rows := PopupView(SettingsRows(v), st); !rows[3].Selected || rows[3].Action != ActStopPublishing {
		t.Fatalf("disarmed row under the pointer %+v", rows[3])
	}
}

// Stop publishing says when it stops the whole fleet (this machine's
// sign-in is shared) or opts this machine out of the fleet's.
func TestStopPublishingFleetWarning(t *testing.T) {
	for _, c := range []struct {
		s    SettingsState
		want string
	}{
		{SettingsState{}, StopArmedText},
		{SettingsState{SharesWithFleet: true}, StopFleetArmedText},
		{SettingsState{Adopted: true}, StopAdoptArmedText},
	} {
		c.s.Open = true
		v := View{Publishing: true, Settings: c.s}
		var st PopupState
		ClickPopup(&st, ActStopPublishing)
		if got := PopupView(SettingsRows(v), st)[3].Text; got != "  "+c.want {
			t.Errorf("%+v: armed %q, want %q", c.s, got, c.want)
		}
	}
}
