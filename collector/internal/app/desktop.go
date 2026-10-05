package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/autostart"
	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
	"github.com/7-of-9/tokenmaxr/collector/internal/instance"
	"github.com/7-of-9/tokenmaxr/collector/internal/lock"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/outbox"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/recent"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
	"github.com/7-of-9/tokenmaxr/collector/internal/tray"
)

// Desktop app timing (docs/agents/SPEC.md "Desktop app (v1.4)"). Pinned,
// the live panel ticks (and so uploads) every 10 s and redraws every
// second, so its counts move almost in real time.
const (
	appTickEvery          = time.Minute
	appPinnedTickEvery    = 10 * time.Second
	appRefreshEvery       = 10 * time.Second
	appPinnedRefreshEvery = time.Second
	// appTickWait is how long a quitting app waits for a tick in flight.
	appTickWait = 20 * time.Second
	// appStopWait is how long install and uninstall wait for the app to
	// quit: its 1 s request poll plus a tick in flight.
	appStopWait = appTickWait + 5*time.Second
)

// tickInterval is the pause between ticks.
func tickInterval(pinned bool) time.Duration {
	if pinned {
		return appPinnedTickEvery
	}
	return appTickEvery
}

// refreshInterval is how often the view is redrawn (relative times move).
func refreshInterval(pinned bool) time.Duration {
	if pinned {
		return appPinnedRefreshEvery
	}
	return appRefreshEvery
}

// DesktopOptions configures the app.
type DesktopOptions struct {
	// Watchdog marks a launch by the Windows watchdog task: it leaves an
	// app quit from its menu stopped until the next login, and a launch
	// while the app runs stays silent.
	Watchdog bool
	// Minimized starts the main window minimized on the taskbar (macOS:
	// hidden, with its Dock icon), so a launch at login never takes the
	// focus (`app --minimized`, what autostart runs; the watchdog's launch
	// implies it).
	Minimized bool
	// UI shows the icon until it quits, on the calling goroutine (the main
	// one: the OS event loop needs it). nil runs headless: the collection
	// loop alone, until ctx ends or a quit request.
	UI func(d *Desktop)
}

// Desktop is the running app: the collection loop and what the icon shows.
// The UI never waits on a tick; ticks run on their own goroutine and hand
// over a small status when they finish.
type Desktop struct {
	a      *App
	ctx    context.Context
	cancel context.CancelFunc
	kick   chan bool // a tick request; true forces the upload (Sync now)
	redraw chan struct{}

	// Side effects, replaceable in tests.
	open     func(target string) error
	openText func(path string) error
	copy     func(text string) error

	mu      sync.Mutex
	ui      tray.UI
	loaded  bool
	in      tray.Input
	win     *recent.Window
	view    tray.View
	restart bool
	// restartShown restarts with the main window shown (a switch to window
	// mode from the settings page); other restarts (an update, install)
	// come back minimized, so they never take the focus.
	restartShown bool
	stopping     bool
	// ready: the UI is up (Ready ran); requests wait for it.
	ready bool
	// panel is the pinned panel as config.json keeps it.
	panel store.Panel
	// popup: the click popup is open.
	popup bool
	// trayOnly is config.json's trayOnly as the app started: no main
	// window (a change restarts the app). minimized starts the window
	// minimized; window is set while it is on screen (not minimized).
	trayOnly bool
	// showOnStart: a restart the user asked for (relaunch); tray only, the
	// popup opens once the icon exists (the window shows by itself).
	showOnStart bool
	minimized   bool
	window      bool
	// hasUI: the app shows a UI (not headless), so the settings page may
	// switch its mode, which restarts it.
	hasUI bool
	// every is the tick interval (tickInterval; tests shorten it).
	every func(pinned bool) time.Duration
	// next is when the collection loop's timer runs the next tick.
	next time.Time

	// bg are panel saves in flight; saveMu runs them one at a time.
	bg     sync.WaitGroup
	saveMu sync.Mutex

	// settings is the local settings page, started on first use.
	settings *Settings
	// uploadPeak is the largest queue since it was last empty (the size of
	// the upload in progress).
	uploadPeak int
}

// Desktop runs the app: one process with the icon and the collection loop.
// A second launch hands over to the running app (which refreshes and syncs)
// and returns nil.
func (a *App) Desktop(ctx context.Context, o DesktopOptions) error {
	if err := os.MkdirAll(a.Home, 0o700); err != nil {
		return err
	}
	if o.Watchdog && instance.Stopped(a.Home) {
		return nil // quit from its menu: stays quit until the next login
	}
	lk, err := instance.Claim(a.Home)
	if errors.Is(err, instance.ErrRunning) {
		if !o.Watchdog {
			// This launch is the user's (the Start-menu entry, a second
			// start): let the running app bring its window to the front.
			allowForeground()
			instance.Send(a.Home, instance.Show)
			if cfg, err := store.LoadConfig(a.Home); err == nil && !cfg.TrayOnly && o.UI != nil {
				a.printf(buildinfo.Product + " is already running; showing its window\n")
			} else {
				a.printf(buildinfo.Product+" is already running; look for its icon in the %s\n", trayPlace())
			}
		}
		return nil
	}
	if err != nil {
		return err
	}
	defer lk.Release()
	if !o.Watchdog {
		instance.ClearStopped(a.Home)
	}
	if err := a.OpenLog(nil); err != nil {
		return err
	}
	defer a.Log.Close()
	a.InApp = true
	AppPriority()
	a.Log.Printf("app: started (%s)", a.Version)

	d := a.newDesktop(ctx)
	// A restart through launchd (relaunch) starts minimized; this marker
	// says the user is waiting to see the app.
	if err := os.Remove(paths.AppShowOnStart(a.Home)); err == nil {
		o.Minimized, o.Watchdog, d.showOnStart = false, false, true
	}
	d.minimized, d.hasUI = o.Minimized || o.Watchdog, o.UI != nil
	if cfg, err := store.LoadConfig(a.Home); err == nil {
		if cfg.Panel != nil {
			d.panel = *cfg.Panel // pinned stays pinned across restarts
		}
		d.trayOnly = cfg.TrayOnly
		if d.hasUI {
			a.upgradeDesktopEntries(&cfg)
		}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); d.collect() }()
	go func() { defer wg.Done(); d.watch() }()
	if !o.Watchdog && a.needsFirstRunSettings() {
		// Publishing nowhere yet: show the choices once.
		a.markFirstRunSettings()
		go d.openSettings()
	}
	if o.UI != nil {
		o.UI(d)
		d.stop()
	} else {
		<-d.ctx.Done()
		d.stop()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); d.bg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(appTickWait):
		// A tick past its budget: its writes are atomic, so leaving now
		// loses nothing that the next tick does not redo.
		a.Log.Printf("app: quitting with a tick still running")
	}
	d.mu.Lock()
	restart, shown := d.restart, d.restartShown
	d.mu.Unlock()
	a.Log.Printf("app: stopped")
	if !restart {
		return nil
	}
	lk.Release()
	a.Log.Close()
	return a.reexecApp(!shown)
}

func (a *App) newDesktop(parent context.Context) *Desktop {
	ctx, cancel := context.WithCancel(parent)
	return &Desktop{
		a: a, ctx: ctx, cancel: cancel,
		kick: make(chan bool, 1), redraw: make(chan struct{}, 1),
		open: openBrowser, openText: tray.OpenText, copy: tray.Copy,
		ui: nopUI{}, every: tickInterval,
	}
}

// Ready attaches the icon (ui.Handler.Ready).
func (d *Desktop) Ready(u tray.UI) {
	d.mu.Lock()
	d.ui, d.ready = u, true
	stopping := d.stopping
	// Tray only, a restart the user asked for opens the popup: there is no
	// window to show where the app went.
	show := d.showOnStart && d.trayOnly
	d.showOnStart = false
	d.mu.Unlock()
	if stopping {
		u.Quit()
		return
	}
	d.Refresh()
	if show {
		u.ShowWindow()
	}
}

// Refresh redraws soon (the menu is about to open, or new numbers).
func (d *Desktop) Refresh() {
	select {
	case d.redraw <- struct{}{}:
	default:
	}
}

// Sync asks for a tick now, uploading despite a backoff; a request while
// one runs starts another right after it.
func (d *Desktop) Sync() {
	select {
	case d.kick <- true:
	default:
	}
	d.Refresh()
}

// Click runs a menu action (ui.Handler.Click).
func (d *Desktop) Click(act tray.Action) {
	d.mu.Lock()
	v, u := d.view, d.ui
	d.mu.Unlock()
	var err error
	switch act {
	case tray.ActCopyFleet:
		if v.Fleet != "" {
			err = d.copy(v.Fleet)
		}
	case tray.ActDashboard:
		err = d.open(v.Dashboard)
	case tray.ActGitHubDashboard:
		if v.GitHubDashboard != "" {
			err = d.open(v.GitHubDashboard)
		}
	case tray.ActSyncNow:
		d.Sync()
	case tray.ActOpenLog:
		err = d.openText(paths.Log(d.a.Home))
	case tray.ActSettings:
		err = d.openSettings()
	case tray.ActPin:
		d.SetPinned(true)
	case tray.ActUnpin:
		d.SetPinned(false)
	case tray.ActQuit, tray.ActQuitNow:
		// The popup confirms on its own row (a second click); the native
		// menu asks.
		if act == tray.ActQuit && !u.Confirm(tray.QuitPrompt) {
			return
		}
		// The watchdog leaves it quit until the next login.
		if err := instance.MarkStopped(d.a.Home); err != nil {
			d.a.Log.Printf("app: %v", err)
		}
		d.a.Log.Printf("app: quit from the menu")
		d.stop()
	}
	if err != nil {
		d.a.Log.Printf("app: action %d: %v", act, err)
	}
}

// Pinned reports whether the live panel is pinned.
func (d *Desktop) Pinned() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.panel.Pinned
}

// PopupShown records that the click popup opened or closed
// (ui.Handler.Popup): while it is open the view redraws every second, so
// its relative times move. Opening it checks again (refreshShown).
func (d *Desktop) PopupShown(open bool) {
	d.mu.Lock()
	d.popup = open
	d.mu.Unlock()
	if open {
		d.refreshShown()
	}
}

// WindowShown records that the main window was restored or brought to the
// front (true), or minimized or hidden (ui.Handler.Shown): while it is on
// screen the view redraws every second, like the popup's. Showing it checks
// again (refreshShown).
func (d *Desktop) WindowShown(visible bool) {
	d.mu.Lock()
	d.window = visible
	d.mu.Unlock()
	if visible {
		d.refreshShown()
	}
}

// showSyncGap: a UI coming on screen ticks unless a tick started this
// recently.
const showSyncGap = 20 * time.Second

// refreshShown checks again when the UI comes on screen (owner direction
// 2026-10-05: "make it check/refresh status whenever the ui is shown"): a
// tick now, so a status from before a sleep (GitHub "unreachable" from a
// tick that ran before the network was back) does not stay up until the
// next tick. A tick that runs, just started, or is about to start (the
// first one) is enough: the view only redraws.
func (d *Desktop) refreshShown() {
	d.mu.Lock()
	fresh := d.in.Ticking || d.in.TickStarted.IsZero() || d.a.Now().Sub(d.in.TickStarted) < showSyncGap
	d.mu.Unlock()
	if fresh {
		d.Refresh()
		return
	}
	d.Sync()
}

// WindowMode reports whether the app has its main window (a taskbar
// button, a Dock icon): config.json's trayOnly as the app started.
func (d *Desktop) WindowMode() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.trayOnly
}

// StartMinimized reports whether the main window starts minimized (a launch
// at login or by the watchdog).
func (d *Desktop) StartMinimized() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.minimized
}

// fastRedraw: the pinned panel, the popup or the main window is on screen.
func (d *Desktop) fastRedraw() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.panel.Pinned || d.popup || d.window
}

// SetPinned pins or unpins the live panel and remembers it in config.json.
// Pinning ticks at once and then every appPinnedTickEvery.
func (d *Desktop) SetPinned(on bool) {
	d.mu.Lock()
	changed := d.panel.Pinned != on
	d.panel.Pinned = on
	d.mu.Unlock()
	if !changed {
		return
	}
	if on {
		d.a.Log.Printf("app: panel pinned")
	} else {
		d.a.Log.Printf("app: panel unpinned")
	}
	// The pinned tick starts once the save has let go of collector.lock
	// (a tick that finds it held only re-reads the status).
	var after func()
	if on {
		after = func() {
			select {
			case d.kick <- false:
			default:
			}
		}
	}
	d.savePanel(after)
	d.Refresh()
}

// Moved records where the panel was dragged to (ui.Handler.Moved).
func (d *Desktop) Moved(x, y int) {
	d.mu.Lock()
	d.panel.X, d.panel.Y, d.panel.Placed = x, y, true
	d.mu.Unlock()
	d.savePanel(nil)
}

// savePanel writes the panel to config.json in the background, under
// collector.lock: a tick saves config.json too (account labels), and the
// lock orders the two so neither loses the other's change. An invalid
// config.json is user-edited and never overwritten. after, if set, runs
// once the lock is released.
func (d *Desktop) savePanel(after func()) {
	d.mu.Lock()
	if d.stopping {
		d.mu.Unlock()
		return
	}
	d.bg.Add(1)
	d.mu.Unlock()
	go func() {
		defer d.bg.Done()
		if err := d.writePanel(); err != nil {
			d.a.Log.Printf("app: panel not saved: %v", err)
		}
		if after != nil {
			after()
		}
	}()
}

func (d *Desktop) writePanel() error {
	d.saveMu.Lock()
	defer d.saveMu.Unlock()
	lk, err := lock.Acquire(paths.Lock(d.a.Home), TickBudget+appTickWait)
	if err != nil {
		return err
	}
	defer lk.Release()
	cfg, err := store.LoadConfig(d.a.Home)
	if err != nil {
		return err
	}
	d.mu.Lock()
	p := d.panel
	d.mu.Unlock()
	if cfg.Panel != nil && *cfg.Panel == p {
		return nil
	}
	cfg.Panel = &p
	return store.SaveConfig(d.a.Home, cfg)
}

// stop ends the loops and the UI.
func (d *Desktop) stop() {
	d.mu.Lock()
	d.stopping = true
	u, set := d.ui, d.settings
	d.mu.Unlock()
	d.cancel()
	if set != nil {
		set.Close()
	}
	u.Quit()
}

// restartFor restarts the app (a mode switch from the settings page): it
// comes back with its main window shown, in the mode config.json now says.
func (d *Desktop) restartFor(why string) {
	d.a.Log.Printf("app: restarting (%s)", why)
	d.mu.Lock()
	d.restart, d.restartShown = true, true
	d.mu.Unlock()
	d.stop()
}

// openSettings opens the settings page in the browser.
func (d *Desktop) openSettings() error {
	d.mu.Lock()
	if d.settings == nil {
		d.settings = NewSettings(d.a, d.Sync)
		if d.hasUI {
			d.settings.restart = func() { d.restartFor("display mode changed in settings") }
		}
	}
	set := d.settings
	d.mu.Unlock()
	u, err := set.URL()
	if err != nil {
		return err
	}
	// Seen once: a later start (a restart from the settings page included)
	// never opens it by itself.
	d.a.markFirstRunSettings()
	return d.open(u)
}

// collect is the collection loop: the last known status, then a tick at
// start, a minute (10 s while pinned) after each one, and on request.
// Every tick uploads what it queued.
func (d *Desktop) collect() {
	d.load(nil)
	d.Refresh()
	force := false
	for {
		d.tick(force)
		d.mu.Lock()
		wait := d.every(d.panel.Pinned)
		d.next = d.a.Now().Add(wait)
		d.mu.Unlock()
		d.Refresh() // the countdown restarts
		next := time.NewTimer(wait)
		select {
		case <-d.ctx.Done():
			next.Stop()
			return
		case <-next.C:
			force = false
		case force = <-d.kick:
			next.Stop()
		}
	}
}

// tick runs one tick under collector.lock; if another process holds it (a
// `sync-now` in a terminal), this round only re-reads the status.
func (d *Desktop) tick(force bool) {
	lk, err := lock.TryAcquire(paths.Lock(d.a.Home))
	if err != nil {
		if !errors.Is(err, lock.ErrHeld) {
			d.a.Log.Printf("app: lock: %v", err)
		}
		d.load(nil)
		d.Refresh()
		return
	}
	defer lk.Release()
	d.mu.Lock()
	d.in.Ticking, d.in.TickStarted = true, d.a.Now()
	d.mu.Unlock()
	d.Refresh()

	rep, err := d.a.Tick(d.ctx, TickOptions{Force: force, Progress: func(p TickProgress) {
		d.mu.Lock()
		d.in.Phase, d.in.Uploading = p.Phase, p.Upload.InFlight
		if p.Recent != nil {
			d.win = p.Recent
		}
		if p.Phase == "uploading" {
			d.in.Outbox = p.Upload.Remaining
		}
		d.mu.Unlock()
		d.Refresh()
	}})
	switch {
	case err == nil, errors.Is(err, ErrNotEnrolled), errors.Is(err, context.Canceled):
		err = nil
	default:
		d.a.Log.Printf("tick: %v", err)
	}
	d.load(rep.State)
	d.mu.Lock()
	d.in.Ticking, d.in.Uploading, d.in.Phase = false, false, ""
	d.in.TickErr = ""
	if err != nil {
		d.in.TickErr = err.Error()
	}
	d.mu.Unlock()
	d.Refresh()
	if rep.Updated != "" {
		d.a.Log.Printf("app: restarting on %s", rep.Updated)
		d.mu.Lock()
		d.restart = true
		d.mu.Unlock()
		d.stop()
	}
}

// load reads what the icon shows: config and identity, and the tick state
// (st, or state.json when nil). Only a few fields are kept: never the
// cursors, the token or K.
func (d *Desktop) load(st *store.State) {
	home := d.a.Home
	cfg, cfgErr := store.LoadConfig(home)
	sec, _ := store.LoadSecrets(home)
	if st == nil {
		var err error
		if st, err = store.LoadState(home); err != nil {
			d.a.Log.Printf("app: state: %v", err)
		}
	}
	in := tray.Input{
		Enrolled: serverOn(&cfg, sec), SetUp: sec.HasFleet(), Machine: cfg.MachineLabel, Endpoint: cfg.Endpoint,
		LastTick: st.LastTick, LastUploadOK: st.LastUploadOK, LastUploadErr: st.LastUploadErr,
		Unauthorized: st.Unauthorized, BackoffUntil: st.Backoff.Until,
		InitialScan: st.LastScan.IsZero(),
		Version:     d.a.Version, BuildTime: d.a.BuildTime,
	}
	if cfgErr != nil {
		in.ConfigErr = cfgErr.Error()
	}
	if k := sec.Key(); k != nil {
		in.Fleet = model.KFingerprint(k)
	}
	if githubEnabled(&cfg, sec) && st != nil {
		in.GitHub, in.GitHubErr, in.PagesURL = cfg.GitHub.Repo, st.GitHub.LastError, st.GitHub.PagesURL
		in.GitHubLogin = sec.GitHub.Login
	}
	for _, s := range st.Sources {
		in.Pending += s.Pending
	}
	ob := outbox.New(paths.Outbox(home))
	if names, _ := ob.List(); len(names) > 0 {
		_, in.Outbox = ob.Count()
	}
	win := st.RecentWindow().Clone()

	d.mu.Lock()
	defer d.mu.Unlock()
	in.Ticking, in.TickStarted, in.TickErr = d.in.Ticking, d.in.TickStarted, d.in.TickErr
	in.Phase, in.Uploading = d.in.Phase, d.in.Uploading
	d.in, d.win, d.loaded = in, win, true
}

// watch redraws every 10 s (every second while the panel is pinned or the
// popup open: relative times move) and on request, and acts on requests
// from other processes.
func (d *Desktop) watch() {
	every := refreshInterval(false)
	refresh := time.NewTicker(every)
	defer refresh.Stop()
	poll := time.NewTicker(instance.PollEvery)
	defer poll.Stop()
	for {
		select {
		case <-d.ctx.Done():
			d.stop()
			return
		case <-d.redraw:
			d.draw()
		case <-refresh.C:
			d.draw()
		case <-poll.C:
			d.requests()
		}
		if want := refreshInterval(d.fastRedraw()); want != every {
			every = want
			refresh.Reset(every)
		}
	}
}

// requests handles a second launch (show), uninstall (quit) and a binary
// replaced by install or another process's update (restart).
func (d *Desktop) requests() {
	d.mu.Lock()
	later := d.stopping || d.hasUI && !d.ready
	d.mu.Unlock()
	if later {
		// Left for the UI about to come up, or for the process a restart
		// starts (install's show follows its restart).
		return
	}
	for _, v := range instance.Take(d.a.Home) {
		switch v {
		case instance.Quit:
			d.a.Log.Printf("app: quit requested")
			d.stop()
			return
		case instance.Restart:
			d.a.Log.Printf("app: restart requested")
			d.mu.Lock()
			d.restart = true
			d.mu.Unlock()
			d.stop()
			return
		case instance.Show:
			d.mu.Lock()
			u := d.ui
			d.mu.Unlock()
			u.ShowWindow()
			d.Sync()
		case instance.Sync:
			d.Sync()
		case instance.Pin:
			d.SetPinned(true)
		case instance.Unpin:
			d.SetPinned(false)
		case instance.Settings:
			if err := d.openSettings(); err != nil {
				d.a.Log.Printf("app: settings: %v", err)
			}
		case instance.Dump:
			d.draw()
			if err := d.dump(); err != nil {
				d.a.Log.Printf("app: dump: %v", err)
			}
		}
	}
}

// draw pushes the current view to the icon.
func (d *Desktop) draw() {
	d.mu.Lock()
	if !d.loaded {
		d.mu.Unlock()
		return
	}
	in := d.in
	if in.Outbox == 0 {
		d.uploadPeak = 0
	} else if in.Outbox > d.uploadPeak {
		d.uploadPeak = in.Outbox
	}
	in.UploadPeak = d.uploadPeak
	in.Now = d.a.Now()
	in.Providers = d.win.Summaries(in.Now)
	in.Pinned = d.panel.Pinned
	in.TickEvery, in.NextTick = d.every(in.Pinned), d.next
	panel := d.panel
	u := d.ui
	d.mu.Unlock()
	v := tray.Evaluate(in)
	d.mu.Lock()
	d.view = v
	d.mu.Unlock()
	u.SetIcon(v.Color)
	u.SetTooltip(v.Tooltip)
	u.SetMenu(tray.Menu(v))
	u.SetPopup(tray.Popup(v))
	u.SetWindow(tray.Window(v))
	ps := tray.PanelState{Shown: panel.Pinned, X: panel.X, Y: panel.Y, Placed: panel.Placed}
	if ps.Shown {
		ps.Lines = tray.Panel(v)
	}
	u.SetPanel(ps)
}

// dump writes what the menu, the panel and the popup show now to
// app.view.txt, and the UI's own state (the popup window): display text
// only (no token, K or paths), for diagnostics. A headless run (no UI: no
// icon and no window) says so, and has no window to describe.
func (d *Desktop) dump() error {
	d.mu.Lock()
	v, pinned, popup, u := d.view, d.panel.Pinned, d.popup, d.ui
	trayOnly, minimized, window, hasUI := d.trayOnly, d.minimized, d.window, d.hasUI
	d.mu.Unlock()
	var b strings.Builder
	switch {
	case !hasUI:
		b.WriteString("mode: headless (no tray icon or window)\n")
	case trayOnly:
		b.WriteString("mode: tray only (config.json trayOnly)\n")
	default:
		fmt.Fprintf(&b, "mode: window (main window with a taskbar button / Dock icon, and the tray icon)\nstarted minimized: %v\nwindow on screen: %v\n", minimized, window)
	}
	fmt.Fprintf(&b, "pinned: %v\npopup open: %v\ntick every: %s\nrefresh every: %s\ncolor: %s\ntooltip: %s\n\nmenu model (the popup draws it):\n",
		pinned, popup, tickInterval(pinned), refreshInterval(pinned || popup || window), v.Color, v.Tooltip)
	for _, it := range tray.Menu(v) {
		switch {
		case it.Hidden:
		case it.Separator:
			b.WriteString("  ----\n")
		default:
			b.WriteString("  " + it.Label("\t") + "\n")
		}
	}
	b.WriteString("\npanel:\n" + tray.SheetText(tray.Panel(v), "  "))
	b.WriteString("\npopup rows:\n" + tray.SheetText(tray.Popup(v), "  "))
	if hasUI && !trayOnly {
		b.WriteString("\nwindow rows:\n" + tray.SheetText(tray.Window(v), "  "))
	}
	if s := u.Debug(); s != "" {
		b.WriteString("\nui:\n" + s)
	}
	return os.WriteFile(paths.AppView(d.a.Home), []byte(b.String()), 0o600)
}

// View is what the icon shows now (tests).
func (d *Desktop) View() tray.View {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.view
}

// nopUI stands in until the icon exists, and in headless mode.
type nopUI struct{}

func (nopUI) SetIcon(tray.Color)         {}
func (nopUI) SetTooltip(string)          {}
func (nopUI) SetMenu([]tray.Item)        {}
func (nopUI) SetPanel(tray.PanelState)   {}
func (nopUI) SetPopup([]tray.PanelLine)  {}
func (nopUI) SetWindow([]tray.PanelLine) {}
func (nopUI) ShowWindow()                {}
func (nopUI) Debug() string              { return "" }
func (nopUI) Confirm(string) bool        { return false }
func (nopUI) Quit()                      {}

// appBinary is the binary the app restarts as: <home>/bin's copy when it
// exists (install and self-update replace that one), else this one.
func (a *App) appBinary() (string, error) {
	if p := filepath.Join(paths.Bin(a.Home), paths.AppExeName()); fileExists(p) {
		return p, nil
	}
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(self); err == nil {
		self = r
	}
	return self, nil
}

// ErrRelaunch: the app exits for launchd to start it again (macOS, run by its
// LaunchAgent, whose KeepAlive restarts an unsuccessful exit). A fresh
// process gets its menu-bar item's saved place back; one restarted in place
// came back hidden past a full menu bar.
var ErrRelaunch = errors.New("relaunch through launchd")

// reexecApp starts the app again on the current binary, with the same
// arguments (minimized, or not, as asked): on macOS through launchd when it
// runs the app (ErrRelaunch; app.showonstart asks for the app to be shown),
// else in place (same pid); on Windows as a detached new process once this
// one has let go of app.lock.
func (a *App) reexecApp(minimized bool) error {
	if runtime.GOOS == "darwin" && os.Getenv("XPC_SERVICE_NAME") == autostart.AgentLabel {
		if !minimized {
			if err := os.WriteFile(paths.AppShowOnStart(a.Home), nil, 0o600); err != nil {
				a.Log.Printf("app: relaunch: %v", err)
			}
		}
		return ErrRelaunch
	}
	exe, err := a.appBinary()
	if err != nil {
		return err
	}
	return reexec(exe, restartArgs(os.Args[1:], minimized))
}

// restartArgs is the app's command line for a restart: its own arguments,
// with the app command made explicit (a start with none, or only --home,
// is the app) and --minimized set as asked. A restart that shows the window
// (a switch to it on the settings page) drops --watchdog too, which would
// start it minimized: that restart is the user's, not the watchdog's.
func restartArgs(args []string, minimized bool) []string {
	var out []string
	cmd := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !cmd && !strings.HasPrefix(arg, "-") {
			cmd = true
		}
		if cmd {
			if name, _, _ := strings.Cut(strings.TrimLeft(arg, "-"), "="); strings.HasPrefix(arg, "-") && (name == "minimized" || name == "watchdog" && !minimized) {
				continue
			}
			out = append(out, arg)
			continue
		}
		out = append(out, arg)
		if (arg == "--home" || arg == "-home") && i+1 < len(args) {
			i++
			out = append(out, args[i])
		}
	}
	if !cmd {
		out = append(out, "app")
	}
	if minimized {
		out = append(out, "--minimized")
	}
	return out
}

// autostartLine is status's autostart line.
func (a *App) autostartLine(cfg *store.Config) string {
	if !cfg.Autostart {
		return "off (install --no-autostart)"
	}
	switch m := a.autostartMode(cfg); {
	case m == "missing":
		return "missing: re-run install"
	case m == "app" && !cfg.App, m == "headless" && cfg.App:
		return m + " (config.json says otherwise: re-run install)"
	case m == "app":
		return "app at login, kept running"
	default:
		return "headless, a tick every minute"
	}
}

// appLine is status's app line.
func (a *App) appLine(cfg *store.Config) string {
	switch {
	case instance.Running(a.Home):
		return "running (app.lock held)"
	case !cfg.App:
		return "off (headless: install --no-app)"
	case instance.Stopped(a.Home):
		return "not running (quit from its menu; starts at the next login)"
	}
	return "not running"
}
