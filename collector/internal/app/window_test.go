package app

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/autostart"
	"github.com/7-of-9/tokenmaxr/collector/internal/instance"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
	"github.com/7-of-9/tokenmaxr/collector/internal/tray"
)

// A restart keeps the app's own arguments, makes the app command explicit,
// and comes back minimized unless it is a switch to window mode.
func TestRestartArgs(t *testing.T) {
	for _, c := range []struct {
		args      []string
		minimized bool
		want      []string
	}{
		{nil, true, []string{"app", "--minimized"}},
		{nil, false, []string{"app"}},
		{[]string{"--home", `C:\h`}, true, []string{"--home", `C:\h`, "app", "--minimized"}},
		{[]string{"--home=x"}, false, []string{"--home=x", "app"}},
		{[]string{"app", "--minimized"}, true, []string{"app", "--minimized"}},
		{[]string{"app", "--minimized"}, false, []string{"app"}},
		{[]string{"--home", "h", "app", "--watchdog", "-minimized=true"}, false, []string{"--home", "h", "app", "--watchdog"}},
		{[]string{"--home", "h", "app", "--watchdog"}, true, []string{"--home", "h", "app", "--watchdog", "--minimized"}},
	} {
		if got := restartArgs(c.args, c.minimized); !slices.Equal(got, c.want) {
			t.Errorf("restartArgs(%q, %v) = %q, want %q", c.args, c.minimized, got, c.want)
		}
	}
}

// The window mode comes from config.json as the app starts; the start is
// minimized at login (and for the watchdog).
func TestDesktopWindowMode(t *testing.T) {
	a, _, _ := newTestApp(t)
	localKey(t, a)
	run := func(o DesktopOptions) (window, minimized bool) {
		done := make(chan struct{})
		o.UI = func(d *Desktop) {
			window, minimized = d.WindowMode(), d.StartMinimized()
			close(done)
		}
		if err := a.Desktop(context.Background(), o); err != nil {
			t.Fatal(err)
		}
		<-done
		return
	}
	if w, m := run(DesktopOptions{}); !w || m {
		t.Fatalf("default: window %v minimized %v", w, m)
	}
	if w, m := run(DesktopOptions{Minimized: true}); !w || !m {
		t.Fatalf("--minimized: window %v minimized %v", w, m)
	}
	if _, m := run(DesktopOptions{Watchdog: true}); !m {
		t.Fatal("the watchdog's launch is not minimized")
	}
	cfg, _ := store.LoadConfig(a.Home)
	cfg.TrayOnly = true
	store.SaveConfig(a.Home, cfg)
	if w, _ := run(DesktopOptions{}); w {
		t.Fatal("trayOnly: the app still has its window")
	}
}

// The window gets the popup's rows; a second launch (Show) brings it to the
// front and syncs, a CLI sign-in (Sync) only syncs; while it is on screen
// the view redraws every second; app.dump describes it.
func TestDesktopWindow(t *testing.T) {
	_, srv := newFakeAPI(t)
	a, _, out := newTestApp(t)
	if err := a.Install(context.Background(), InstallOptions{Join: "D0M1-window", Endpoint: srv.URL, Label: "STUDIO", Yes: true, NoAutostart: true}); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	ui := newRecUI()
	done, dp := startApp(t, a, ui, nil)
	d := *dp
	waitFor(t, "the window rows", func() bool {
		ui.mu.Lock()
		defer ui.mu.Unlock()
		return len(ui.window) > 4 && slices.Equal(ui.window, ui.popup)
	})

	if d.fastRedraw() {
		t.Fatal("fast redraw with nothing on screen")
	}
	d.WindowShown(true)
	if !d.fastRedraw() {
		t.Fatal("window on screen, but no fast redraw")
	}
	d.WindowShown(false)
	if d.fastRedraw() {
		t.Fatal("window minimized, still a fast redraw")
	}

	st0, _ := store.LoadState(a.Home)
	instance.Send(a.Home, instance.Show)
	waitFor(t, "the window shown for a second launch", func() bool {
		ui.mu.Lock()
		defer ui.mu.Unlock()
		return ui.shows == 1
	})
	waitFor(t, "a tick after Show", func() bool {
		st, _ := store.LoadState(a.Home)
		return st.LastTick.After(st0.LastTick)
	})
	st1, _ := store.LoadState(a.Home)
	instance.Send(a.Home, instance.Sync)
	waitFor(t, "a tick after Sync", func() bool {
		st, _ := store.LoadState(a.Home)
		return st.LastTick.After(st1.LastTick)
	})
	ui.mu.Lock()
	shows := ui.shows
	ui.mu.Unlock()
	if shows != 1 {
		t.Fatalf("Sync showed the window (%d shows)", shows)
	}

	instance.Send(a.Home, instance.Dump)
	waitFor(t, "app.view.txt", func() bool {
		b, err := os.ReadFile(paths.AppView(a.Home))
		return err == nil && strings.Contains(string(b), "window rows:")
	})
	b, _ := os.ReadFile(paths.AppView(a.Home))
	view := string(b)
	for _, w := range []string{"mode: window", "started minimized: false", "window on screen: false", "\nwindow rows:\n  ● tokenmaxr · STUDIO", "  Quit   [action: quit]"} {
		if !strings.Contains(view, w) {
			t.Errorf("app.view.txt lacks %q:\n%s", w, view)
		}
	}

	d.Click(tray.ActQuitNow)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(appTickWait + 5*time.Second):
		t.Fatal("app did not quit")
	}
}

// The settings page shows the display mode; switching it saves trayOnly and
// restarts the app (after the reply); the same choice again does nothing.
func TestSettingsTrayOnly(t *testing.T) {
	a, _, _ := newTestApp(t)
	localKey(t, a)
	defer func(d time.Duration) { restartDelay = d }(restartDelay)
	restartDelay = time.Millisecond

	// Headless (no UI to switch): the option is shown as unavailable.
	s, base := settingsPage(t, a, nil)
	if st, _ := getState(t, base); st.App.TrayOnly || st.App.CanSwitch || st.App.Tray == "" {
		t.Fatalf("headless state %+v", st.App)
	}
	if code, res := postJSON(t, base, "api/app", map[string]bool{"trayOnly": true}); code != 200 || res["restart"] != false {
		t.Fatalf("headless switch: %d %v", code, res)
	}
	if cfg, _ := store.LoadConfig(a.Home); !cfg.TrayOnly {
		t.Fatal("trayOnly not saved")
	}
	postJSON(t, base, "api/app", map[string]bool{"trayOnly": false})

	restarts := make(chan struct{}, 4)
	s.restart = func() { restarts <- struct{}{} }
	if st, _ := getState(t, base); st.App.TrayOnly || !st.App.CanSwitch {
		t.Fatalf("app state %+v", st.App)
	}
	code, res := postJSON(t, base, "api/app", map[string]bool{"trayOnly": true})
	if code != 200 || res["restart"] != true {
		t.Fatalf("switch: %d %v", code, res)
	}
	select {
	case <-restarts:
	case <-time.After(5 * time.Second):
		t.Fatal("no restart after switching to tray only")
	}
	if cfg, _ := store.LoadConfig(a.Home); !cfg.TrayOnly {
		t.Fatal("trayOnly not saved")
	}
	if st, _ := getState(t, base); !st.App.TrayOnly {
		t.Fatalf("state after the switch %+v", st.App)
	}
	if _, res := postJSON(t, base, "api/app", map[string]bool{"trayOnly": true}); res["restart"] != false {
		t.Fatalf("an unchanged mode restarts: %v", res)
	}
	if code, res := postJSON(t, base, "api/app", map[string]bool{"trayOnly": false}); code != 200 || res["restart"] != true {
		t.Fatalf("switch back: %d %v", code, res)
	}
	<-restarts
	select {
	case <-restarts:
		t.Fatal("restarted for an unchanged mode")
	case <-time.After(50 * time.Millisecond):
	}
	page := embeddedSettingsPage(t)
	if !strings.Contains(page, `id="app-tray"`) || !strings.Contains(page, "Run only in the system tray / menu bar") {
		t.Fatal("the page lacks the tray-only checkbox")
	}
}

// From the app, the switch restarts it with the window shown.
func TestDesktopSettingsRestart(t *testing.T) {
	a, _, _ := newTestApp(t)
	localKey(t, a)
	defer func(d time.Duration) { restartDelay = d }(restartDelay)
	restartDelay = time.Millisecond
	d := a.newDesktop(context.Background())
	d.hasUI = true
	var page string
	d.open = func(u string) error { page = u; return nil }
	if err := d.openSettings(); err != nil {
		t.Fatal(err)
	}
	if st, _ := getState(t, page); !st.App.CanSwitch {
		t.Fatal("the app's page cannot switch modes")
	}
	if code, res := postJSON(t, page, "api/app", map[string]bool{"trayOnly": true}); code != 200 || res["restart"] != true {
		t.Fatalf("switch: %d %v", code, res)
	}
	waitFor(t, "the app to stop for its restart", func() bool { return d.ctx.Err() != nil })
	d.mu.Lock()
	restart, shown := d.restart, d.restartShown
	d.mu.Unlock()
	if !restart || !shown {
		t.Fatalf("restart %v shown %v", restart, shown)
	}
	if _, err := http.Get(page + "api/state"); err == nil {
		t.Fatal("the page outlived the app")
	}
}

func TestStartMenuShortcutPath(t *testing.T) {
	appData := `C:\Users\A B\AppData\Roaming`
	want := filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "tokenmaxr.lnk")
	if got := StartMenuShortcutPath(StartMenuPrograms(appData)); got != want {
		t.Fatalf("shortcut %s, want %s", got, want)
	}
	a, _, _ := newTestApp(t)
	s := a.startMenuShortcut(`P:\Programs`)
	if s.Path != filepath.Join(`P:\Programs`, "tokenmaxr.lnk") || s.Target != filepath.Join(paths.Bin(a.Home), paths.ExeName(true)) ||
		s.Args != "--home "+winArg(a.Home)+" app" || s.Dir != paths.Bin(a.Home) || s.Icon != filepath.Join(paths.Bin(a.Home), "tokenmaxr.ico") {
		t.Fatalf("shortcut %+v", s)
	}
	// Another home is no business of the real Start menu.
	if a.startMenu() != nil {
		t.Fatal("a test home uses the real Start menu")
	}
}

// Install (app mode) adds the Start-menu entry with the app's icon;
// headless mode and uninstall remove it.
func TestInstallStartMenu(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("drives the Windows entries")
	}
	_, srv := newFakeAPI(t)
	a, _, out := newTestApp(t)
	run, tasks := fakeRun{}, &fakeTasks{tasks: map[string]autostart.Options{}}
	a.Autostart = &autostart.System{GOOS: "windows", Run: run, Tasks: tasks, Spawn: func(string, []string) error { return nil }}
	a.TaskName, a.RunValue = `\tokenmaxr-TEST\collector`, "tokenmaxr-TEST"
	sm := &fakeStartMenu{programs: filepath.Join(t.TempDir(), "Programs")}
	a.StartMenu = sm
	ctx := context.Background()
	lnk := filepath.Join(sm.programs, "tokenmaxr.lnk")

	if err := a.Install(ctx, InstallOptions{Join: "D0M1-menu", Endpoint: srv.URL, Label: "box", Yes: true}); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if len(sm.made) != 1 || sm.made[0].Path != lnk || !fileExists(lnk) || !strings.Contains(out.String(), "start menu: "+lnk) {
		t.Fatalf("start menu %+v\n%s", sm.made, out)
	}
	if ico, err := os.ReadFile(sm.made[0].Icon); err != nil || len(ico) < 6 || ico[2] != 1 {
		t.Fatalf("icon %v", err)
	}
	if v := run[a.RunValue]; !strings.HasSuffix(v, " app --minimized") {
		t.Fatalf("Run value %q", v)
	}

	// Headless: the window and its entry go.
	if err := a.Install(ctx, InstallOptions{Yes: true, NoApp: true}); err != nil {
		t.Fatal(err)
	}
	if fileExists(lnk) {
		t.Fatal("headless install kept the Start-menu entry")
	}
	if err := a.Install(ctx, InstallOptions{Yes: true}); err != nil || !fileExists(lnk) {
		t.Fatalf("app install again: %v", err)
	}
	if err := a.Uninstall(UninstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if fileExists(lnk) || !strings.Contains(out.String(), "start menu entry removed") {
		t.Fatalf("uninstall kept the Start-menu entry\n%s", out)
	}
}

// The app brings an earlier version's install up to date as it starts: the
// login item starts it minimized, and the Start-menu entry exists in
// window mode.
func TestUpgradeDesktopEntries(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("drives the Windows entries")
	}
	a, _, _ := newTestApp(t)
	localKey(t, a)
	run, tasks := fakeRun{}, &fakeTasks{tasks: map[string]autostart.Options{}}
	a.Autostart = &autostart.System{GOOS: "windows", Run: run, Tasks: tasks}
	a.RunValue = "tokenmaxr-TEST"
	sm := &fakeStartMenu{programs: filepath.Join(t.TempDir(), "Programs")}
	a.StartMenu = sm
	cfg, _ := store.LoadConfig(a.Home)
	o := a.autostartOptions(&cfg, false)
	run[a.RunValue] = autostart.CommandLine(o.Exe, o.AppArgs()) // as 0.4 registered it

	done := make(chan struct{})
	if err := a.Desktop(context.Background(), DesktopOptions{Minimized: true, UI: func(*Desktop) { close(done) }}); err != nil {
		t.Fatal(err)
	}
	<-done
	if v := run[a.RunValue]; v != autostart.CommandLine(o.Exe, o.LoginArgs()) {
		t.Fatalf("Run value %q", v)
	}
	if len(sm.made) != 1 || !fileExists(filepath.Join(sm.programs, "tokenmaxr.lnk")) {
		t.Fatalf("start menu %+v", sm.made)
	}

	// Tray only, or without autostart, nothing is added.
	os.Remove(filepath.Join(sm.programs, "tokenmaxr.lnk"))
	cfg.TrayOnly = true
	store.SaveConfig(a.Home, cfg)
	if err := a.Desktop(context.Background(), DesktopOptions{UI: func(*Desktop) {}}); err != nil {
		t.Fatal(err)
	}
	cfg.TrayOnly, cfg.Autostart = false, false
	store.SaveConfig(a.Home, cfg)
	if err := a.Desktop(context.Background(), DesktopOptions{UI: func(*Desktop) {}}); err != nil {
		t.Fatal(err)
	}
	if len(sm.made) != 1 {
		t.Fatalf("start menu entry added again: %+v", sm.made)
	}
}
