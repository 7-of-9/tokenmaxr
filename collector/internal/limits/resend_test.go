package limits

import (
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
)

func meter(window string, used float64, observed time.Time) model.LimitSnapshot {
	return model.LimitSnapshot{ID: "id", Provider: model.ProviderAnthropic, Window: window, UsedPercent: pct(used), ObservedAt: observed}
}

func TestResendTracksProviderReReports(t *testing.T) {
	t0 := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	first := SentMark(meter("week", 47, t0))

	if !ShouldResend("", first) {
		t.Fatal("a never-sent meter must be queued")
	}
	if ShouldResend(first, SentMark(meter("week", 47, t0))) {
		t.Fatal("an identical reading must not be re-queued")
	}
	if ShouldResend(first, SentMark(meter("week", 47, t0.Add(ResendAfter-time.Second)))) {
		t.Fatal("a re-report sooner than ResendAfter must not be re-queued")
	}
	if !ShouldResend(first, SentMark(meter("week", 47, t0.Add(ResendAfter)))) {
		t.Fatal("an unchanged meter re-reported ResendAfter later must be re-queued")
	}
	if !ShouldResend(first, SentMark(meter("week", 48, t0.Add(time.Second)))) {
		t.Fatal("a changed value must be queued immediately")
	}
	if ShouldResend(first, SentMark(meter("week", 47, t0.Add(-time.Hour)))) {
		t.Fatal("an older re-read must not be re-queued")
	}
}

func TestResendUpgradeFromBareFingerprint(t *testing.T) {
	t0 := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	legacy := Fingerprint(meter("week", 47, t0)) // what pre-0.2.6 state.json holds
	if !ShouldResend(legacy, SentMark(meter("week", 47, t0))) {
		t.Fatal("the first timed mark after upgrading must refresh the server's observedAt once")
	}
	if !ShouldResend(legacy, SentMark(meter("week", 50, t0))) {
		t.Fatal("a changed value must still be queued after upgrading")
	}
}

func TestSyntheticPlanRowsIgnoreLocalTime(t *testing.T) {
	t0 := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	plan := model.LimitSnapshot{ID: "id", Provider: model.ProviderAnthropic, Window: "plan", Label: "a@example.com", ObservedAt: t0}
	later := plan
	later.ObservedAt = t0.Add(23 * time.Hour)
	if ShouldResend(SentMark(plan), SentMark(later)) {
		t.Fatal("a plan row stamped with local time must not be re-queued within the day")
	}
	// Once a day, so the server's reading of a signed-in account stays fresh.
	later.ObservedAt = t0.Add(24 * time.Hour)
	if !ShouldResend(SentMark(plan), SentMark(later)) {
		t.Fatal("an unchanged plan row must be re-queued the next day")
	}
	// The bare fingerprint an older collector stored is re-queued once.
	if !ShouldResend(Fingerprint(plan), SentMark(plan)) {
		t.Fatal("a plan row marked by an older collector must be re-queued once")
	}
}
