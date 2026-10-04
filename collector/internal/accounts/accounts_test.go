package accounts

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	b, _ := json.Marshal(v)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func jwt(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func TestProbe(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	writeJSON(t, filepath.Join(home, ".claude.json"), map[string]any{
		"numStartups":  3,
		"oauthAccount": map[string]any{"accountUuid": "claude-uuid-1", "emailAddress": "user@example.com", "organizationName": "Example Org"},
	})
	writeJSON(t, filepath.Join(codexHome, "auth.json"), map[string]any{
		"tokens": map[string]any{
			"id_token":   jwt(map[string]any{"email": "user@example.org", "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "chatgpt-acct-1"}}),
			"account_id": "fallback-acct",
		},
	})
	writeJSON(t, filepath.Join(home, ".grok", "auth.json"), map[string]any{
		"https://auth.x.ai::older": map[string]any{"user_id": "grok-old", "email": "old@example.net", "expires_at": "2026-01-01T00:00:00Z"},
		"https://auth.x.ai::newer": map[string]any{"user_id": "grok-new", "email": "new@example.net", "expires_at": "2026-09-01T00:00:00.123456789Z"},
	})
	writeJSON(t, filepath.Join(home, ".gemini", "google_accounts.json"), map[string]any{"active": "g@example.com", "old": []string{}})
	writeJSON(t, filepath.Join(home, ".gemini", "oauth_creds.json"), map[string]any{"access_token": "never-read"})
	got := map[string]Observation{}
	for _, o := range Probe(home, codexHome) {
		got[o.Provider] = o
	}
	if o := got[model.ProviderAnthropic]; o.NativeID != "claude-uuid-1" || o.Label != "user@example.com · Example Org" {
		t.Errorf("claude: %+v", o)
	}
	if o := got[model.ProviderOpenAI]; o.NativeID != "chatgpt-acct-1" || o.Label != "user@example.org" {
		t.Errorf("codex: %+v", o)
	}
	if o := got[model.ProviderXAI]; o.NativeID != "grok-new" || o.Label != "new@example.net" {
		t.Errorf("grok: %+v", o)
	}
	if o := got["google"]; o.NativeID != "g@example.com" || o.Label != "g@example.com" {
		t.Errorf("gemini: %+v", o)
	}

	// Codex falls back to tokens.account_id when the claim is missing.
	writeJSON(t, filepath.Join(codexHome, "auth.json"), map[string]any{
		"tokens": map[string]any{"id_token": jwt(map[string]any{"email": "e@example.org"}), "account_id": "fallback-acct"},
	})
	for _, o := range Probe(home, codexHome) {
		if o.Provider == model.ProviderOpenAI && o.NativeID != "fallback-acct" {
			t.Errorf("codex fallback: %+v", o)
		}
	}
	// No auth files: no observations.
	if obs := Probe(t.TempDir(), t.TempDir()); len(obs) != 0 {
		t.Errorf("empty home: %+v", obs)
	}
}

func TestHashIsFleetWide(t *testing.T) {
	k := make([]byte, 32)
	o := Observation{Provider: model.ProviderOpenAI, NativeID: "x"}
	if Hash(k, o) != model.AccountHash(k, "openai", "x") {
		t.Fatal("hash mismatch")
	}
}

func TestTimelineAndAttribution(t *testing.T) {
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	var ivs []store.Interval
	ivs = Observe(ivs, "", "anthropic", "a_A", t0)
	ivs = Observe(ivs, "", "anthropic", "a_A", t0.Add(time.Hour)) // extended
	ivs = Observe(ivs, "", "anthropic", "a_B", t0.Add(3*time.Hour))
	ivs = Observe(ivs, "", "anthropic", "a_B", t0.Add(4*time.Hour))
	ivs = Observe(ivs, "", "openai", "a_O", t0)
	if len(ivs) != 3 || !ivs[0].To.Equal(t0.Add(time.Hour)) {
		t.Fatalf("intervals: %+v", ivs)
	}
	// Another home with the same provider keeps its own timeline: it neither
	// extends nor is attributed from the OS home's intervals.
	const wsl = `\\wsl$\Ubuntu\home\dom`
	ivs = Observe(ivs, wsl, "anthropic", "a_W", t0.Add(30*time.Minute))
	ivs = Observe(ivs, wsl, "anthropic", "a_W", t0.Add(40*time.Minute))
	if len(ivs) != 4 || ivs[3].Home != wsl || !ivs[0].To.Equal(t0.Add(time.Hour)) {
		t.Fatalf("per-home intervals: %+v", ivs)
	}
	if acct, q := Attribute(ivs, wsl, "anthropic", t0.Add(35*time.Minute)); acct != "a_W" || q != model.AcctTimeline {
		t.Errorf("wsl home: %s/%s", acct, q)
	}
	if acct, q := Attribute(ivs, wsl, "anthropic", t0.Add(-24*time.Hour)); acct != "a_W" || q != model.AcctInferred {
		t.Errorf("wsl home inferred: %s/%s", acct, q)
	}
	if acct, q := Attribute(ivs, wsl, "openai", t0); acct != "" || q != model.AcctUnknown {
		t.Errorf("wsl home, provider only seen in the OS home: %s/%s", acct, q)
	}
	cases := []struct {
		provider string
		ts       time.Time
		acct, q  string
	}{
		{"anthropic", t0.Add(30 * time.Minute), "a_A", model.AcctTimeline},
		{"anthropic", t0.Add(-24 * time.Hour), "a_A", model.AcctInferred}, // before first observation
		{"anthropic", t0.Add(-time.Minute), "a_A", model.AcctTimeline},    // within a tick of it
		{"anthropic", t0.Add(3*time.Hour + 30*time.Minute), "a_B", model.AcctTimeline},
		{"anthropic", t0.Add(90 * time.Minute), "a_A", model.AcctInferred},  // gap, nearer A
		{"anthropic", t0.Add(170 * time.Minute), "a_B", model.AcctInferred}, // gap, nearer B
		{"anthropic", t0.Add(4*time.Hour + time.Minute), "a_B", model.AcctTimeline},
		{"anthropic", t0.Add(30 * time.Hour), "a_B", model.AcctInferred}, // long after last seen
		{"xai", t0, "", model.AcctUnknown},
	}
	for _, c := range cases {
		acct, q := Attribute(ivs, "", c.provider, c.ts)
		if acct != c.acct || q != c.q {
			t.Errorf("%s @%s = %s/%s, want %s/%s", c.provider, c.ts.Sub(t0), acct, q, c.acct, c.q)
		}
	}
}
