package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/logx"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/outbox"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/rollup"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

func TestAccountHistoryDefaultScheduleOutboxAndFailures(t *testing.T) {
	now := time.Now()
	home := t.TempDir()
	codex := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codex, 0700); err != nil {
		t.Fatal(err)
	}
	auth := filepath.Join(codex, "auth.json")
	if err := os.WriteFile(auth, []byte(`{"tokens":{"account_id":"account-one"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	a := &App{Home: home, UserHome: home, CodexHome: codex, Log: logx.Discard(), Now: func() time.Time { return now }}
	calls := 0
	count := int64(100)
	var readErr error
	a.ReadAccountUsage = func(ctx context.Context, _, _ string, _ []byte, _ time.Time) ([]model.AccountUsageSnapshot, error) {
		calls++
		if d, ok := ctx.Deadline(); !ok || d.After(now.Add(accountUsageTimeout)) {
			t.Fatal("reader lacks bounded timeout")
		}
		return []model.AccountUsageSnapshot{{ID: "stable-day-id", TotalTokens: count}}, readErr
	}
	cfg := store.DefaultConfig()
	st := store.NewState()
	ob := outbox.New(filepath.Join(home, "outbox"))
	read := func() int {
		t.Helper()
		n, err := a.collectAccountUsage(context.Background(), &cfg, []byte("key"), st, ob, nil, now.Add(time.Minute), nil)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := read(); n != 1 || calls != 1 {
		t.Fatalf("default initial backfill: queued=%d calls=%d", n, calls)
	}
	if n := read(); n != 0 || calls != 1 {
		t.Fatal("repeated provider read before cadence")
	}
	loaded, err := store.LoadState(home)
	if err != nil || loaded.AccountHistory.Queued["stable-day-id"] != 100 || loaded.AccountHistory.LastSuccess.IsZero() {
		t.Fatal("successful cache was not persisted")
	}
	st = loaded // schedule survives a collector restart
	now = now.Add(accountUsageEvery)
	if n := read(); n != 0 || calls != 2 {
		t.Fatal("unchanged daily snapshot queued again")
	}
	count = 120
	now = now.Add(accountUsageEvery)
	if n := read(); n != 1 {
		t.Fatal("changed daily count not queued")
	}
	_, pending := ob.Count()
	if pending != 2 {
		t.Fatalf("outbox pending=%d", pending)
	}
	// Another collector may have sent 150 while this one still remembers
	// 120. The provider correcting back to 120 must still reach the server.
	now = now.Add(accountUsageRefreshEvery - 2*accountUsageEvery)
	if n := read(); n != 1 {
		t.Fatal("unchanged history was not refreshed after one hour")
	}
	if !st.AccountHistory.LastRefresh.Equal(now) {
		t.Fatal("refresh timestamp was not persisted after queue")
	}
	readErr = errors.New("provider unavailable")
	now = now.Add(accountUsageEvery)
	if n := read(); n != 0 || st.AccountHistory.LastError == "" {
		t.Fatal("provider failure must be soft and recorded")
	}
	before := calls
	read()
	if calls != before {
		t.Fatal("failed reader retried every tick")
	}
	// A login change must not wait for the previous account's retry cadence.
	if err := os.WriteFile(auth, []byte(`{"tokens":{"account_id":"account-two"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	read()
	if calls != before+1 {
		t.Fatal("new account not collected immediately")
	}
	cfg.Sources[model.SourceCodex] = false
	now = now.Add(accountUsageEvery)
	read()
	if calls != before+1 {
		t.Fatal("disabled source still queried provider")
	}
}

func TestAccountHistoryDoesNotAdvanceAfterOutboxFailure(t *testing.T) {
	now := time.Now()
	home := t.TempDir()
	codex := filepath.Join(home, ".codex")
	os.MkdirAll(codex, 0700)
	os.WriteFile(filepath.Join(codex, "auth.json"), []byte(`{"tokens":{"account_id":"account"}}`), 0600)
	badDir := filepath.Join(home, "not-a-directory")
	os.WriteFile(badDir, []byte("file"), 0600)
	a := &App{Home: home, UserHome: home, CodexHome: codex, Log: logx.Discard(), Now: func() time.Time { return now }, ReadAccountUsage: func(context.Context, string, string, []byte, time.Time) ([]model.AccountUsageSnapshot, error) {
		return []model.AccountUsageSnapshot{{ID: "day", TotalTokens: 99}}, nil
	}}
	st := store.NewState()
	cfg := store.DefaultConfig()
	_, err := a.collectAccountUsage(context.Background(), &cfg, []byte("key"), st, outbox.New(badDir), nil, now.Add(time.Minute), nil)
	if err == nil || len(st.AccountHistory.Queued) > 0 || !st.AccountHistory.LastSuccess.IsZero() {
		t.Fatal("cache advanced without durable queue")
	}
}

// With a server and GitHub both on, one read feeds both: the outbox gets what
// changed (as before) and the rollup keeps the newest totals for publishing.
func TestAccountHistoryFeedsTheOutboxAndTheRollup(t *testing.T) {
	now := time.Now()
	home := t.TempDir()
	codex := filepath.Join(home, ".codex")
	os.MkdirAll(codex, 0700)
	os.WriteFile(filepath.Join(codex, "auth.json"), []byte(`{"tokens":{"account_id":"account"}}`), 0600)
	date := now.UTC().AddDate(0, 0, -1).Format("2006-01-02")
	a := &App{Home: home, UserHome: home, CodexHome: codex, Log: logx.Discard(), Now: func() time.Time { return now },
		ReadAccountUsage: func(context.Context, string, string, []byte, time.Time) ([]model.AccountUsageSnapshot, error) {
			return []model.AccountUsageSnapshot{{ID: "day", Provider: model.ProviderOpenAI, Source: model.SourceCodex, Acct: "a_1",
				AcctQ: model.AcctRecorded, Date: date, Timezone: "UTC", TotalTokens: 99, ObservedAt: now}}, nil
		}}
	st := store.NewState()
	cfg := store.DefaultConfig()
	ob := outbox.New(filepath.Join(home, "outbox"))
	ru := rollup.New()
	n, err := a.collectAccountUsage(context.Background(), &cfg, []byte("key"), st, ob, ru, now.Add(time.Minute), nil)
	if err != nil || n != 1 || st.AccountHistory.Queued["day"] != 99 {
		t.Fatalf("queued %d (%v), cache %v", n, err, st.AccountHistory.Queued)
	}
	if _, pending := ob.Count(); pending != 1 {
		t.Fatalf("outbox pending %d", pending)
	}
	saved, err := rollup.Load(paths.Rollup(home))
	if err != nil || len(saved.AccountUsage()) != 1 || saved.AccountUsage()[0].TotalTokens != 99 {
		t.Fatalf("rollup totals %+v (%v)", saved.AccountUsage(), err)
	}
}

// Only the account-history sources' ledger is published, and only from a
// machine with totals or a signed-in account.
func TestAccountHistoryPublishesTheLedgerOfItsSources(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	ru := rollup.New()
	ts := now.Add(-48 * time.Hour)
	ru.AddUsage(model.UsageEvent{ID: "c1", Provider: model.ProviderOpenAI, Source: model.SourceCodex, TS: ts, Acct: "a_1", AcctQ: model.AcctRecorded,
		Tokens: model.Tokens{In: 10, Out: 5}}, now)
	ru.AddUsage(model.UsageEvent{ID: "c2", Provider: model.ProviderAnthropic, Source: model.SourceClaudeCode, TS: ts, Acct: "a_2", AcctQ: model.AcctRecorded,
		Tokens: model.Tokens{In: 7}}, now)
	st := store.NewState()
	if h := accountHistory(ru, st); len(h.Snapshots) != 0 || len(h.Ledger) != 0 {
		t.Fatalf("a machine without Codex account history published %+v", h)
	}
	st.AccountHistory.Account = "a_1"
	h := accountHistory(ru, st)
	if len(h.Ledger) != 1 || h.Ledger[0].Source != model.SourceCodex || h.Ledger[0].Tokens != 15 || h.Ledger[0].Acct != "a_1" {
		t.Fatalf("ledger %+v", h.Ledger)
	}
}
