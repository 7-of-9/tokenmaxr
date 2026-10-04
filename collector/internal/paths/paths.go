// Package paths resolves the collector's state directory and the user's tool
// directories (see docs/agents/SPEC.md "Collector behaviour").
package paths

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
	"strings"
)

// HomeEnv overrides the state directory: TOKENMAXR_HOME (D0M1_COLLECTOR_HOME
// for the d0m1-collector product). The --home flag wins over it.
var HomeEnv = strings.ToUpper(strings.ReplaceAll(buildinfo.Product, "-", "_")) + "_HOME"

// UserHomeEnv overrides the user's home directory that sources and account
// probes read (tests and fixtures only).
const UserHomeEnv = "D0M1_USER_HOME"

// Home returns the state directory: flag, then $HomeEnv, then the per-OS
// default.
func Home(flag string) (string, error) {
	if flag != "" {
		return filepath.Abs(flag)
	}
	if v := os.Getenv(HomeEnv); v != "" {
		return filepath.Abs(v)
	}
	return DefaultHome()
}

// DefaultHome is %LOCALAPPDATA%\<product> on Windows and
// ~/Library/Application Support/<product> on macOS.
func DefaultHome() (string, error) { return defaultHome(buildinfo.Product) }

// LegacyHome is where a d0m1-collector install kept its state (DefaultHome
// under the old name); the migration moves it to DefaultHome.
func LegacyHome() (string, error) { return defaultHome(buildinfo.LegacyProduct) }

func defaultHome(name string) (string, error) {
	switch runtime.GOOS {
	case "windows":
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			return filepath.Join(v, name), nil
		}
		h, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(h, "AppData", "Local", name), nil
	case "darwin":
		h, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(h, "Library", "Application Support", name), nil
	default:
		if v := os.Getenv("XDG_STATE_HOME"); v != "" {
			return filepath.Join(v, name), nil
		}
		h, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(h, ".local", "state", name), nil
	}
}

// IsDefaultHome reports whether home is the per-OS default (so the scheduled
// task can omit --home).
func IsDefaultHome(home string) bool {
	d, err := DefaultHome()
	if err != nil {
		return false
	}
	return filepath.Clean(d) == filepath.Clean(home)
}

// UserHome is the directory holding .claude, .codex and .grok.
func UserHome() (string, error) {
	if v := os.Getenv(UserHomeEnv); v != "" {
		return filepath.Abs(v)
	}
	return os.UserHomeDir()
}

// CodexHome honours $CODEX_HOME like Codex itself.
func CodexHome(userHome string) string {
	if v := os.Getenv("CODEX_HOME"); v != "" && os.Getenv(UserHomeEnv) == "" {
		return v
	}
	return filepath.Join(userHome, ".codex")
}

// Files inside the state directory.
func Config(home string) string  { return filepath.Join(home, "config.json") }
func Secrets(home string) string { return filepath.Join(home, "secrets.json") }
func State(home string) string   { return filepath.Join(home, "state.json") }
func Outbox(home string) string  { return filepath.Join(home, "outbox") }
func Log(home string) string     { return filepath.Join(home, "collector.log") }
func Lock(home string) string    { return filepath.Join(home, "collector.lock") }
func Bin(home string) string     { return filepath.Join(home, "bin") }

// Quota holds fresh quota meters fetched through the installed clients
// (internal/accountusage/quota.go); QuotaWork is the neutral working
// directory those clients run in.
func Quota(home string) string      { return filepath.Join(home, "quota") }
func CodexQuota(home string) string { return filepath.Join(Quota(home), "codex-rate-limits.jsonl") }

// Rollup is the full-history daily totals the GitHub publisher reads
// (internal/rollup).
func Rollup(home string) string { return filepath.Join(home, "rollup.json") }

func QuotaWork(home string) string { return filepath.Join(Quota(home), "work") }

// Desktop app (SPEC "Desktop app (v1.4)"): the running app holds app.lock;
// other commands leave it a flag file (app.show, app.quit, app.restart);
// app.stopped marks a Quit from its menu, which the Windows watchdog honours.
func AppLock(home string) string         { return filepath.Join(home, "app.lock") }
func AppSignal(home, verb string) string { return filepath.Join(home, "app."+verb) }
func AppStopped(home string) string      { return filepath.Join(home, "app.stopped") }
func AppView(home string) string         { return filepath.Join(home, "app.view.txt") }

// AppExeName is the binary the app runs as: the windowsgui build on
// Windows (no console window), the one binary on macOS.
func AppExeName() string { return ExeName(runtime.GOOS == "windows") }

// ExeName is the platform file name of a binary variant; gui selects the
// Windows -H windowsgui build that autostart runs (the app).
func ExeName(gui bool) string { return exeName(buildinfo.Product, gui) }

// LegacyExeName is a d0m1-collector install's binary name.
func LegacyExeName(gui bool) string { return exeName(buildinfo.LegacyProduct, gui) }

func exeName(name string, gui bool) string {
	if runtime.GOOS != "windows" {
		return name
	}
	if gui {
		return name + "w.exe"
	}
	return name + ".exe"
}
