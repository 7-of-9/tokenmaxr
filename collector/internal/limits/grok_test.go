package limits

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
)

func grokTestEnv(t *testing.T, rows ...string) *sources.Env {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".grok", "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "unified.jsonl"), []byte(strings.Join(rows, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &sources.Env{Home: home, HashID: func(p, id string) string { return model.AccountHash([]byte("test-grok-limits"), p, id) }}
}

func grokTestAuth(pid int, id string) string {
	return grokTestLog(pid, "2026-10-03T00:00:00Z", "auth init user_info check", map[string]any{"user_id": id})
}

func grokTestLog(pid int, ts, msg string, ctx map[string]any) string {
	b, _ := json.Marshal(map[string]any{"pid": pid, "ts": ts, "msg": msg, "ctx": ctx})
	return string(b)
}

func grokTestBilling(pid int, ts string, used any, period string, unified bool) string {
	c := map[string]any{"currentPeriod": map[string]any{"type": period, "start": "2026-10-01T05:59:59.126253+00:00", "end": "2026-10-08T05:59:59.126253+00:00"}, "isUnifiedBillingUser": unified}
	if used != nil {
		c["creditUsagePercent"] = used
	}
	return grokTestLog(pid, ts, "billing: fetched credits config", map[string]any{"config": c, "subscriptionTier": "SuperGrok Heavy"})
}

func TestGrokWeeklyBilling(t *testing.T) {
	env := grokTestEnv(t,
		grokTestAuth(10, "first"),
		grokTestLog(10, "2026-10-03T00:00:01Z", "auth started", map[string]any{"method": "cached_token"}),
		grokTestBilling(10, "2026-10-03T01:00:00Z", 70, "USAGE_PERIOD_TYPE_WEEKLY", true),
		grokTestBilling(10, "2026-10-03T02:00:00Z", 3, "USAGE_PERIOD_TYPE_WEEKLY", true),
		grokTestAuth(20, "second"),
		grokTestBilling(20, "2026-10-03T02:00:00Z", 100, "USAGE_PERIOD_TYPE_WEEKLY", true),
	)
	got := grokBilling(env)
	if len(got) != 2 {
		t.Fatalf("want two account meters, got %+v", got)
	}
	first, second := got[0], got[1]
	if first.UsedPercent == nil || *first.UsedPercent != 3 || first.Status != "ok" {
		t.Fatalf("must take latest, not maximum: %+v", first)
	}
	if first.Acct != env.HashID(model.ProviderXAI, "first") || first.AcctQ != model.AcctRecorded || first.Window != "week" || first.Plan != "SuperGrok Heavy" || first.Detail != "Shared across Grok products" {
		t.Fatalf("bad identity/window: %+v", first)
	}
	if first.ResetsAt == nil || first.ResetsAt.Format(time.RFC3339Nano) != "2026-10-08T05:59:59.126253Z" {
		t.Fatalf("reset must retain provider instant: %+v", first.ResetsAt)
	}
	if second.Acct == first.Acct || second.UsedPercent == nil || *second.UsedPercent != 100 || second.Status != "full" {
		t.Fatalf("second account lost: %+v", second)
	}
}

func TestGrokQuotaShapes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		used    any
		period  string
		unified bool
		valid   bool
	}{
		{"explicit zero", 0, "USAGE_PERIOD_TYPE_WEEKLY", false, true},
		{"proto zero", nil, "USAGE_PERIOD_TYPE_WEEKLY", true, true},
		{"unconfirmed omission", nil, "USAGE_PERIOD_TYPE_WEEKLY", false, false},
		{"monthly is not weekly", 20, "USAGE_PERIOD_TYPE_MONTHLY", true, false},
		{"unknown period", 20, "", true, false},
		{"invalid type", "0", "USAGE_PERIOD_TYPE_WEEKLY", true, false},
		{"negative", -1, "USAGE_PERIOD_TYPE_WEEKLY", true, false},
		{"over 100", 101, "USAGE_PERIOD_TYPE_WEEKLY", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := grokTestEnv(t, grokTestAuth(1, "account"), grokTestBilling(1, "2026-10-03T01:00:00Z", tc.used, tc.period, tc.unified))
			got := grokBilling(env)
			if (len(got) == 1) != tc.valid {
				t.Fatalf("valid=%t got %+v", tc.valid, got)
			}
			if tc.valid && (got[0].UsedPercent == nil || *got[0].UsedPercent != 0) {
				t.Fatalf("zero must remain a real reading: %+v", got)
			}
		})
	}
}

func TestGrokAccountBoundaries(t *testing.T) {
	for _, boundary := range []struct {
		msg string
		ctx map[string]any
	}{
		{"auth started", map[string]any{"method": "grok.com"}},
		{"agent initialized", nil},
		{"auth init user_info check", map[string]any{"user_id": ""}},
	} {
		t.Run(boundary.msg, func(t *testing.T) {
			env := grokTestEnv(t, grokTestAuth(1, "old"), grokTestLog(1, "2026-10-03T00:01:00Z", boundary.msg, boundary.ctx), grokTestBilling(1, "2026-10-03T01:00:00Z", 40, "USAGE_PERIOD_TYPE_WEEKLY", true))
			if got := grokBilling(env); len(got) != 0 {
				t.Fatalf("crossed account/process boundary: %+v", got)
			}
		})
	}
	// A different process cannot borrow a neighboring process's account.
	env := grokTestEnv(t, grokTestAuth(1, "known"), grokTestBilling(2, "2026-10-03T01:00:00Z", 40, "USAGE_PERIOD_TYPE_WEEKLY", true))
	if got := grokBilling(env); len(got) != 0 {
		t.Fatalf("attributed unknown process: %+v", got)
	}
}

func TestCurrentLoginDoesNotRelabelHistoricalMeter(t *testing.T) {
	env := grokTestEnv(t, grokTestAuth(1, "old"), grokTestBilling(1, "2026-10-03T01:00:00Z", 30, "USAGE_PERIOD_TYPE_WEEKLY", true), grokTestAuth(2, "current"), grokTestBilling(2, "2026-10-03T01:00:00Z", 50, "USAGE_PERIOD_TYPE_WEEKLY", true))
	body := `{"https://auth.x.ai::test":{"user_id":"current","email":"current@example.com","expires_at":"2027-01-01T00:00:00Z"}}`
	if err := os.WriteFile(filepath.Join(env.Home, ".grok", "auth.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Collect(env)
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	for _, r := range got {
		if r.Acct == env.HashID(model.ProviderXAI, "old") && r.Label != "" {
			t.Fatalf("old account mislabeled: %+v", r)
		}
		if r.Acct == env.HashID(model.ProviderXAI, "current") && r.Label != "current@example.com" {
			t.Fatalf("current account unlabeled: %+v", r)
		}
	}
}

func TestGrokMalformedAndPartialRows(t *testing.T) {
	env := grokTestEnv(t, grokTestAuth(1, "known"), `{"invalid":`, grokTestBilling(1, "bad timestamp", 30, "USAGE_PERIOD_TYPE_WEEKLY", true), grokTestBilling(1, "2026-10-03T01:00:00Z", 20, "USAGE_PERIOD_TYPE_WEEKLY", true))
	f, err := os.OpenFile(filepath.Join(env.Home, ".grok", "logs", "unified.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(grokTestBilling(1, "2026-10-03T02:00:00Z", 90, "USAGE_PERIOD_TYPE_WEEKLY", true))
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	got := grokBilling(env)
	if len(got) != 1 || *got[0].UsedPercent != 20 {
		t.Fatalf("malformed/unfinished rows replaced reading: %+v", got)
	}
}

func TestHistoricalMeterKeepsCurrentUnmeteredAccount(t *testing.T) {
	env := grokTestEnv(t, grokTestAuth(1, "old"), grokTestBilling(1, "2026-10-03T01:00:00Z", 30, "USAGE_PERIOD_TYPE_WEEKLY", true))
	body := `{"https://auth.x.ai::test":{"user_id":"current","email":"current@example.com","expires_at":"2027-01-01T00:00:00Z"}}`
	if err := os.WriteFile(filepath.Join(env.Home, ".grok", "auth.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Collect(env)
	if len(got) != 2 {
		t.Fatalf("old meter hid current signed-in account: %+v", got)
	}
	for _, r := range got {
		if r.Acct == env.HashID(model.ProviderXAI, "current") {
			if r.Window != "plan" || r.Label != "current@example.com" || r.UsedPercent != nil {
				t.Fatalf("bad current unmetered account: %+v", r)
			}
		} else if r.Label != "" {
			t.Fatalf("old meter borrowed current label: %+v", r)
		}
	}
}

func TestGrokCachedPlanScopeIdentity(t *testing.T) {
	// Freeze the upstream NUL-separated scope hash, not a guessed user-id hash.
	h := sha256.Sum256([]byte("native-user\x00team-one\x00\x00https://auth.x.ai\x00oidc\x00\x00"))
	identity := hex.EncodeToString(h[:])
	for _, tc := range []struct {
		name, user, team, issuer, mode string
		match                          bool
	}{
		{"same scope", "native-user", "team-one", "https://auth.x.ai", "oidc", true},
		{"different user", "another-user", "team-one", "https://auth.x.ai", "oidc", false},
		{"different team", "native-user", "team-two", "https://auth.x.ai", "oidc", false},
		{"different issuer", "native-user", "team-one", "https://other.example", "oidc", false},
		{"different mode", "native-user", "team-one", "https://auth.x.ai", "web_login", false},
		{"no user identity", "", "team-one", "https://auth.x.ai", "oidc", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := grokTestEnv(t)
			entry := map[string]any{"user_id": tc.user, "team_id": tc.team, "oidc_issuer": tc.issuer, "auth_mode": tc.mode, "email": "owner@example.com", "key": "must-not-be-used", "refresh_token": "must-not-be-used"}
			auth, _ := json.Marshal(map[string]any{"scope": entry})
			payload, _ := json.Marshal(map[string]any{"fetched_at": "2026-10-03T01:00:00Z", "identity": identity, "settings": map[string]string{"subscription_tier_display": "SuperGrok Heavy"}})
			cache, _ := json.Marshal(map[string]string{"payload": string(payload)})
			if err := os.WriteFile(filepath.Join(env.Home, ".grok", "auth.json"), auth, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(env.Home, ".grok", "settings_cache.json"), cache, 0o600); err != nil {
				t.Fatal(err)
			}
			got := grokPlan(env)
			if len(got) != 1 {
				t.Fatalf("missing plan: %+v", got)
			}
			wantAcct := env.HashID(model.ProviderXAI, identity)
			if got[0].Acct != wantAcct || got[0].ID != snapshotID(model.ProviderXAI, model.SourceGrokCLI, wantAcct, "plan", "") {
				t.Fatal("legacy plan id changed")
			}
			if (got[0].Label == "owner@example.com") != tc.match {
				t.Fatalf("scope match=%t label=%q", tc.match, got[0].Label)
			}
		})
	}
}
