package extension

import (
	"encoding/json"
	"io/fs"
	"testing"
)

func TestIDIsPinnedByTheManifestKey(t *testing.T) {
	if got, want := ID(), "cbbaflndpdfcnifplodaobkmbcghdaoc"; got != want {
		t.Fatalf("ID() = %q, want %q (a changed key changes the extension id every install allows)", got, want)
	}
}

func TestEmbeddedManifestNamesEveryFile(t *testing.T) {
	b, err := fs.ReadFile(Files(), "manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		ManifestVersion int      `json:"manifest_version"`
		Permissions     []string `json:"permissions"`
		HostPermissions []string `json:"host_permissions"`
		Background      struct {
			ServiceWorker string `json:"service_worker"`
		} `json:"background"`
		ContentScripts []struct {
			Matches []string `json:"matches"`
			JS      []string `json:"js"`
			World   string   `json:"world"`
		} `json:"content_scripts"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.ManifestVersion != 3 || len(m.HostPermissions) != 0 {
		t.Fatalf("manifest: v%d host permissions %v", m.ManifestVersion, m.HostPermissions)
	}
	want := map[string]bool{"nativeMessaging": true, "alarms": true, "storage": true}
	for _, p := range m.Permissions {
		if !want[p] {
			t.Fatalf("unexpected permission %q", p)
		}
	}
	names := []string{m.Background.ServiceWorker, "queue.js", "config.json"}
	for _, cs := range m.ContentScripts {
		for _, u := range cs.Matches {
			if u != "https://claude.ai/*" {
				t.Fatalf("content script matches %q", u)
			}
		}
		names = append(names, cs.JS...)
	}
	for _, n := range names {
		if _, err := fs.Stat(Files(), n); err != nil {
			t.Fatalf("%s is not embedded: %v", n, err)
		}
	}
}
