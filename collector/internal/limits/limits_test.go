package limits

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
)

func TestClaudeCache(t *testing.T) {
	home := t.TempDir()
	body := `{
	  "oauthAccount": {"accountUuid": "acct-1", "organizationName": "Example Org", "organizationRateLimitTier": "default_claude_max_20x", "emailAddress": "person@example.com"},
	  "cachedUsageUtilization": {
	    "fetchedAtMs": 1790734254355,
	    "accountUuid": "acct-1",
	    "utilization": {
	      "extra_usage": {"is_enabled": false, "utilization": 0, "disabled_reason": "out_of_credits"},
	      "limits": [
	        {"kind": "session", "percent": 3, "resets_at": "2026-09-30T06:50:00Z", "is_active": false},
	        {"kind": "weekly_all", "percent": 60, "resets_at": "2026-10-06T15:00:00Z", "is_active": true},
	        {"kind": "weekly_scoped", "percent": 53, "resets_at": "2026-10-06T15:00:00Z", "is_active": false, "scope": {"model": {"display_name": "Fable"}}}
	      ]
	    }
	  }
	}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	env := &sources.Env{Home: home, HashID: func(p, id string) string {
		if id == "acct-1" && p == model.ProviderAnthropic {
			return "a_1111111111111111"
		}
		return ""
	}}
	got := Collect(env)
	if len(got) != 4 {
		t.Fatalf("snapshots %d: %+v", len(got), got)
	}
	by := map[string]model.LimitSnapshot{}
	for _, s := range got {
		by[s.Window+"/"+s.Scope] = s
	}
	week := by["week/"]
	if week.Plan != "Max (20x)" || week.Name != "Example Org" || week.Label != "person@example.com" || week.Acct != "a_1111111111111111" || week.AcctQ != model.AcctRecorded {
		t.Fatalf("week identity %+v", week)
	}
	if week.UsedPercent == nil || *week.UsedPercent != 60 || week.Status != "ok" {
		t.Fatalf("week meter %+v", week)
	}
	if week.ResetsAt == nil || !week.ResetsAt.Equal(time.Date(2026, 10, 6, 15, 0, 0, 0, time.UTC)) {
		t.Fatalf("week reset %v", week.ResetsAt)
	}
	if by["session/"].Status != "ok" || *by["session/"].UsedPercent != 3 {
		t.Fatalf("session %+v", by["session/"])
	}
	if by["week/Fable"].Status != "ok" || *by["week/Fable"].UsedPercent != 53 {
		t.Fatalf("fable %+v", by["week/Fable"])
	}
	if by["extra/"].Status != "disabled" || by["extra/"].Detail != "out of credits" {
		t.Fatalf("extra %+v", by["extra/"])
	}
	for _, s := range got {
		if s.Name == "person@example.com" || s.Detail == "person@example.com" || s.Label != "person@example.com" {
			t.Fatalf("email belongs on label only: %+v", s)
		}
	}
}

func TestCodexTailAndDedupe(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".codex", "sessions", "2026", "09", "19")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := `{"timestamp":"2026-09-01T00:00:00Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"plan_type":"pro","primary":{"used_percent":90,"window_minutes":10080,"resets_at":1788300000}}}}` + "\n"
	meta := `{"timestamp":"2026-09-19T00:00:00Z","type":"session_meta","payload":{"id":"11111111-2222-3333-4444-555555555555","cwd":"/work"}}` + "\n"
	pad := stringsRepeat(300)
	newest := `{"timestamp":"2026-09-19T19:10:05Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"plan_type":"pro","limit_name":null,"primary":{"used_percent":15,"window_minutes":10080,"resets_at":1790318597},"secondary":null}}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-09-19T19-10-05-11111111-2222-3333-4444-555555555555.jsonl"), []byte(meta+old+pad+newest), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := tailBytes
	tailBytes = 500
	t.Cleanup(func() { tailBytes = prev })
	env := &sources.Env{Home: home, Attribute: func(provider string, ts time.Time, sessionID string, hint sources.Hint) (string, string) {
		if sessionID == "11111111-2222-3333-4444-555555555555" {
			return "a_2222222222222222", model.AcctInferred
		}
		return "", model.AcctUnknown
	}}
	got := Collect(env)
	if len(got) != 1 {
		t.Fatalf("snapshots %d: %+v", len(got), got)
	}
	s := got[0]
	if s.Window != "week" || s.Plan != "Pro" || s.Acct != "a_2222222222222222" || s.UsedPercent == nil || *s.UsedPercent != 15 {
		t.Fatalf("codex %+v", s)
	}
	if !s.ObservedAt.Equal(time.Date(2026, 9, 19, 19, 10, 5, 0, time.UTC)) {
		t.Fatalf("observed %s", s.ObservedAt)
	}
	older := s
	older.ObservedAt = s.ObservedAt.Add(-time.Hour)
	older.UsedPercent = pct(99)
	if out := Dedupe([]model.LimitSnapshot{older, s}); len(out) != 1 || *out[0].UsedPercent != 15 {
		t.Fatalf("dedupe kept %+v", out)
	}
}

func TestGrokPlanOnly(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".grok"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"payload":"{\"fetched_at\":\"2026-10-01T01:47:15Z\",\"identity\":\"abc\",\"settings\":{\"subscription_tier_display\":\"SuperGrok Heavy\"}}"}`
	if err := os.WriteFile(filepath.Join(home, ".grok", "settings_cache.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Collect(&sources.Env{Home: home, HashID: func(string, string) string { return "a_3333333333333333" }})
	if len(got) != 1 || got[0].Plan != "SuperGrok Heavy" || got[0].Window != "plan" || got[0].UsedPercent != nil || got[0].ResetsAt != nil {
		t.Fatalf("%+v", got)
	}
	if got[0].Acct != "a_3333333333333333" || got[0].AcctQ != model.AcctRecorded {
		t.Fatalf("acct %+v", got[0])
	}
}

func TestCollectThisHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip()
	}
	env := &sources.Env{
		Home: home,
		HashID: func(provider, id string) string {
			return model.AccountHash([]byte("limit-summary-only"), provider, id)
		},
	}
	got := Collect(env)
	if len(got) == 0 {
		t.Skip("no AI tool has written a usage meter on this machine (CI)")
	}
	for _, s := range got {
		pct := "—"
		if s.UsedPercent != nil {
			pct = fmt.Sprintf("%.4g%%", *s.UsedPercent)
		}
		reset := "—"
		if s.ResetsAt != nil {
			reset = s.ResetsAt.Format(time.RFC3339)
		}
		t.Logf("%s plan=%q name=%q window=%s scope=%q pct=%s status=%s detail=%q reset=%s observed=%s acctQ=%s",
			s.Source, s.Plan, s.Name, s.Window, s.Scope, pct, s.Status, s.Detail, reset, s.ObservedAt.Format(time.RFC3339), s.AcctQ)
	}
}

func stringsRepeat(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b) + "\n"
}

func TestFingerprintIgnoresObservedAt(t *testing.T) {
	used := 40.0
	a := model.LimitSnapshot{ID: "x", Provider: "anthropic", Window: "week", UsedPercent: &used, ObservedAt: time.Now()}
	b := a
	b.ObservedAt = a.ObservedAt.Add(time.Hour)
	if Fingerprint(a) != Fingerprint(b) {
		t.Fatal("a re-read of the same meter must not be sent again")
	}
	more := 41.0
	b.UsedPercent = &more
	if Fingerprint(a) == Fingerprint(b) {
		t.Fatal("a moved meter must be sent")
	}
}

// A Team or Enterprise seat's tier code names no plan; the organisation's
// type does (owner report 2026-10-09: "Plan unavailable" on a Team account).
func TestClaudePlanFromOrgType(t *testing.T) {
	for _, c := range []struct{ tier, orgType, seat, want string }{
		{"default_claude_max_20x", "claude_max", "", "Max (20x)"},
		{"default_raven", "claude_team", "team_premium", "Team (Premium)"},
		{"default_raven", "claude_team", "standard", "Team (Standard)"},
		{"default_raven", "claude_team", "", "Team"},
		{"default_raven", "claude_enterprise", "", "Enterprise"},
		{"default_raven", "", "", ""},
	} {
		got := claudePlan(c.tier)
		if got == "" {
			got = claudeOrgPlan(c.orgType, c.seat)
		}
		if got != c.want {
			t.Errorf("%s/%s/%s: %q, want %q", c.tier, c.orgType, c.seat, got, c.want)
		}
	}
}
