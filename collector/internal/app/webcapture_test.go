package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/webcapture"
	"github.com/7-of-9/tokenmaxr/collector/internal/webclaude"
)

// fakeWebCapture lays out an installed Claude Web capture in the state
// directory without registering anything with Chrome: the install record
// names a host that does not exist, so removing it changes nothing on this
// machine. It returns the spool file, holding one line with prompt text.
func fakeWebCapture(t *testing.T, home string) string {
	t.Helper()
	dir := paths.WebClaude(home)
	ext := paths.ChromeExtension(home)
	if err := os.MkdirAll(ext, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(ext, "manifest.json"), []byte("{}"), 0o644)
	in := webcapture.Install{Host: "com.d0m1.tokenmaxr.test-uninstall-absent", Exe: "tokenmaxr", ExtensionDir: ext,
		Manifest: filepath.Join(dir, "com.d0m1.tokenmaxr.test-uninstall-absent.json"), At: time.Now().UTC()}
	b, _ := json.Marshal(in)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "install.json"), b, 0o600)
	text := "SECRET PROMPT"
	r := webclaude.Record{V: 1, Kind: webclaude.KindLive, Org: "11111111-2222-4333-8444-555555555555", Conv: "73c9aaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		Human: "a0a0aaaa-0000-4000-8000-000000000001", Assistant: "a0a0aaaa-0000-4000-8000-000000000002",
		TS: "2026-10-23T07:39:09Z", Calls: 1, Est: webclaude.Estimate{In: 5, Out: 1, Algo: webclaude.Algorithm}, Text: &text, At: "2026-10-23T07:39:10Z"}
	line, _ := json.Marshal(r)
	now := time.Date(2026, 10, 23, 8, 0, 0, 0, time.UTC)
	if err := webclaude.Append(dir, now, [][]byte{line}, nil); err != nil {
		t.Fatal(err)
	}
	files, _ := webclaude.SpoolFiles(dir)
	if len(files) != 1 || !webcapture.Installed(home) {
		t.Fatalf("setup: %v", files)
	}
	return files[0]
}

// `tokenmaxr uninstall` removes Claude Web capture: the host registration,
// the extension folder and the spool's prompt text (the spool's numbers stay
// without --purge).
func TestUninstallRemovesWebCapture(t *testing.T) {
	a, _, out := newTestApp(t)
	spool := fakeWebCapture(t, a.Home)
	if err := a.Uninstall(UninstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if webcapture.Installed(a.Home) || fileExists(filepath.Join(paths.ChromeExtension(a.Home), "manifest.json")) {
		t.Fatal("web capture left installed")
	}
	if !strings.Contains(out.String(), "Claude Web capture removed") {
		t.Fatalf("output:\n%s", out)
	}
	b, err := os.ReadFile(spool)
	if err != nil || strings.Contains(string(b), "SECRET") {
		t.Fatalf("spool %v: prompt text kept: %q", err, b)
	}
	if r, err := webclaude.Decode([]byte(strings.TrimSpace(string(b)))); err != nil || r.Text != nil || r.Est.In != 5 {
		t.Fatalf("spool record %+v %v", r, err)
	}
}

func TestUninstallPurgeDeletesTheWebCaptureSpool(t *testing.T) {
	a, _, _ := newTestApp(t)
	fakeWebCapture(t, a.Home)
	if err := a.Uninstall(UninstallOptions{Purge: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.WebClaude(a.Home)); !os.IsNotExist(err) {
		t.Fatalf("web capture state kept: %v", err)
	}
}
