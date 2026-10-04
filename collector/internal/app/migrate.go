package app

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/autostart"
	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
	"github.com/7-of-9/tokenmaxr/collector/internal/instance"
	"github.com/7-of-9/tokenmaxr/collector/internal/lock"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

// tokenmaxr began as d0m1.com's collector, "d0m1-collector". A machine that
// ran it moves to tokenmaxr once, by itself, when the new binary first runs:
// the old updater installs tokenmaxr in its own bin folder under the old
// names (the last release through the old channel), the app restarts on it,
// and Migrate moves everything to the tokenmaxr home. The machine keeps its
// enrolment (its server stays as configured), fleet key, cursors, outbox,
// rollup and GitHub sign-in; its autostart and PATH entries are swapped for
// tokenmaxr's; updates come from the tokenmaxr channel from then on.

// legacyUpdateURL is the old channel; a config naming it is reset to the
// default so updates come from tokenmaxr's releases.
const legacyUpdateURL = "https://github.com/7-of-9/tokenmaxr/collector/latest.json"

// migratedMark in the old home says it was moved, and where to.
const migratedMark = "MOVED-TO-TOKENMAXR.txt"

// skipOnMigrate are the old home's entries that are not state: locks and
// mailboxes of the old process, its binaries, and the view dump.
var skipOnMigrate = map[string]bool{
	"bin": true, "app.lock": true, "collector.lock": true, "app.view.txt": true, migratedMark: true,
}

// MigrateResult says what Migrate did.
type MigrateResult struct {
	// Moved: the old home's state now lives in a.Home.
	Moved bool
	// Handover: this process runs the old binary; the new app was started
	// and this one should exit.
	Handover bool
	// Cleaned: a migrated old home was removed.
	Cleaned bool
}

// Migrate moves a d0m1-collector install to tokenmaxr (see above), or
// removes an old home that was migrated before. It does nothing for a
// machine that never ran d0m1-collector, or with an explicit --home (legacy
// is "" then).
func (a *App) Migrate(legacy string) (MigrateResult, error) {
	var res MigrateResult
	if legacy == "" || sameDir(legacy, a.Home) {
		return res, nil
	}
	info, err := os.Stat(legacy)
	if err != nil || !info.IsDir() {
		return res, nil
	}
	if fileExists(filepath.Join(legacy, migratedMark)) {
		// Moved before: once nothing runs from it any more, remove it.
		if !instance.Running(legacy) && !runningFrom(paths.Bin(legacy)) {
			if err := os.RemoveAll(legacy); err == nil {
				res.Cleaned = true
				a.Log.Printf("migrate: removed the old home %s", legacy)
			}
		}
		return res, nil
	}
	if !fileExists(filepath.Join(legacy, "secrets.json")) && !fileExists(filepath.Join(legacy, "config.json")) {
		return res, nil
	}
	if fileExists(filepath.Join(a.Home, "secrets.json")) {
		// tokenmaxr was set up separately: never overwrite it. The old
		// install stays for its owner to remove (uninstall).
		a.Log.Printf("migrate: %s and %s both exist; leaving the old one", legacy, a.Home)
		return res, nil
	}
	if instance.Running(legacy) {
		// An old app still runs there (its update to tokenmaxr comes
		// through its own updater, which restarts it on this binary).
		return res, nil
	}

	// The old home's lock keeps its scheduler ticks (and a second new
	// process: the watchdog may start one) out while files move.
	lk, err := lock.Acquire(paths.Lock(legacy), 2*time.Minute)
	if err != nil {
		return res, fmt.Errorf("migrate: waiting for the old collector: %w", err)
	}
	defer lk.Release()
	if fileExists(filepath.Join(legacy, migratedMark)) || fileExists(filepath.Join(a.Home, "secrets.json")) {
		return res, nil // another process migrated while this one waited
	}
	if err := os.MkdirAll(a.Home, 0o700); err != nil {
		return res, err
	}
	if err := copyState(legacy, a.Home); err != nil {
		return res, fmt.Errorf("migrate: %w", err)
	}
	// Secrets through the store, so the new file gets its user-only ACL.
	sec, err := store.LoadSecrets(legacy)
	if err != nil {
		return res, fmt.Errorf("migrate: %w", err)
	}
	if err := store.SaveSecrets(a.Home, sec); err != nil {
		return res, fmt.Errorf("migrate: %w", err)
	}
	cfg, err := store.LoadConfig(a.Home)
	if err != nil {
		return res, fmt.Errorf("migrate: %w", err)
	}
	if cfg.UpdateURL == legacyUpdateURL {
		cfg.UpdateURL = "" // the default: tokenmaxr's releases
	}
	if err := store.SaveConfig(a.Home, cfg); err != nil {
		return res, fmt.Errorf("migrate: %w", err)
	}
	cfg, _ = store.LoadConfig(a.Home)
	res.Moved = true
	a.Log.Printf("migrate: moved %s to %s (server %q)", legacy, a.Home, cfg.Server())

	// Binaries: this one (and its console or app twin) under the new names.
	if err := a.copyBinariesFrom(paths.Bin(legacy)); err != nil {
		return res, fmt.Errorf("migrate: %w", err)
	}
	// The move is complete: mark the old home before anything starts from the
	// new one.
	note := fmt.Sprintf("This d0m1-collector install moved to %s on %s (%s). This folder is removed automatically.\n",
		a.Home, a.Now().UTC().Format(time.RFC3339), buildinfo.Product)
	if err := os.WriteFile(filepath.Join(legacy, migratedMark), []byte(note), 0o600); err != nil {
		return res, fmt.Errorf("migrate: %w", err)
	}
	// Login and PATH entries: tokenmaxr's in first, the old ones out last.
	// On macOS removing the old LaunchAgent stops the process it runs, which
	// may be this one, so nothing may follow it.
	sys := a.autostartSys()
	if cfg.Autostart {
		ao := a.autostartOptions(&cfg, false)
		if _, err := sys.Register(ao); err != nil {
			a.Log.Printf("migrate: autostart: %v", err)
		} else if _, err := a.pathAdd(paths.Bin(a.Home)); err != nil {
			a.Log.Printf("migrate: PATH: %v", err)
		}
		if runningFrom(paths.Bin(legacy)) {
			if err := sys.Start(ao); err != nil {
				a.Log.Printf("migrate: start: %v", err)
			} else {
				res.Handover = true
			}
		}
	}
	if err := a.pathRemove(paths.Bin(legacy)); err != nil {
		a.Log.Printf("migrate: old PATH entry: %v", err)
	}
	a.Log.Printf("migrate: removing the old autostart entries")
	if err := sys.Unregister(autostart.Legacy(runtime.GOOS)); err != nil {
		a.Log.Printf("migrate: old autostart: %v", err)
	}
	return res, nil
}

// copyState copies the old home's state files and folders (the outbox) into
// dst, leaving out skipOnMigrate.
func copyState(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if rel == "." {
			return nil
		}
		top := strings.Split(filepath.ToSlash(rel), "/")[0]
		if skipOnMigrate[top] || rel == "secrets.json" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o700)
		}
		return copyFile(p, out)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".migrating"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// copyBinariesFrom installs this binary, and its twin from dir (the old
// names there), under tokenmaxr's names in a.Home's bin.
func (a *App) copyBinariesFrom(dir string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if r, err := filepath.EvalSymlinks(self); err == nil {
		self = r
	}
	if err := os.MkdirAll(paths.Bin(a.Home), 0o755); err != nil {
		return err
	}
	base := filepath.Base(self)
	gui := runtime.GOOS == "windows" && (strings.EqualFold(base, paths.ExeName(true)) || strings.EqualFold(base, paths.LegacyExeName(true)))
	pairs := [][2]string{{self, a.binPath(gui)}}
	if runtime.GOOS == "windows" {
		twin := filepath.Join(filepath.Dir(self), paths.ExeName(!gui))
		if !fileExists(twin) {
			twin = filepath.Join(dir, paths.LegacyExeName(!gui))
		}
		pairs = append(pairs, [2]string{twin, a.binPath(!gui)})
	}
	for _, p := range pairs {
		if !fileExists(p[0]) || sameFile(p[0], p[1]) {
			continue
		}
		if err := copyExe(p[0], p[1]); err != nil {
			return fmt.Errorf("copy %s: %w", filepath.Base(p[1]), err)
		}
	}
	return nil
}

// runningFrom reports whether this process's binary is in dir.
func runningFrom(dir string) bool {
	self, err := os.Executable()
	if err != nil {
		return false
	}
	if r, err := filepath.EvalSymlinks(self); err == nil {
		self = r
	}
	return sameDir(filepath.Dir(self), dir)
}

func sameDir(a, b string) bool {
	ca, err1 := filepath.Abs(a)
	cb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(ca), filepath.Clean(cb))
	}
	return filepath.Clean(ca) == filepath.Clean(cb)
}
