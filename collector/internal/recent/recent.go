// Package recent keeps the desktop app's local numbers: per provider, the
// latest event and its tokens, hourly totals for the last 24 h and daily
// totals for the last 30 days (docs/agents/SPEC.md "Desktop app (v1.4)",
// "Local numbers"). They live in state.json under "recent", with the id set
// that keeps them idempotent under "recentIds".
//
// Every tick adds the usage events it parsed. A tick sees a growing Claude
// message again, and the weekly full reparse sees everything again, so events
// merge by id with the server's invariant: a new id adds its tokens, a live id
// (one young enough to still grow) adds only its fieldwise-max growth, and any
// other known id adds nothing. The id set is bounded: live ids keep their
// values for LiveWindow, older ones only a 48-bit prefix per local day, for
// the 30-day window; if the set outgrows MaxIDs its oldest days are sealed
// (their numbers stay, and no event dated on or before them counts again).
package recent

import (
	"cmp"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"maps"
	"slices"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
)

const (
	// Days is the daily window, today included (the menu's "30d").
	Days = 30
	// Hours is the hourly window (the menu's "24h").
	Hours = 24
	// LiveWindow is how long an event keeps its values so that its growth
	// merges by fieldwise max (a Claude message streams for minutes and the
	// parser's carry lives 30); it also bounds the exact PastHour.
	LiveWindow = 2 * time.Hour
	// MaxIDs caps the id set (live and sealed prefixes together).
	MaxIDs = 250_000

	dateLayout = "2006-01-02"
	prefixLen  = 6 // bytes of the id kept once it is no longer live
)

// Hour is the tokens of one UTC clock hour (At is its start, Unix seconds).
type Hour struct {
	At     int64 `json:"t"`
	Tokens int64 `json:"n"`
}

// Day is the tokens of one local calendar date.
type Day struct {
	Date   string `json:"d"`
	Tokens int64  `json:"n"`
}

// Provider is one provider's entry in state.json "recent".
type Provider struct {
	LastTS     time.Time `json:"lastTs"`
	LastTokens int64     `json:"lastTokens"`
	// LastID lets a growing latest event update LastTokens.
	LastID string `json:"lastId,omitempty"`
	Hourly []Hour `json:"hourly,omitempty"`
	Daily  []Day  `json:"daily,omitempty"`
}

// Live is a young event's values: in, cacheW, cacheR, out.
type Live struct {
	Provider string    `json:"p"`
	TS       time.Time `json:"ts"`
	Date     string    `json:"d"`
	T        [4]int64  `json:"t"`
}

// IDs is state.json "recentIds", the bounded id set.
type IDs struct {
	// Live is event id -> values, for events newer than LiveWindow.
	Live map[string]Live `json:"live,omitempty"`
	// Sealed: no event dated on or before it counts again.
	Sealed string `json:"sealed,omitempty"`

	// seen is local date -> 48-bit id prefixes counted for it; it is
	// state.json's "seen" (date -> base64 of the sorted prefixes).
	seen map[string]map[uint64]struct{}
}

type idsJSON struct {
	Live   map[string]Live   `json:"live,omitempty"`
	Seen   map[string]string `json:"seen,omitempty"`
	Sealed string            `json:"sealed,omitempty"`
}

// MarshalJSON packs each day's prefixes as sorted base64.
func (s IDs) MarshalJSON() ([]byte, error) {
	out := idsJSON{Live: s.Live, Sealed: s.Sealed}
	if len(s.seen) > 0 {
		out.Seen = make(map[string]string, len(s.seen))
		for d, set := range s.seen {
			keys := slices.Sorted(maps.Keys(set))
			b := make([]byte, 0, prefixLen*len(keys))
			for _, k := range keys {
				var buf [8]byte
				binary.BigEndian.PutUint64(buf[:], k)
				b = append(b, buf[8-prefixLen:]...)
			}
			out.Seen[d] = base64.RawStdEncoding.EncodeToString(b)
		}
	}
	return json.Marshal(out)
}

// UnmarshalJSON reads what MarshalJSON wrote; a malformed day is dropped
// (that day may then count a re-read event once more, never lose one).
func (s *IDs) UnmarshalJSON(b []byte) error {
	var in idsJSON
	if err := json.Unmarshal(b, &in); err != nil {
		return err
	}
	*s = IDs{Live: in.Live, Sealed: in.Sealed}
	for d, enc := range in.Seen {
		raw, err := base64.RawStdEncoding.DecodeString(enc)
		if err != nil || len(raw)%prefixLen != 0 {
			continue
		}
		set := make(map[uint64]struct{}, len(raw)/prefixLen)
		for i := 0; i < len(raw); i += prefixLen {
			var buf [8]byte
			copy(buf[8-prefixLen:], raw[i:i+prefixLen])
			set[binary.BigEndian.Uint64(buf[:])] = struct{}{}
		}
		if s.seen == nil {
			s.seen = map[string]map[uint64]struct{}{}
		}
		s.seen[d] = set
	}
	return nil
}

func prefix(id string) uint64 {
	var buf [8]byte
	if b, err := hex.DecodeString(id); err == nil && len(b) >= prefixLen {
		copy(buf[8-prefixLen:], b[:prefixLen])
	} else {
		// Not a hex id (tests, future sources): hash-free fallback.
		copy(buf[8-prefixLen:], id)
	}
	return binary.BigEndian.Uint64(buf[:])
}

func (s *IDs) has(date string, id string) bool {
	_, ok := s.seen[date][prefix(id)]
	return ok
}

// mark records the prefix k as counted on date.
func (s *IDs) mark(date string, k uint64) {
	if s.seen == nil {
		s.seen = map[string]map[uint64]struct{}{}
	}
	if s.seen[date] == nil {
		s.seen[date] = map[uint64]struct{}{}
	}
	s.seen[date][k] = struct{}{}
}

func (s *IDs) count() int {
	n := len(s.Live)
	for _, set := range s.seen {
		n += len(set)
	}
	return n
}

// Window is the pair of state.json fields the numbers live in.
type Window struct {
	Providers map[string]*Provider
	IDs       *IDs
}

// localDate is t's calendar date at offMin minutes east of UTC.
func localDate(t time.Time, offMin int) string {
	return t.UTC().Add(time.Duration(offMin) * time.Minute).Format(dateLayout)
}

// FirstDay is the oldest local date of the 30-day window ending today.
func FirstDay(now time.Time) string {
	return now.Local().AddDate(0, 0, -(Days - 1)).Format(dateLayout)
}

// firstHour is the start of the oldest hour of the 24-hour window.
func firstHour(now time.Time) int64 {
	return now.UTC().Truncate(time.Hour).Add(-(Hours - 1) * time.Hour).Unix()
}

func sum(t [4]int64) int64 { return t[0] + t[1] + t[2] + t[3] }

func (w *Window) provider(p string) *Provider {
	if w.Providers[p] == nil {
		w.Providers[p] = &Provider{}
	}
	return w.Providers[p]
}

// Add merges one usage event. Tokens are the site's: in + cacheW + cacheR + out.
func (w *Window) Add(e model.UsageEvent, now time.Time) {
	if w.IDs.Live == nil {
		w.IDs.Live = map[string]Live{}
	}
	tok := [4]int64{max(e.In, 0), max(e.CacheW, 0), max(e.CacheR, 0), max(e.Out, 0)}
	p := w.provider(e.Provider)
	date := localDate(e.TS, e.TZOffsetMin)
	at, total, delta := e.TS, sum(tok), int64(0)
	switch l, live := w.IDs.Live[e.ID]; {
	case live:
		for i := range tok {
			tok[i] = max(tok[i], l.T[i])
		}
		delta = sum(tok) - sum(l.T)
		l.T = tok
		w.IDs.Live[e.ID] = l
		at, date, total = l.TS, l.Date, sum(tok)
	case date < FirstDay(now) || (w.IDs.Sealed != "" && date <= w.IDs.Sealed) || w.IDs.has(date, e.ID):
		// Outside the window, or already counted.
	case now.Sub(e.TS) < LiveWindow:
		w.IDs.Live[e.ID] = Live{Provider: e.Provider, TS: e.TS, Date: date, T: tok}
		delta = total
	default:
		w.IDs.mark(date, prefix(e.ID))
		delta = total
	}
	if delta > 0 {
		p.addHour(at, delta, now)
		p.addDay(date, delta, now)
	}
	switch {
	case at.After(p.LastTS):
		p.LastTS, p.LastTokens, p.LastID = at, total, e.ID
	case e.ID == p.LastID:
		p.LastTokens = max(p.LastTokens, total)
	}
}

func (p *Provider) addHour(t time.Time, n int64, now time.Time) {
	at := t.UTC().Truncate(time.Hour).Unix()
	if at < firstHour(now) {
		return
	}
	i, found := slices.BinarySearchFunc(p.Hourly, at, func(h Hour, at int64) int { return cmp.Compare(h.At, at) })
	if !found {
		p.Hourly = slices.Insert(p.Hourly, i, Hour{At: at})
	}
	p.Hourly[i].Tokens += n
}

func (p *Provider) addDay(date string, n int64, now time.Time) {
	if date < FirstDay(now) {
		return
	}
	i, found := slices.BinarySearchFunc(p.Daily, date, func(d Day, date string) int { return cmp.Compare(d.Date, date) })
	if !found {
		p.Daily = slices.Insert(p.Daily, i, Day{Date: date})
	}
	p.Daily[i].Tokens += n
}

// Prune rolls the windows forward: hours and days that left them are
// dropped, live ids older than LiveWindow keep only their prefix, and the
// id set is capped at MaxIDs by sealing its oldest days.
func (w *Window) Prune(now time.Time) {
	first, fh := FirstDay(now), firstHour(now)
	for _, p := range w.Providers {
		p.Hourly = slices.DeleteFunc(p.Hourly, func(h Hour) bool { return h.At < fh })
		p.Daily = slices.DeleteFunc(p.Daily, func(d Day) bool { return d.Date < first })
	}
	for id, l := range w.IDs.Live {
		if now.Sub(l.TS) >= LiveWindow {
			delete(w.IDs.Live, id)
			if l.Date >= first {
				w.IDs.mark(l.Date, prefix(id))
			}
		}
	}
	for d := range w.IDs.seen {
		if d < first {
			delete(w.IDs.seen, d)
		}
	}
	if w.IDs.Sealed != "" && w.IDs.Sealed < first {
		w.IDs.Sealed = ""
	}
	for w.IDs.count() > MaxIDs && len(w.IDs.seen) > 0 {
		oldest := slices.Min(slices.Collect(maps.Keys(w.IDs.seen)))
		delete(w.IDs.seen, oldest)
		w.IDs.Sealed = max(w.IDs.Sealed, oldest)
	}
}

// Summary is what the menu shows for one provider.
type Summary struct {
	Provider   string
	LastTS     time.Time
	LastTokens int64
	// PastHour is the tokens of events in the last 60 minutes, PastDay
	// those of the 24-hour window (the last 24 UTC clock hours, the current
	// one included) and PastMonth those of the 30-day window.
	PastHour, PastDay, PastMonth int64
}

// Summaries lists every provider ever seen (in no particular order).
func (w *Window) Summaries(now time.Time) []Summary {
	first, fh := FirstDay(now), firstHour(now)
	var out []Summary
	for name, p := range w.Providers {
		if p.LastTS.IsZero() {
			continue
		}
		s := Summary{Provider: name, LastTS: p.LastTS, LastTokens: p.LastTokens}
		for _, h := range p.Hourly {
			if h.At >= fh {
				s.PastDay += h.Tokens
			}
		}
		for _, d := range p.Daily {
			if d.Date >= first {
				s.PastMonth += d.Tokens
			}
		}
		out = append(out, s)
	}
	for _, l := range w.IDs.Live {
		if age := now.Sub(l.TS); age < time.Hour {
			for i := range out {
				if out[i].Provider == l.Provider {
					out[i].PastHour += sum(l.T)
				}
			}
		}
	}
	return out
}

// Clone copies what Summaries reads (providers and live ids, not the
// prefix sets), for the UI to read while the next tick writes.
func (w *Window) Clone() *Window {
	c := &Window{Providers: make(map[string]*Provider, len(w.Providers)), IDs: &IDs{Live: maps.Clone(w.IDs.Live)}}
	for name, p := range w.Providers {
		cp := *p
		cp.Hourly, cp.Daily = slices.Clone(p.Hourly), slices.Clone(p.Daily)
		c.Providers[name] = &cp
	}
	return c
}
