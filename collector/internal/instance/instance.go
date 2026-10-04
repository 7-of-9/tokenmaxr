// Package instance keeps the desktop app to one running process
// (docs/agents/SPEC.md "Desktop app (v1.4)"). The app holds app.lock for as
// long as it runs; a second launch, install, uninstall and a self-update
// talk to it through flag files in the state directory, which it polls.
package instance

import (
	"errors"
	"os"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/lock"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
)

// Verb is a request to the running app.
type Verb string

const (
	// Show: a second launch; the app refreshes and syncs now.
	Show Verb = "show"
	// Quit: uninstall or a switch to headless mode.
	Quit Verb = "quit"
	// Restart: its binary was replaced; it re-execs the new one.
	Restart Verb = "restart"
	// Pin and Unpin show and close the live panel, as the menu does.
	Pin   Verb = "pin"
	Unpin Verb = "unpin"
	// Dump writes what the menu and panel show to app.view.txt
	// (diagnostics: display text only, never the token or K).
	Dump Verb = "dump"
	// Settings opens the settings page in the browser.
	Settings Verb = "settings"
)

// verbs in the order Take reports them (the most final first).
var verbs = []Verb{Quit, Restart, Show, Pin, Unpin, Dump, Settings}

// ErrRunning means another process holds app.lock.
var ErrRunning = errors.New("the app is already running")

// Claim takes app.lock without waiting and clears requests left for an
// earlier process. It returns ErrRunning when an app runs.
func Claim(home string) (*lock.Lock, error) {
	lk, err := lock.TryAcquire(paths.AppLock(home))
	if errors.Is(err, lock.ErrHeld) {
		return nil, ErrRunning
	}
	if err != nil {
		return nil, err
	}
	for _, v := range verbs {
		os.Remove(paths.AppSignal(home, string(v)))
	}
	return lk, nil
}

// Running reports whether an app holds app.lock.
func Running(home string) bool { return lock.Held(paths.AppLock(home)) }

// Send leaves v for the running app, which acts on it within PollEvery.
func Send(home string, v Verb) error {
	return os.WriteFile(paths.AppSignal(home, string(v)), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600)
}

// PollEvery is how often the app looks for requests.
const PollEvery = time.Second

// Take returns the waiting requests and clears them.
func Take(home string) []Verb {
	var out []Verb
	for _, v := range verbs {
		p := paths.AppSignal(home, string(v))
		if _, err := os.Stat(p); err == nil {
			os.Remove(p)
			out = append(out, v)
		}
	}
	return out
}

// Stop asks a running app to quit and waits up to wait for it to let go of
// app.lock. It reports whether no app runs afterwards.
func Stop(home string, wait time.Duration) bool {
	if !Running(home) {
		return true
	}
	if Send(home, Quit) != nil {
		return false
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		if !Running(home) {
			return true
		}
	}
	os.Remove(paths.AppSignal(home, string(Quit)))
	return !Running(home)
}

// MarkStopped records a Quit from the app's menu: the Windows watchdog then
// leaves the app stopped until something else starts it (the next login).
func MarkStopped(home string) error {
	return os.WriteFile(paths.AppStopped(home), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600)
}

// Stopped reports whether the app was quit from its menu.
func Stopped(home string) bool {
	_, err := os.Stat(paths.AppStopped(home))
	return err == nil
}

// ClearStopped forgets a Quit (a start by login, install or the user).
func ClearStopped(home string) { os.Remove(paths.AppStopped(home)) }
