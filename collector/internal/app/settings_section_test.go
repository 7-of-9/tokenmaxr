package app

import (
	"context"
	"strings"
	"testing"

	"github.com/7-of-9/tokenmaxr/collector/internal/store"
	"github.com/7-of-9/tokenmaxr/collector/internal/tray"
)

// sectionDesktop is a desktop app with a UI, its opens recorded.
func sectionDesktop(t *testing.T) (*Desktop, *recUI, *[]string) {
	t.Helper()
	a, _, _ := newTestApp(t)
	d := a.newDesktop(context.Background())
	ui := newRecUI()
	d.ui, d.hasUI = ui, true
	var opened []string
	d.open = func(s string) error { opened = append(opened, s); return nil }
	t.Cleanup(func() {
		if d.settings != nil {
			d.settings.Close()
		}
	})
	return d, ui, &opened
}

// publishing makes the app publish to GitHub, with a dashboard.
func publishing(t *testing.T, a *App) {
	t.Helper()
	localKey(t, a)
	sec, _ := store.LoadSecrets(a.Home)
	sec.GitHub = &store.GitHubSecrets{Token: "ghu_test", Login: "octo", UserID: 42}
	store.SaveSecrets(a.Home, sec)
	cfg, _ := store.LoadConfig(a.Home)
	cfg.GitHub = &store.GitHubConfig{Repo: "octo/agent-usage", Label: "laptop"}
	store.SaveConfig(a.Home, cfg)
	st, _ := store.LoadState(a.Home)
	st.GitHub.PagesURL = "https://octo.github.io/agent-usage/"
	store.SaveState(a.Home, st)
}

// section is the Settings section as the popup draws it now.
func section(d *Desktop, ui *recUI) string {
	d.load(nil)
	d.draw()
	rows := ui.popupRows()
	for i, l := range rows {
		if strings.HasPrefix(l.Text, tray.SettingsText+" ") {
			out := tray.SheetText(rows[i:], "")
			// Up to Open log, which follows the section.
			if j := strings.Index(out, "Open log"); j >= 0 {
				out = out[:j]
			}
			return out
		}
	}
	return ""
}

// Settings… from another process (`tokenmaxr settings`) opens the section
// and shows the window; its heading opens and closes it; no browser.
func TestSettingsSectionOpens(t *testing.T) {
	d, ui, opened := sectionDesktop(t)
	publishing(t, d.a)
	if got := section(d, ui); got != "Settings ►   [action: settings-toggle]\n" {
		t.Fatalf("closed:\n%s", got)
	}
	d.showSettings()
	want := "Settings ▼   [action: settings-toggle]\n" +
		"  [ ] Run only in the " + trayName() + "   [action: tray-only]\n" +
		"      Changing this restarts tokenmaxr.\n" +
		"  Stop publishing   [action: stop-publishing]\n" +
		"  Advanced…   [action: advanced]\n"
	if got := section(d, ui); got != want || ui.shows != 1 || len(*opened) != 0 {
		t.Fatalf("shown %d, opened %q:\n%s", ui.shows, *opened, got)
	}
	d.Click(tray.ActSettingsToggle)
	if got := section(d, ui); !strings.Contains(got, "►") || strings.Contains(got, "Advanced") {
		t.Fatalf("toggled closed:\n%s", got)
	}
	// The account line's "Settings…" link opens it in place.
	d.Click(tray.ActSettings)
	if got := section(d, ui); !strings.Contains(got, "Advanced") || ui.shows != 1 {
		t.Fatalf("link:\n%s", got)
	}
}

// Advanced… and Connect GitHub… open the settings page in the browser.
func TestSettingsSectionOpensThePage(t *testing.T) {
	d, ui, opened := sectionDesktop(t)
	localKey(t, d.a)
	d.Click(tray.ActSettings)
	if got := section(d, ui); !strings.Contains(got, "Connect GitHub…   [action: connect-github]") {
		t.Fatalf("not publishing:\n%s", got)
	}
	d.Click(tray.ActConnectGitHub)
	d.Click(tray.ActAdvanced)
	if len(*opened) != 2 || !strings.Contains((*opened)[0], "/s/") || !strings.HasSuffix((*opened)[0], "#github") || !strings.HasSuffix((*opened)[1], "#advanced") {
		t.Fatalf("opened %q", *opened)
	}
}

// Stop publishing's second click (the armed row) signs this machine out of
// GitHub; the section then offers Connect GitHub….
func TestSettingsSectionStopPublishing(t *testing.T) {
	d, ui, _ := sectionDesktop(t)
	publishing(t, d.a)
	d.Click(tray.ActSettings)
	d.Click(tray.ActStopPublishing) // the first click only arms the row (the UI's)
	if sec, _ := store.LoadSecrets(d.a.Home); sec.GitHub == nil {
		t.Fatal("stopped on the first click")
	}
	d.Click(tray.ActStopPublishingNow)
	if sec, _ := store.LoadSecrets(d.a.Home); sec.GitHub != nil {
		t.Fatal("still signed in")
	}
	if got := section(d, ui); !strings.Contains(got, "Connect GitHub…") || strings.Contains(got, "Stop publishing") {
		t.Fatalf("after stop:\n%s", got)
	}
}

// The display-mode box saves config.json and restarts in that mode.
func TestSettingsSectionTrayOnly(t *testing.T) {
	d, _, _ := sectionDesktop(t)
	d.Click(tray.ActTrayOnly)
	if cfg, _ := store.LoadConfig(d.a.Home); !cfg.TrayOnly {
		t.Fatal("not saved")
	}
	if !d.restart || !d.restartShown || d.ctx.Err() == nil {
		t.Fatalf("restart %v shown %v ctx %v", d.restart, d.restartShown, d.ctx.Err())
	}
}

// A failure shows in the section, in red, until it is closed or opened again.
func TestSettingsSectionError(t *testing.T) {
	d, ui, _ := sectionDesktop(t)
	d.open = func(string) error { return errBrowser }
	d.Click(tray.ActSettings)
	d.Click(tray.ActAdvanced)
	rows := ui.popupRows()
	d.load(nil)
	d.draw()
	rows = ui.popupRows()
	found := false
	for _, l := range rows {
		found = found || l.Kind == tray.LineArmed && strings.Contains(l.Text, "no browser")
	}
	if !found {
		t.Fatalf("error not shown:\n%s", tray.SheetText(rows, ""))
	}
	d.Click(tray.ActSettingsToggle)
	d.Click(tray.ActSettingsToggle)
	if got := section(d, ui); strings.Contains(got, "no browser") {
		t.Fatalf("error kept:\n%s", got)
	}
}

type browserErr struct{}

func (browserErr) Error() string { return "no browser" }

var errBrowser = browserErr{}
