package limits

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/accountusage"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
)

// A reading fetched through Codex's app server must land on the same meter
// (same snapshot id) as Codex's own rollout reading of that account, and
// replace it when newer, so the page never shows the account twice.
func TestCodexQuotaFileMatchesRolloutMeter(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".codex", "sessions", "2026", "10", "03")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Codex's own rollout, 20 hours before the refresh: 47% used.
	rollout := `{"timestamp":"2026-10-03T10:00:00Z","type":"session_meta","payload":{"id":"sess-1","creator_account_id":"user-acct"}}` + "\n" +
		`{"timestamp":"2026-10-03T10:00:05Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"limit_id":"codex","limit_name":null,"plan_type":"pro","primary":{"used_percent":47,"window_minutes":10080,"resets_at":1791590602},"secondary":null}}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-10-03T10-00-00-sess-1.jsonl"), []byte(rollout), 0o644); err != nil {
		t.Fatal(err)
	}
	// The app server's answer now: 25% used, same account.
	refreshed := time.Date(2026, 10, 4, 6, 0, 0, 0, time.UTC)
	raw := json.RawMessage(`{"accountId":"user-acct","rateLimits":{"limitId":"codex","limitName":null,"planType":"pro",` +
		`"primary":{"usedPercent":25,"windowDurationMins":10080,"resetsAt":1791590602},"secondary":null}}`)
	body, err := accountusage.CodexRollout(raw, refreshed)
	if err != nil {
		t.Fatal(err)
	}
	quota := filepath.Join(t.TempDir(), "codex-rate-limits.jsonl")
	if err := os.WriteFile(quota, body, 0o600); err != nil {
		t.Fatal(err)
	}

	hash := func(provider, nativeID string) string { return "h_" + nativeID }
	attribute := func(provider string, ts time.Time, sessionID string, hint sources.Hint) (string, string) {
		if hint.ID == "h_user-acct" {
			return "a_user", model.AcctRecorded
		}
		return "", model.AcctUnknown
	}
	rolloutOnly := Collect(&sources.Env{Home: home, HashID: hash, Attribute: attribute})
	both := Collect(&sources.Env{Home: home, HashID: hash, Attribute: attribute, CodexQuota: quota})

	var oldMeter, newMeter *model.LimitSnapshot
	for i := range rolloutOnly {
		if rolloutOnly[i].Provider == model.ProviderOpenAI && rolloutOnly[i].Window == "week" {
			oldMeter = &rolloutOnly[i]
		}
	}
	weeks := 0
	for i := range both {
		if both[i].Provider == model.ProviderOpenAI && both[i].Window == "week" {
			weeks++
			newMeter = &both[i]
		}
	}
	if oldMeter == nil || newMeter == nil {
		t.Fatalf("meters missing: rollout %+v, both %+v", rolloutOnly, both)
	}
	if weeks != 1 {
		t.Fatalf("the refreshed reading duplicated the meter (%d weekly rows)", weeks)
	}
	if newMeter.ID != oldMeter.ID || newMeter.Acct != "a_user" {
		t.Fatalf("refreshed meter id/acct %s/%s, rollout %s/%s", newMeter.ID, newMeter.Acct, oldMeter.ID, oldMeter.Acct)
	}
	if !newMeter.ObservedAt.Equal(refreshed) || *newMeter.UsedPercent != 25 || newMeter.Plan != oldMeter.Plan {
		t.Fatalf("refreshed meter %+v", *newMeter)
	}
}

func TestCodexRolloutRejectsEmptyAnswers(t *testing.T) {
	for _, raw := range []string{`{}`, `{"rateLimits":null}`, `{"rateLimits":{"primary":null,"secondary":null}}`, `not json`} {
		if _, err := accountusage.CodexRollout(json.RawMessage(raw), time.Now()); err == nil {
			t.Errorf("CodexRollout(%s) accepted an answer without a meter", raw)
		}
	}
}
