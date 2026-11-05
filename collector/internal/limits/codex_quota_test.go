package limits

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/accounts"
	"github.com/7-of-9/tokenmaxr/collector/internal/accountusage"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
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

	hash := func(provider, nativeID string) string { return "a_user" }
	attribute := func(provider string, ts time.Time, sessionID string, hint sources.Hint) (string, string) {
		if sessionID == "" && hint.IsZero() {
			return "a_user", model.AcctTimeline
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
	for _, raw := range []string{`{}`, `{"rateLimits":null}`, `{"accountId":"a","rateLimits":{"primary":null,"secondary":null}}`, `{"rateLimits":{"primary":{"usedPercent":1,"windowDurationMins":10080}}}`, `{"accountId":"","rateLimits":{"primary":{"usedPercent":1,"windowDurationMins":10080}}}`, `not json`} {
		if _, err := accountusage.CodexRollout(json.RawMessage(raw), time.Now()); err == nil {
			t.Errorf("CodexRollout(%s) accepted an answer without a meter", raw)
		}
	}
}

func TestCodexQuotaAfterLoginSwitchIgnoresThreadCreator(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".codex", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "rollout-active.jsonl")
	start := time.Date(2026, 11, 5, 20, 0, 0, 0, time.UTC)
	switched := start.Add(time.Hour)
	readAt := switched.Add(time.Minute)
	ivs := []store.Interval{
		{Home: home, Provider: model.ProviderOpenAI, Acct: "a_old", From: start, To: switched.Add(-time.Second)},
		{Home: home, Provider: model.ProviderOpenAI, Acct: "a_new", From: switched, To: readAt},
	}
	env := &sources.Env{Home: home, HashID: func(_, id string) string { return "a_" + id },
		Attribute: func(provider string, ts time.Time, session string, hint sources.Hint) (string, string) {
			if session != "" || !hint.IsZero() {
				t.Fatal("quota must not use thread identity")
			}
			return accounts.Attribute(ivs, home, provider, ts)
		},
	}
	write := func(at time.Time, used string) {
		t.Helper()
		body := `{"type":"session_meta","payload":{"id":"old-thread","creator_account_id":"old"}}` + "\n" +
			`{"timestamp":"` + at.Format(time.RFC3339) + `","type":"event_msg","payload":{"rate_limits":{"primary":{"used_percent":` + used + `,"window_minutes":10080,"resets_at":1794390285}}}}` + "\n"
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(start.Add(time.Minute), "25")
	if got := codexFile(env, p); len(got) != 1 || got[0].Acct != "a_old" {
		t.Fatalf("before switch: %+v", got)
	}
	history, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rollout-history.jsonl"), history, 0o600); err != nil {
		t.Fatal(err)
	}
	write(readAt, "1")
	if got := codexFile(env, p); len(got) != 1 || got[0].Acct != "a_new" || got[0].AcctQ != model.AcctTimeline {
		t.Fatalf("after switch: %+v", got)
	}
	// Recovery must retain the previous account's actual historical reading,
	// not merely suppress the poisoned current one or stamp the old one now.
	all := Collect(env)
	if len(all) != 2 {
		t.Fatalf("both accounts must survive recovery: %+v", all)
	}
	for _, row := range all {
		if row.Acct == "a_old" && (row.UsedPercent == nil || *row.UsedPercent != 25 ||
			!row.ObservedAt.Equal(start.Add(time.Minute)) || row.ResetsAt == nil || row.ResetsAt.Unix() != 1794390285) {
			t.Fatalf("historical reading changed: %+v", row)
		}
		if row.Acct == "a_new" && (row.UsedPercent == nil || *row.UsedPercent != 1 || !row.ObservedAt.Equal(readAt)) {
			t.Fatalf("current reading changed: %+v", row)
		}
	}
	ivs = nil
	if got := codexFile(env, p); len(got) != 0 {
		t.Fatalf("unknown login must not invent a quota owner: %+v", got)
	}
	ivs = []store.Interval{{Home: home, Provider: model.ProviderOpenAI, Acct: "a_new", From: start, To: start}}
	if got := codexFile(env, p); len(got) != 0 {
		t.Fatalf("inferred login must not claim a quota: %+v", got)
	}
	// An account-bound API response remains authoritative even if that
	// account is no longer the current login when the cache is scanned.
	env.CodexQuota = p
	if got := codexFile(env, p); len(got) != 1 || got[0].Acct != "a_old" || got[0].AcctQ != model.AcctRecorded {
		t.Fatalf("API identity: %+v", got)
	}
}
