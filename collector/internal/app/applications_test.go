package app

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/7-of-9/tokenmaxr/collector/internal/autostart"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

// The macOS Applications entry follows the app: install adds it in either
// app mode, --no-app and uninstall remove it, and the app puts it back as
// it starts (an install from before it existed, or one the owner deleted).
func TestApplicationsEntry(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("tokenmaxr.app is macOS only")
	}
	_, srv := newFakeAPI(t)
	a, _, out := newTestApp(t)
	a.Autostart = &autostart.System{GOOS: "darwin", AgentsDir: t.TempDir(), Domain: "gui/501",
		Launchctl: func(...string) (string, error) { return "", nil }}
	a.ApplicationsDirs = []string{t.TempDir()}
	bundle := filepath.Join(a.ApplicationsDirs[0], "tokenmaxr.app")
	present := func() bool { return fileExists(filepath.Join(bundle, "Contents", "Info.plist")) }
	ctx := context.Background()

	if err := a.Install(ctx, InstallOptions{Join: "D0M1-apps", Endpoint: srv.URL, Label: "box", Yes: true}); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	launcher, err := os.ReadFile(filepath.Join(bundle, "Contents", "MacOS", "tokenmaxr"))
	if err != nil || !strings.HasSuffix(string(launcher), "exec '"+a.binPath(false)+"' app\n") || !strings.Contains(out.String(), "applications: "+bundle) {
		t.Fatalf("bundle %v, launcher:\n%s\noutput:\n%s", err, launcher, out)
	}

	// Headless: the entry goes; back in app mode it returns.
	if err := a.Install(ctx, InstallOptions{Yes: true, NoApp: true}); err != nil || present() {
		t.Fatalf("headless install: %v, bundle kept %v", err, present())
	}
	if err := a.Install(ctx, InstallOptions{Yes: true}); err != nil || !present() {
		t.Fatalf("app install again: %v\n%s", err, out)
	}

	// The app's start puts a missing entry back, tray only included.
	cfg, _ := store.LoadConfig(a.Home)
	for _, trayOnly := range []bool{false, true} {
		os.RemoveAll(bundle)
		cfg.TrayOnly = trayOnly
		a.upgradeDesktopEntries(&cfg)
		if !present() {
			t.Fatalf("tray only %v: entry not restored", trayOnly)
		}
	}

	if err := a.Uninstall(UninstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if present() || !strings.Contains(out.String(), "applications entry removed") {
		t.Fatalf("uninstall kept the entry\n%s", out)
	}
}
