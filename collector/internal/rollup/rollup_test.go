package rollup

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
)

var now = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

func usage(id string, ts time.Time, in, out int64) model.UsageEvent {
	return model.UsageEvent{ID: id, Provider: model.ProviderAnthropic, Source: model.SourceClaudeCode, TS: ts, Model: "claude-x", Acct: "a_1",
		Tokens: model.Tokens{In: in, Out: out}}
}

func total(r *Rollup, date string) (tok, events, prompts int64) {
	for _, c := range r.Days[date] {
		tok += c.Tokens()
		events += c.Events
		prompts += c.Prompts
	}
	return
}

func TestReReadsNeverDoubleCount(t *testing.T) {
	r := New()
	old := now.Add(-72 * time.Hour)
	for range 3 { // three ticks / full reparses see the same events
		r.AddUsage(usage("aa01", old, 100, 10), now)
		r.AddUsage(usage("aa02", old, 50, 5), now)
	}
	if tok, ev, _ := total(r, "2026-10-01"); tok != 165 || ev != 2 {
		t.Fatalf("tokens %d events %d, want 165 and 2", tok, ev)
	}
}

func TestLiveEventAddsOnlyItsGrowth(t *testing.T) {
	r := New()
	ts := now.Add(-10 * time.Minute)
	r.AddUsage(usage("bb01", ts, 100, 10), now)
	r.AddUsage(usage("bb01", ts, 100, 40), now) // the message streamed on
	r.AddUsage(usage("bb01", ts, 90, 40), now)  // a smaller re-read never shrinks it
	if tok, ev, _ := total(r, "2026-10-04"); tok != 140 || ev != 1 {
		t.Fatalf("tokens %d events %d, want 140 and 1", tok, ev)
	}
	// After LiveWindow it is retired to the id set and still counted once.
	later := now.Add(3 * time.Hour)
	r.Settle(later, false)
	r.AddUsage(usage("bb01", ts, 100, 40), later)
	if tok, _, _ := total(r, "2026-10-04"); tok != 140 {
		t.Fatalf("retired live id counted again: %d", tok)
	}
}

func TestPromptsCountOnce(t *testing.T) {
	r := New()
	a := model.ActivityEvent{ID: "cc01", Provider: model.ProviderAnthropic, Source: model.SourceClaudeCode, TS: now.Add(-time.Hour), Acct: "a_1"}
	r.AddActivity(a)
	r.AddActivity(a)
	if _, _, p := total(r, "2026-10-04"); p != 1 {
		t.Fatalf("prompts %d, want 1", p)
	}
}

func TestSealingWaitsForACompleteScanThenIgnoresReReads(t *testing.T) {
	r := New()
	old := now.Add(-20 * 24 * time.Hour) // 2026-09-14
	r.AddUsage(usage("dd01", old, 100, 0), now)
	r.Settle(now, false)
	if r.Sealed != "" {
		t.Fatal("sealed while the scan was still incomplete")
	}
	r.AddUsage(usage("dd02", old, 7, 0), now) // backfill delivers more of that day
	r.Settle(now, true)
	if r.Sealed != "2026-09-27" {
		t.Fatalf("sealed %q, want 2026-09-27", r.Sealed)
	}
	if len(r.seen["2026-09-14"]) != 0 {
		t.Fatal("sealed dates must drop their id sets")
	}
	r.AddUsage(usage("dd01", old, 100, 0), now) // the weekly full reparse
	r.AddUsage(usage("dd03", old, 999, 0), now)
	if tok, _, _ := total(r, "2026-09-14"); tok != 107 {
		t.Fatalf("sealed date changed: %d, want 107", tok)
	}
	recentDay := now.Add(-48 * time.Hour)
	r.AddUsage(usage("ee01", recentDay, 5, 0), now)
	r.AddUsage(usage("ee01", recentDay, 5, 0), now)
	if tok, _, _ := total(r, "2026-10-02"); tok != 5 {
		t.Fatalf("unsealed date double counted: %d", tok)
	}
}

func TestSaveLoadRoundTripKeepsIdempotency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollup.json")
	r := New()
	r.AddUsage(usage("ff01", now.Add(-72*time.Hour), 10, 1), now)
	r.AddUsage(usage("ff02", now.Add(-5*time.Minute), 20, 2), now) // live
	r.AddActivity(model.ActivityEvent{ID: "ff03", Provider: model.ProviderOpenAI, Source: model.SourceCodex, TS: now.Add(-time.Hour)})
	if !r.Dirty() {
		t.Fatal("changes must mark the rollup dirty")
	}
	if err := r.Save(path); err != nil {
		t.Fatal(err)
	}
	if r.Dirty() {
		t.Fatal("Save must clear dirty")
	}
	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	back.AddUsage(usage("ff01", now.Add(-72*time.Hour), 10, 1), now)
	back.AddUsage(usage("ff02", now.Add(-5*time.Minute), 20, 9), now) // live growth survives the reload
	back.AddActivity(model.ActivityEvent{ID: "ff03", Provider: model.ProviderOpenAI, Source: model.SourceCodex, TS: now.Add(-time.Hour)})
	if tok, _, _ := total(back, "2026-10-01"); tok != 11 {
		t.Fatalf("reloaded old day %d, want 11", tok)
	}
	if tok, ev, p := total(back, "2026-10-04"); tok != 29 || ev != 1 || p != 1 {
		t.Fatalf("reloaded today tokens %d events %d prompts %d, want 29 1 1", tok, ev, p)
	}
	rows := back.Rows()
	if len(rows) != 3 || rows[0].Date != "2026-10-01" || rows[0].Provider != model.ProviderAnthropic || rows[0].Tokens() != 11 {
		t.Fatalf("rows %+v", rows)
	}
}

func TestLoadMissingAndBadVersion(t *testing.T) {
	dir := t.TempDir()
	r, err := Load(filepath.Join(dir, "absent.json"))
	if err != nil || len(r.Days) != 0 {
		t.Fatalf("missing file: %v %+v", err, r)
	}
	bad := filepath.Join(dir, "v9.json")
	os.WriteFile(bad, []byte(`{"v":9,"days":{}}`), 0o600)
	if _, err := Load(bad); err != ErrVersion {
		t.Fatalf("bad version: %v", err)
	}
}

func TestLocalDateUsesTheEventsOffset(t *testing.T) {
	r := New()
	e := usage("gg01", time.Date(2026, 9, 30, 20, 0, 0, 0, time.UTC), 1, 0)
	e.TZOffsetMin = 7 * 60 // 03:00 on 1 Oct in UTC+7
	r.AddUsage(e, now)
	if _, ok := r.Days["2026-10-01"]; !ok {
		t.Fatalf("days %v", r.Days)
	}
}
