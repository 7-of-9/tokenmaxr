package webcapture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/extension"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
	"github.com/7-of-9/tokenmaxr/collector/internal/webclaude"
)

// fakeRegistry stands in for HKCU / the NativeMessagingHosts folders.
func fakeRegistry(t *testing.T) map[string]string {
	reg := map[string]string{}
	register = func(name, manifest string) error { reg[name] = manifest; return nil }
	unregister = func(name string) error { delete(reg, name); return nil }
	registered = func(name string) string { return reg[name] }
	t.Cleanup(func() { register, unregister, registered = osRegister, osUnregister, osRegistered })
	return reg
}

func TestInstallWritesThePinnedExtensionAndHost(t *testing.T) {
	reg := fakeRegistry(t)
	home := t.TempDir()
	exe := filepath.Join(home, "bin", "tokenmaxr.exe")
	in, err := Do(Options{Home: home, Exe: exe, Dev: true})
	if err != nil {
		t.Fatal(err)
	}
	if in.Host != DevHostName || reg[DevHostName] != in.Manifest || in.ExtensionID != extension.ID() {
		t.Fatalf("%+v %v", in, reg)
	}
	var m HostManifest
	b, _ := os.ReadFile(in.Manifest)
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.Name != DevHostName || m.Type != "stdio" || len(m.AllowedOrigins) != 1 || m.AllowedOrigins[0] != "chrome-extension://"+extension.ID()+"/" {
		t.Fatalf("host manifest %+v", m)
	}
	l, _ := os.ReadFile(m.Path)
	if !strings.Contains(string(l), exe) || !strings.Contains(string(l), home) || !strings.Contains(string(l), "native-host") {
		t.Fatalf("launcher %q", l)
	}
	cfg, _ := os.ReadFile(filepath.Join(in.ExtensionDir, "config.json"))
	if !strings.Contains(string(cfg), DevHostName) {
		t.Fatalf("extension config %s", cfg)
	}
	for _, f := range []string{"manifest.json", "main-world.js", "relay.js", "sw.js", "queue.js", "estimate.js"} {
		if _, err := os.Stat(filepath.Join(in.ExtensionDir, f)); err != nil {
			t.Fatal(err)
		}
	}
	if s := Read(home, time.Now()); s.State != StateWaiting || !s.Registered {
		t.Fatalf("status %+v", s)
	}
	// The host connecting makes it active.
	if err := webclaude.SaveHello(paths.WebClaude(home), webclaude.Hello{Ext: "0.1.0", ConnectedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if s := Read(home, time.Now()); s.State != StateActive || s.Extension != "0.1.0" {
		t.Fatalf("status %+v", s)
	}
}

func TestRemoveKeepsCapturedRecords(t *testing.T) {
	reg := fakeRegistry(t)
	home := t.TempDir()
	if _, err := Do(Options{Home: home, Exe: "tokenmaxr"}); err != nil {
		t.Fatal(err)
	}
	dir := paths.WebClaude(home)
	if err := webclaude.Append(dir, time.Now(), [][]byte{[]byte(`{}`)}, nil); err != nil {
		t.Fatal(err)
	}
	if err := Remove(home); err != nil {
		t.Fatal(err)
	}
	if len(reg) != 0 {
		t.Fatalf("still registered: %v", reg)
	}
	if _, err := os.Stat(paths.ChromeExtension(home)); !os.IsNotExist(err) {
		t.Fatal("extension folder left behind")
	}
	if files, _ := webclaude.SpoolFiles(dir); len(files) != 1 {
		t.Fatal("captured records were deleted")
	}
	if s := Read(home, time.Now()); s.State != StateNotInstalled {
		t.Fatalf("status %+v", s)
	}
}

func TestSweeperAndInferenceSettings(t *testing.T) {
	home := t.TempDir()
	if err := SetSweeper(home, true); err != nil {
		t.Fatal(err)
	}
	if err := SetInferApps(home, false); err != nil {
		t.Fatal(err)
	}
	s := webclaude.LoadSettings(paths.WebClaude(home))
	if !s.Sweeper || s.Infer() {
		t.Fatalf("%+v", s)
	}
}

// Chat prompt text needs both switches; the status shows each and the result.
func TestStatusShowsTheEffectiveChatPromptState(t *testing.T) {
	home := t.TempDir()
	if s := Read(home, time.Now()); s.Prompts != "off" || s.PromptCapture || s.LivePrompts || s.SweepPrompts {
		t.Fatalf("no config: %+v", s)
	}
	cfg := store.DefaultConfig()
	cfg.Prompts = true
	if err := store.SaveConfig(home, cfg); err != nil {
		t.Fatal(err)
	}
	if s := Read(home, time.Now()); !s.PromptCapture || s.LivePrompts || s.SweepPrompts {
		t.Fatalf("prompt capture alone turned on chat text: %+v", s)
	}
	if err := SetPrompts(home, "live"); err != nil {
		t.Fatal(err)
	}
	if s := Read(home, time.Now()); !s.LivePrompts || s.SweepPrompts || s.Prompts != "live" {
		t.Fatalf("live: %+v", s)
	}
	if err := SetPrompts(home, "all"); err != nil {
		t.Fatal(err)
	}
	if s := Read(home, time.Now()); !s.LivePrompts || !s.SweepPrompts {
		t.Fatalf("all: %+v", s)
	}
	if SetPrompts(home, "everything") == nil {
		t.Fatal("a bad mode was saved")
	}
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if err := SetChatBefore(home, at); err != nil {
		t.Fatal(err)
	}
	if s := Read(home, time.Now()); !s.ChatBefore.Equal(at) {
		t.Fatalf("chatBefore %v", s.ChatBefore)
	}
	if err := SetChatBefore(home, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if s := Read(home, time.Now()); !s.ChatBefore.IsZero() {
		t.Fatalf("chatBefore not cleared: %v", s.ChatBefore)
	}
}

// Without an install record, Remove takes only a registration that starts
// this state directory's launcher: Chrome's own copy of the host manifest
// (macOS, Linux) counts, another state directory's does not.
func TestRemoveWithoutRecordTakesOnlyOurRegistration(t *testing.T) {
	reg := fakeRegistry(t)
	home, other := t.TempDir(), t.TempDir()
	chrome := t.TempDir()
	write := func(name, launcherDir string) string {
		p := filepath.Join(chrome, name+".json")
		b, _ := json.Marshal(HostManifest{Name: name, Path: filepath.Join(launcherDir, launcherName), Type: "stdio"})
		os.WriteFile(p, b, 0o644)
		return p
	}
	reg[HostName] = write(HostName, paths.WebClaude(home))
	reg[DevHostName] = write(DevHostName, paths.WebClaude(other))
	if !Installed(home) {
		t.Fatal("our registration not seen")
	}
	if err := Remove(home); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg[HostName]; ok {
		t.Fatal("our registration kept")
	}
	if _, ok := reg[DevHostName]; !ok {
		t.Fatal("another state directory's registration removed")
	}
	if Installed(home) {
		t.Fatal("still installed")
	}
}
