package buildinfo

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The defaults compiled into an unflagged build must be release.json's values,
// so an official build behaves the same with or without the -X flags.
func TestDefaultsMatchReleaseJSON(t *testing.T) {
	b, err := os.ReadFile("../../release.json")
	if err != nil {
		t.Fatal(err)
	}
	var rel map[string]string
	if err := json.Unmarshal(b, &rel); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{
		"defaultEndpoint":    DefaultEndpoint,
		"updateUrl":          UpdateURL,
		"downloadBase":       DownloadBase,
		"releasePublicKey":   ReleasePublicKey,
		"githubAppSlug":      GitHubAppSlug,
		"githubAppId":        GitHubAppID,
		"githubClientId":     GitHubClientID,
		"githubTemplateRepo": GitHubTemplateRepo,
	}
	if len(rel) != len(LDFlagNames) {
		t.Fatalf("release.json has %d fields, buildinfo maps %d", len(rel), len(LDFlagNames))
	}
	for field, name := range LDFlagNames {
		if rel[field] == "" && field != "defaultEndpoint" {
			t.Errorf("release.json lacks %s", field)
		}
		if rel[field] != got[field] {
			t.Errorf("%s: release.json %q, buildinfo.%s %q", field, rel[field], name, got[field])
		}
	}
}

func TestDefaultsAreWellFormed(t *testing.T) {
	if GitHubAppSlug == "" || GitHubClientID == "" || len(strings.Split(GitHubTemplateRepo, "/")) != 2 {
		t.Errorf("GitHub App identity incomplete")
	}
	for _, u := range []string{UpdateURL, DownloadBase} {
		if !strings.HasPrefix(u, "https://") {
			t.Errorf("%q is not https", u)
		}
	}
	if k, err := base64.StdEncoding.DecodeString(ReleasePublicKey); err != nil || len(k) != 32 {
		t.Errorf("ReleasePublicKey is not a 32-byte base64 ed25519 key")
	}
}
