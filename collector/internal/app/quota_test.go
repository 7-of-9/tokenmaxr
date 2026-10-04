package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/logx"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

func TestRefreshQuotasCadenceAndToggles(t *testing.T) {
	now := time.Date(2026, 10, 4, 6, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	calls := map[string]int{}
	a := &App{Home: t.TempDir(), Log: logx.Discard(), Now: func() time.Time { return now },
		RefreshQuota: func(_ context.Context, provider string) error {
			mu.Lock()
			calls[provider]++
			mu.Unlock()
			if provider == model.ProviderXAI {
				return errors.New("Grok quota read timed out") // failures never stop the tick
			}
			return nil
		}}
	cfg := store.DefaultConfig()
	st := &store.State{}
	deadline := func() time.Time { return now.Add(time.Minute) }

	a.refreshQuotas(context.Background(), &cfg, st, deadline())
	for _, p := range []string{model.ProviderAnthropic, model.ProviderOpenAI, model.ProviderXAI} {
		if calls[p] != 1 {
			t.Fatalf("first round: %s called %d times", p, calls[p])
		}
	}

	now = now.Add(quotaRefreshEvery - time.Second)
	a.refreshQuotas(context.Background(), &cfg, st, deadline())
	if calls[model.ProviderAnthropic] != 1 {
		t.Fatal("refreshed again before quotaRefreshEvery")
	}

	now = now.Add(time.Second)
	a.refreshQuotas(context.Background(), &cfg, st, deadline())
	if calls[model.ProviderAnthropic] != 2 || calls[model.ProviderXAI] != 2 {
		t.Fatalf("not refreshed after quotaRefreshEvery (or a failure blocked a retry): %v", calls)
	}

	// A disabled source is never refreshed.
	cfg.Sources = map[string]bool{model.SourceCodex: false}
	now = now.Add(quotaRefreshEvery)
	a.refreshQuotas(context.Background(), &cfg, st, deadline())
	if calls[model.ProviderOpenAI] != 2 {
		t.Fatalf("a disabled source was refreshed: %v", calls)
	}

	// Too little time left in the tick: skipped without stamping.
	now = now.Add(quotaRefreshEvery)
	before := st.QuotaRefreshed[model.ProviderAnthropic]
	a.refreshQuotas(context.Background(), &cfg, st, now.Add(10*time.Second))
	if !st.QuotaRefreshed[model.ProviderAnthropic].Equal(before) {
		t.Fatal("a short tick stamped a refresh it did not run")
	}

	// No refresher (tests, nil): nothing runs.
	a.RefreshQuota = nil
	a.refreshQuotas(context.Background(), &cfg, st, deadline())
}
