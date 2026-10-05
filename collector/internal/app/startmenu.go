package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/7-of-9/tokenmaxr/collector/internal/autostart"
	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
	"github.com/7-of-9/tokenmaxr/collector/internal/tray"
)

// The Windows Start-menu entry (window mode): install adds
// %APPDATA%\Microsoft\Windows\Start Menu\Programs\tokenmaxr.lnk, which runs
// <home>\bin\tokenmaxrw.exe app; uninstall removes it. Started while the
// app runs, it restores and focuses the app's window (a second launch).

// Shortcut is one Windows shell link.
type Shortcut struct {
	// Path is the .lnk file.
	Path        string
	Target      string
	Args        string
	Dir         string
	Icon        string
	Description string
}

// StartMenu makes and removes the Start-menu shortcut (tests pass fakes).
type StartMenu interface {
	// Programs is the user's Start-menu Programs folder.
	Programs() (string, error)
	Create(s Shortcut) error
	Remove(path string) error
}

// defaultStartMenu is the shell's (startmenu_windows.go); the package's
// TestMain replaces it, so tests never touch the real Start menu.
var defaultStartMenu StartMenu = noStartMenu{}

// noStartMenu is every OS without a Start menu.
type noStartMenu struct{}

func (noStartMenu) Programs() (string, error) { return "", errors.ErrUnsupported }
func (noStartMenu) Create(Shortcut) error     { return errors.ErrUnsupported }
func (noStartMenu) Remove(string) error       { return nil }

// StartMenuPrograms is the Programs folder under %APPDATA%.
func StartMenuPrograms(appData string) string {
	return filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs")
}

// StartMenuShortcutPath is the app's entry in a Programs folder:
// <programs>\tokenmaxr.lnk.
func StartMenuShortcutPath(programs string) string {
	return filepath.Join(programs, tray.WindowTitle+".lnk")
}

// startMenu is the Start menu this install manages, or nil: a fake when a
// test set one, else the real one, for the default home only (an install
// in another directory never replaces the main install's entry).
func (a *App) startMenu() StartMenu {
	if a.StartMenu != nil {
		return a.StartMenu
	}
	if runtime.GOOS != "windows" || !paths.IsDefaultHome(a.Home) {
		return nil
	}
	return defaultStartMenu
}

// startMenuShortcut is this install's entry: the windowsgui binary in
// <home>\bin starting the app (with --home for another home), with the
// app's icon (written next to it).
func (a *App) startMenuShortcut(programs string) Shortcut {
	o := a.autostartOptions(&store.Config{App: true}, false)
	o.Exe = a.binPath(true)
	return Shortcut{
		Path:        StartMenuShortcutPath(programs),
		Target:      o.Exe,
		Args:        autostart.ArgLine(o.AppArgs()),
		Dir:         paths.Bin(a.Home),
		Icon:        filepath.Join(paths.Bin(a.Home), startMenuIconName()),
		Description: buildinfo.Product + ": AI token usage collector",
	}
}

// startMenuIconName is the entry's icon file in <home>\bin, named from its
// contents (tokenmaxr-<8 hex of its SHA-256>.ico): the shell caches icons
// by path and index, so a new icon needs a new path, never a rewrite.
func startMenuIconName() string {
	sum := sha256.Sum256(tray.IconICO(tray.Green))
	return buildinfo.Product + "-" + hex.EncodeToString(sum[:4]) + ".ico"
}

// removeOldStartMenuIcons deletes the icons earlier versions wrote next to
// keep (tokenmaxr.ico, tokenmaxr-<hash>.ico).
func removeOldStartMenuIcons(keep string) {
	old, _ := filepath.Glob(filepath.Join(filepath.Dir(keep), buildinfo.Product+"*.ico"))
	for _, p := range old {
		if p != keep {
			_ = os.Remove(p)
		}
	}
}

// startMenuMark records that this install made its Start-menu entry once,
// so an entry the user deleted is not made again when the app starts (only
// install makes it again).
func startMenuMark(home string) string { return filepath.Join(home, "start-menu-added") }

func markStartMenu(home string) {
	_ = os.WriteFile(startMenuMark(home), []byte("1\n"), 0o600)
}

// addStartMenu creates (or refreshes) the Start-menu entry. It returns
// the shortcut's path, "" when this install has none.
func (a *App) addStartMenu() (string, error) {
	sm := a.startMenu()
	if sm == nil {
		return "", nil
	}
	programs, err := sm.Programs()
	if err != nil {
		return "", err
	}
	s := a.startMenuShortcut(programs)
	if err := os.MkdirAll(filepath.Dir(s.Icon), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(s.Icon, tray.IconICO(tray.Green), 0o644); err != nil {
		return "", err
	}
	if err := sm.Create(s); err != nil {
		return "", err
	}
	removeOldStartMenuIcons(s.Icon) // the link no longer points at them
	markStartMenu(a.Home)
	return s.Path, nil
}

// removeStartMenu deletes the Start-menu entry, if any. It reports whether
// there was one.
func (a *App) removeStartMenu() (bool, error) {
	sm := a.startMenu()
	if sm == nil {
		return false, nil
	}
	programs, err := sm.Programs()
	if err != nil {
		return false, err
	}
	p := StartMenuShortcutPath(programs)
	if !fileExists(p) {
		return false, nil
	}
	return true, sm.Remove(p)
}

// upgradeDesktopEntries brings an install made by an earlier version up to
// this one's desktop entries when the app starts (self-update replaces the
// binary, not the entries): the login item starts the app minimized, the
// macOS Applications entry exists and runs this binary, and the Start-menu
// entry is brought up to date (upgradeStartMenu). Each is touched only when
// this install registered autostart for the app.
func (a *App) upgradeDesktopEntries(cfg *store.Config) {
	if !cfg.Autostart || !cfg.App {
		return
	}
	if a.Autostart != nil || paths.IsDefaultHome(a.Home) {
		if up, err := a.autostartSys().UpgradeLoginItem(a.autostartOptions(cfg, false)); err != nil {
			a.Log.Printf("app: login item: %v", err)
		} else if up {
			a.Log.Printf("app: login item now starts the app minimized")
		}
	}
	if p, changed, err := a.addApplications(); err != nil {
		a.Log.Printf("app: applications entry: %v", err)
	} else if changed {
		a.Log.Printf("app: applications entry written: %s", p)
	}
	a.upgradeStartMenu(cfg)
}

// upgradeStartMenu brings the Windows Start-menu entry up to date: an
// existing one, in either mode, gets this version's icon (a link pointing
// at another icon file is saved again, pointing at this one's; the shell
// is told, so it drops the icon it cached); in window mode a missing one
// is made, once (startMenuMark: one the user deleted stays deleted). Tray
// only, none is ever made here.
func (a *App) upgradeStartMenu(cfg *store.Config) {
	sm := a.startMenu()
	if sm == nil {
		return
	}
	programs, err := sm.Programs()
	if err != nil {
		return
	}
	if fileExists(StartMenuShortcutPath(programs)) {
		markStartMenu(a.Home) // made by an earlier version's install
		if !fileExists(a.startMenuShortcut(programs).Icon) {
			if _, err := a.addStartMenu(); err != nil {
				a.Log.Printf("app: start menu icon: %v", err)
			} else {
				a.Log.Printf("app: start menu icon updated")
			}
		}
		return
	}
	if cfg.TrayOnly || fileExists(startMenuMark(a.Home)) {
		return
	}
	if p, err := a.addStartMenu(); err != nil {
		a.Log.Printf("app: start menu: %v", err)
	} else {
		a.Log.Printf("app: start menu entry added: %s", p)
	}
}
