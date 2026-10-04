package rollup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestNewTokenFieldsAndQualityKeys(t *testing.T) {
	r := New()
	ts := now.Add(-10 * time.Minute)
	e := usage("hh01", ts, 100, 10)
	e.CacheW, e.CacheW1h, e.Reasoning, e.Q = 40, 30, 4, model.QualityExact
	r.AddUsage(e, now)
	e.CacheW1h, e.Reasoning = 35, 9 // the live event grows in every field
	r.AddUsage(e, now)
	est := usage("hh02", ts, 7, 0)
	est.Q = model.QualityEstimated
	r.AddUsage(est, now)
	other := usage("hh03", ts, 1, 0)
	other.Q = "something-else" // only "estimated" is estimated, as on the server
	r.AddUsage(other, now)

	exact := r.Days["2026-10-04"][Key{model.ProviderAnthropic, model.SourceClaudeCode, "claude-x", "a_1", ""}.String()]
	if exact == nil || exact.In != 101 || exact.CacheW != 40 || exact.CacheW1h != 35 || exact.Reasoning != 9 || exact.Out != 10 || exact.Events != 2 {
		t.Fatalf("exact cell %+v", exact)
	}
	if exact.Tokens() != 151 {
		t.Fatalf("cacheW1h and reasoning are subsets and never add to the total: %d", exact.Tokens())
	}
	e2 := r.Days["2026-10-04"][Key{model.ProviderAnthropic, model.SourceClaudeCode, "claude-x", "a_1", QEstimated}.String()]
	if e2 == nil || e2.In != 7 || e2.Events != 1 {
		t.Fatalf("estimated cell %+v", e2)
	}
	if got := r.LastEvent(); !got.Equal(ts) {
		t.Fatalf("last event %v, want %v", got, ts)
	}
}

func activity(id string, ts time.Time, hasUsage bool) model.ActivityEvent {
	return model.ActivityEvent{ID: id, Provider: model.ProviderAnthropic, Source: model.SourceClaudeCode, TS: ts, Acct: "a_1", HasUsage: hasUsage}
}

func noUsage(r *Rollup, date string) (prompts, untracked int64) {
	for _, c := range r.Days[date] {
		prompts += c.Prompts
		untracked += c.PromptsNoUsage
	}
	return
}

func TestPromptsNoUsageMergesHasUsageByOR(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollup.json")
	r := New()
	ts := now.Add(-time.Hour)
	r.AddActivity(activity("ii01", ts, false)) // seen before its answer was written
	r.AddActivity(activity("ii02", ts, true))
	r.AddActivity(activity("ii03", ts, false)) // never answered
	r.AddActivity(activity("ii02", ts, false)) // a later sighting without usage never undoes one with it
	if p, u := noUsage(r, "2026-10-04"); p != 3 || u != 2 {
		t.Fatalf("prompts %d untracked %d, want 3 and 2", p, u)
	}
	// Reloaded, a later sighting of ii01 with usage takes it out of the
	// untracked count; a repeat changes nothing more.
	r.Save(path)
	r, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	r.AddActivity(activity("ii01", ts, true))
	r.AddActivity(activity("ii01", ts, true))
	r.AddActivity(activity("ii01", ts, false))
	if p, u := noUsage(r, "2026-10-04"); p != 3 || u != 1 {
		t.Fatalf("after usage: prompts %d untracked %d, want 3 and 1", p, u)
	}

	// Once its date is sealed, an untracked prompt's count is final.
	old := now.Add(-20 * 24 * time.Hour)
	r.AddActivity(activity("ii04", old, false))
	r.Settle(now, true)
	r.AddActivity(activity("ii04", old, true))
	if p, u := noUsage(r, "2026-09-14"); p != 1 || u != 1 {
		t.Fatalf("sealed date: prompts %d untracked %d, want 1 and 1", p, u)
	}
	if len(r.noUsage["2026-09-14"]) != 0 {
		t.Fatal("sealed dates must drop their revisable ids")
	}
}

func prompt(id string, ts time.Time, modelName, text string) model.PromptRecord {
	return model.PromptRecord{ID: id, Provider: model.ProviderAnthropic, Source: model.SourceClaudeCode, TS: ts, Model: modelName, Acct: "a_1",
		Workspace: "C:/secret/project", Text: text}
}

func modelPrompts(r *Rollup, date string) map[string]int64 {
	out := map[string]int64{}
	for ks, c := range r.Days[date] {
		if k, _ := parseKey(ks); c.ModelPrompts != 0 {
			out[k.Model] += c.ModelPrompts
		}
	}
	return out
}

func TestAddPromptCountsEachIdOncePerModelAndNeverItsText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollup.json")
	r := New()
	ts := now.Add(-time.Hour)
	const secret = "PRIVATE PROMPT TEXT 7c1e"
	r.AddPrompt(prompt("jj01", ts, "claude-x", secret))
	r.AddPrompt(prompt("jj01", ts, "claude-x", secret)) // a re-read
	r.AddPrompt(prompt("jj02", ts, "", secret))         // the answer (and model) not read yet
	r.AddPrompt(prompt("jj03", ts, "", secret))         // never learns its model
	if got := modelPrompts(r, "2026-10-04"); got["claude-x"] != 1 || got[""] != 2 || len(got) != 2 {
		t.Fatalf("model prompts %v", got)
	}
	if err := r.Save(path); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	for _, private := range []string{secret, "PRIVATE", "secret/project"} {
		if strings.Contains(string(b), private) {
			t.Fatalf("the rollup file holds %q", private)
		}
	}
	r, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	r.AddPrompt(prompt("jj02", ts, "claude-y", secret)) // the model arrives: the count moves to it
	r.AddPrompt(prompt("jj02", ts, "claude-y", secret))
	r.AddPrompt(prompt("jj01", ts, "claude-z", secret)) // a known model never moves
	if got := modelPrompts(r, "2026-10-04"); got["claude-x"] != 1 || got["claude-y"] != 1 || got[""] != 1 || len(got) != 3 {
		t.Fatalf("after the model arrived %v", got)
	}
	if !r.LastEvent().IsZero() {
		t.Fatal("prompt records are not events for lastEventAt (activity is)")
	}
	rows := r.Rows()
	for _, row := range rows {
		if row.Q != "" {
			t.Fatalf("prompt rows are keyed with q \"\": %+v", row)
		}
	}
}

// A version 1 file (6-column cells, 4-part keys) does not load: the caller
// rebuilds the rollup from all history so every new column is filled.
func TestVersion1FileRequiresARebuild(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rollup.json")
	os.WriteFile(p, []byte(`{"v":1,"days":{"2026-10-01":{"anthropic|claude-code|claude-x|a_1":[1,2,3,4,5,6]}}}`), 0o600)
	if _, err := Load(p); err != ErrVersion {
		t.Fatalf("v1 file: %v, want ErrVersion", err)
	}
}

func TestCellJSONRoundTripsEveryField(t *testing.T) {
	c := Cell{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	b, _ := c.MarshalJSON()
	if string(b) != "[1,2,3,4,5,6,7,8,9,10]" {
		t.Fatalf("cell json %s", b)
	}
	var back Cell
	if err := back.UnmarshalJSON(b); err != nil || back != c {
		t.Fatalf("round trip %+v %v", back, err)
	}
}

// A version 2 file has cells but no account ledger: it is rebuilt too, since a
// ledger missing that history would let account totals count it again.
func TestVersion2FileRequiresARebuildForTheLedger(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rollup.json")
	os.WriteFile(p, []byte(`{"v":2,"days":{"2026-10-01":{"openai|codex|gpt-5|a_1|":[1,2,0,4,5,0,1,0,0,0]}}}`), 0o600)
	if _, err := Load(p); err != ErrVersion {
		t.Fatalf("v2 file: %v, want ErrVersion", err)
	}
}

func codexUsage(id string, ts time.Time, offMin int, acctQ string, in, cacheW, cacheR, out int64) model.UsageEvent {
	return model.UsageEvent{ID: id, Provider: model.ProviderOpenAI, Source: model.SourceCodex, TS: ts, TZOffsetMin: offMin, Model: "gpt-5",
		Acct: "a_1", AcctQ: acctQ, Tokens: model.Tokens{In: in, CacheW: cacheW, CacheW1h: 1, CacheR: cacheR, Out: out, Reasoning: 2}}
}

func ledger(r *Rollup) map[string]int64 {
	out := map[string]int64{}
	for _, l := range r.LedgerRows() {
		out[l.Date+"|"+l.Provider+"|"+l.Source+"|"+l.Acct] = l.Tokens
	}
	return out
}

// The ledger mirrors api/src/lib/account-usage.js accountLedger: the UTC day
// (never the local date), in + cacheW + cacheR + out, and the account only for
// labelled attribution.
func TestLedgerSumsUTCDaysWithLabelledAccountsOnly(t *testing.T) {
	r := New()
	late := time.Date(2026, 9, 30, 23, 30, 0, 0, time.UTC) // 2026-10-01 locally at +60
	r.AddUsage(codexUsage("cc01", late, 60, model.AcctRecorded, 100, 10, 1000, 20), now)
	r.AddUsage(codexUsage("cc02", late, 60, model.AcctBounded, 1, 0, 0, 1), now)
	r.AddUsage(codexUsage("cc03", late, 60, model.AcctInferred, 5, 0, 0, 5), now)
	r.AddUsage(codexUsage("cc04", late, 60, model.AcctLineage, 7, 0, 0, 0), now)
	for range 2 { // re-reads add nothing
		r.AddUsage(codexUsage("cc01", late, 60, model.AcctRecorded, 100, 10, 1000, 20), now)
	}
	want := map[string]int64{"2026-09-30|openai|codex|a_1": 1132, "2026-09-30|openai|codex|": 17}
	if got := ledger(r); len(got) != len(want) || got["2026-09-30|openai|codex|a_1"] != 1132 || got["2026-09-30|openai|codex|"] != 17 {
		t.Fatalf("ledger %v, want %v", got, want)
	}
	if _, ok := r.Days["2026-10-01"]; !ok {
		t.Fatal("the cells must stay on the local date")
	}
	// The ledger counts exactly what the cells count.
	if tok, _, _ := total(r, "2026-10-01"); tok != 1132+17 {
		t.Fatalf("cells %d", tok)
	}
}

func TestLedgerFollowsALiveEventsGrowth(t *testing.T) {
	r := New()
	ts := now.Add(-10 * time.Minute)
	r.AddUsage(codexUsage("dd01", ts, 0, model.AcctRecorded, 100, 0, 0, 10), now)
	r.AddUsage(codexUsage("dd01", ts, 0, model.AcctRecorded, 100, 0, 50, 40), now)
	r.AddUsage(codexUsage("dd01", ts, 0, model.AcctRecorded, 90, 0, 50, 40), now) // never shrinks
	if got := ledger(r)["2026-10-04|openai|codex|a_1"]; got != 190 {
		t.Fatalf("ledger %d, want 190", got)
	}
	// Across a save and load, the live event keeps growing the same entry.
	p := filepath.Join(t.TempDir(), "rollup.json")
	if err := r.Save(p); err != nil {
		t.Fatal(err)
	}
	r, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	r.AddUsage(codexUsage("dd01", ts, 0, model.AcctRecorded, 100, 0, 50, 45), now)
	if got := ledger(r)["2026-10-04|openai|codex|a_1"]; got != 195 {
		t.Fatalf("ledger after reload %d, want 195", got)
	}
	if tok, _, _ := total(r, "2026-10-04"); tok != 195 {
		t.Fatalf("cells %d", tok)
	}
}

func snap(date string, total int64, at time.Time) model.AccountUsageSnapshot {
	return model.AccountUsageSnapshot{ID: "id-" + date, Provider: model.ProviderOpenAI, Source: model.SourceCodex, Acct: "a_1",
		AcctQ: model.AcctRecorded, Date: date, Timezone: "UTC", TotalTokens: total, ObservedAt: at}
}

// The newest total per day and account wins, as in the server's
// upsertAccountUsage; re-reading an unchanged total changes nothing.
func TestAccountUsageKeepsTheNewestTotal(t *testing.T) {
	r := New()
	t0 := now.Add(-time.Hour)
	r.AddAccountUsage([]model.AccountUsageSnapshot{snap("2026-10-01", 500, t0), snap("2026-10-02", 700, t0)})
	p := filepath.Join(t.TempDir(), "rollup.json")
	if err := r.Save(p); err != nil {
		t.Fatal(err)
	}
	r.AddAccountUsage([]model.AccountUsageSnapshot{snap("2026-10-01", 500, now), snap("2026-10-02", 700, now)})
	if r.Dirty() {
		t.Fatal("an unchanged re-read made the rollup dirty")
	}
	if got := r.AccountUsage(); len(got) != 2 || !got[0].ObservedAt.Equal(t0) {
		t.Fatalf("unchanged total lost its first reading: %+v", got)
	}
	// A later correction downward replaces it; an older reading never does.
	r.AddAccountUsage([]model.AccountUsageSnapshot{snap("2026-10-02", 650, now), snap("2026-10-01", 900, t0.Add(-time.Hour))})
	// At the same time, the larger total wins.
	r.AddAccountUsage([]model.AccountUsageSnapshot{snap("2026-10-02", 640, now), snap("2026-10-02", 660, now)})
	// Malformed totals are ignored.
	bad := snap("2026-10-3", 1, now)
	noAcct := snap("2026-10-03", 1, now)
	noAcct.Acct = ""
	local := snap("2026-10-03", 1, now)
	local.Timezone = "Europe/London"
	r.AddAccountUsage([]model.AccountUsageSnapshot{bad, noAcct, local})
	got := r.AccountUsage()
	if len(got) != 2 || got[0].Date != "2026-10-01" || got[0].TotalTokens != 500 || got[1].TotalTokens != 660 || !got[1].ObservedAt.Equal(now) {
		t.Fatalf("totals %+v", got)
	}
	if err := r.Save(p); err != nil {
		t.Fatal(err)
	}
	back, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if again := back.AccountUsage(); len(again) != 2 || again[1] != got[1] {
		t.Fatalf("after reload %+v", again)
	}
}

// A change of the machine's time zone moves a re-read event's local date; its
// id is still found (sets are per UTC day), so it stays counted once, on the
// date of its first sighting, as the server pins it.
func TestTimeZoneChangeNeverCountsAnIdTwice(t *testing.T) {
	r := New()
	ts := time.Date(2026, 10, 1, 23, 30, 0, 0, time.UTC) // 2 Oct at +60, 1 Oct at -240
	e := usage("tz01", ts, 100, 10)
	e.TZOffsetMin = 60
	a := activity("tz02", ts, false)
	a.TZOffsetMin = 60
	p := prompt("tz03", ts, "", "text")
	p.TZOffsetMin = 60
	r.AddUsage(e, now)
	r.AddActivity(a)
	r.AddPrompt(p)
	// The weekly full reparse after the move to UTC-4.
	e.TZOffsetMin, a.TZOffsetMin, p.TZOffsetMin = -240, -240, -240
	r.AddUsage(e, now)
	a.HasUsage = true
	r.AddActivity(a)
	p.Model = "claude-x"
	r.AddPrompt(p)
	if _, ok := r.Days["2026-10-01"]; ok {
		t.Fatalf("counted again on the new local date: %v", r.Days["2026-10-01"])
	}
	tok, ev, pr := total(r, "2026-10-02")
	if _, u := noUsage(r, "2026-10-02"); tok != 110 || ev != 1 || pr != 1 || u != 0 {
		t.Fatalf("first date: tokens %d events %d prompts %d untracked %d", tok, ev, pr, u)
	}
	if got := modelPrompts(r, "2026-10-02"); got["claude-x"] != 1 || len(got) != 1 {
		t.Fatalf("model prompts %v", got)
	}
}

// An id's UTC day is kept until every local date it can be on is sealed.
func TestSealingKeepsTheIdsOfTheLastSealedUTCDay(t *testing.T) {
	r := New()
	ts := time.Date(2026, 9, 25, 23, 0, 0, 0, time.UTC) // UTC day 25 Sep, 26 Sep at +120
	e := usage("sd01", ts, 5, 0)
	e.TZOffsetMin = 120
	late := usage("sd02", time.Date(2026, 9, 27, 23, 0, 0, 0, time.UTC), 7, 0)
	late.TZOffsetMin = 120 // 28 Sep locally: not sealed, though its UTC day is the cut
	r.AddUsage(e, now)
	r.AddUsage(late, now)
	r.Settle(now, true) // seals up to 2026-09-27
	if _, ok := r.seen["2026-09-25"]; ok {
		t.Fatal("ids of a UTC day before the cut must go")
	}
	r.AddUsage(late, now)
	if tok, _, _ := total(r, "2026-09-28"); tok != 7 {
		t.Fatalf("28 Sep: %d", tok)
	}
	if _, ok := r.seen["2026-09-27"]; !ok {
		t.Fatal("the cut's UTC day keeps its ids")
	}
}

// v1File is a rollup as release 0.3 kept it: 6-column cells under 4-part keys,
// id sets per local date.
func v1File(t *testing.T, days map[string]map[string][6]int64, seen map[string][]string) string {
	t.Helper()
	sets := map[string]map[uint64]struct{}{}
	for d, ids := range seen {
		for _, id := range ids {
			mark(sets, d, prefix(id))
		}
	}
	b, _ := json.Marshal(map[string]any{"v": 1, "days": days, "seen": packSets(sets)})
	p := filepath.Join(t.TempDir(), "rollup.json")
	os.WriteFile(p, b, 0o600)
	return p
}

// An older file is never thrown away: through the rebuild it is a floor, and
// whatever the logs no longer hold is put back, counter by counter, without
// counting anything the rebuild re-read twice.
func TestSalvageKeepsWhatTheLogsNoLongerHold(t *testing.T) {
	const opus, sonnet = "anthropic|claude-code|opus|a_1", "anthropic|claude-code|sonnet|a_1"
	p := v1File(t, map[string]map[string][6]int64{
		"2025-01-15": {opus: {1000, 0, 500, 40, 3, 0}, "anthropic|claude-code||a_1": {0, 0, 0, 0, 0, 12}}, // logs deleted
		"2026-09-20": {opus: {10, 0, 0, 1, 1, 0}},                                                         // re-read in full
		"2026-09-21": {opus: {10, 0, 0, 1, 1, 0}, sonnet: {20, 0, 0, 2, 1, 0}},                            // sonnet's log gone
		"2026-09-22": {"anthropic|claude-code|opus|a_old": {10, 0, 0, 1, 1, 0}},                           // re-attributed since
		"2026-10-03": {opus: {30, 0, 0, 3, 2, 0}},                                                         // a WSL distro not running
	}, map[string][]string{"2026-10-03": {"fe01", "fe02"}})
	if _, err := Load(p); err != ErrVersion {
		t.Fatalf("v1: %v", err)
	}
	r := Salvage(p)
	if !r.AccountsStale() || len(r.Days) != 0 || r.floor == nil {
		t.Fatalf("salvaged %+v", r)
	}
	at := func(id, date string, in, out int64, m, acct string) model.UsageEvent {
		ts, _ := time.Parse(dateLayout, date)
		e := usage(id, ts.Add(12*time.Hour), in, out)
		e.Model, e.Acct = m, acct
		return e
	}
	r.AddUsage(at("ab20", "2026-09-20", 10, 1, "opus", "a_1"), now)
	r.AddUsage(at("ab21", "2026-09-21", 10, 1, "opus", "a_1"), now)
	r.AddUsage(at("ab22", "2026-09-22", 10, 1, "opus", "a_1"), now)
	r.AddUsage(at("fe01", "2026-10-03", 20, 2, "opus", "a_1"), now) // fe01's home is read; fe02's distro is not running
	r.AddUsage(at("ab04", "2026-10-04", 5, 0, "opus", "a_1"), now)  // new since the old file

	// The floor survives a save and load (a rebuild takes several ticks).
	path := filepath.Join(t.TempDir(), "rollup.json")
	if err := r.Save(path); err != nil {
		t.Fatal(err)
	}
	r, err := Load(path)
	if err != nil || r.floor == nil {
		t.Fatalf("reloaded mid-rebuild: %v", err)
	}
	r.ApplyFloor()
	cell := func(date, key string) Cell {
		if c := r.Days[date][key+"|"]; c != nil {
			return *c
		}
		return Cell{}
	}
	if c := cell("2025-01-15", opus); c.In != 1000 || c.CacheR != 500 || c.Out != 40 || c.Events != 3 {
		t.Fatalf("deleted logs' date: %+v", c)
	}
	if _, _, pr := total(r, "2025-01-15"); pr != 12 {
		t.Fatalf("deleted logs' prompts %d", pr)
	}
	if tok, ev, _ := total(r, "2026-09-20"); tok != 11 || ev != 1 {
		t.Fatalf("re-read date counted twice: %d %d", tok, ev)
	}
	if c := cell("2026-09-21", sonnet); c.In != 20 || c.Out != 2 || c.Events != 1 {
		t.Fatalf("the missing model: %+v", c)
	}
	if tok, _, _ := total(r, "2026-09-21"); tok != 33 {
		t.Fatalf("21 Sep: %d", tok)
	}
	if tok, ev, _ := total(r, "2026-09-22"); tok != 11 || ev != 1 {
		t.Fatalf("re-attributed history counted twice: %d %d", tok, ev)
	}
	if tok, ev, _ := total(r, "2026-10-03"); tok != 33 || ev != 2 {
		t.Fatalf("3 Oct: %d %d", tok, ev)
	}
	if tok, _, _ := total(r, "2026-10-04"); tok != 5 {
		t.Fatalf("new history: %d", tok)
	}
	var got []string
	for _, u := range r.Unledgered() {
		got = append(got, u.Date+"/"+u.Source)
	}
	if strings.Join(got, ",") != "2025-01-15/claude-code,2026-09-21/claude-code,2026-10-03/claude-code" {
		t.Fatalf("unledgered %v", got)
	}
	// The distro's event, read when it runs again, is counted already.
	r.AddUsage(at("fe02", "2026-10-03", 10, 1, "opus", "a_1"), now)
	if tok, _, _ := total(r, "2026-10-03"); tok != 33 {
		t.Fatalf("the old file's id counted again: %d", tok)
	}
	if r.floor != nil {
		t.Fatal("the floor must go once applied")
	}
	if err := r.Save(path); err != nil {
		t.Fatal(err)
	}
	if back, _ := Load(path); len(back.Unledgered()) != 3 || back.floor != nil {
		t.Fatalf("after reload: %+v", back.Unledgered())
	}
}

// A version 3 file's account totals come through a rebuild (they do not
// depend on local logs); without any, the rollup is stale until a read.
func TestSalvageCarriesAccountTotals(t *testing.T) {
	dir := t.TempDir()
	v3 := filepath.Join(dir, "v3.json")
	os.WriteFile(v3, []byte(`{"v":3,"days":{"2026-10-01":{"openai|codex|gpt-5|a_1|":[1,2,0,4,5,0,1,0,0,0]}},`+
		`"accounts":[{"provider":"openai","source":"codex","acct":"a_1","date":"2026-10-01","totalTokens":900,"observedAt":"2026-10-02T00:00:00Z"}]}`), 0o600)
	r := Salvage(v3)
	if r.AccountsStale() || len(r.AccountUsage()) != 1 || r.AccountUsage()[0].TotalTokens != 900 {
		t.Fatalf("v3 accounts %+v stale %v", r.AccountUsage(), r.AccountsStale())
	}
	r.ApplyFloor()
	if c := r.Days["2026-10-01"]["openai|codex|gpt-5|a_1|"]; c == nil || c.In != 1 || c.CacheW != 2 || c.CacheR != 4 || c.Out != 5 || c.Events != 1 {
		t.Fatalf("v3 cell %+v", c)
	}

	for name, content := range map[string]string{"damaged": `{"v":1,"days":`, "newer": `{"v":99,"days":{"2026-10-01":{}}}`} {
		p := filepath.Join(dir, name+".json")
		os.WriteFile(p, []byte(content), 0o600)
		if r := Salvage(p); !r.AccountsStale() || r.floor != nil || len(r.Days) != 0 {
			t.Fatalf("%s: %+v", name, r)
		}
	}
	r = Salvage(filepath.Join(dir, "absent.json"))
	path := filepath.Join(dir, "rollup.json")
	r.Save(path)
	if back, _ := Load(path); !back.AccountsStale() {
		t.Fatal("stale must survive a save and load")
	}
	r.AddAccountUsage(nil) // a successful read, even of nothing
	if r.AccountsStale() || !r.Dirty() {
		t.Fatal("a read must end stale")
	}
}
