// Package rollup keeps this machine's full-history daily totals: tokens and
// prompts per local date, provider, source, model, account hash and event
// quality. It feeds aggregate-only destinations (the GitHub publisher), which
// never see events, and it never holds prompt text.
//
// It merges by event id with the server's invariant, exactly like
// internal/recent: a new id adds its tokens, a live id (young enough to still
// grow, see LiveWindow) adds only its fieldwise-max growth, and a known id adds
// nothing, so per-tick re-reads and the weekly full reparse never double count.
// Ids are kept as 48-bit prefixes per UTC day of the event, which no change of
// the machine's time zone moves: an id stays on the local date of its first
// sighting, as the server pins it. Once a complete scan has run, dates older
// than SealAge are sealed: their totals stay, their ids are dropped, and no
// event dated on or before Sealed counts again. That keeps the file small;
// Rebuild starts over (with a full reparse) if old history appears later,
// e.g. a newly found WSL home.
//
// The rollup is the only full record of sealed history (its logs may be gone
// since), so a rebuild never throws an older file away: Salvage keeps its
// daily totals as a floor, and ApplyFloor puts back whatever the logs no
// longer hold once the rebuild is complete.
//
// Prompts are counted twice over, like the server does: activity events give
// the prompts per account (and how many never had their tokens recorded), and
// prompt records give the prompts per model. Both keep the server's merge: a
// prompt first seen without usage (or without a model) is revised when a
// later sighting has it, until its date is sealed.
//
// Account history (provider account totals, internal/accountusage) is
// reconciled by the reader against local tokens per UTC day, never per local
// date. So the rollup also keeps the server's account ledger
// (api/src/lib/account-usage.js accountLedger): tokens per UTC day, provider,
// source and account, the account only when its attribution is labelled
// quality. It grows with the same merge as the daily cells, so it counts
// exactly the tokens they count. The newest account totals are kept beside it.
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
	// Version is the file format version. A file of another version does not
	// load, and the caller rebuilds the rollup from all history (version 2
	// added the quality key and the cacheW1h, reasoning and prompt columns;
	// version 3 the account ledger, which must cover all history: a ledger
	// that misses local tokens would let account history count them again;
	// version 4 keys the id sets by UTC day). An older file is salvaged
	// (Salvage), never dropped.
	Version = 4
	// LiveWindow: how long a usage event keeps its values so that its growth
	// merges by fieldwise max (same as internal/recent).
	LiveWindow = 2 * time.Hour
	// SealAge: dates older than this are sealed after a complete scan.
	SealAge = 7 * 24 * time.Hour

	dateLayout = "2006-01-02"
	prefixLen  = 6
)

// Cell is one day's totals for one key. CacheW1h is a subset of CacheW and
// Reasoning of Out (model.Tokens), so Tokens never counts them twice.
type Cell struct {
	In, CacheW, CacheW1h, CacheR, Out, Reasoning int64
	// Events is the usage events counted, Prompts the user prompts (activity
	// events) and PromptsNoUsage those of them whose tokens were never
	// recorded. ModelPrompts counts prompt records on the cell of their model.
	Events, Prompts, PromptsNoUsage, ModelPrompts int64
}

// Tokens is the site's token total: in + cacheW + cacheR + out.
func (c Cell) Tokens() int64 { return c.In + c.CacheW + c.CacheR + c.Out }

func (c Cell) MarshalJSON() ([]byte, error) {
	return json.Marshal([10]int64{c.In, c.CacheW, c.CacheW1h, c.CacheR, c.Out, c.Reasoning, c.Events, c.Prompts, c.PromptsNoUsage, c.ModelPrompts})
}

func (c *Cell) UnmarshalJSON(b []byte) error {
	var a [10]int64
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*c = Cell{In: a[0], CacheW: a[1], CacheW1h: a[2], CacheR: a[3], Out: a[4], Reasoning: a[5],
		Events: a[6], Prompts: a[7], PromptsNoUsage: a[8], ModelPrompts: a[9]}
	return nil
}

// QEstimated marks the cells of estimated usage events (Key.Q); exact ones
// have "". Like the server, only model.QualityEstimated is estimated.
const QEstimated = "e"

// QualityCode is the Key.Q of an event quality.
func QualityCode(q string) string {
	if q == model.QualityEstimated {
		return QEstimated
	}
	return ""
}

// Key identifies a cell within a day. Prompts are keyed with Q "".
type Key struct{ Provider, Source, Model, Acct, Q string }

func (k Key) String() string {
	return k.Provider + "|" + k.Source + "|" + k.Model + "|" + k.Acct + "|" + k.Q
}

func parseKey(s string) (Key, bool) {
	p := strings.Split(s, "|")
	if len(p) != 5 {
		return Key{}, false
	}
	return Key{p[0], p[1], p[2], p[3], p[4]}, true
}

// tokens are the merged token fields of a live event, in Cell order.
type tokens [6]int64

func (t tokens) addTo(c *Cell) {
	c.In, c.CacheW, c.CacheW1h = c.In+t[0], c.CacheW+t[1], c.CacheW1h+t[2]
	c.CacheR, c.Out, c.Reasoning = c.CacheR+t[3], c.Out+t[4], c.Reasoning+t[5]
}

// ledgerTokens is the part of t the account ledger sums: in + cacheW + cacheR
// + out, like Cell.Tokens and the server's ledger.
func (t tokens) ledgerTokens() int64 { return t[0] + t[1] + t[3] + t[4] }

type live struct {
	Key  string    `json:"k"`
	Date string    `json:"d"`
	TS   time.Time `json:"ts"`
	T    tokens    `json:"t"`
	// L is the event's account ledger entry: UTC date + "|" + ledger key.
	L string `json:"l,omitempty"`
}

// LedgerKey is the account ledger key of an event: provider|source|acct. The
// account is kept only for labelled attribution (model.LabelledQ), as on the
// server: a weak candidate is not evidence that tokens belong to an account,
// so they stay "" and are reserved against every account of that source.
func LedgerKey(provider, source, acct, acctQ string) string {
	if !model.LabelledQ(acctQ) {
		acct = ""
	}
	return provider + "|" + source + "|" + acct
}

// LedgerRow is one account ledger entry: local tokens on a UTC day.
type LedgerRow struct {
	Date, Provider, Source, Acct string
	Tokens                       int64
}

// AccountTotal is a provider's account-wide total for one UTC day (one
// model.AccountUsageSnapshot, without its id or reporter).
type AccountTotal struct {
	Provider    string    `json:"provider"`
	Source      string    `json:"source"`
	Acct        string    `json:"acct"`
	Date        string    `json:"date"`
	TotalTokens int64     `json:"totalTokens"`
	ObservedAt  time.Time `json:"observedAt"`
}

func (t AccountTotal) key() string { return t.Date + "|" + t.Source + "|" + t.Acct }

// Rollup is the in-memory rollup; Load and Save move it to and from disk.
type Rollup struct {
	// Days is local date -> key -> totals.
	Days map[string]map[string]*Cell
	// Sealed: no event dated on or before it counts again.
	Sealed string
	// Ledger is UTC date -> LedgerKey -> tokens (never sealed or pruned: one
	// entry per UTC day, provider, source and account).
	Ledger map[string]map[string]int64

	accounts map[string]AccountTotal // date|source|acct -> newest total
	// accountsStale: the account totals were lost with an older file and not
	// read again yet (cleared by AddAccountUsage).
	accountsStale bool
	live          map[string]live
	// The id sets are per UTC day of the event (bucket), not per local date.
	seen  map[string]map[uint64]struct{} // usage ids
	seenA map[string]map[uint64]struct{} // activity (prompt) ids
	seenP map[string]map[uint64]struct{} // prompt record ids
	// Revisable counts, per UTC day: id -> where it was counted (local date
	// + "|" + cell key).
	noUsage map[string]map[uint64]string // activity counted in PromptsNoUsage
	noModel map[string]map[uint64]string // prompt record counted with no model
	last    time.Time                    // the newest usage or activity event
	// floor is an older file's totals, kept through a rebuild (Salvage);
	// unledgered the "date|provider|source" cells it had to fill in.
	floor      *floor
	unledgered map[string]bool
	dirty      bool
}

// New returns an empty rollup.
func New() *Rollup {
	return &Rollup{Days: map[string]map[string]*Cell{}, Ledger: map[string]map[string]int64{},
		accounts: map[string]AccountTotal{}, live: map[string]live{},
		seen: map[string]map[uint64]struct{}{}, seenA: map[string]map[uint64]struct{}{}, seenP: map[string]map[uint64]struct{}{},
		noUsage: map[string]map[uint64]string{}, noModel: map[string]map[uint64]string{}, unledgered: map[string]bool{}}
}

func localDate(t time.Time, offMin int) string {
	return t.UTC().Add(time.Duration(offMin) * time.Minute).Format(dateLayout)
}

// bucket is the UTC day an id set keeps an event's id under. A local date is
// within a day of it (offsets are under 24 hours).
func bucket(t time.Time) string { return t.UTC().Format(dateLayout) }

// counted is where a revisable id was counted: its local date and cell key.
func counted(date, key string) string { return date + "|" + key }

func splitCounted(s string) (date, key string) {
	date, key, _ = strings.Cut(s, "|")
	return date, key
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

func markKey(sets map[string]map[uint64]string, date string, k uint64, key string) {
	if sets[date] == nil {
		sets[date] = map[uint64]string{}
	}
	sets[date][k] = key
}

// revise takes id k off its revisable set and returns the key it was counted on.
func revise(sets map[string]map[uint64]string, date string, k uint64) (string, bool) {
	key, ok := sets[date][k]
	if ok {
		delete(sets[date], k)
		if len(sets[date]) == 0 {
			delete(sets, date)
		}
	}
	return key, ok
}

// saw keeps the newest event time.
func (r *Rollup) saw(ts time.Time) {
	if ts.After(r.last) {
		r.last = ts.UTC()
		r.dirty = true
	}
}

// LastEvent is the time of the newest usage or activity event added (zero
// when none).
func (r *Rollup) LastEvent() time.Time { return r.last }

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

// addLedger adds n tokens to entry (UTC date + "|" + LedgerKey).
func (r *Rollup) addLedger(entry string, n int64) {
	date, key, ok := strings.Cut(entry, "|")
	if !ok {
		return
	}
	if r.Ledger[date] == nil {
		r.Ledger[date] = map[string]int64{}
	}
	r.Ledger[date][key] += n
}

// LedgerRows lists the account ledger in date then key order.
func (r *Rollup) LedgerRows() []LedgerRow {
	var out []LedgerRow
	for _, date := range slices.Sorted(maps.Keys(r.Ledger)) {
		for _, key := range slices.Sorted(maps.Keys(r.Ledger[date])) {
			p := strings.SplitN(key, "|", 3)
			if len(p) != 3 {
				continue
			}
			out = append(out, LedgerRow{Date: date, Provider: p[0], Source: p[1], Acct: p[2], Tokens: r.Ledger[date][key]})
		}
	}
	return out
}

// AddAccountUsage keeps the newest provider total per UTC day, source and
// account (the server's upsertAccountUsage: a newer reading wins, also when
// the provider corrects a total down; at the same time the larger wins). An
// unchanged total keeps the time it was first read, so re-reading the same
// history every few minutes changes nothing that is published. Call it with
// every successful read (even of no totals): that ends AccountsStale.
func (r *Rollup) AddAccountUsage(snaps []model.AccountUsageSnapshot) {
	if r.accountsStale {
		r.accountsStale, r.dirty = false, true
	}
	for _, s := range snaps {
		if s.Acct == "" || s.Timezone != "UTC" || s.TotalTokens < 0 || s.ObservedAt.IsZero() {
			continue
		}
		if d, err := time.Parse(dateLayout, s.Date); err != nil || d.Format(dateLayout) != s.Date {
			continue
		}
		t := AccountTotal{Provider: s.Provider, Source: s.Source, Acct: s.Acct, Date: s.Date, TotalTokens: s.TotalTokens, ObservedAt: s.ObservedAt.UTC()}
		have, ok := r.accounts[t.key()]
		switch {
		case ok && have.TotalTokens == t.TotalTokens:
			continue
		case ok && (have.ObservedAt.After(t.ObservedAt) || have.ObservedAt.Equal(t.ObservedAt) && have.TotalTokens > t.TotalTokens):
			continue
		}
		r.accounts[t.key()] = t
		r.dirty = true
	}
}

// AccountsStale reports that a rebuild lost the account totals and no read
// has brought them back yet: publishing them now would withdraw them.
func (r *Rollup) AccountsStale() bool { return r.accountsStale }

// AccountUsage lists the newest account totals in date, source, account order.
func (r *Rollup) AccountUsage() []AccountTotal {
	out := make([]AccountTotal, 0, len(r.accounts))
	for _, k := range slices.Sorted(maps.Keys(r.accounts)) {
		out = append(out, r.accounts[k])
	}
	return out
}

// AddUsage merges one usage event.
func (r *Rollup) AddUsage(e model.UsageEvent, now time.Time) {
	r.saw(e.TS)
	tok := tokens{max(e.In, 0), max(e.CacheW, 0), max(e.CacheW1h, 0), max(e.CacheR, 0), max(e.Out, 0), max(e.Reasoning, 0)}
	date := localDate(e.TS, e.TZOffsetMin)
	key := Key{e.Provider, e.Source, e.Model, e.Acct, QualityCode(e.Q)}.String()
	if l, ok := r.live[e.ID]; ok {
		var grow tokens
		changed := false
		for i := range tok {
			if tok[i] > l.T[i] {
				grow[i] = tok[i] - l.T[i]
				l.T[i] = tok[i]
				changed = true
			}
		}
		if changed {
			grow.addTo(r.cell(l.Date, l.Key))
			r.addLedger(l.L, grow.ledgerTokens())
			r.live[e.ID] = l
			r.dirty = true
		}
		return
	}
	if r.sealed(date) {
		return
	}
	if _, ok := r.seen[bucket(e.TS)][prefix(e.ID)]; ok {
		return
	}
	c := r.cell(date, key)
	tok.addTo(c)
	c.Events++
	entry := bucket(e.TS) + "|" + LedgerKey(e.Provider, e.Source, e.Acct, e.AcctQ)
	r.addLedger(entry, tok.ledgerTokens())
	if now.Sub(e.TS) < LiveWindow {
		r.live[e.ID] = live{Key: key, Date: date, TS: e.TS, T: tok, L: entry}
	} else {
		mark(r.seen, bucket(e.TS), prefix(e.ID))
	}
	r.dirty = true
}

// AddActivity counts one user prompt (activity event) once, in PromptsNoUsage
// too while no sighting of it has usage: HasUsage merges by OR, as on the
// server, so a later sighting with usage takes it back out (until sealed).
func (r *Rollup) AddActivity(e model.ActivityEvent) {
	r.saw(e.TS)
	date := localDate(e.TS, e.TZOffsetMin)
	if r.sealed(date) {
		return
	}
	k := prefix(e.ID)
	if _, ok := r.seenA[bucket(e.TS)][k]; ok {
		if !e.HasUsage {
			return
		}
		if at, ok := revise(r.noUsage, bucket(e.TS), k); ok {
			if d, key := splitCounted(at); !r.sealed(d) {
				if c := r.Days[d][key]; c != nil && c.PromptsNoUsage > 0 {
					c.PromptsNoUsage--
				}
			}
			r.dirty = true
		}
		return
	}
	mark(r.seenA, bucket(e.TS), k)
	key := Key{e.Provider, e.Source, "", e.Acct, ""}.String()
	c := r.cell(date, key)
	c.Prompts++
	if !e.HasUsage {
		c.PromptsNoUsage++
		markKey(r.noUsage, bucket(e.TS), k, counted(date, key))
	}
	r.dirty = true
}

// AddPrompt counts one prompt record once, on the cell of its model (Q "").
// Only its id, time, provider, source, model and account are read: the text
// never enters the rollup. A record first seen without a model moves to its
// model when a later sighting knows it (until sealed); one that never learns
// it stays on the model "" cell.
func (r *Rollup) AddPrompt(p model.PromptRecord) {
	date := localDate(p.TS, p.TZOffsetMin)
	if r.sealed(date) {
		return
	}
	k := prefix(p.ID)
	key := Key{p.Provider, p.Source, p.Model, p.Acct, ""}.String()
	if _, ok := r.seenP[bucket(p.TS)][k]; ok {
		if p.Model == "" {
			return
		}
		if at, ok := revise(r.noModel, bucket(p.TS), k); ok {
			// It moves within the date it was counted on (its first sighting's).
			if d, was := splitCounted(at); !r.sealed(d) {
				if c := r.Days[d][was]; c != nil && c.ModelPrompts > 0 {
					c.ModelPrompts--
					r.cell(d, key).ModelPrompts++
				}
			}
			r.dirty = true
		}
		return
	}
	mark(r.seenP, bucket(p.TS), k)
	r.cell(date, key).ModelPrompts++
	if p.Model == "" {
		markKey(r.noModel, bucket(p.TS), k, counted(date, key))
	}
	r.dirty = true
}

// Settle retires live ids older than LiveWindow to the date sets and, after a
// complete scan (no backfill pending anywhere), seals dates older than SealAge.
func (r *Rollup) Settle(now time.Time, scanComplete bool) {
	for id, l := range r.live {
		if now.Sub(l.TS) >= LiveWindow {
			delete(r.live, id)
			mark(r.seen, bucket(l.TS), prefix(id))
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
	// An id of UTC day cut can still be on local date cut+1 (east of UTC), so
	// a UTC day's ids go only once every local date it can reach is sealed.
	for _, sets := range []map[string]map[uint64]struct{}{r.seen, r.seenA, r.seenP} {
		for d := range sets {
			if d < cut {
				delete(sets, d)
			}
		}
	}
	// A sealed date's revisable counts are final.
	for _, sets := range []map[string]map[uint64]string{r.noUsage, r.noModel} {
		for d, ids := range sets {
			for k, at := range ids {
				if date, _ := splitCounted(at); d < cut || date <= cut {
					delete(ids, k)
				}
			}
			if len(ids) == 0 {
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

// --- an older file, through a rebuild ---

// floor is what an older file held: its daily totals (as this version's
// cells; columns it did not have stay zero) and, per local date it had not
// sealed, the ids it had counted (packed, like fileJSON's sets).
type floor struct {
	Days  map[string]map[string]*Cell `json:"days"`
	Seen  map[string]string           `json:"seen,omitempty"`
	SeenA map[string]string           `json:"seenA,omitempty"`
	SeenP map[string]string           `json:"seenP,omitempty"`
}

// fields are a cell's counters in file order.
func (c *Cell) fields() [10]*int64 {
	return [10]*int64{&c.In, &c.CacheW, &c.CacheW1h, &c.CacheR, &c.Out, &c.Reasoning, &c.Events, &c.Prompts, &c.PromptsNoUsage, &c.ModelPrompts}
}

// ledgerField reports whether counter i is one the account ledger sums.
func ledgerField(i int) bool { return i == 0 || i == 1 || i == 3 || i == 4 }

// Salvage starts the rebuild of a rollup whose file at path does not load
// (an older version, or damaged): an empty rollup that keeps the old file's
// daily totals as its floor (ApplyFloor) and, when the file had them, its
// account totals. Whatever it cannot read is left out; without account totals
// the rollup is AccountsStale until the next read.
func Salvage(path string) *Rollup {
	r := New()
	r.accountsStale = true
	b, err := os.ReadFile(path)
	if err != nil {
		return r
	}
	var old struct {
		V    int                                   `json:"v"`
		Days map[string]map[string]json.RawMessage `json:"days"`
		Live map[string]struct {
			Date string `json:"d"`
		} `json:"live"`
		Seen     map[string]string `json:"seen"`
		SeenA    map[string]string `json:"seenA"`
		SeenP    map[string]string `json:"seenP"`
		Accounts []AccountTotal    `json:"accounts"`
	}
	if json.Unmarshal(b, &old) != nil || old.V < 1 || old.V >= Version {
		return r // damaged, or a newer format than this version reads
	}
	f := &floor{Days: map[string]map[string]*Cell{}}
	for date, cells := range old.Days {
		if d, err := time.Parse(dateLayout, date); err != nil || d.Format(dateLayout) != date {
			continue
		}
		for ks, raw := range cells {
			var a []int64
			if json.Unmarshal(raw, &a) != nil {
				continue
			}
			var c Cell
			switch {
			case old.V == 1 && len(a) == 6 && strings.Count(ks, "|") == 3:
				// Version 1: in, cacheW, cacheR, out, events, prompts, and
				// no quality key (its cells hold exact and estimated alike).
				ks += "|"
				c = Cell{In: a[0], CacheW: a[1], CacheR: a[2], Out: a[3], Events: a[4], Prompts: a[5]}
			case old.V > 1 && len(a) == 10 && strings.Count(ks, "|") == 4:
				for i, p := range c.fields() {
					*p = a[i]
				}
			default:
				continue
			}
			if f.Days[date] == nil {
				f.Days[date] = map[string]*Cell{}
			}
			f.Days[date][ks] = &c
		}
	}
	// Every version keyed its id sets by local date; a live id is counted too.
	seen := unpackSets(old.Seen)
	for id, l := range old.Live {
		mark(seen, l.Date, prefix(id))
	}
	f.Seen, f.SeenA, f.SeenP = packSets(seen), old.SeenA, old.SeenP
	r.floor = f
	for _, t := range old.Accounts {
		r.accounts[t.key()] = t
	}
	r.accountsStale = len(r.accounts) == 0
	r.dirty = true
	return r
}

// ApplyFloor ends a rebuild that started from Salvage: wherever the old
// file's totals are higher than the rebuilt ones, the difference is put back,
// so history whose logs are gone (deleted, pruned, or in a WSL distro that
// was not running) is never lost. It compares per local date, provider,
// source and model, counter by counter, and keeps the larger: history re-read
// under another account or quality key is never counted twice. What it puts
// back has no account ledger (its UTC days are unknown), so those dates are
// recorded as unledgered; and the ids the old file had counted on those
// dates join the id sets, so a later re-read of them adds nothing again.
func (r *Rollup) ApplyFloor() {
	f := r.floor
	if f == nil {
		return
	}
	r.floor, r.dirty = nil, true
	filled := map[string]bool{}
	for _, date := range slices.Sorted(maps.Keys(f.Days)) {
		type level struct {
			old, now [10]int64
			keys     []string
		}
		levels := map[string]*level{} // provider|source|model
		name := func(k Key) string { return k.Provider + "|" + k.Source + "|" + k.Model }
		for ks, c := range f.Days[date] {
			k, ok := parseKey(ks)
			if !ok {
				continue
			}
			l := levels[name(k)]
			if l == nil {
				l = &level{}
				levels[name(k)] = l
			}
			for i, p := range c.fields() {
				l.old[i] += *p
			}
			l.keys = append(l.keys, ks)
		}
		for ks, c := range r.Days[date] {
			if k, ok := parseKey(ks); ok && levels[name(k)] != nil {
				for i, p := range c.fields() {
					levels[name(k)].now[i] += *p
				}
			}
		}
		for _, n := range slices.Sorted(maps.Keys(levels)) {
			l := levels[n]
			slices.Sort(l.keys)
			for i := range l.old {
				// The missing part goes to the old cells that hold more than
				// their rebuilt namesakes, which always add up to enough.
				need := l.old[i] - l.now[i]
				for _, ks := range l.keys {
					if need <= 0 {
						break
					}
					var have int64
					if c := r.Days[date][ks]; c != nil {
						have = *c.fields()[i]
					}
					give := min(need, *f.Days[date][ks].fields()[i]-have)
					if give <= 0 {
						continue
					}
					*r.cell(date, ks).fields()[i] += give
					need -= give
					filled[date] = true
					if ledgerField(i) {
						k, _ := parseKey(ks)
						r.unledgered[date+"|"+k.Provider+"|"+k.Source] = true
					}
				}
			}
		}
	}
	// A local date's ids are under its UTC day or one either side.
	for _, s := range []struct {
		old  map[string]string
		sets map[string]map[uint64]struct{}
	}{{f.Seen, r.seen}, {f.SeenA, r.seenA}, {f.SeenP, r.seenP}} {
		for date, ids := range unpackSets(s.old) {
			d, err := time.Parse(dateLayout, date)
			if err != nil || !filled[date] {
				continue
			}
			for _, b := range []string{d.AddDate(0, 0, -1).Format(dateLayout), date, d.AddDate(0, 0, 1).Format(dateLayout)} {
				for k := range ids {
					mark(s.sets, b, k)
				}
			}
		}
	}
}

// DaySource is one local date of a provider and source.
type DaySource struct{ Date, Provider, Source string }

// Unledgered lists, in order, the local dates whose totals of a provider and
// source came partly from an older file (ApplyFloor): the account ledger
// does not hold those tokens, so account history must not be reconciled
// against it there.
func (r *Rollup) Unledgered() []DaySource {
	var out []DaySource
	for _, s := range slices.Sorted(maps.Keys(r.unledgered)) {
		if p := strings.SplitN(s, "|", 3); len(p) == 3 {
			out = append(out, DaySource{p[0], p[1], p[2]})
		}
	}
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
	SeenP  map[string]string           `json:"seenP,omitempty"`
	// NoUsage and NoModel are date -> cell key -> packed ids.
	NoUsage map[string]map[string]string `json:"noUsage,omitempty"`
	NoModel map[string]map[string]string `json:"noModel,omitempty"`
	Last    time.Time                    `json:"last,omitzero"`
	// Ledger is UTC date -> ledger key -> tokens; Accounts the newest
	// provider account totals.
	Ledger        map[string]map[string]int64 `json:"ledger,omitempty"`
	Accounts      []AccountTotal              `json:"accounts,omitempty"`
	AccountsStale bool                        `json:"accountsStale,omitempty"`
	// Floor is an older file's totals while a rebuild runs; Unledgered the
	// "date|provider|source" cells ApplyFloor filled in.
	Floor      *floor   `json:"floor,omitempty"`
	Unledgered []string `json:"unledgered,omitempty"`
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

func packKeyed(sets map[string]map[uint64]string) map[string]map[string]string {
	if len(sets) == 0 {
		return nil
	}
	out := make(map[string]map[string]string, len(sets))
	for d, ids := range sets {
		byKey := map[string]map[uint64]struct{}{}
		for k, key := range ids {
			mark(byKey, key, k)
		}
		out[d] = packSets(byKey)
	}
	return out
}

func unpackKeyed(in map[string]map[string]string) map[string]map[uint64]string {
	out := map[string]map[uint64]string{}
	for d, enc := range in {
		for key, set := range unpackSets(enc) {
			for k := range set {
				markKey(out, d, k, key)
			}
		}
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
	r.seen, r.seenA, r.seenP = unpackSets(f.Seen), unpackSets(f.SeenA), unpackSets(f.SeenP)
	r.noUsage, r.noModel = unpackKeyed(f.NoUsage), unpackKeyed(f.NoModel)
	r.last = f.Last
	if f.Ledger != nil {
		r.Ledger = f.Ledger
	}
	for _, t := range f.Accounts {
		r.accounts[t.key()] = t
	}
	r.accountsStale, r.floor = f.AccountsStale, f.Floor
	for _, s := range f.Unledgered {
		r.unledgered[s] = true
	}
	return r, nil
}

// Save writes path atomically and clears Dirty.
func (r *Rollup) Save(path string) error {
	b, err := json.Marshal(fileJSON{V: Version, Sealed: r.Sealed, Days: r.Days, Live: r.live,
		Seen: packSets(r.seen), SeenA: packSets(r.seenA), SeenP: packSets(r.seenP),
		NoUsage: packKeyed(r.noUsage), NoModel: packKeyed(r.noModel), Last: r.last,
		Ledger: r.Ledger, Accounts: r.AccountUsage(), AccountsStale: r.accountsStale,
		Floor: r.floor, Unledgered: slices.Sorted(maps.Keys(r.unledgered))})
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
