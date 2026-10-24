// Package webcapture installs Claude Web capture (docs/agents/SPEC.md
// "Claude chat"): it writes the embedded browser extension to the state
// directory for the owner to load unpacked, and registers tokenmaxr as the
// Chrome native-messaging host that only that extension may start. Nothing
// listens on the network, and the collector still reads no browser data:
// the extension sends the host numbers and ids, and the host writes them to
// a spool that the web-claude source reads.
package webcapture

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/extension"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
	"github.com/7-of-9/tokenmaxr/collector/internal/webclaude"
)

// The OS registration of a native host (register_*.go); tests replace them.
var (
	register   = osRegister
	unregister = osUnregister
	registered = osRegistered
)

// Host names: the release host, and a separate one for development builds
// so a test install never replaces the owner's.
const (
	HostName    = "com.d0m1.tokenmaxr"
	DevHostName = HostName + ".dev"
)

// Options for Install.
type Options struct {
	// Home is the collector state directory.
	Home string
	// Exe is the tokenmaxr binary Chrome starts (a console build on Windows).
	Exe string
	// Dev registers DevHostName.
	Dev bool
}

// Install is what was installed.
type Install struct {
	Host         string    `json:"host"`
	Exe          string    `json:"exe"`
	ExtensionID  string    `json:"extensionId"`
	ExtensionDir string    `json:"extensionDir"`
	Manifest     string    `json:"manifest"`
	At           time.Time `json:"at"`
}

func installPath(home string) string { return filepath.Join(paths.WebClaude(home), "install.json") }

// HostManifest is Chrome's native-messaging host manifest.
type HostManifest struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Path           string   `json:"path"`
	Type           string   `json:"type"`
	AllowedOrigins []string `json:"allowed_origins"`
}

// Do installs or repairs the capture.
func Do(o Options) (Install, error) {
	if o.Home == "" || o.Exe == "" {
		return Install{}, errors.New("web capture: state directory and binary are required")
	}
	id := extension.ID()
	if id == "" {
		return Install{}, errors.New("web capture: the embedded extension has no key")
	}
	name := HostName
	if o.Dev {
		name = DevHostName
	}
	ext := paths.ChromeExtension(o.Home)
	if err := writeExtension(ext, name); err != nil {
		return Install{}, fmt.Errorf("web capture: writing the extension: %w", err)
	}
	dir := paths.WebClaude(o.Home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Install{}, err
	}
	launch := filepath.Join(dir, launcherName)
	if err := os.WriteFile(launch, launcher(o.Exe, o.Home), 0o700); err != nil {
		return Install{}, err
	}
	man := filepath.Join(dir, name+".json")
	b, _ := json.MarshalIndent(HostManifest{
		Name:           name,
		Description:    "tokenmaxr Claude Web capture",
		Path:           launch,
		Type:           "stdio",
		AllowedOrigins: []string{"chrome-extension://" + id + "/"},
	}, "", "  ")
	if err := os.WriteFile(man, append(b, '\n'), 0o644); err != nil {
		return Install{}, err
	}
	if err := register(name, man); err != nil {
		return Install{}, fmt.Errorf("web capture: registering the native host: %w", err)
	}
	in := Install{Host: name, Exe: o.Exe, ExtensionID: id, ExtensionDir: ext, Manifest: man, At: time.Now().UTC().Truncate(time.Second)}
	b, _ = json.MarshalIndent(in, "", "  ")
	return in, os.WriteFile(installPath(o.Home), append(b, '\n'), 0o600)
}

// writeExtension replaces the unpacked extension folder with the embedded
// files and a config.json naming the host.
func writeExtension(dir, host string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	err := fs.WalkDir(extension.Files(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(extension.Files(), p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, filepath.FromSlash(p)), b, 0o644)
	})
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(map[string]string{"host": host}, "", "  ")
	return os.WriteFile(filepath.Join(dir, "config.json"), append(b, '\n'), 0o644)
}

// Load returns the recorded install, if any.
func Load(home string) (Install, bool) {
	var in Install
	b, err := os.ReadFile(installPath(home))
	if err != nil || json.Unmarshal(b, &in) != nil || in.Host == "" {
		return Install{}, false
	}
	return in, true
}

// Remove unregisters the host and deletes the extension folder, launcher
// and host manifest. Captured records (the spool) are kept, but the prompt
// text in them is deleted: the collector has normally read it within a
// minute, and nothing is captured any more.
func Remove(home string) error {
	in, ok := Load(home)
	var errs error
	names := []string{HostName, DevHostName}
	if ok {
		names = []string{in.Host}
	}
	for _, n := range names {
		// Without an install record, only a registration pointing into this
		// state directory is ours to remove.
		if !ok && !ours(registered(n), home) {
			continue
		}
		errs = errors.Join(errs, unregister(n))
		_ = os.Remove(filepath.Join(paths.WebClaude(home), n+".json"))
	}
	_ = os.Remove(filepath.Join(paths.WebClaude(home), launcherName))
	errs = errors.Join(errs, os.RemoveAll(paths.ChromeExtension(home)))
	_ = os.Remove(installPath(home))
	if _, err := os.Stat(paths.WebClaude(home)); err == nil {
		errs = errors.Join(errs, webclaude.ScrubAll(paths.WebClaude(home)))
	}
	return errs
}

// Installed reports whether this state directory has Claude Web capture
// installed (an install record or a registration pointing into it).
func Installed(home string) bool {
	if _, ok := Load(home); ok {
		return true
	}
	return ours(registered(HostName), home) || ours(registered(DevHostName), home)
}

// ours reports whether the host manifest at reg ("" if none) belongs to this
// state directory: it lives there (Windows: the registry names it) or, as a
// copy in Chrome's folder (macOS, Linux), it starts the launcher there.
func ours(reg, home string) bool {
	if reg == "" {
		return false
	}
	dir := paths.WebClaude(home)
	if filepath.Dir(reg) == dir {
		return true
	}
	var m HostManifest
	b, err := os.ReadFile(reg)
	return err == nil && json.Unmarshal(b, &m) == nil && m.Path != "" && filepath.Dir(m.Path) == dir
}

// Status states.
const (
	StateNotInstalled = "not-installed"
	StateWaiting      = "waiting"
	StateActive       = "active"
)

// Status is what the settings page and `web-capture status` show.
type Status struct {
	State        string    `json:"state"`
	Host         string    `json:"host,omitempty"`
	Registered   bool      `json:"registered"`
	ExtensionDir string    `json:"extensionDir,omitempty"`
	ExtensionID  string    `json:"extensionId,omitempty"`
	Extension    string    `json:"extension,omitempty"`
	LastSeen     time.Time `json:"lastSeen,omitzero"`
	LastRecord   time.Time `json:"lastRecord,omitzero"`
	RecordsToday int       `json:"recordsToday"`
	Records      int64     `json:"records"`
	Sweeper      bool      `json:"sweeper"`
	InferApps    bool      `json:"inferApps"`
	T0           time.Time `json:"t0,omitzero"`
	// Prompts is the chat prompt text setting (off, live or all) and
	// PromptCapture the collector's own prompt capture. Chat text is kept
	// only when both allow it: LivePrompts and SweepPrompts are the result.
	Prompts       string `json:"prompts"`
	PromptCapture bool   `json:"promptCapture"`
	LivePrompts   bool   `json:"livePrompts"`
	SweepPrompts  bool   `json:"sweepPrompts"`
	// ChatBefore is the fleet's capture start, when set (Settings.ChatBefore).
	ChatBefore time.Time `json:"chatBefore,omitzero"`
}

// PromptCapture reports whether the collector's prompt capture is on for
// this state directory. A state directory without config.json (never set
// up) counts as off.
func PromptCapture(home string) bool {
	if _, err := os.Stat(paths.Config(home)); err != nil {
		return false
	}
	cfg, err := store.LoadConfig(home)
	return err == nil && cfg.Prompts
}

// Read reports the capture's state.
func Read(home string, now time.Time) Status {
	dir := paths.WebClaude(home)
	set := webclaude.LoadSettings(dir)
	capture := PromptCapture(home)
	s := Status{State: StateNotInstalled, Sweeper: set.Sweeper, InferApps: set.Infer(), T0: webclaude.LoadEnrolled(dir).T0,
		Prompts: set.PromptMode(), PromptCapture: capture, ChatBefore: set.ChatBefore,
		LivePrompts:  capture && set.TextAllowed(webclaude.KindLive),
		SweepPrompts: capture && set.TextAllowed(webclaude.KindSweep)}
	in, ok := Load(home)
	if !ok {
		return s
	}
	s.Host, s.ExtensionDir, s.ExtensionID = in.Host, in.ExtensionDir, in.ExtensionID
	s.Registered = registered(in.Host) != ""
	s.State = StateWaiting
	if h, ok := webclaude.LoadHello(dir); ok {
		s.Extension, s.LastSeen, s.LastRecord, s.Records = h.Ext, h.ConnectedAt, h.LastRecordAt, h.Records
		if h.Day == now.UTC().Format("2006-01-02") {
			s.RecordsToday = h.RecordsToday
		}
		if s.Registered && !h.ConnectedAt.IsZero() {
			s.State = StateActive
		}
	}
	return s
}

// SetSweeper turns this machine's history sweep on or off.
func SetSweeper(home string, on bool) error {
	dir := paths.WebClaude(home)
	set := webclaude.LoadSettings(dir)
	set.Sweeper = on
	return webclaude.SaveSettings(dir, set)
}

// SetPrompts sets the chat prompt text setting: off, live or all.
func SetPrompts(home, mode string) error {
	switch mode {
	case webclaude.PromptsOff, webclaude.PromptsLive, webclaude.PromptsAll:
	default:
		return fmt.Errorf("web capture: prompts must be off, live or all")
	}
	dir := paths.WebClaude(home)
	set := webclaude.LoadSettings(dir)
	set.Prompts = mode
	return webclaude.SaveSettings(dir, set)
}

// SetChatBefore sets the fleet's capture start (zero clears it).
func SetChatBefore(home string, t time.Time) error {
	dir := paths.WebClaude(home)
	set := webclaude.LoadSettings(dir)
	set.ChatBefore = t.UTC()
	if t.IsZero() {
		set.ChatBefore = time.Time{}
	}
	return webclaude.SaveSettings(dir, set)
}

// SetInferApps turns the Claude Native (apps) inference on or off.
func SetInferApps(home string, on bool) error {
	dir := paths.WebClaude(home)
	set := webclaude.LoadSettings(dir)
	set.InferApps = &on
	return webclaude.SaveSettings(dir, set)
}
