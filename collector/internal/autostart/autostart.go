// Package autostart keeps the collector running (docs/agents/SPEC.md
// "Desktop app (v1.4)" and "Autostart").
//
// App mode (the default): on Windows an HKCU Run value starts the app at
// login and a per-user scheduled task relaunches it every 5 minutes as a
// watchdog (a launch while it runs exits at once); on macOS the LaunchAgent
// com.d0m1.collector runs the app with RunAtLoad and KeepAlive. Headless
// mode (install --no-app): the task or LaunchAgent runs one tick every
// minute.
//
// Every OS call goes through System's fields, so tests exercise both
// platforms' paths with fakes on any OS.
package autostart

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
)

// Names of the entries.
const (
	// TaskName is the Windows scheduled task (see FallbackName).
	TaskName = `\tokenmaxr\collector`
	// FallbackName is the root-folder task used where policy refuses the
	// \tokenmaxr folder.
	FallbackName = buildinfo.Product
	// RunValueName is the Windows HKCU Run value that starts the app.
	RunValueName = buildinfo.Product
	// AgentLabel is the macOS LaunchAgent.
	AgentLabel = "com.tokenmaxr.collector"
)

// The entries a d0m1-collector install registered; the migration removes them.
const (
	LegacyTaskName     = `\d0m1\collector`
	LegacyFallbackName = buildinfo.LegacyProduct
	LegacyRunValueName = buildinfo.LegacyProduct
	LegacyAgentLabel   = "com.d0m1.collector"
)

// Legacy names a d0m1-collector install's entries for Unregister.
func Legacy(goos string) Options {
	o := Options{Name: LegacyTaskName, RunValue: LegacyRunValueName}
	if goos == "darwin" {
		o.Name = LegacyAgentLabel
	}
	return o
}

// Options describes what to register.
type Options struct {
	// Name is the task (Windows) or LaunchAgent label (macOS); "" is the
	// default. Tests use clearly TEST names.
	Name string
	// RunValue is the Windows Run value name; "" is RunValueName.
	RunValue string
	// Exe is the binary: <product>w.exe on Windows.
	Exe string
	// Home, when non-empty, is passed as --home.
	Home string
	// App selects app mode; false is the headless every-minute tick.
	App bool
}

func (o Options) home() []string {
	if o.Home == "" {
		return nil
	}
	return []string{"--home", o.Home}
}

// AppArgs is how the app is started: [--home H] app.
func (o Options) AppArgs() []string { return append(o.home(), "app") }

// LoginArgs is the app as login (and install) starts it:
// [--home H] app --minimized. Its main window starts minimized on the
// taskbar (macOS: hidden, with its Dock icon), so it never takes the focus.
func (o Options) LoginArgs() []string { return append(o.AppArgs(), "--minimized") }

// WatchdogArgs is the Windows watchdog's command: the app, marked so that a
// Quit from its menu is honoured until the next login.
func (o Options) WatchdogArgs() []string { return append(o.AppArgs(), "--watchdog") }

// RunArgs is one headless tick: [--home H] run.
func (o Options) RunArgs() []string { return append(o.home(), "run") }

// TaskArgs is what the Windows scheduled task runs: the watchdog in app
// mode, else one tick.
func (o Options) TaskArgs() []string {
	if o.App {
		return o.WatchdogArgs()
	}
	return o.RunArgs()
}

func (o Options) taskName() string {
	if o.Name != "" {
		return o.Name
	}
	return TaskName
}

func (o Options) label() string {
	if o.Name != "" {
		return o.Name
	}
	return AgentLabel
}

func (o Options) runValue() string {
	if o.RunValue != "" {
		return o.RunValue
	}
	return RunValueName
}

// RunKey is HKCU\Software\Microsoft\Windows\CurrentVersion\Run.
type RunKey interface {
	Get(name string) (value string, ok bool, err error)
	Set(name, value string) error
	Delete(name string) error
}

// Scheduler is the Windows Task Scheduler.
type Scheduler interface {
	Install(o Options) (name string, err error)
	Start(name string) error
	Uninstall(name string) error
	Present(name string) bool
	Describe(name string) (string, error)
}

// System is the OS's autostart mechanisms.
type System struct {
	// GOOS picks the mechanism: "windows" or "darwin"; anything else is
	// unsupported.
	GOOS string
	// Run and Tasks are the Windows Run key and Task Scheduler.
	Run   RunKey
	Tasks Scheduler
	// Spawn starts the app now on Windows (detached, no console).
	Spawn func(exe string, args []string) error
	// AgentsDir is ~/Library/LaunchAgents; Launchctl runs launchctl and
	// returns its output; Domain is gui/<uid>.
	AgentsDir string
	Launchctl func(args ...string) (string, error)
	Domain    string
}

var errUnsupported = errors.New("autostart is supported on Windows and macOS only; run `" + buildinfo.Product + " run` from cron")

// Register sets up autostart for o's mode, replacing the other mode's
// entries (idempotent). It returns what it registered, for install's output.
func (s *System) Register(o Options) (string, error) {
	switch s.GOOS {
	case "windows":
		if s.Run == nil || s.Tasks == nil {
			return "", errUnsupported
		}
		if !o.App {
			if err := s.Run.Delete(o.runValue()); err != nil {
				return "", err
			}
			name, err := s.Tasks.Install(o)
			if err != nil {
				return "", err
			}
			return "task " + name + " (a tick every minute)", nil
		}
		if err := s.Run.Set(o.runValue(), CommandLine(o.Exe, o.LoginArgs())); err != nil {
			return "", fmt.Errorf("login item: %w", err)
		}
		name, err := s.Tasks.Install(o)
		if err != nil {
			return "", err
		}
		return "login item " + o.runValue() + " + watchdog task " + name + " (every 5 minutes)", nil
	case "darwin":
		if s.AgentsDir == "" || s.Launchctl == nil {
			return "", errUnsupported
		}
		if err := os.MkdirAll(s.AgentsDir, 0o755); err != nil {
			return "", err
		}
		label := o.label()
		p := s.plistPath(label)
		if err := os.WriteFile(p, []byte(Plist(label, o)), 0o644); err != nil {
			return "", err
		}
		s.Launchctl("bootout", s.Domain+"/"+label) // not loaded yet is fine
		if _, err := s.Launchctl("bootstrap", s.Domain, p); err != nil {
			return "", err
		}
		if o.App {
			return "LaunchAgent " + label + " (the app, at login, kept alive)", nil
		}
		return "LaunchAgent " + label + " (a tick every minute)", nil
	}
	return "", errUnsupported
}

// Start runs what o registered once now: the app (Windows: a detached
// launch; macOS: kickstart, for an agent loaded while the app stayed quit)
// or, headless, the first tick (Windows: the task; macOS: RunAtLoad already
// ran it).
func (s *System) Start(o Options) error {
	switch s.GOOS {
	case "windows":
		if !o.App {
			if s.Tasks == nil {
				return errUnsupported
			}
			return s.Tasks.Start(o.taskName())
		}
		if s.Spawn == nil {
			return errUnsupported
		}
		return s.Spawn(o.Exe, o.LoginArgs())
	case "darwin":
		if !o.App {
			return nil
		}
		if s.Launchctl == nil {
			return errUnsupported
		}
		_, err := s.Launchctl("kickstart", s.Domain+"/"+o.label())
		return err
	}
	return errUnsupported
}

// Unregister removes both modes' entries; missing ones are not an error.
// On macOS unloading the agent also stops an app it runs.
func (s *System) Unregister(o Options) error {
	switch s.GOOS {
	case "windows":
		var errs []error
		if s.Run != nil {
			errs = append(errs, s.Run.Delete(o.runValue()))
		}
		if s.Tasks != nil {
			errs = append(errs, s.Tasks.Uninstall(o.taskName()))
		}
		return errors.Join(errs...)
	case "darwin":
		if s.AgentsDir == "" {
			return nil
		}
		label := o.label()
		if s.Launchctl != nil {
			s.Launchctl("bootout", s.Domain+"/"+label)
		}
		if err := os.Remove(s.plistPath(label)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// UpgradeLoginItem rewrites an app-mode login entry that an earlier version
// registered without --minimized (self-update replaces the binary, not the
// entries), so the next login starts the window minimized. Only an entry
// that is exactly what that version wrote for o is touched: Windows' Run
// value (`"exe" [--home H] app`), or the macOS plist, whose new contents
// launchd reads at the next login (it is not reloaded now, which would stop
// the running app). It reports whether it rewrote one.
func (s *System) UpgradeLoginItem(o Options) (bool, error) {
	if !o.App {
		return false, nil
	}
	switch s.GOOS {
	case "windows":
		if s.Run == nil {
			return false, nil
		}
		v, ok, err := s.Run.Get(o.runValue())
		if err != nil || !ok || v != CommandLine(o.Exe, o.AppArgs()) {
			return false, err
		}
		if err := s.Run.Set(o.runValue(), CommandLine(o.Exe, o.LoginArgs())); err != nil {
			return false, fmt.Errorf("login item: %w", err)
		}
		return true, nil
	case "darwin":
		if s.AgentsDir == "" {
			return false, nil
		}
		label := o.label()
		p := s.plistPath(label)
		b, err := os.ReadFile(p)
		if err != nil || string(b) != plist(label, o, o.AppArgs()) {
			return false, nil
		}
		if err := os.WriteFile(p, []byte(Plist(label, o)), 0o644); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// Registration is what is registered.
type Registration struct {
	// LoginItem: the Windows Run value, or a macOS agent that runs the app.
	LoginItem bool
	// Task: the Windows task (watchdog or tick), or a macOS agent that
	// runs the headless tick.
	Task bool
}

// Mode is "app", "headless" or "missing": app mode needs every entry it
// registers (Windows: the Run value and the watchdog task), headless mode
// only its task or agent.
func (r Registration) Mode(goos string) string {
	switch {
	case goos == "darwin" && r.LoginItem, goos == "windows" && r.LoginItem && r.Task:
		return "app"
	case r.Task && !r.LoginItem:
		return "headless"
	}
	return "missing"
}

// Registered reports the entries present.
func (s *System) Registered(o Options) Registration {
	var r Registration
	switch s.GOOS {
	case "windows":
		if s.Run != nil {
			_, ok, err := s.Run.Get(o.runValue())
			r.LoginItem = ok && err == nil
		}
		if s.Tasks != nil {
			r.Task = s.Tasks.Present(o.taskName())
		}
	case "darwin":
		if s.AgentsDir == "" {
			break
		}
		b, err := os.ReadFile(s.plistPath(o.label()))
		if err != nil {
			break
		}
		app := bytes.Contains(b, []byte("<string>app</string>"))
		r.LoginItem, r.Task = app, !app
	}
	return r
}

// Describe is doctor's view: the Run value and the task's state (Windows),
// the plist and launchd's view of it (macOS).
func (s *System) Describe(o Options) []string {
	var out []string
	switch s.GOOS {
	case "windows":
		if s.Run != nil {
			if v, ok, _ := s.Run.Get(o.runValue()); ok {
				out = append(out, "login item: "+v)
			}
		}
		if s.Tasks != nil && s.Tasks.Present(o.taskName()) {
			d, _ := s.Tasks.Describe(o.taskName())
			out = append(out, pick(d, "Task To Run:", "Status:", "Last Run Time:", "Last Result:", "Next Run Time:")...)
		}
	case "darwin":
		p := s.plistPath(o.label())
		if _, err := os.Stat(p); err == nil {
			out = append(out, "LaunchAgent: "+p)
			if s.Launchctl != nil {
				d, _ := s.Launchctl("print", s.Domain+"/"+o.label())
				out = append(out, pick(d, "state =", "pid =", "last exit code", "runs =")...)
			}
		}
	}
	return out
}

// pick keeps the lines of text that start with one of keys.
func pick(text string, keys ...string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		l := strings.TrimSpace(line)
		for _, k := range keys {
			if strings.HasPrefix(l, k) {
				out = append(out, l)
				break
			}
		}
	}
	return out
}

func (s *System) plistPath(label string) string { return filepath.Join(s.AgentsDir, label+".plist") }

// winQuote quotes one command-line argument for CommandLineToArgvW.
func winQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for _, r := range s {
		switch r {
		case '\\':
			slashes++
			continue
		case '"':
			b.WriteString(strings.Repeat(`\`, 2*slashes+1))
		default:
			b.WriteString(strings.Repeat(`\`, slashes))
		}
		slashes = 0
		b.WriteRune(r)
	}
	b.WriteString(strings.Repeat(`\`, 2*slashes))
	b.WriteByte('"')
	return b.String()
}

// CommandLine is a Windows command line: "<exe>" args...
func CommandLine(exe string, args []string) string {
	if len(args) == 0 {
		return `"` + exe + `"`
	}
	return `"` + exe + `" ` + ArgLine(args)
}

// ArgLine is args quoted for CommandLineToArgvW and joined (a shell
// link's arguments).
func ArgLine(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = winQuote(a)
	}
	return strings.Join(parts, " ")
}

func xmlText(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// Plist renders the LaunchAgent. App mode: started at login and restarted
// by launchd only if it dies (a Quit from the menu exits 0 and stays quit
// until the next login), in the GUI session. Headless: a tick every minute
// at background priority.
func Plist(label string, o Options) string {
	// launchd itself is the watchdog on macOS: the app gets no --watchdog.
	run := o.RunArgs()
	if o.App {
		run = o.LoginArgs()
	}
	return plist(label, o, run)
}

// plist renders the LaunchAgent with run as the program's arguments.
func plist(label string, o Options, run []string) string {
	var args strings.Builder
	for _, a := range append([]string{o.Exe}, run...) {
		args.WriteString("    <string>" + xmlText(a) + "</string>\n")
	}
	mode := `  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
  <key>ProcessType</key>
  <string>Interactive</string>
  <key>LimitLoadToSessionType</key>
  <string>Aqua</string>
`
	if !o.App {
		mode = `  <key>StartInterval</key>
  <integer>60</integer>
  <key>ProcessType</key>
  <string>Background</string>
  <key>LowPriorityIO</key>
  <true/>
  <key>Nice</key>
  <integer>10</integer>
`
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>` + xmlText(label) + `</string>
  <key>ProgramArguments</key>
  <array>
` + args.String() + `  </array>
  <key>RunAtLoad</key>
  <true/>
` + mode + `  <key>StandardOutPath</key>
  <string>/dev/null</string>
  <key>StandardErrorPath</key>
  <string>/dev/null</string>
</dict>
</plist>
`
}
