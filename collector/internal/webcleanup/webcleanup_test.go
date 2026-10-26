package webcleanup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
)

// fakeRegistry stands in for HKCU and Chrome's NativeMessagingHosts folders:
// host name -> the manifest paths Chrome would find.
func fakeRegistry(t *testing.T) map[string][]string {
	reg := map[string][]string{}
	registrations = func(name string) []string { return slices.Clone(reg[name]) }
	unregister = func(name, manifest string) error {
		reg[name] = slices.DeleteFunc(reg[name], func(p string) bool { return p == manifest })
		if len(reg[name]) == 0 {
			delete(reg, name)
		}
		return nil
	}
	t.Cleanup(func() { registrations, unregister = osRegistrations, osUnregister })
	return reg
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// manifest writes a host manifest at path that starts launcher.
func manifest(t *testing.T, path, name, launcher string) string {
	b, _ := json.Marshal(map[string]any{"name": name, "path": launcher, "type": "stdio",
		"allowed_origins": []string{"chrome-extension://abcdefghijklmnopabcdefghijklmnop/"}})
	write(t, path, string(b))
	return path
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// What a 0.4.16 `web-capture install` left goes: this state directory's
// registration (HKCU's manifest path inside it), the extension folder, and
// the capture folder with its launcher, host manifest and spool. Another
// state directory's registration stays.
func TestCleanRemovesWhat0416Installed(t *testing.T) {
	reg := fakeRegistry(t)
	home, other, chrome := t.TempDir(), t.TempDir(), t.TempDir()
	dir, ext := paths.WebClaude(home), paths.ChromeExtension(home)
	reg[hostName] = []string{manifest(t, filepath.Join(dir, hostName+".json"), hostName, filepath.Join(dir, "native-host.cmd"))}
	write(t, filepath.Join(dir, "native-host.cmd"), "@echo off\r\n")
	write(t, filepath.Join(dir, "spool", "2026-10-25.jsonl"), `{"v":1}`+"\n")
	write(t, filepath.Join(dir, "settings.json"), `{"prompts":"off"}`)
	write(t, filepath.Join(ext, "manifest.json"), `{}`)
	write(t, filepath.Join(ext, "sw.js"), "")
	// Another state directory's development host, as Chrome's own copy.
	theirs := manifest(t, filepath.Join(chrome, devHostName+".json"), devHostName, filepath.Join(paths.WebClaude(other), "native-host.sh"))
	reg[devHostName] = []string{theirs}

	removed, err := Clean(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 3 {
		t.Fatalf("removed %q", removed)
	}
	if _, ok := reg[hostName]; ok {
		t.Fatal("our registration kept")
	}
	if got := reg[devHostName]; len(got) != 1 || got[0] != theirs {
		t.Fatalf("another state directory's registration changed: %v", reg)
	}
	if exists(dir) || exists(ext) {
		t.Fatal("capture or extension folder left behind")
	}
	if !exists(home) {
		t.Fatal("the state directory itself went")
	}

	// Done once, it finds nothing more.
	if removed, err := Clean(home); err != nil || len(removed) != 0 {
		t.Fatalf("second clean: %q %v", removed, err)
	}
}

// On macOS and Linux Chrome keeps its own copy of the host manifest, in each
// browser's folder; a copy that starts this state directory's launcher is
// ours even when the capture folder is gone already.
func TestCleanTakesChromesManifestCopies(t *testing.T) {
	reg := fakeRegistry(t)
	home, chrome, chromium := t.TempDir(), t.TempDir(), t.TempDir()
	launcher := filepath.Join(paths.WebClaude(home), "native-host.sh")
	reg[hostName] = []string{
		manifest(t, filepath.Join(chrome, hostName+".json"), hostName, launcher),
		manifest(t, filepath.Join(chromium, hostName+".json"), hostName, launcher),
	}
	removed, err := Clean(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 || len(reg) != 0 {
		t.Fatalf("removed %q, left %v", removed, reg)
	}
}

func TestCleanWithNothingInstalled(t *testing.T) {
	reg := fakeRegistry(t)
	home := t.TempDir()
	// An unrelated host of the same name whose manifest cannot be read.
	reg[hostName] = []string{filepath.Join(t.TempDir(), "missing", hostName+".json")}
	write(t, filepath.Join(home, "rollup.json"), `{"v":6}`)
	removed, err := Clean(home)
	if err != nil || len(removed) != 0 {
		t.Fatalf("removed %q, err %v", removed, err)
	}
	if len(reg[hostName]) != 1 || !exists(filepath.Join(home, "rollup.json")) {
		t.Fatal("cleaned what was not the capture's")
	}
}

// The real registration reader never fails on a machine without Chrome or
// without the host.
func TestOSRegistrationsOfAnUnknownHost(t *testing.T) {
	if got := osRegistrations("com.d0m1.tokenmaxr.test-never-registered"); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}
