package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/accountusage"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

// quotaRefreshEvery is how often each provider's installed client is asked
// for a fresh quota meter. The page's "Read … ago" then tracks the provider
// within about this interval (plus limits.ResendAfter).
const quotaRefreshEvery = 5 * time.Minute

// quotaRefreshTimeout bounds one round; clients answer in about 1-2 s.
const quotaRefreshTimeout = 25 * time.Second

var quotaProviders = []struct{ provider, source string }{
	{model.ProviderAnthropic, model.SourceClaudeCode},
	{model.ProviderOpenAI, model.SourceCodex},
	{model.ProviderXAI, model.SourceGrokCLI},
}

// errQuotaNotInUse: the client shows no sign of use in this home, so it is
// not started.
var errQuotaNotInUse = errors.New("not in use")

func quietQuotaErr(err error) bool {
	return errors.Is(err, errQuotaNotInUse) || errors.Is(err, accountusage.ErrNoClaude) ||
		errors.Is(err, accountusage.ErrNoGrok) || errors.Is(err, accountusage.ErrNoClient)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// refreshQuotaWithClient is the production RefreshQuota. A client runs only
// when its own files show it is in use here; nothing is read from them.
func (a *App) refreshQuotaWithClient(ctx context.Context, provider string) error {
	work := paths.QuotaWork(a.Home)
	switch provider {
	case model.ProviderAnthropic:
		if !exists(filepath.Join(a.UserHome, ".claude.json")) {
			return errQuotaNotInUse
		}
		return accountusage.RefreshClaude(ctx, a.UserHome, filepath.Join(work, "claude"))
	case model.ProviderOpenAI:
		if !exists(filepath.Join(a.CodexHome, "auth.json")) {
			return errQuotaNotInUse
		}
		return accountusage.RefreshCodex(ctx, a.UserHome, a.CodexHome, paths.CodexQuota(a.Home), a.Now())
	case model.ProviderXAI:
		if !exists(filepath.Join(a.UserHome, ".grok", "logs")) {
			return errQuotaNotInUse
		}
		return accountusage.RefreshGrok(ctx, a.UserHome, filepath.Join(work, "grok"))
	}
	return errQuotaNotInUse
}

// refreshQuotas asks each enabled provider's client for a fresh meter, at
// most every quotaRefreshEvery per provider, in parallel, before the scan
// reads meters. Attempts are recorded in st so headless runs (a process a
// minute) keep the same cadence. Failures never stop collection.
func (a *App) refreshQuotas(ctx context.Context, cfg *store.Config, st *store.State, deadline time.Time) {
	if a.RefreshQuota == nil {
		return
	}
	now := a.Now()
	if deadline.Sub(now) < quotaRefreshTimeout+5*time.Second {
		return
	}
	if st.QuotaRefreshed == nil {
		st.QuotaRefreshed = map[string]time.Time{}
	}
	type due struct{ provider, source string }
	var run []due
	for _, p := range quotaProviders {
		if !cfg.SourceEnabled(p.source) {
			continue
		}
		if last := st.QuotaRefreshed[p.provider]; !last.IsZero() && now.Sub(last) < quotaRefreshEvery && !last.After(now) {
			continue
		}
		st.QuotaRefreshed[p.provider] = now
		run = append(run, due{p.provider, p.source})
	}
	if len(run) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, quotaRefreshTimeout)
	defer cancel()
	type result struct {
		source string
		err    error
		took   time.Duration
	}
	results := make([]result, len(run))
	var wg sync.WaitGroup
	for i, p := range run {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t0 := time.Now()
			err := a.RefreshQuota(ctx, p.provider)
			results[i] = result{p.source, err, time.Since(t0)}
		}()
	}
	wg.Wait()
	var parts []string
	for _, r := range results {
		switch {
		case r.err == nil:
			parts = append(parts, fmt.Sprintf("%s ok (%s)", r.source, r.took.Round(100*time.Millisecond)))
		case quietQuotaErr(r.err):
		default:
			// accountusage errors are fixed, credential-free messages.
			parts = append(parts, fmt.Sprintf("%s: %v", r.source, r.err))
		}
	}
	if len(parts) > 0 {
		a.Log.Printf("quota refresh: %s", strings.Join(parts, "; "))
	}
}
