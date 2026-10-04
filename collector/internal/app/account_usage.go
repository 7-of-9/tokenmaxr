package app

import (
	"context"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/accountusage"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/outbox"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/rollup"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

const accountUsageEvery = 15 * time.Minute
const accountUsageTimeout = 12 * time.Second
const accountUsageRefreshEvery = time.Hour

// collectAccountUsage is enabled by default with the Codex source. The
// provider snapshot is account-wide and deliberately excluded from the tray's
// local-machine counters. API reconciliation prevents multiple machines or
// later recovered transcripts from counting the same tokens again.
//
// It runs for either destination, on the same schedule: a server (ob) is
// sent what changed, as before; the GitHub publisher (ru) keeps the newest
// totals in the rollup, beside the account ledger they are reconciled
// against on the published dashboard. Either may be nil.
func (a *App) collectAccountUsage(ctx context.Context, cfg *store.Config, key []byte, st *store.State, ob *outbox.Outbox, ru *rollup.Rollup, deadline time.Time, onRead func()) (int, error) {
	if a.ReadAccountUsage == nil || !cfg.SourceEnabled(model.SourceCodex) {
		return 0, nil
	}
	now := a.Now()
	if deadline.Sub(now) < 2*time.Second {
		return 0, nil
	}
	acct := accountusage.Account(a.UserHome, a.CodexHome, key)
	state := &st.AccountHistory
	if state.Account == acct && !state.LastAttempt.IsZero() && now.Sub(state.LastAttempt) < accountUsageEvery {
		return 0, nil
	}
	if state.Account != acct {
		state.LastSuccess = time.Time{}
		state.LastRefresh = time.Time{}
		state.Days, state.TotalTokens = 0, 0
	}
	state.Account, state.LastAttempt = acct, now
	if acct == "" {
		state.LastError = accountusage.ErrNoAccount.Error()
		st.Checks["codexAccountHistory"] = "not signed in"
		return 0, nil
	}
	readDeadline := now.Add(accountUsageTimeout)
	if deadline.Before(readDeadline) {
		readDeadline = deadline
	}
	readCtx, cancel := context.WithDeadline(ctx, readDeadline)
	defer cancel()
	if onRead != nil {
		onRead()
	}
	rows, err := a.ReadAccountUsage(readCtx, a.UserHome, a.CodexHome, key, now)
	if err != nil {
		// The reader returns only fixed, credential-free diagnostics.
		state.LastError = err.Error()
		st.Checks["codexAccountHistory"] = "unavailable"
		a.Log.Printf("account history: %v; local collection continues", err)
		return 0, nil
	}
	queued, err := a.queueAccountUsage(state, ob, rows, now)
	if err != nil {
		return 0, err
	}
	if ru != nil {
		// Saved before the state, like the outbox: the next read repeats it.
		ru.AddAccountUsage(rows)
		if ru.Dirty() {
			if err := ru.Save(paths.Rollup(a.Home)); err != nil {
				return queued, err
			}
		}
	}
	state.LastSuccess, state.LastError = now, ""
	state.Days, state.TotalTokens = len(rows), 0
	for _, row := range rows {
		state.TotalTokens += row.TotalTokens
	}
	st.Checks["codexAccountHistory"] = "ok"
	if ob != nil {
		a.Log.Printf("account history: Codex read %d UTC days, %d account tokens; %d daily totals queued", state.Days, state.TotalTokens, queued)
	} else {
		a.Log.Printf("account history: Codex read %d UTC days, %d account tokens", state.Days, state.TotalTokens)
	}
	if err := store.SaveState(a.Home, st); err != nil {
		return queued, err
	}
	return queued, nil
}

// queueAccountUsage writes the changed daily totals to the server's outbox
// (none without a server) and returns how many it queued.
func (a *App) queueAccountUsage(state *store.AccountHistoryState, ob *outbox.Outbox, rows []model.AccountUsageSnapshot, now time.Time) (int, error) {
	if ob == nil {
		// Queued is what the server was sent: without one it stays as it is.
		return 0, nil
	}
	if state.Queued == nil {
		state.Queued = map[string]int64{}
	}
	// Another machine can publish a different snapshot while this one is
	// offline. Periodic refresh also delivers downward provider corrections
	// even when this machine's own last queued value happens to match again.
	refresh := state.LastRefresh.IsZero() || now.Sub(state.LastRefresh) >= accountUsageRefreshEvery
	var changed []model.AccountUsageSnapshot
	for _, row := range rows {
		if previous, ok := state.Queued[row.ID]; refresh || !ok || previous != row.TotalTokens {
			changed = append(changed, row)
		}
	}
	// Commit the outbox before the cache. A crash may re-send stable IDs, but
	// can never advance the cache past records that were not durably queued.
	for _, batch := range (&outbox.Batch{AccountUsage: changed}).Split(90) {
		if _, err := ob.Write(batch); err != nil {
			return 0, err
		}
	}
	for _, row := range changed {
		state.Queued[row.ID] = row.TotalTokens
	}
	if refresh {
		state.LastRefresh = now
	}
	return len(changed), nil
}
