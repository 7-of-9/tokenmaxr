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

// claudeHome writes a ~/.claude.json for a login (account uuid, org uuid,
// org type, tier) with a usage cache of cacheAcct fetched at fetchedMs.
func claudeHome(t *testing.T, acct, org, orgType, tier, cacheAcct string, fetchedMs int64) string {
	t.Helper()
	home := t.TempDir()
	body := fmt.Sprintf(`{"oauthAccount": {"accountUuid": %q, "emailAddress": "dm@example.com", "organizationUuid": %q,
	  "organizationName": "Org %s", "organizationType": %q, "organizationRateLimitTier": %q},
	  "cachedUsageUtilization": {"fetchedAtMs": %d, "accountUuid": %q, "utilization": {"limits": [
	    {"kind": "session", "percent": 40, "resets_at": "2026-10-09T15:00:00Z"},
	    {"kind": "weekly_all", "percent": 97, "resets_at": "2026-10-13T15:00:00Z"}]}}}`,
		acct, org, org, orgType, tier, fetchedMs, cacheAcct)
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

func hashID(p, id string) string { return model.AccountHash([]byte("org-test"), p, id) }

func hashEnv(home string) *sources.Env { return &sources.Env{Home: home, HashID: hashID} }

// byWindow splits snapshots into meters by window and the plan row.
func byWindow(t *testing.T, got []model.LimitSnapshot) (map[string]model.LimitSnapshot, *model.LimitSnapshot) {
	t.Helper()
	meters := map[string]model.LimitSnapshot{}
	var plan *model.LimitSnapshot
	for i, s := range got {
		if s.Window == "plan" {
			plan = &got[i]
			continue
		}
		meters[s.Window] = s
	}
	return meters, plan
}

// One login (the same accountUuid) in a Team organisation and in its
// personal organisation is two accounts: separate ids, each with its own
// plan, name and organisation, and each with a plan row so it stays listed
// while the other is signed in (owner report 2026-10-09).
func TestClaudeTeamAndPersonalOrgAreSeparate(t *testing.T) {
	const fetched = 1791500000000
	team := Collect(hashEnv(claudeHome(t, "user-1", "org-team", "claude_team", "default_raven", "user-1", fetched)))
	personal := Collect(hashEnv(claudeHome(t, "user-1", "org-me", "claude_max", "default_claude_max_20x", "user-1", fetched)))
	if len(team) != 3 || len(personal) != 3 {
		t.Fatalf("team %+v personal %+v", team, personal)
	}
	ids := map[string]bool{}
	for _, s := range append(append([]model.LimitSnapshot{}, team...), personal...) {
		if ids[s.ID] {
			t.Fatalf("id %s shared between organisations", s.ID)
		}
		ids[s.ID] = true
		if s.Label != "dm@example.com" || s.Org == "" || s.Acct != team[0].Acct {
			t.Fatalf("identity %+v", s)
		}
	}
	tm, tp := byWindow(t, team)
	pm, pp := byWindow(t, personal)
	if tm["week"].Plan != "Team" || tm["week"].OrgKind != "team" || tm["week"].Name != "Org org-team" {
		t.Fatalf("team %+v", tm["week"])
	}
	if pm["week"].Plan != "Max (20x)" || pm["week"].OrgKind != "personal" || pm["week"].Org == tm["week"].Org {
		t.Fatalf("personal %+v", pm["week"])
	}
	if tp == nil || tp.Plan != "Team" || tp.Org != tm["week"].Org || pp == nil || pp.Plan != "Max (20x)" || pp.OrgKind != "personal" {
		t.Fatalf("plan rows %+v %+v", tp, pp)
	}
	// Without an organisation id a meter keeps its pre-organisation id, and
	// a login with a meter gets no plan row, as before.
	legacy := Collect(hashEnv(claudeHome(t, "user-1", "", "", "default_claude_max_20x", "user-1", fetched)))
	if len(legacy) != 2 {
		t.Fatalf("legacy %+v", legacy)
	}
	l := legacy[0]
	if l.Org != "" || l.OrgKind != "" || l.ID != snapshotID(model.ProviderAnthropic, model.SourceClaudeCode, l.Acct, l.Window, "") {
		t.Fatalf("legacy %+v", l)
	}
	// A Pro-to-Max upgrade stays the same personal account.
	if claudeOrgKind("claude_pro") != claudeOrgKind("claude_max") || claudeOrgKind("claude_enterprise") != "enterprise" || claudeOrgKind("") != "" {
		t.Fatal("org kinds")
	}
	if got := ClaudeOrg(claudeHome(t, "user-1", "org-me", "claude_max", "", "user-1", fetched), hashID); got != pm["week"].Org {
		t.Fatalf("ClaudeOrg %q, want %q", got, pm["week"].Org)
	}
	if ClaudeOrg(claudeHome(t, "user-1", "", "", "", "user-1", fetched), hashID) != "" || ClaudeOrg(t.TempDir(), hashID) != "" {
		t.Fatal("ClaudeOrg without an organisation")
	}
}

// A cache still holding the previous account's meters gets neither the
// login's plan, name nor organisation; the login gets a plan row with them.
func TestClaudeCacheOfAnotherAccount(t *testing.T) {
	got := Collect(hashEnv(claudeHome(t, "user-2", "org-me", "claude_max", "default_claude_max_20x", "user-1", 1791500000000)))
	meters, plan := byWindow(t, got)
	for _, s := range meters {
		if s.Plan != "" || s.Name != "" || s.Org != "" || s.OrgKind != "" || s.Label != "" {
			t.Fatalf("the other account's meter carries the login's identity: %+v", s)
		}
	}
	if plan == nil || plan.Plan != "Max (20x)" || plan.Name != "Org org-me" || plan.Org == "" || plan.OrgKind != "personal" || plan.Label != "dm@example.com" {
		t.Fatalf("plan row %+v", plan)
	}
}

// After the login moves to another organisation, a usage cache read before
// the move is the other organisation's: it is left out until read again. A
// reading fetched after the switch was noted (the collector notes it before
// asking Claude Code for a fresh one) counts at once.
func TestClaudeCacheOlderThanAnOrgSwitch(t *testing.T) {
	type seen struct {
		org   string
		since time.Time
	}
	mem := map[string]seen{}
	clock := time.UnixMilli(1791500000000).UTC()
	env := func(home string) *sources.Env {
		e := hashEnv(home)
		e.OrgSince = func(provider, org string) time.Time {
			prev, ok := mem[provider]
			if ok && prev.org == org {
				return prev.since
			}
			s := seen{org: org}
			if ok {
				s.since = clock
			}
			mem[provider] = s
			return s.since
		}
		return e
	}
	fetched := clock.Add(-time.Hour).UnixMilli()
	if m, _ := byWindow(t, Collect(env(claudeHome(t, "user-1", "org-team", "claude_team", "default_raven", "user-1", fetched)))); len(m) != 2 {
		t.Fatalf("first org: %+v", m)
	}
	// Switched to the personal organisation; the cache is still the Team's.
	got := Collect(env(claudeHome(t, "user-1", "org-me", "claude_max", "default_claude_max_20x", "user-1", fetched)))
	if len(got) != 1 || got[0].Window != "plan" || got[0].Plan != "Max (20x)" || got[0].OrgKind != "personal" {
		t.Fatalf("stale cache after the switch: %+v", got)
	}
	// Claude Code read it again for the personal organisation.
	got = Collect(env(claudeHome(t, "user-1", "org-me", "claude_max", "default_claude_max_20x", "user-1", clock.Add(time.Minute).UnixMilli())))
	if m, _ := byWindow(t, got); len(m) != 2 || m["week"].OrgKind != "personal" {
		t.Fatalf("refetched cache: %+v", got)
	}
}

// owner.json keeps an organisation that is no longer signed in: its last
// rows, with their own reading times, until 14 days past their reset.
func TestRetainOrgMeters(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	snap := func(id, org, window string, resets *time.Time) model.LimitSnapshot {
		return model.LimitSnapshot{ID: id, Provider: model.ProviderAnthropic, Acct: "a_1", Org: org, Window: window, ResetsAt: resets, ObservedAt: now.Add(-time.Hour)}
	}
	team := []model.LimitSnapshot{snap("t-week", "o_team", "week", at(48*time.Hour)), snap("t-plan", "o_team", "plan", nil)}
	legacy := snap("legacy", "", "week", at(time.Hour))
	out, kept := RetainOrgMeters(nil, append(append([]model.LimitSnapshot{}, team...), legacy), now)
	if len(out) != 3 || len(kept) != 2 {
		t.Fatalf("first: %d out, %d kept", len(out), len(kept))
	}
	// Signed into the personal organisation now: the Team's last rows stay.
	personal := []model.LimitSnapshot{snap("p-week", "o_me", "week", at(72*time.Hour))}
	out, kept = RetainOrgMeters(kept, personal, now.Add(time.Hour))
	ids := map[string]model.LimitSnapshot{}
	for _, s := range out {
		ids[s.ID] = s
	}
	if len(out) != 3 || ids["t-week"].ObservedAt != team[0].ObservedAt || ids["t-plan"].ID == "" || len(kept) != 3 {
		t.Fatalf("after the switch: %+v", out)
	}
	// 14 days past the Team week's reset it goes; the plan row stays.
	out, kept = RetainOrgMeters(kept, personal, now.Add(48*time.Hour+RetainFor+time.Minute))
	ids = map[string]model.LimitSnapshot{}
	for _, s := range out {
		ids[s.ID] = s
	}
	if _, ok := ids["t-week"]; ok || ids["t-plan"].ID == "" || len(out) != 2 || len(kept) != 2 {
		t.Fatalf("expired: %+v kept %v", out, kept)
	}
	// Signed into the Team again: only the fresh rows are published for it.
	fresh := snap("t-week", "o_team", "week", at(200*time.Hour))
	fresh.ObservedAt = now.Add(100 * time.Hour)
	out, _ = RetainOrgMeters(kept, []model.LimitSnapshot{fresh}, now.Add(100*time.Hour))
	if len(out) != 2 || out[0].ObservedAt != fresh.ObservedAt {
		t.Fatalf("back on the Team: %+v", out)
	}
}

// An older Codex build gives the reset in seconds from the reading.
func TestCodexResetsInSeconds(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".codex", "sessions", "2026", "09", "19")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"timestamp":"2026-09-19T19:10:05Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":100,"window_minutes":43200,"resets_in_seconds":3600}}}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-09-19T19-10-05-x.jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Collect(&sources.Env{Home: home})
	if len(got) != 1 || got[0].ResetsAt == nil || !got[0].ResetsAt.Equal(time.Date(2026, 9, 19, 20, 10, 5, 0, time.UTC)) {
		t.Fatalf("%+v", got)
	}
}
