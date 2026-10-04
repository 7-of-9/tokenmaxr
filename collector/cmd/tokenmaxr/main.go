// Command tokenmaxr reads local Claude Code, Codex, Grok CLI, Cursor and
// Gemini CLI logs, counts AI token usage and publishes it: daily totals to the
// user's own GitHub repository and Pages dashboard, and/or every event (and,
// privately, prompts) to their own server. See README.md and docs/.
//
// It is one program: with no command (or `app`) on a desktop it is the app,
// a tray icon (Windows) or menu-bar item (macOS) and the collection loop in
// one process; every other command is the CLI. On Windows the same code is
// also linked with -H windowsgui as tokenmaxrw.exe, which autostart
// runs (no console window).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"slices"
	"strings"
	"syscall"

	"github.com/7-of-9/tokenmaxr/collector/internal/app"
	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
)

// version is set with -ldflags "-X main.version=0.1.0"; "dev" never self-updates.
var version = "dev"

// buildTime is the release's UTC build time (RFC 3339), set with
// -ldflags "-X main.buildTime=2026-10-04T08:15:00Z". It is the commit time of
// the collector/VERSION bump, so every platform's binary carries the same
// value and rebuilds stay byte-identical.
var buildTime = ""

const usage = `usage: tokenmaxr [--home DIR] [<command> [flags]]

commands:
  app                 the desktop app: tray icon / menu-bar item and the
                      collection loop in one process (the default with no
                      command on a desktop); a second start shows the first.
                      On a machine that is not set up it installs first.
  install [--endpoint URL|off] [--join CODE] [--label NAME] [--yes]
          [--no-fix-config] [--no-autostart] [--no-prompts] [--no-app]
                      copy the binaries, start the app and keep it running at
                      login (--no-app: headless, a tick every minute). With a
                      server (--endpoint) it enrols there: the browser opens
                      for the owner to approve, or --join takes a code from
                      "invite". Without one, publish with "github login".
  label NEW_NAME      rename this machine on your server's dashboard
  run                 one tick (headless mode's scheduler runs it every minute)
  sync-now [--since DATE]
                      tick in the foreground until everything is uploaded
  scan --dry-run [--json] [--since DATE|TS] [--until DATE|TS]
                      parse all logs from scratch and print totals; no state, no upload
  status              local health summary
  doctor              status plus live checks
  invite              print a single-use join code for another machine (server)
  github login [--label NAME]
                      publish daily totals and quota meters to your own GitHub
                      repository and Pages dashboard (signs in with GitHub and
                      guides the setup; --label renames this machine there)
  github status | logout
                      show, or stop, publishing to GitHub
  settings            open the settings page (GitHub, server, label)
  uninstall [--purge] quit the app, remove autostart and the PATH entry
                      (--purge: all local data)
  version             print the version

--home DIR (or $TOKENMAXR_HOME) overrides the state directory.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	global := flag.NewFlagSet(buildinfo.Product, flag.ContinueOnError)
	global.SetOutput(stderr)
	global.Usage = func() { fmt.Fprint(stderr, usage) }
	home := global.String("home", "", "state directory")
	if err := global.Parse(args); err != nil {
		return 2
	}
	rest := global.Args()
	if len(rest) == 0 {
		if !desktopSession() {
			fmt.Fprint(stderr, usage)
			return 2
		}
		rest = []string{"app"}
	}
	cmd, rest := rest[0], rest[1:]
	// github takes its subcommand before any flags: github login --label X.
	sub := ""
	if cmd == "github" && len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		sub, rest = rest[0], rest[1:]
	}

	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(home, "home", *home, "state directory")
	var (
		join        = fs.String("join", "", "join code D0M1-...")
		endpoint    = fs.String("endpoint", "", "API base URL (default "+buildinfo.DefaultEndpoint+")")
		label       = fs.String("label", "", "public machine label (default: hostname)")
		yes         = fs.Bool("yes", false, "do not ask for confirmation")
		noFix       = fs.Bool("no-fix-config", false, "never edit tool config files")
		noAutostart = fs.Bool("no-autostart", false, "skip scheduler registration and PATH changes")
		noPrompts   = fs.Bool("no-prompts", false, "do not capture prompt text")
		noApp       = fs.Bool("no-app", false, "headless: no desktop app, a scheduled tick every minute")
		watchdog    = fs.Bool("watchdog", false, "started by the Windows watchdog task (app)")
		since       = fs.String("since", "", "only events at or after DATE (YYYY-MM-DD, local) or RFC 3339 TS")
		until       = fs.String("until", "", "only events at or before DATE or TS")
		dryRun      = fs.Bool("dry-run", false, "required for scan")
		asJSON      = fs.Bool("json", false, "JSON output")
		purge       = fs.Bool("purge", false, "also delete the state directory")
	)
	allowed := map[string][]string{
		"app":       {"watchdog"},
		"install":   {"join", "endpoint", "label", "yes", "no-fix-config", "no-autostart", "no-prompts", "no-app"},
		"run":       {},
		"sync-now":  {"since"},
		"scan":      {"dry-run", "json", "since", "until"},
		"status":    {},
		"doctor":    {},
		"invite":    {},
		"github":    {"label"},
		"label":     {},
		"settings":  {},
		"uninstall": {"purge"},
		"version":   {},
	}
	names, ok := allowed[cmd]
	if !ok {
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", cmd, usage)
		return 2
	}
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	// label takes the new name as its arguments (joined, so quotes are optional).
	newLabel := strings.Join(fs.Args(), " ")
	switch {
	case cmd == "github" && sub != "login" && sub != "logout" && sub != "status":
		fmt.Fprintln(stderr, "usage: "+buildinfo.Product+" github login [--label NAME] | status | logout")
		return 2
	case cmd == "github" && sub != "login" && *label != "":
		fmt.Fprintln(stderr, "--label only goes with github login")
		return 2
	case cmd == "label" && strings.TrimSpace(newLabel) == "":
		fmt.Fprintln(stderr, "usage: "+buildinfo.Product+" label NEW_NAME")
		return 2
	case cmd != "label" && fs.NArg() > 0:
		fmt.Fprintf(stderr, "%s: unexpected argument %q\n", cmd, fs.Arg(0))
		return 2
	}
	bad := ""
	fs.Visit(func(f *flag.Flag) {
		if f.Name != "home" && !slices.Contains(names, f.Name) {
			bad = f.Name
		}
	})
	if bad != "" {
		fmt.Fprintf(stderr, "%s does not take --%s\n", cmd, bad)
		return 2
	}

	if cmd == "version" {
		built := ""
		if buildTime != "" {
			built = ", built " + buildTime
		}
		fmt.Fprintf(stdout, buildinfo.Product+" %s (%s, %s/%s%s)\n", version, runtime.Version(), runtime.GOOS, runtime.GOARCH, built)
		return 0
	}

	a, err := app.New(*home, version, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	a.BuildTime = buildTime
	// A d0m1-collector install moves to tokenmaxr once (app.Migrate).
	if *home == "" && os.Getenv(paths.HomeEnv) == "" && cmd != "uninstall" {
		if legacy, _ := paths.LegacyHome(); legacy != "" {
			if code, done := migrate(a, legacy, cmd, stderr); done {
				return code
			}
		}
	}
	// SIGTERM: launchctl bootout (and a logout) stops the app cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "app":
		// Downloaded and run on a new machine: install first (the browser
		// links it to the owner's fleet, no join code to type). Install
		// copies the binaries and starts the installed app, so this process
		// is done after it.
		if !*watchdog && !a.Installed() {
			if err = a.OpenLog(nil); err == nil {
				err = a.Install(ctx, app.InstallOptions{Yes: true, In: os.Stdin})
				a.Log.Close()
			}
			break
		}
		ui, note := desktopUI()
		if note != "" {
			fmt.Fprintln(stderr, note)
		}
		err = a.Desktop(ctx, app.DesktopOptions{Watchdog: *watchdog, UI: ui})
	case "install":
		if err = a.OpenLog(nil); err == nil {
			err = a.Install(ctx, app.InstallOptions{
				Join: *join, Endpoint: *endpoint, Label: strings.TrimSpace(*label), Yes: *yes,
				NoFixConfig: *noFix, NoAutostart: *noAutostart, NoPrompts: *noPrompts, NoApp: *noApp, In: os.Stdin,
			})
			a.Log.Close()
		}
	case "run":
		err = a.Run(ctx)
	case "sync-now":
		err = a.SyncNow(ctx, *since)
	case "scan":
		if !*dryRun {
			fmt.Fprintln(stderr, "scan only supports --dry-run (the app and `run` do real scans)")
			return 2
		}
		err = a.ScanDryRun(*asJSON, *since, *until)
	case "status":
		err = a.Status()
	case "doctor":
		err = a.Doctor(ctx)
	case "invite":
		err = a.Invite(ctx)
	case "github":
		switch sub {
		case "login":
			if err = a.OpenLog(nil); err == nil {
				err = a.GitHubLoginCLI(ctx, strings.TrimSpace(*label))
				a.Log.Close()
			}
		case "logout":
			err = a.GitHubLogoutCLI()
		case "status":
			err = a.GitHubStatusCLI()
		}
	case "label":
		if err = a.OpenLog(nil); err == nil {
			err = a.SetLabel(ctx, newLabel)
			a.Log.Close()
		}
	case "settings":
		err = a.OpenSettingsCLI()
	case "uninstall":
		err = a.Uninstall(app.UninstallOptions{Purge: *purge})
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(stderr, "interrupted")
			return 130
		}
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

// migrate runs app.Migrate; done means this process should exit with code
// (the migrated app took over, or the move failed and nothing should run on
// a half-made home).
func migrate(a *app.App, legacy, cmd string, stderr io.Writer) (int, bool) {
	if _, err := os.Stat(legacy); err != nil {
		return 0, false
	}
	// The log opens only after the move: it lives in the new home, and an
	// open file there cannot be replaced by the old home's log on Windows.
	res, err := a.Migrate(legacy)
	if res.Moved || err != nil {
		if a.OpenLog(nil) == nil {
			if err != nil {
				a.Log.Printf("migrate: %v", err)
			} else {
				a.Log.Printf("migrate: moved %s to %s", legacy, a.Home)
			}
			a.Log.Close()
		}
	}
	switch {
	case err != nil:
		fmt.Fprintf(stderr, "error: moving the d0m1-collector install to %s: %v\n", buildinfo.Product, err)
		return 1, true
	case res.Moved:
		fmt.Fprintf(stderr, "moved the d0m1-collector install to %s\n", a.Home)
	}
	if res.Handover && (cmd == "app" || cmd == "run") {
		return 0, true
	}
	return 0, false
}
