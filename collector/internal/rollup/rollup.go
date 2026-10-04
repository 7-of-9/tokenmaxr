// Package rollup keeps this machine's full-history daily totals: tokens and
// prompts per local date, provider, source, model and account hash. It feeds
// aggregate-only destinations (the GitHub publisher), which never see events.
//
// It merges by event id with the server's invariant, exactly like
// internal/recent: a new id adds its tokens, a live id (young enough to still
// grow, see LiveWindow) adds only its fieldwise-max growth, and a known id adds
// nothing, so per-tick re-reads and the weekly full reparse never double count.
// Ids are kept as 48-bit prefixes per local date. Once a complete scan has run,
// dates older than SealAge are sealed: their totals stay, their ids are
// dropped, and no event dated on or before Sealed counts again. That keeps the
// file small; Rebuild starts over (with a full reparse) if old history appears
// later, e.g. a newly found WSL home.
package rollup

import (
	"cmp"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
)

const (
	// Version is the file format version.
	Version = 1
	// LiveWindow: how long a usage event keeps its values so that its growth
	// merges by fieldwise max (same as internal/recent).
	LiveWindow = 2 * time.Hour
	// SealAge: dates older than this are sealed after a complete scan.
	SealAge = 7 * 24 * time.Hour

	dateLayout = "2006-01-02"
	prefixLen  = 6
)

// Cell is one day's totals for one key.
type Cell struct {
	In, CacheW, CacheR, Out int64
	// Events is the usage events counted, Prompts the user prompts.
	Events, Prompts int64
}

// Tokens is the site's token total: in + cacheW + cacheR + out.
func (c Cell) Tokens() int64 { return c.In + c.CacheW + c.CacheR + c.Out }

func (c Cell) MarshalJSON() ([]byte, error) {
	return json.Marshal([6]int64{c.In, c.CacheW, c.CacheR, c.Out, c.Events, c.Prompts})
}

func (c *Cell) UnmarshalJSON(b []byte) error {
	var a [6]int64
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*c = Cell{In: a[0], CacheW: a[1], CacheR: a[2], Out: a[3], Events: a[4], Prompts: a[5]}
	return nil
}

// Key identifies a cell within a day.
type Key struct{ Provider, Source, Model, Acct string }

func (k Key) String() string { return k.Provider + "|" + k.Source + "|" + k.Model + "|" + k.Acct }

func parseKey(s string) (Key, bool) {
	p := strings.Split(s, "|")
	if len(p) != 4 {
		return Key{}, false
	}
	return Key{p[0], p[1], p[2], p[3]}, true
}

type live struct {
	Key  string    `json:"k"`
	Date string    `json:"d"`
	TS   time.Time `json:"ts"`
	T    [4]int64  `json:"t"`
}

// Rollup is the in-memory rollup; Load and Save move it to and from disk.
type Rollup struct {
	// Days is local date -> key -> totals.
	Days map[string]map[string]*Cell
	// Sealed: no event dated on or before it counts again.
	Sealed string

	live  map[string]live
	seen  map[string]map[uint64]struct{} // usage ids per date
	seenA map[string]map[uint64]struct{} // activity (prompt) ids per date
	dirty bool
}

// New returns an empty rollup.
func New() *Rollup {
	return &Rollup{Days: map[string]map[string]*Cell{}, live: map[string]live{},
		seen: map[string]map[uint64]struct{}{}, seenA: map[string]map[uint64]struct{}{}}
}

func localDate(t time.Time, offMin int) string {
	return t.UTC().Add(time.Duration(offMin) * time.Minute).Format(dateLayout)
}

func prefix(id string) uint64 {
	var buf [8]byte
	if b, err := hex.DecodeString(id); err == nil && len(b) >= prefixLen {
		copy(buf[8-prefixLen:], b[:prefixLen])
	} else {
		copy(buf[8-prefixLen:], id)
	}
	return binary.BigEndian.Uint64(buf[:])
}

func mark(sets map[string]map[uint64]struct{}, date string, k uint64) {
	if sets[date] == nil {
		sets[date] = map[uint64]struct{}{}
	}
	sets[date][k] = struct{}{}
}

func (r *Rollup) cell(date, key string) *Cell {
	if r.Days[date] == nil {
		r.Days[date] = map[string]*Cell{}
	}
	c := r.Days[date][key]
	if c == nil {
		c = &Cell{}
		r.Days[date][key] = c
	}
	return c
}

func (r *Rollup) sealed(date string) bool { return r.Sealed != "" && date <= r.Sealed }

// AddUsage merges one usage event.
func (r *Rollup) AddUsage(e model.UsageEvent, now time.Time) {
	tok := [4]int64{max(e.In, 0), max(e.CacheW, 0), max(e.CacheR, 0), max(e.Out, 0)}
	date := localDate(e.TS, e.TZOffsetMin)
	key := Key{e.Provider, e.Source, e.Model, e.Acct}.String()
	if l, ok := r.live[e.ID]; ok {
		var grow [4]int64
		changed := false
		for i := range tok {
			if tok[i] > l.T[i] {
				grow[i] = tok[i] - l.T[i]
				l.T[i] = tok[i]
				changed = true
			}
		}
		if changed {
			c := r.cell(l.Date, l.Key)
			c.In, c.CacheW, c.CacheR, c.Out = c.In+grow[0], c.CacheW+grow[1], c.CacheR+grow[2], c.Out+grow[3]
			r.live[e.ID] = l
			r.dirty = true
		}
		return
	}
	if r.sealed(date) {
		return
	}
	if _, ok := r.seen[date][prefix(e.ID)]; ok {
		return
	}
	c := r.cell(date, key)
	c.In, c.CacheW, c.CacheR, c.Out = c.In+tok[0], c.CacheW+tok[1], c.CacheR+tok[2], c.Out+tok[3]
	c.Events++
	if now.Sub(e.TS) < LiveWindow {
		r.live[e.ID] = live{Key: key, Date: date, TS: e.TS, T: tok}
	} else {
		mark(r.seen, date, prefix(e.ID))
	}
	r.dirty = true
}

// AddActivity counts one user prompt (activity event) once.
func (r *Rollup) AddActivity(e model.ActivityEvent) {
	date := localDate(e.TS, e.TZOffsetMin)
	if r.sealed(date) {
		return
	}
	k := prefix(e.ID)
	if _, ok := r.seenA[date][k]; ok {
		return
	}
	mark(r.seenA, date, k)
	r.cell(date, Key{e.Provider, e.Source, "", e.Acct}.String()).Prompts++
	r.dirty = true
}

// Settle retires live ids older than LiveWindow to the date sets and, after a
// complete scan (no backfill pending anywhere), seals dates older than SealAge.
func (r *Rollup) Settle(now time.Time, scanComplete bool) {
	for id, l := range r.live {
		if now.Sub(l.TS) >= LiveWindow {
			delete(r.live, id)
			mark(r.seen, l.Date, prefix(id))
			r.dirty = true
		}
	}
	if !scanComplete {
		return
	}
	cut := now.Add(-SealAge).Format(dateLayout)
	// Never seal a date that still has a live (growing) event: stop the day
	// before the oldest one.
	for _, l := range r.live {
		if l.Date <= cut {
			if d, err := time.Parse(dateLayout, l.Date); err == nil {
				cut = d.AddDate(0, 0, -1).Format(dateLayout)
			}
		}
	}
	if cut <= r.Sealed {
		return
	}
	r.Sealed = cut
	for _, sets := range []map[string]map[uint64]struct{}{r.seen, r.seenA} {
		for d := range sets {
			if d <= cut {
				delete(sets, d)
			}
		}
	}
	r.dirty = true
}

// Dirty reports whether the rollup changed since it was loaded or saved.
func (r *Rollup) Dirty() bool { return r.dirty }

// Row is one published daily total.
type Row struct {
	Date string `json:"date"`
	Key
	Cell
}

// Rows lists every daily total in date then key order.
func (r *Rollup) Rows() []Row {
	var out []Row
	for _, date := range slices.Sorted(maps.Keys(r.Days)) {
		cells := r.Days[date]
		for _, ks := range slices.Sorted(maps.Keys(cells)) {
			k, ok := parseKey(ks)
			if !ok {
				continue
			}
			out = append(out, Row{Date: date, Key: k, Cell: *cells[ks]})
		}
	}
	slices.SortStableFunc(out, func(a, b Row) int { return cmp.Compare(a.Date, b.Date) })
	return out
}

// --- persistence ---

type fileJSON struct {
	V      int                         `json:"v"`
	Sealed string                      `json:"sealed,omitempty"`
	Days   map[string]map[string]*Cell `json:"days"`
	Live   map[string]live             `json:"live,omitempty"`
	Seen   map[string]string           `json:"seen,omitempty"`
	SeenA  map[string]string           `json:"seenA,omitempty"`
}

func packSets(sets map[string]map[uint64]struct{}) map[string]string {
	if len(sets) == 0 {
		return nil
	}
	out := make(map[string]string, len(sets))
	for d, set := range sets {
		keys := slices.Sorted(maps.Keys(set))
		b := make([]byte, 0, prefixLen*len(keys))
		for _, k := range keys {
			var buf [8]byte
			binary.BigEndian.PutUint64(buf[:], k)
			b = append(b, buf[8-prefixLen:]...)
		}
		out[d] = base64.RawStdEncoding.EncodeToString(b)
	}
	return out
}

func unpackSets(in map[string]string) map[string]map[uint64]struct{} {
	out := map[string]map[uint64]struct{}{}
	for d, enc := range in {
		raw, err := base64.RawStdEncoding.DecodeString(enc)
		if err != nil || len(raw)%prefixLen != 0 {
			continue // that date may count a re-read event once more, never lose one
		}
		set := make(map[uint64]struct{}, len(raw)/prefixLen)
		for i := 0; i < len(raw); i += prefixLen {
			var buf [8]byte
			copy(buf[8-prefixLen:], raw[i:i+prefixLen])
			set[binary.BigEndian.Uint64(buf[:])] = struct{}{}
		}
		out[d] = set
	}
	return out
}

// ErrVersion: the file was written by an incompatible version.
var ErrVersion = errors.New("rollup: unsupported file version")

// Load reads path; a missing file is an empty rollup.
func Load(path string) (*Rollup, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return nil, err
	}
	var f fileJSON
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	if f.V != Version {
		return nil, ErrVersion
	}
	r := New()
	r.Sealed = f.Sealed
	if f.Days != nil {
		r.Days = f.Days
	}
	if f.Live != nil {
		r.live = f.Live
	}
	r.seen, r.seenA = unpackSets(f.Seen), unpackSets(f.SeenA)
	return r, nil
}

// Save writes path atomically and clears Dirty.
func (r *Rollup) Save(path string) error {
	b, err := json.Marshal(fileJSON{V: Version, Sealed: r.Sealed, Days: r.Days, Live: r.live, Seen: packSets(r.seen), SeenA: packSets(r.seenA)})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	r.dirty = false
	return nil
}
