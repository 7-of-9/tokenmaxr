package app

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/autostart"
	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
	"github.com/7-of-9/tokenmaxr/collector/internal/instance"
	"github.com/7-of-9/tokenmaxr/collector/internal/joincode"
	"github.com/7-of-9/tokenmaxr/collector/internal/lock"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
	"github.com/7-of-9/tokenmaxr/collector/internal/upload"
	"github.com/7-of-9/tokenmaxr/collector/internal/userpath"
)

type InstallOptions struct {
	Join        string
	Endpoint    string
	Label       string
	Yes         bool
	NoFixConfig bool
	NoAutostart bool
	NoPrompts   bool
	// NoApp is headless mode: no desktop app, the scheduler runs a tick
	// every minute (servers without a desktop).
	NoApp bool
	In    io.Reader
}

// Install enrolls (if needed), copies the binaries into <home>/bin, runs the
// config checks and registers autostart. Re-running it repairs or upgrades.
// enrolWith enrols this machine with server using invite and returns the
// saved secrets. The fleet key is the join code's (k), else this machine's
// (re-enrolling after a 401, or a GitHub or local machine adding a server),
// else a new one. A machine that collected before without a server re-reads
// its history, so the server gets all of it. The caller holds the lock.
func (a *App) enrolWith(ctx context.Context, server, label, invite string, k []byte, sec store.Secrets, st *store.State) (store.Secrets, error) {
	if k != nil && sec.GitHub != nil && sec.HasFleet() && !bytes.Equal(k, sec.Key()) {
		return sec, errors.New("this join code is for another fleet than the GitHub one this machine publishes to: sign out of GitHub first, or invite from a machine of the same fleet")
	}
	if k == nil {
		k = sec.Key()
	}
	if k == nil {
		// First machine of the fleet: generate K. The server pins its
		// fingerprint and refuses a second K with 409.
		k = make([]byte, 32)
		if _, err := rand.Read(k); err != nil {
			return sec, err
		}
	}
	resp, err := upload.NewClient(server, "", a.Version).Enroll(ctx, model.EnrollRequest{
		Invite:       invite,
		MachineLabel: label,
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		Version:      a.Version,
		KFingerprint: model.KFingerprint(k),
	})
	if err != nil {
		if upload.Code(err) == 409 {
			return sec, fmt.Errorf("enroll refused (HTTP 409): join code must come from `" + buildinfo.Product + " invite` on an enrolled machine")
		}
		return sec, fmt.Errorf("enroll: %w", err)
	}
	rehash := sec.HasFleet() && !bytes.Equal(k, sec.Key())
	firstServer := !sec.Enrolled()
	// A GitHub sign-in survives (re-)enrolling.
	sec = store.Secrets{Token: resp.Token, K: base64.StdEncoding.EncodeToString(k), MachineID: resp.MachineID, GitHub: sec.GitHub}
	if err := store.SaveSecrets(a.Home, sec); err != nil {
		return sec, err
	}
	if st != nil {
		if (firstServer && len(st.Cursors) > 0) || rehash {
			st.Cursors = map[string]map[string]store.FileCursor{}
		}
		if rehash {
			os.Remove(paths.Rollup(a.Home))
		}
		st.Unauthorized = false
		st.LastUploadErr = ""
		st.Backoff = store.Backoff{}
	}
	a.Log.Printf("enrolled with %s as %s", server, resp.MachineID)
	return sec, nil
}

// Installed reports whether this machine is set up: it holds a fleet key
// (server enrolment, GitHub sign-in or a local key). `app` on a machine that
// is not runs the install first.
func (a *App) Installed() bool {
	sec, err := store.LoadSecrets(a.Home)
	return err == nil && sec.HasFleet()
}

func (a *App) Install(ctx context.Context, o InstallOptions) error {
	if err := os.MkdirAll(a.Home, 0o700); err != nil {
		return err
	}
	lk, err := lock.Acquire(paths.Lock(a.Home), 2*time.Minute)
	if err != nil {
		return fmt.Errorf("waiting for a running tick: %w", err)
	}
	defer lk.Release()

	cfg, err := store.LoadConfig(a.Home)
	if err != nil {
		return err
	}
	sec, err := store.LoadSecrets(a.Home)
	if err != nil {
		return err
	}
	st, err := store.LoadState(a.Home)
	if err != nil {
		a.Log.Printf("state: %v", err)
	}
	if o.Endpoint != "" {
		if cfg.Endpoint, err = store.ParseServer(o.Endpoint); err != nil {
			return err
		}
		cfg.ServerOff = false
	}
	// A label saved by an earlier install (or the label command) is kept.
	hadLabel := fileExists(paths.Config(a.Home)) && cfg.MachineLabel != ""
	if o.Label != "" {
		if cfg.MachineLabel = CleanLabel(o.Label); cfg.MachineLabel == "" {
			return errors.New("--label needs at least one visible character")
		}
	}
	// Opt-outs are sticky: re-running the one-line installer without the
	// flag never silently re-enables prompt capture or config edits.
	cfg.Prompts = cfg.Prompts && !o.NoPrompts
	cfg.FixConfig = cfg.FixConfig && !o.NoFixConfig
	cfg.Autostart = !o.NoAutostart
	cfg.App = !o.NoApp

	var invite string
	var k []byte
	if o.Join != "" {
		if invite, k, err = joincode.Parse(o.Join); err != nil {
			return err
		}
	}
	if invite != "" && cfg.Server() == "" {
		return errors.New("--join enrols with a server: pass --endpoint URL too")
	}
	// Without --join, a machine that needs enrolling gets its code from the
	// owner's browser (linkJoin) once the settings are confirmed. Without a
	// server there is nothing to enrol with: the machine keeps its own fleet
	// key until a GitHub sign-in joins it to a fleet.
	needEnroll := cfg.Server() != "" && (!sec.Enrolled() || st.Unauthorized || (k != nil && string(k) != string(sec.Key())))

	// The machine label is public: ask for it on a first install when
	// someone is at a terminal, else use the hostname and say so.
	if o.Label == "" && !hadLabel {
		def := defaultLabel()
		if t, ok := openTerminal(); ok {
			cfg.MachineLabel = askLabel(t, def)
			t.Close()
		} else {
			cfg.MachineLabel = def
			a.printf("machine label %q is %s; change it with `"+buildinfo.Product+" label NEW_NAME`\n", def, publicNote)
		}
	}

	server := cfg.Server()
	if server == "" {
		server = "none (GitHub or local only)"
	}
	a.printf(buildinfo.Product+" %s\n  state dir: %s\n  server:    %s\n  machine:   %s (public)\n", a.Version, a.Home, server, cfg.MachineLabel)
	mode := "app (" + appPlace(&cfg) + ")"
	if !cfg.App {
		mode = "headless (a tick every minute)"
	}
	a.printf("  prompts:   %s\n  config fixes: %s\n  autostart: %s\n  mode:      %s\n", onOff(cfg.Prompts), onOff(cfg.FixConfig), onOff(cfg.Autostart), mode)
	if cfg.FixConfig {
		a.printf("  (Claude Code cleanupPeriodDays -> 3650 and Grok cleanup_ttl_days -> 0, with backups)\n")
	}
	if !o.Yes && !confirm(o.In, a.Out) {
		return errors.New("cancelled")
	}

	if needEnroll {
		if invite == "" {
			code, err := a.linkJoin(ctx, cfg.Server())
			if err != nil {
				return err
			}
			if invite, k, err = joincode.Parse(code); err != nil {
				return err
			}
		}
		if sec, err = a.enrolWith(ctx, cfg.Server(), cfg.MachineLabel, invite, k, sec, st); err != nil {
			return err
		}
		a.printf("enrolled as %s\n", sec.MachineID)
	} else if sec.Enrolled() {
		a.printf("already enrolled as %s\n", sec.MachineID)
	} else if !sec.HasFleet() {
		// No server: a local fleet key, so account ids hash from the start.
		// A GitHub sign-in later adopts the fleet's key (and re-reads).
		k := make([]byte, 32)
		if _, err := rand.Read(k); err != nil {
			return err
		}
		sec.K = base64.StdEncoding.EncodeToString(k)
		if err := store.SaveSecrets(a.Home, sec); err != nil {
			return err
		}
		a.printf("set up without a server\n")
		a.Log.Printf("install: local fleet key (no server)")
	}

	if err := a.copyBinaries(); err != nil {
		return err
	}
	hr := a.homes(&cfg)
	a.probeAccounts(sec.Key(), &cfg, st, a.Now(), hr.Homes)
	res := a.runChecks(&cfg, st, cfg.FixConfig, hr.Homes)
	st.LastConfigCheck = a.Now()
	a.checked = true
	for _, n := range res.Notes {
		a.printf("  %s\n", n)
	}
	if err := store.SaveConfig(a.Home, cfg); err != nil {
		return err
	}

	if cfg.Autostart {
		sys, ao := a.autostartSys(), a.autostartOptions(&cfg, true)
		wasRunning := instance.Running(a.Home)
		if !cfg.App && wasRunning && !instance.Stop(a.Home, appStopWait) {
			a.printf("app: still running; quit it from its menu\n")
		}
		registered, err := sys.Register(ao)
		if err != nil {
			return fmt.Errorf("autostart: %w", err)
		}
		st.Checks["autostart"] = "ok"
		a.printf("autostart: %s\n", registered)
		switch {
		case cfg.App && !cfg.TrayOnly:
			// The app's window is in the taskbar; the Start menu finds it.
			if p, err := a.addStartMenu(); err != nil {
				a.printf("start menu: could not add the entry (%v)\n", err)
			} else if p != "" {
				a.printf("start menu: %s\n", p)
			}
		case !cfg.App:
			if _, err := a.removeStartMenu(); err != nil {
				a.printf("start menu: could not remove the entry (%v)\n", err)
			}
		}
		if hint, err := a.pathAdd(paths.Bin(a.Home)); err != nil {
			a.printf("PATH: could not update (%v)\n", err)
		} else if hint != "" {
			a.printf("%s\n", hint)
		}
		if err := store.SaveState(a.Home, st); err != nil {
			return err
		}
		lk.Release()
		if cfg.App {
			instance.ClearStopped(a.Home)
		}
		if cfg.App && wasRunning && runtime.GOOS == "windows" {
			// The running app re-execs onto the binary just copied (on
			// macOS re-loading the agent already replaced it).
			if err := instance.Send(a.Home, instance.Restart); err != nil {
				a.Log.Printf("install: app restart: %v", err)
			}
		} else if err := sys.Start(ao); err != nil {
			a.printf("could not start it now (%v); it starts at the next login\n", err)
			a.Log.Printf("install: start: %v", err)
		}
		if cfg.App {
			a.printf("done. The app is in the %s; the first backfill runs in the background.\n", appPlace(&cfg))
		} else {
			a.printf("done. The first backfill runs in the background; `" + buildinfo.Product + " status` shows progress.\n")
		}
		a.publishHint(&cfg, sec)
		return nil
	}
	if err := store.SaveState(a.Home, st); err != nil {
		return err
	}
	a.printf("done (no autostart). Run `" + buildinfo.Product + " sync-now` to collect and upload now.\n")
	a.publishHint(&cfg, sec)
	return nil
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// confirm asks on an interactive terminal; non-interactive installs proceed.
func confirm(in io.Reader, out io.Writer) bool {
	f, ok := in.(*os.File)
	if !ok || f == nil {
		return true
	}
	if st, err := f.Stat(); err != nil || st.Mode()&os.ModeCharDevice == 0 {
		return true
	}
	fmt.Fprint(out, "continue? [Y/n] ")
	line, _ := bufio.NewReader(in).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "" || line == "y" || line == "yes"
}

func (a *App) pathAdd(dir string) (string, error) {
	if a.UserPath != nil {
		return a.UserPath.Add(dir)
	}
	return userpath.Add(dir)
}

func (a *App) pathRemove(dir string) error {
	if a.UserPath != nil {
		return a.UserPath.Remove(dir)
	}
	return userpath.Remove(dir)
}

func (a *App) autostartSys() *autostart.System {
	if a.Autostart != nil {
		return a.Autostart
	}
	return autostart.Default()
}

// autostartOptions describes this install's entries for cfg's mode. They
// run the windowsgui build on Windows (the app, or a tick with no console
// window) if it is present; warn says so when it is not.
func (a *App) autostartOptions(cfg *store.Config, warn bool) autostart.Options {
	o := autostart.Options{Name: a.TaskName, RunValue: a.RunValue, Exe: a.binPath(false), App: cfg.App}
	if runtime.GOOS == "windows" {
		if w := a.binPath(true); fileExists(w) {
			o.Exe = w
		} else if warn {
			a.printf("warning: %s is missing; autostart will use the console build\n", paths.ExeName(true))
		}
	}
	if !paths.IsDefaultHome(a.Home) {
		o.Home = a.Home
	}
	return o
}

// autostartMode is "app", "headless" or "missing".
func (a *App) autostartMode(cfg *store.Config) string {
	return a.autostartSys().Registered(a.autostartOptions(cfg, false)).Mode(runtime.GOOS)
}

// appPlace is where the app shows: its window's taskbar button or Dock
// icon and the tray icon, or (tray only) the icon alone.
func appPlace(cfg *store.Config) string {
	if cfg.TrayOnly {
		return trayPlace() + " (icon only)"
	}
	if runtime.GOOS == "darwin" {
		return "Dock and the menu bar"
	}
	return "taskbar and the notification area"
}

// trayPlace is where the app's icon lives.
func trayPlace() string {
	if runtime.GOOS == "darwin" {
		return "menu bar"
	}
	return "notification area"
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// copyBinaries puts the running binary (and, on Windows, its sibling
// variant from the same directory) into <home>/bin.
func (a *App) copyBinaries() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if r, err := filepath.EvalSymlinks(self); err == nil {
		self = r
	}
	// The twin may carry the old names (a binary from the legacy channel).
	return a.copyBinariesFrom(filepath.Dir(self))
}

func sameFile(a, b string) bool {
	sa, err1 := os.Stat(a)
	sb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(sa, sb)
}

// copyExe copies via a temp file and a rename; a running destination on
// Windows is renamed to .old first.
func copyExe(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
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
	if runtime.GOOS == "windows" && fileExists(dst) {
		old := dst + ".old"
		os.Remove(old)
		if err := os.Rename(dst, old); err != nil {
			old = fmt.Sprintf("%s.%d.old", dst, time.Now().UnixNano())
			if err := os.Rename(dst, old); err != nil {
				os.Remove(tmp)
				return err
			}
		}
	}
	return os.Rename(tmp, dst)
}

// Invite asks the server for a single-use invite and prints the join code
// with this fleet's K.
func (a *App) Invite(ctx context.Context) error {
	cfg, err := store.LoadConfig(a.Home)
	if err != nil {
		return err
	}
	sec, err := store.LoadSecrets(a.Home)
	if err != nil {
		return err
	}
	if !serverOn(&cfg, sec) {
		return errors.New("invite needs a server: machines join a GitHub fleet by signing in to GitHub (" + buildinfo.Product + " github login)")
	}
	resp, err := upload.NewClient(cfg.Server(), sec.Token, a.Version).Invite(ctx)
	if err != nil {
		return fmt.Errorf("invite: %w", err)
	}
	a.printf("%s\n", joincode.Format(resp.Invite, sec.Key()))
	if !resp.ExpiresAt.IsZero() {
		fmt.Fprintf(os.Stderr, "single use; expires %s. On the new machine run the installer with --join <code>.\n", resp.ExpiresAt.Local().Format(time.RFC1123))
	}
	return nil
}

type UninstallOptions struct {
	Purge bool
}

// Uninstall removes autostart (both modes' entries), quits the app and
// removes the PATH entry; --purge also deletes the state directory (outbox,
// state, secrets, binaries).
func (a *App) Uninstall(o UninstallOptions) error {
	cfg, _ := store.LoadConfig(a.Home)
	sys, ao := a.autostartSys(), a.autostartOptions(&cfg, false)
	var errs []error
	// Autostart first, so the watchdog cannot start the app again; then the
	// app, which on Windows runs from <home>/bin (deleted by --purge).
	if r := sys.Registered(ao); !r.LoginItem && !r.Task {
		a.printf("autostart: none registered\n")
	} else if err := sys.Unregister(ao); err != nil {
		errs = append(errs, err)
	} else {
		a.printf("autostart removed\n")
	}
	if removed, err := a.removeStartMenu(); err != nil {
		errs = append(errs, err)
	} else if removed {
		a.printf("start menu entry removed\n")
	}
	if instance.Running(a.Home) {
		if instance.Stop(a.Home, appStopWait) {
			a.printf("app stopped\n")
		} else {
			errs = append(errs, errors.New("the app is still running; quit it from its menu"))
		}
	}
	if err := a.pathRemove(paths.Bin(a.Home)); err != nil {
		errs = append(errs, err)
	}
	if o.Purge {
		// Wait for an in-flight tick, then let go of everything in the dir.
		if lk, err := lock.Acquire(paths.Lock(a.Home), time.Minute); err == nil {
			lk.Release()
		}
		a.Log.Close()
		if err := os.RemoveAll(a.Home); err != nil {
			if runtime.GOOS == "windows" {
				// Most likely the running exe lives in <home>/bin: delete the
				// directory from a detached shell once this process exits.
				cmd := exec.Command("cmd.exe", "/c", "ping -n 3 127.0.0.1 >nul & rmdir /s /q \""+a.Home+"\"")
				hideWindow(cmd)
				if cmd.Start() == nil {
					a.printf("state directory will be removed in a few seconds: %s\n", a.Home)
					return errors.Join(errs...)
				}
			}
			errs = append(errs, err)
		} else {
			a.printf("removed %s\n", a.Home)
		}
	} else {
		a.printf("state kept in %s (use --purge to delete it)\n", a.Home)
	}
	return errors.Join(errs...)
}

// publishHint says how to publish when this machine publishes nowhere yet.
func (a *App) publishHint(cfg *store.Config, sec store.Secrets) {
	if serverOn(cfg, sec) || githubEnabled(cfg, sec) {
		return
	}
	if cfg.App && cfg.Autostart {
		a.printf("Publishing nowhere yet: the settings page opens to publish to your GitHub (later: Settings… in the icon's menu).\n")
	} else {
		a.printf("Publishing nowhere yet: publish to your GitHub with `" + buildinfo.Product + " github login`.\n")
	}
}
