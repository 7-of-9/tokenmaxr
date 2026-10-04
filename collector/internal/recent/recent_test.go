package recent

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
)

func newWindow() *Window { return &Window{Providers: map[string]*Provider{}, IDs: &IDs{}} }

func ev(id, provider string, ts time.Time, in, out int64) model.UsageEvent {
	return model.UsageEvent{ID: id, Provider: provider, TS: ts, Tokens: model.Tokens{In: in, Out: out, CacheR: 10, CacheW: 5}}
}

// roundTrip saves and reloads the window as state.json would.
func roundTrip(t *testing.T, w *Window) *Window {
	t.Helper()
	b, err := json.Marshal(struct {
		R map[string]*Provider `json:"recent"`
		I *IDs                 `json:"recentIds"`
	}{w.Providers, w.IDs})
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		R map[string]*Provider `json:"recent"`
		I IDs                  `json:"recentIds"`
	}
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	return &Window{Providers: back.R, IDs: &back.I}
}

func summary(t *testing.T, w *Window, now time.Time, provider string) Summary {
	t.Helper()
	for _, s := range w.Summaries(now) {
		if s.Provider == provider {
			return s
		}
	}
	t.Fatalf("no summary for %s", provider)
	return Summary{}
}

func TestIdempotentMerge(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	w := newWindow()
	old := ev("aa11", "openai", now.Add(-72*time.Hour), 100, 50) // 165 tokens, prefix only
	young := ev("bb22", "anthropic", now.Add(-10*time.Minute), 1000, 10)
	w.Add(old, now)
	w.Add(young, now)
	// Re-reading everything, any number of times and after a save, adds nothing.
	for i := range 3 {
		w = roundTrip(t, w)
		w.Add(old, now)
		w.Add(young, now)
		if i == 1 {
			w.Prune(now)
		}
	}
	if s := summary(t, w, now, "openai"); s.PastMonth != 165 || s.PastHour != 0 || s.LastTokens != 165 {
		t.Fatalf("openai %+v", s)
	}
	if s := summary(t, w, now, "anthropic"); s.PastMonth != 1025 || s.PastHour != 1025 || s.LastTokens != 1025 {
		t.Fatalf("anthropic %+v", s)
	}

	// A growing message adds only its fieldwise-max growth, in any order.
	grown := ev("bb22", "anthropic", now.Add(-10*time.Minute), 1000, 400)
	shrunk := ev("bb22", "anthropic", now.Add(-10*time.Minute), 2000, 5) // more input, less output
	w.Add(grown, now)
	w.Add(shrunk, now)
	w.Add(grown, now)
	// max: in 2000, out 400, cacheR 10, cacheW 5.
	if s := summary(t, w, now, "anthropic"); s.PastMonth != 2415 || s.PastHour != 2415 || s.LastTokens != 2415 {
		t.Fatalf("after growth %+v", s)
	}
	h := w.Providers["anthropic"].Hourly
	if len(h) != 1 || h[0].Tokens != 2415 {
		t.Fatalf("hourly %+v", h)
	}

	// Once the event is no longer live only its prefix remains, and a
	// re-read of it (weekly reparse) still counts nothing.
	later := now.Add(3 * time.Hour)
	w.Prune(later)
	if len(w.IDs.Live) != 0 {
		t.Fatalf("live ids after prune: %v", w.IDs.Live)
	}
	w = roundTrip(t, w)
	w.Add(grown, later)
	if s := summary(t, w, later, "anthropic"); s.PastMonth != 2415 || s.PastHour != 0 || s.LastTokens != 2415 {
		t.Fatalf("after reparse %+v", s)
	}
}

func TestLatestEvent(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	w := newWindow()
	w.Add(ev("01", "xai", now.Add(-5*time.Minute), 10, 0), now)
	w.Add(ev("02", "xai", now.Add(-50*time.Minute), 900, 0), now) // older: not the latest
	// An event older than the window still tells when the provider was used.
	w.Add(ev("03", "google", now.AddDate(0, 0, -45), 7, 0), now)
	if s := summary(t, w, now, "xai"); !s.LastTS.Equal(now.Add(-5*time.Minute)) || s.LastTokens != 25 || s.PastHour != 940 {
		t.Fatalf("xai %+v", s)
	}
	if s := summary(t, w, now, "google"); s.PastMonth != 0 || s.LastTokens != 22 {
		t.Fatalf("google %+v", s)
	}
	if len(w.Summaries(now)) != 2 {
		t.Fatalf("summaries %+v", w.Summaries(now))
	}
}

func TestRollOver(t *testing.T) {
	loc := time.FixedZone("ICT", 7*3600)
	// The window's "today" is the machine's local date.
	was := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = was })
	now := time.Date(2026, 9, 30, 23, 30, 0, 0, loc)
	w := newWindow()
	e := func(id string, ts time.Time) model.UsageEvent {
		return model.UsageEvent{ID: id, Provider: "cursor", TS: ts, TZOffsetMin: 420, Tokens: model.Tokens{In: 100}}
	}
	for d := range 40 {
		w.Add(e(fmt.Sprintf("d%02d", d), now.AddDate(0, 0, -d)), now)
	}
	for h := range 30 {
		w.Add(e(fmt.Sprintf("h%02d", h), now.Add(-time.Duration(h)*time.Hour-time.Minute)), now)
	}
	p := w.Providers["cursor"]
	if len(p.Daily) != Days || p.Daily[0].Date != FirstDay(now) {
		t.Fatalf("daily %d from %s, want %d from %s", len(p.Daily), p.Daily[0].Date, Days, FirstDay(now))
	}
	if len(p.Hourly) != Hours {
		t.Fatalf("hourly %d", len(p.Hourly))
	}
	// Local dates follow the event's offset: 23:30 ICT is still the 30th.
	if last := p.Daily[len(p.Daily)-1]; last.Date != "2026-09-30" {
		t.Fatalf("last day %s", last.Date)
	}

	// Two days later the window has moved: old buckets and id sets go.
	next := now.Add(48 * time.Hour)
	w.Prune(next)
	if len(p.Daily) != Days-2 || p.Daily[0].Date < FirstDay(next) {
		t.Fatalf("after roll-over %d days from %s", len(p.Daily), p.Daily[0].Date)
	}
	if len(p.Hourly) != 0 {
		t.Fatalf("hourly after 48h: %+v", p.Hourly)
	}
	for d := range w.IDs.seen {
		if d < FirstDay(next) {
			t.Fatalf("id set for %s kept", d)
		}
	}
	month := summary(t, w, next, "cursor").PastMonth
	// Re-reading every event again counts nothing new.
	for d := range 40 {
		w.Add(e(fmt.Sprintf("d%02d", d), now.AddDate(0, 0, -d)), next)
	}
	if got := summary(t, w, next, "cursor").PastMonth; got != month {
		t.Fatalf("re-read changed the month: %d -> %d", month, got)
	}
}

func TestSealOnCap(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	w := newWindow()
	w.Add(ev("aa", "openai", now.AddDate(0, 0, -20), 1, 0), now)
	w.Add(ev("bb", "openai", now.AddDate(0, 0, -2), 1, 0), now)
	// Pretend the set is full: one old day holds MaxIDs prefixes.
	big := map[uint64]struct{}{}
	for i := range MaxIDs {
		big[uint64(i)+1<<40] = struct{}{}
	}
	day := now.AddDate(0, 0, -25).Format(dateLayout)
	w.IDs.seen[day] = big
	w.Prune(now)
	if w.IDs.Sealed != day || w.IDs.count() != 2 {
		t.Fatalf("sealed %q, %d ids", w.IDs.Sealed, w.IDs.count())
	}
	before := summary(t, w, now, "openai").PastMonth
	// A sealed day counts no event again, even one whose id was dropped;
	// later days keep their ids.
	w.Add(ev("cc", "openai", now.AddDate(0, 0, -25), 1, 0), now)
	w.Add(ev("aa", "openai", now.AddDate(0, 0, -20), 1, 0), now)
	if got := summary(t, w, now, "openai").PastMonth; got != before {
		t.Fatalf("sealed or known event counted again: %d -> %d", before, got)
	}
	w.Add(ev("dd", "openai", now.AddDate(0, 0, -1), 1, 0), now)
	if got := summary(t, w, now, "openai").PastMonth; got != before+16 {
		t.Fatalf("new event: %d -> %d", before, got)
	}
}

func TestPastDay(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	w := newWindow()
	for i, c := range []struct {
		age    time.Duration
		tokens int64
	}{
		{30 * time.Minute, 1},                 // this hour
		{10 * time.Hour, 10},                  // hours ago, no longer live
		{23 * time.Hour, 100},                 // the oldest hour of the window
		{23*time.Hour + 30*time.Minute, 1000}, // the hour before it: 30d only
		{30 * time.Hour, 10000},
	} {
		w.Add(ev(fmt.Sprintf("%02x", i+1), "anthropic", now.Add(-c.age), c.tokens, 0), now)
	}
	s := summary(t, w, now, "anthropic")
	// ev adds 15 cache tokens to each event: 16, 25, 115, 1015, 10015.
	if s.PastDay != 156 || s.PastMonth != 11186 || s.PastHour != 16 {
		t.Fatalf("summary %+v", s)
	}
	// An hour on, the oldest hour has left the window.
	w.Prune(now.Add(time.Hour))
	if s := summary(t, w, now.Add(time.Hour), "anthropic"); s.PastDay != 41 || s.PastMonth != 11186 {
		t.Fatalf("an hour on %+v", s)
	}
}
