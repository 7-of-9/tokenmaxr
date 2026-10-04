package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/autostart"
	"github.com/7-of-9/tokenmaxr/collector/internal/instance"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/outbox"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
	"github.com/7-of-9/tokenmaxr/collector/internal/tray"
)

// fakePath is the user PATH.
type fakePath struct{ dirs []string }

func (f *fakePath) Add(dir string) (string, error) { f.dirs = append(f.dirs, dir); return "", nil }
func (f *fakePath) Remove(dir string) error {
	f.dirs = slices.DeleteFunc(f.dirs, func(d string) bool { return d == dir })
	return nil
}

// recUI records what the app shows, like the systray renderer would.
type recUI struct {
	mu      sync.Mutex
	color   tray.Color
	tip     string
	menu    []tray.Item
	panel   tray.PanelState
	popup   []tray.PanelLine
	draws   int
	answer  bool
	asked   []string
	quit    chan struct{}
	quitted sync.Once
}

func newRecUI() *recUI { return &recUI{quit: make(chan struct{})} }

func (u *recUI) SetIcon(c tray.Color) { u.mu.Lock(); u.color = c; u.draws++; u.mu.Unlock() }
func (u *recUI) SetTooltip(s string)  { u.mu.Lock(); u.tip = s; u.mu.Unlock() }
func (u *recUI) SetMenu(items []tray.Item) {
	u.mu.Lock()
	u.menu = slices.Clone(items)
	u.mu.Unlock()
}
func (u *recUI) SetPanel(p tray.PanelState) {
	u.mu.Lock()
	u.panel = p
	u.mu.Unlock()
}
func (u *recUI) SetPopup(lines []tray.PanelLine) {
	u.mu.Lock()
	u.popup = slices.Clone(lines)
	u.mu.Unlock()
}
func (u *recUI) Debug() string { return "" }
func (u *recUI) popupRows() []tray.PanelLine {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.popup
}
func (u *recUI) shownPanel() tray.PanelState {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.panel
}
func (u *recUI) Confirm(q string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.asked = append(u.asked, q)
	return u.answer
}
func (u *recUI) Quit() { u.quitted.Do(func() { close(u.quit) }) }

// line is the visible menu item with key, or "".
func (u *recUI) line(key string) string {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, it := range u.menu {
		if it.Key == key && !it.Hidden {
			return it.Label(" │ ")
		}
	}
	return ""
}

func (u *recUI) state() (tray.Color, string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.color, u.tip
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// startApp runs the app with ui in the background; the returned channel
// yields Desktop's error once it exits.
func startApp(t *testing.T, a *App, ui *recUI, setup func(d *Desktop)) (<-chan error, **Desktop) {
	t.Helper()
	var d *Desktop
	var mu sync.Mutex
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- a.Desktop(context.Background(), DesktopOptions{UI: func(dd *Desktop) {
			mu.Lock()
			d = dd
			mu.Unlock()
			if setup != nil {
				setup(dd)
			}
			close(ready)
			dd.Ready(ui)
			<-ui.quit
		}})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("app exited at once: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("app did not start")
	}
	return done, &d
}

func TestDesktopApp(t *testing.T) {
	_, srv := newFakeAPI(t)
	a, _, out := newTestApp(t)
	if err := a.Install(context.Background(), InstallOptions{Join: "D0M1-app", Endpoint: srv.URL, Label: "STUDIO", Yes: true, NoAutostart: true}); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	sec, _ := store.LoadSecrets(a.Home)
	fleet := model.KFingerprint(sec.Key())

	ui := newRecUI()
	var copied, opened []string
	var side sync.Mutex
	done, dp := startApp(t, a, ui, func(d *Desktop) {
		d.copy = func(s string) error { side.Lock(); copied = append(copied, s); side.Unlock(); return nil }
		d.open = func(s string) error { side.Lock(); opened = append(opened, s); side.Unlock(); return nil }
	})

	// The first tick runs at start: green, sent, and the provider line.
	waitFor(t, "green after the first tick", func() bool {
		c, tip := ui.state()
		return c == tray.Green && tip == "tokenmaxr · Up to date"
	})
	if got := ui.line("machine"); got != "● STUDIO" {
		t.Fatalf("status %q", got)
	}
	// Two synthetic events of 1 + 7 tokens, an hour old.
	claude := ui.line("provider:anthropic")
	if !strings.HasPrefix(claude, "Claude   ") || !strings.HasSuffix(claude, "   +8 │ 24h "+tray.MenuFigure("16")+"   30d "+tray.MenuFigure("16")) {
		t.Fatalf("provider line %q", claude)
	}
	if ui.line("provider:openai") != "" {
		t.Fatal("a provider never seen is shown")
	}
	if got := ui.line("identity"); got != "" {
		t.Fatalf("identity %q", got)
	}

	// Clicks: copy the fleet id, open the dashboard behind the endpoint.
	d := *dp
	d.Click(tray.ActCopyFleet)
	d.Click(tray.ActDashboard)
	side.Lock()
	if !slices.Equal(copied, []string{fleet}) || len(opened) != 1 || opened[0] != srv.URL+"/tokens" {
		t.Fatalf("copied %q opened %q", copied, opened)
	}
	side.Unlock()

	// A second launch hands over and exits 0; the first syncs.
	st0, _ := store.LoadState(a.Home)
	b := *a
	bout := &bytes.Buffer{}
	b.Out = bout
	if err := b.Desktop(context.Background(), DesktopOptions{UI: func(*Desktop) { t.Error("second launch showed a UI") }}); err != nil {
		t.Fatalf("second launch: %v", err)
	}
	if !strings.Contains(bout.String(), "already running") {
		t.Fatalf("second launch said %q", bout.String())
	}
	waitFor(t, "a tick after the handoff", func() bool {
		st, _ := store.LoadState(a.Home)
		return st.LastTick.After(st0.LastTick)
	})
	// The watchdog's launch is silent and leaves no request.
	b.Out = &bytes.Buffer{}
	if err := b.Desktop(context.Background(), DesktopOptions{Watchdog: true}); err != nil || b.Out.(*bytes.Buffer).Len() != 0 {
		t.Fatalf("watchdog launch: %v %q", err, b.Out)
	}

	// Quit asks first; "no" keeps it running.
	d.Click(tray.ActQuit)
	ui.mu.Lock()
	asked := slices.Clone(ui.asked)
	ui.mu.Unlock()
	if !instance.Running(a.Home) || len(asked) != 1 || asked[0] != tray.QuitPrompt {
		t.Fatalf("quit without confirmation: running=%v asked=%q", instance.Running(a.Home), asked)
	}
	ui.mu.Lock()
	ui.answer = true
	ui.mu.Unlock()
	d.Click(tray.ActQuit)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("app: %v", err)
		}
	case <-time.After(appTickWait + 5*time.Second):
		t.Fatal("app did not quit")
	}
	if instance.Running(a.Home) || !instance.Stopped(a.Home) {
		t.Fatal("quit from the menu must release the lock and mark it stopped")
	}
	// The watchdog honours that until a normal start (login) clears it.
	if err := b.Desktop(context.Background(), DesktopOptions{Watchdog: true, UI: func(*Desktop) { t.Error("watchdog restarted a quit app") }}); err != nil {
		t.Fatal(err)
	}
	if err := b.Desktop(context.Background(), DesktopOptions{UI: func(*Desktop) {}}); err != nil {
		t.Fatal(err)
	}
	if instance.Stopped(a.Home) {
		t.Fatal("a normal start did not clear the stopped mark")
	}
}

func TestDesktopHeadlessQuitRequest(t *testing.T) {
	_, srv := newFakeAPI(t)
	a, _, out := newTestApp(t)
	if err := a.Install(context.Background(), InstallOptions{Join: "D0M1-headless", Endpoint: srv.URL, Label: "box", Yes: true, NoAutostart: true}); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	done := make(chan error, 1)
	go func() { done <- a.Desktop(context.Background(), DesktopOptions{}) }()
	waitFor(t, "the app to hold its lock", func() bool { return instance.Running(a.Home) })
	waitFor(t, "the first tick", func() bool {
		st, _ := store.LoadState(a.Home)
		return !st.LastTick.IsZero()
	})
	// Uninstall's path: a quit request, and the lock goes.
	if !instance.Stop(a.Home, appStopWait) {
		t.Fatal("headless app did not stop")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if instance.Stopped(a.Home) {
		t.Fatal("a quit request is not a quit from the menu")
	}
}

func TestTickLocalNumbersIdempotent(t *testing.T) {
	_, srv := newFakeAPI(t)
	a, _, out := newTestApp(t)
	ctx := context.Background()
	if err := a.Install(ctx, InstallOptions{Join: "D0M1-n", Endpoint: srv.URL, Label: "box", Yes: true, NoAutostart: true}); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	month := func() int64 {
		st, _ := store.LoadState(a.Home)
		for _, s := range st.RecentWindow().Summaries(time.Now()) {
			if s.Provider == "anthropic" {
				return s.PastMonth
			}
		}
		return -1
	}
	if _, err := a.Tick(ctx, TickOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := month(); got != 16 {
		t.Fatalf("30d after the first tick: %d", got)
	}
	// The weekly full reparse reads everything again: nothing doubles.
	st, _ := store.LoadState(a.Home)
	st.LastFullReparse = time.Now().Add(-8 * 24 * time.Hour)
	store.SaveState(a.Home, st)
	for range 2 {
		if _, err := a.Tick(ctx, TickOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if got := month(); got != 16 {
		t.Fatalf("30d after reparses: %d", got)
	}
}

// fakeRun and fakeTasks are the Windows Run key and Task Scheduler.
type fakeRun map[string]string

func (f fakeRun) Get(name string) (string, bool, error) { v, ok := f[name]; return v, ok, nil }
func (f fakeRun) Set(name, value string) error          { f[name] = value; return nil }
func (f fakeRun) Delete(name string) error              { delete(f, name); return nil }

type fakeTasks struct {
	tasks   map[string]autostart.Options
	started []string
}

func (f *fakeTasks) Install(o autostart.Options) (string, error) {
	f.tasks[o.Name] = o
	return o.Name, nil
}
func (f *fakeTasks) Start(name string) error              { f.started = append(f.started, name); return nil }
func (f *fakeTasks) Uninstall(name string) error          { delete(f.tasks, name); return nil }
func (f *fakeTasks) Present(name string) bool             { _, ok := f.tasks[name]; return ok }
func (f *fakeTasks) Describe(name string) (string, error) { return "Status: Ready", nil }

func TestInstallAutostartModes(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("drives the Windows entries (the macOS agent is covered in internal/autostart)")
	}
	_, srv := newFakeAPI(t)
	a, _, out := newTestApp(t)
	run, tasks := fakeRun{}, &fakeTasks{tasks: map[string]autostart.Options{}}
	var spawned [][]string
	a.Autostart = &autostart.System{GOOS: "windows", Run: run, Tasks: tasks, Spawn: func(exe string, args []string) error {
		spawned = append(spawned, append([]string{exe}, args...))
		return nil
	}}
	a.TaskName, a.RunValue = `\d0m1-TEST\collector`, "d0m1-collector-TEST"
	path := a.UserPath.(*fakePath)
	ctx := context.Background()

	// App mode (the default): Run value, watchdog task, app started.
	instance.MarkStopped(a.Home)
	if err := a.Install(ctx, InstallOptions{Join: "D0M1-auto", Endpoint: srv.URL, Label: "box", Yes: true}); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if v := run[a.RunValue]; !strings.HasSuffix(v, `" --home `+winArg(a.Home)+` app`) {
		t.Fatalf("Run value %q", v)
	}
	if o := tasks.tasks[a.TaskName]; !o.App || !slices.Equal(o.TaskArgs(), []string{"--home", a.Home, "app", "--watchdog"}) {
		t.Fatalf("watchdog %+v", o)
	}
	if len(spawned) != 1 || spawned[0][len(spawned[0])-1] != "app" || instance.Stopped(a.Home) {
		t.Fatalf("app not started (or still marked stopped): %q", spawned)
	}
	if len(path.dirs) != 1 {
		t.Fatalf("PATH %v", path.dirs)
	}
	out.Reset()
	if err := a.Status(); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "autostart     app at login, kept running") || !strings.Contains(s, "app           not running") {
		t.Fatalf("status:\n%s", s)
	}

	// Re-install while the app runs: it is told to restart on the new binary.
	lk, err := instance.Claim(a.Home)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Install(ctx, InstallOptions{Yes: true}); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	if got := instance.Take(a.Home); !slices.Equal(got, []instance.Verb{instance.Restart}) || len(spawned) != 1 {
		t.Fatalf("requests %v, spawned %d", got, len(spawned))
	}
	out.Reset()
	a.Status()
	if !strings.Contains(out.String(), "app           running (app.lock held)") {
		t.Fatalf("status:\n%s", out)
	}

	// --no-app: the app is asked to quit, the Run value goes, the task ticks.
	quitting := make(chan struct{})
	go func() {
		defer close(quitting)
		for range 200 {
			if slices.Contains(instance.Take(a.Home), instance.Quit) {
				lk.Release()
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
	}()
	if err := a.Install(ctx, InstallOptions{Yes: true, NoApp: true}); err != nil {
		t.Fatalf("headless install: %v", err)
	}
	<-quitting
	if _, ok := run[a.RunValue]; ok || instance.Running(a.Home) {
		t.Fatalf("headless: Run value kept or app running (%v)", run)
	}
	if o := tasks.tasks[a.TaskName]; o.App || !slices.Equal(o.TaskArgs(), []string{"--home", a.Home, "run"}) || !slices.Equal(tasks.started, []string{a.TaskName}) {
		t.Fatalf("headless task %+v started %q", o, tasks.started)
	}
	if cfg, _ := store.LoadConfig(a.Home); cfg.App {
		t.Fatal("config still in app mode")
	}

	// Uninstall removes everything it registered.
	if err := a.Uninstall(UninstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(run) != 0 || len(tasks.tasks) != 0 || len(path.dirs) != 0 {
		t.Fatalf("left: run %v tasks %v path %v", run, tasks.tasks, path.dirs)
	}
}

// winArg is how CommandLine quotes one argument.
func winArg(s string) string {
	line := autostart.CommandLine("x", []string{s})
	return strings.TrimPrefix(line, `"x" `)
}

func TestDesktopRestartRequest(t *testing.T) {
	a, _, _ := newTestApp(t)
	d := a.newDesktop(context.Background())
	os.MkdirAll(a.Home, 0o700)
	instance.Send(a.Home, instance.Restart)
	d.requests()
	if !d.restart || d.ctx.Err() == nil {
		t.Fatalf("restart %v, ctx %v", d.restart, d.ctx.Err())
	}
	if !errors.Is(d.ctx.Err(), context.Canceled) {
		t.Fatal(d.ctx.Err())
	}
}

func TestTickInterval(t *testing.T) {
	for _, c := range []struct {
		pinned        bool
		tick, refresh time.Duration
	}{
		{false, time.Minute, 10 * time.Second},
		{true, 10 * time.Second, time.Second},
	} {
		if got := tickInterval(c.pinned); got != c.tick {
			t.Errorf("tickInterval(%v) = %s, want %s", c.pinned, got, c.tick)
		}
		if got := refreshInterval(c.pinned); got != c.refresh {
			t.Errorf("refreshInterval(%v) = %s, want %s", c.pinned, got, c.refresh)
		}
	}
}

// TestDesktopPin: pinning shows the panel, ticks fast and is remembered in
// config.json across a restart; unpinning (the ×) closes it and slows the
// ticks down again.
func TestDesktopPin(t *testing.T) {
	_, srv := newFakeAPI(t)
	a, _, out := newTestApp(t)
	if err := a.Install(context.Background(), InstallOptions{Join: "D0M1-pin", Endpoint: srv.URL, Label: "STUDIO", Yes: true, NoAutostart: true}); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	// pinnedEvery is the pinned interval the app sees (an hour at first, to
	// show that pinning itself ticks at once).
	var pinnedEvery atomic.Int64
	pinnedEvery.Store(int64(time.Hour))
	fast := func(d *Desktop) {
		d.mu.Lock()
		d.every = func(pinned bool) time.Duration {
			if pinned {
				return time.Duration(pinnedEvery.Load())
			}
			return time.Hour
		}
		d.mu.Unlock()
	}
	lastTick := func() time.Time {
		st, _ := store.LoadState(a.Home)
		return st.LastTick
	}
	savedPanel := func() store.Panel {
		cfg, _ := store.LoadConfig(a.Home)
		if cfg.Panel == nil {
			return store.Panel{}
		}
		return *cfg.Panel
	}
	ui := newRecUI()
	done, dp := startApp(t, a, ui, fast)
	d := *dp
	waitFor(t, "the first tick", func() bool { return !lastTick().IsZero() })
	if ui.shownPanel().Shown || ui.line("pin") != "Pin to screen" {
		t.Fatalf("panel %+v, pin item %q", ui.shownPanel(), ui.line("pin"))
	}

	first := lastTick()
	d.Click(tray.ActPin)
	waitFor(t, "the panel", func() bool { p := ui.shownPanel(); return p.Shown && len(p.Lines) > 0 })
	waitFor(t, "a tick on pinning", func() bool { return lastTick().After(first) })
	if got := ui.line("pin"); got != "Unpin" {
		t.Fatalf("pin item %q", got)
	}
	p := ui.shownPanel()
	if len(p.Lines) != 5 || p.Lines[0].Text != "● tokenmaxr · STUDIO" || p.Lines[0].Kind != tray.LineOK || p.Lines[1].Kind != tray.LineDim || !strings.HasPrefix(p.Lines[2].Text, "GitHub: not signed in") || p.Lines[3].Kind != tray.LineRule || !strings.HasPrefix(p.Lines[4].Text, "Claude   ") {
		t.Fatalf("panel lines %+v", p.Lines)
	}
	for _, l := range p.Lines {
		if strings.Contains(l.Text, "Checks local") || strings.Contains(l.Text, "Pushes new") {
			t.Fatalf("panel still has the log and push lines: %+v", p.Lines)
		}
	}
	// Pinned: ticks follow each other at the pinned interval.
	pinnedEvery.Store(int64(50 * time.Millisecond))
	d.Sync() // re-arms the timer with the new interval
	seen := map[time.Time]bool{}
	waitFor(t, "fast ticks", func() bool { seen[lastTick()] = true; return len(seen) >= 4 })
	d.Moved(40, 50)
	waitFor(t, "the pin saved", func() bool { return savedPanel() == store.Panel{Pinned: true, X: 40, Y: 50, Placed: true} })

	// A restart comes back pinned, where it was left.
	if !instance.Stop(a.Home, appStopWait) {
		t.Fatal("app did not stop")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	ui = newRecUI()
	done, dp = startApp(t, a, ui, fast)
	d = *dp
	waitFor(t, "the panel after a restart", func() bool {
		p := ui.shownPanel()
		return p.Shown && p.X == 40 && p.Y == 50 && p.Placed
	})

	// The × unpins: closed, remembered, and back to the slow interval.
	d.Click(tray.ActUnpin)
	waitFor(t, "the panel closed", func() bool { return !ui.shownPanel().Shown && ui.line("pin") == "Pin to screen" })
	waitFor(t, "the unpin saved", func() bool { return !savedPanel().Pinned && savedPanel().Placed })
	time.Sleep(300 * time.Millisecond) // a pinned-interval tick may still be due
	before := lastTick()
	time.Sleep(500 * time.Millisecond)
	if !lastTick().Equal(before) {
		t.Fatal("unpinned, the app still ticks at the pinned interval")
	}
	if !instance.Stop(a.Home, appStopWait) {
		t.Fatal("app did not stop")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDesktopPopup(t *testing.T) {
	_, srv := newFakeAPI(t)
	a, _, out := newTestApp(t)
	if err := a.Install(context.Background(), InstallOptions{Join: "D0M1-popup", Endpoint: srv.URL, Label: "STUDIO", Yes: true, NoAutostart: true}); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	ui := newRecUI()
	done, dp := startApp(t, a, ui, nil)
	d := *dp
	// The popup's rows follow every redraw: the panel's sheet, then the
	// actions ending in Quit, then the recessive build footer.
	waitFor(t, "the popup rows, idle", func() bool {
		rows := ui.popupRows()
		return len(rows) > 4 && rows[len(rows)-1].Text == "dev build" && rows[len(rows)-1].Kind == tray.LineDim &&
			rows[len(rows)-2].Text == "Quit" && rows[len(rows)-5].Text == "Sync now" &&
			rows[0].Text == "● tokenmaxr · STUDIO" && rows[0].Kind == tray.LineOK
	})
	var acts []tray.Action
	for _, l := range ui.popupRows() {
		if l.Hoverable() {
			acts = append(acts, l.Action)
		}
	}
	if want := []tray.Action{tray.ActDashboard, tray.ActPin, tray.ActSyncNow, tray.ActSettings, tray.ActOpenLog, tray.ActQuit}; !slices.Equal(acts, want) {
		t.Fatalf("popup actions %v, want %v", acts, want)
	}

	// Open, it redraws every second (relative times move); closed, not.
	if d.fastRedraw() {
		t.Fatal("fast redraw with nothing on screen")
	}
	d.PopupShown(true)
	if !d.fastRedraw() {
		t.Fatal("popup open, but no fast redraw")
	}
	ui.mu.Lock()
	before := ui.draws
	ui.mu.Unlock()
	waitFor(t, "redraws while the popup is open", func() bool {
		ui.mu.Lock()
		defer ui.mu.Unlock()
		return ui.draws >= before+3
	})
	d.PopupShown(false)
	if d.fastRedraw() {
		t.Fatal("popup closed, still a fast redraw")
	}

	// The popup's Quit row confirms itself (a second click): no dialog.
	d.Click(tray.ActQuitNow)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("app: %v", err)
		}
	case <-time.After(appTickWait + 5*time.Second):
		t.Fatal("app did not quit")
	}
	ui.mu.Lock()
	asked := len(ui.asked)
	ui.mu.Unlock()
	if asked != 0 || !instance.Stopped(a.Home) {
		t.Fatalf("quit-now asked %d times, stopped=%v", asked, instance.Stopped(a.Home))
	}
}

func TestDesktopLiveUploadProgress(t *testing.T) {
	_, api := newFakeAPI(t)
	gates := []chan struct{}{make(chan struct{}), make(chan struct{})}
	var closes [2]sync.Once
	release := func(i int) { closes[i].Do(func() { close(gates[i]) }) }
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/ingest" {
			n := int(requests.Add(1)) - 1
			if n < len(gates) {
				<-gates[n]
			}
		}
		api.Config.Handler.ServeHTTP(w, r)
	}))
	defer srv.Close()
	defer func() { release(0); release(1) }()
	a, _, out := newTestApp(t)
	if err := a.Install(context.Background(), InstallOptions{Join: "D0M1-live", Endpoint: srv.URL, Label: "STUDIO", Yes: true, NoAutostart: true}); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	var batch outbox.Batch
	for i := range 900 {
		batch.Usage = append(batch.Usage, model.UsageEvent{ID: fmt.Sprintf("progress-%d", i), Provider: "anthropic"})
	}
	if _, err := outbox.New(paths.Outbox(a.Home)).Write(batch); err != nil {
		t.Fatal(err)
	}
	ui := newRecUI()
	done, dp := startApp(t, a, ui, func(d *Desktop) {
		d.mu.Lock()
		d.panel.Pinned = true
		d.every = func(bool) time.Duration { return time.Hour }
		d.mu.Unlock()
	})
	d := *dp
	count := func() int { d.mu.Lock(); defer d.mu.Unlock(); return d.in.Outbox }
	check := func(n int) {
		waitFor(t, "live batch in both views", func() bool {
			p, rows := ui.shownPanel(), ui.popupRows()
			want := "● tokenmaxr · STUDIO"
			left := " · " + tray.Count(n) + " left"
			up := func(l string) bool { return strings.HasPrefix(l, "Uploading ") && strings.HasSuffix(l, left) }
			return len(p.Lines) > 1 && len(rows) > 1 && p.Lines[0].Text == want && rows[0].Text == want && p.Lines[0].Kind == tray.LineOK && up(p.Lines[1].Text) && rows[1].Text == p.Lines[1].Text && count() == n
		})
	}
	waitFor(t, "first request", func() bool { return requests.Load() == 1 })
	// The first network request is still blocked: the just-scanned provider
	// must already be visible in both views, independently of upload success.
	waitFor(t, "local provider before the first upload completes", func() bool {
		hasProvider := func(lines []tray.PanelLine) bool {
			for _, l := range lines {
				if strings.HasPrefix(l.Text, "Claude   ") {
					return true
				}
			}
			return false
		}
		return hasProvider(ui.shownPanel().Lines) && hasProvider(ui.popupRows())
	})
	first := count()
	check(first)
	release(0)
	waitFor(t, "second request", func() bool { return requests.Load() == 2 })
	second := count()
	if second >= first || second <= 0 {
		t.Fatalf("remaining count did not decrease: %d -> %d", first, second)
	}
	check(second)
	release(1)
	waitFor(t, "idle after upload", func() bool {
		return ui.line("machine") == "● STUDIO" && ui.line("sync") == "Sync now"
	})
	d.stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
