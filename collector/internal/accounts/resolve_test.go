package accounts

import (
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/evidence"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

// Resolve's order: the stream's own record, then the live timeline, then
// the evidence fallbacks; the timeline's own inferred answer stands only
// when the evidence knows nothing.
func TestResolveOrder(t *testing.T) {
	p := model.ProviderAnthropic
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	ivs := []store.Interval{{Provider: p, Acct: "a_live", From: t0, To: t0.Add(time.Hour)}}
	ix := evidence.Build([]evidence.Record{
		{Provider: p, Kind: evidence.KindBoundary, Source: evidence.SrcClaudeLogin, TS: t0.Add(-48 * time.Hour)},
		{Provider: p, Kind: evidence.KindSample, Source: evidence.SrcClaudeBackup, Q: evidence.QStrong, Acct: "a_old", TS: t0.Add(-47 * time.Hour)},
		{Provider: p, Kind: evidence.KindBoundary, Source: evidence.SrcClaudeLogin, TS: t0.Add(-24 * time.Hour)},
	}, Spans(ivs))
	hint := sources.Hint{Kind: sources.HintAccount, ID: "a_hint", At: t0}

	if a, q := Resolve(ivs, ix, "", p, t0.Add(time.Minute), "", hint); a != "a_hint" || q != model.AcctRecorded {
		t.Errorf("hint: %s %s", a, q)
	}
	if a, q := Resolve(ivs, ix, "", p, t0.Add(time.Minute), "", sources.Hint{}); a != "a_live" || q != model.AcctTimeline {
		t.Errorf("timeline: %s %s", a, q)
	}
	if a, q := Resolve(ivs, ix, "", p, t0.Add(-40*time.Hour), "", sources.Hint{}); a != "a_old" || q != model.AcctBounded {
		t.Errorf("bounded: %s %s", a, q)
	}
	// Before every login: nearest sample, inferred.
	if a, q := Resolve(ivs, ix, "", p, t0.Add(-100*time.Hour), "", sources.Hint{}); a != "a_old" || q != model.AcctInferred {
		t.Errorf("inferred: %s %s", a, q)
	}
	// No evidence at all: Attribute's answer.
	if a, q := Resolve(ivs, nil, "", p, t0.Add(-100*time.Hour), "", sources.Hint{}); a != "a_live" || q != model.AcctInferred {
		t.Errorf("no index: %s %s", a, q)
	}
	if a, q := Resolve(nil, evidence.Build(nil, nil), "", p, t0, "", sources.Hint{}); a != "" || q != model.AcctUnknown {
		t.Errorf("nothing: %s %s", a, q)
	}
}
