// Package evidence attributes historical events to accounts from what the
// tools themselves recorded (docs/agents/SPEC.md "Accounts"): per-line
// identity records (Claude credential_org, Codex creator_account_id, Grok
// user_info), login boundaries, identity snapshots and plan lineage.
//
// Harvesters (claude.go, codex.go, grok.go, gemini.go, cursor.go) read only
// identity fields into narrow structs and hash every native id at once with
// the fleet key, so a Record never holds a raw id, email, token, prompt text
// or path. The Index built from the records answers, per event, the steps of
// the SPEC's attribution order; accounts.Resolve combines it with the live
// probe timeline.
package evidence

import (
	"cmp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
)

// AttribVersion is bumped whenever attribution gets stronger evidence. A
// state below it resets the source cursors once, so history is re-emitted
// and the server upgrades it in place (merge keeps the higher acctQ).
const AttribVersion = 1

// Record kinds.
const (
	// KindSample is an identity seen at TS (or throughout TS..To).
	KindSample = "sample"
	// KindBoundary is a login (or logout) that names no identity: it only
	// splits intervals.
	KindBoundary = "boundary"
	// KindPlan is a plan tier seen on one stream from TS to To.
	KindPlan = "plan"
	// KindOrgMap says the organization Org belongs to the account Acct.
	KindOrgMap = "orgmap"
	// KindSession is a stream's own identity record (recorded from TS on).
	KindSession = "session"
	// KindSpan says the session Stream ran in the process Proc from TS to To.
	KindSpan = "span"
	// KindWindow is a log folder covering TS..To that names Acct ("" when
	// the folder names more than one).
	KindWindow = "window"
)

// Sources (enum; never a path).
const (
	SrcClaudeCredOrg   = "claude-credential-org"
	SrcClaudeBridge    = "claude-bridge-session"
	SrcClaudeLedger    = "claude-autoreact-ledger"
	SrcClaudeBackup    = "claude-backup"
	SrcClaudeJSONBak   = "claude-json-backup"
	SrcClaudeJSON      = "claude-json"
	SrcClaudeDesktop   = "claude-desktop"
	SrcClaudeLogin     = "claude-login"
	SrcCodexCreator    = "codex-creator"
	SrcCodexThreads    = "codex-threads"
	SrcCodexAuthLog    = "codex-auth-log"
	SrcCodexPlan       = "codex-plan"
	SrcGrokUserInfo    = "grok-user-info"
	SrcGrokSession     = "grok-session"
	SrcGrokLogin       = "grok-login"
	SrcGeminiAccounts  = "gemini-accounts"
	SrcCursorRetrieval = "cursor-retrieval-log"
	SrcProbe           = "probe"
)

// Sample qualities.
const (
	QExact  = "exact"  // a log line naming the identity at that moment
	QStrong = "strong" // a snapshot (state after the write)
)

// Record is one piece of identity evidence, as kept in state.json. Acct and
// Org are account hashes (a_ + 16 hex), Stream a session key (s_ + 16 hex),
// Proc a process or log-folder name; nothing else identifies anything.
type Record struct {
	Provider string    `json:"p"`
	Home     string    `json:"h,omitempty"`
	Kind     string    `json:"k"`
	Source   string    `json:"src"`
	Acct     string    `json:"a,omitempty"`
	Org      string    `json:"o,omitempty"`
	Stream   string    `json:"s,omitempty"`
	Proc     string    `json:"pr,omitempty"`
	Plan     string    `json:"pl,omitempty"`
	Sole     bool      `json:"sole,omitempty"`
	Q        string    `json:"q,omitempty"`
	TS       time.Time `json:"t"`
	To       time.Time `json:"to,omitzero"`
}

func (r Record) key() string {
	sole := ""
	if r.Sole {
		sole = "1"
	}
	ts := r.TS.UTC().Format(time.RFC3339Nano)
	if r.Kind == KindSpan || r.Kind == KindPlan {
		// One record per (stream, process) or (stream, plan): later reads
		// only widen it, so active sessions do not grow the state.
		ts = ""
	}
	return strings.Join([]string{r.Provider, r.Home, r.Kind, r.Source, r.Acct, r.Org, r.Stream, r.Proc, r.Plan, sole, r.Q, ts}, "\x00")
}

// end is To, or TS for a point record.
func (r Record) end() time.Time {
	if r.To.After(r.TS) {
		return r.To
	}
	return r.TS
}

// Merge adds recs to have: records that differ only in To are one record
// whose To is the later of the two. The result is sorted by time.
func Merge(have, recs []Record) []Record {
	idx := make(map[string]int, len(have)+len(recs))
	out := make([]Record, 0, len(have)+len(recs))
	for _, list := range [][]Record{have, recs} {
		for _, r := range list {
			r.TS = r.TS.UTC()
			if !r.To.IsZero() {
				r.To = r.To.UTC()
			}
			k := r.key()
			if i, ok := idx[k]; ok {
				o := &out[i]
				end := o.end()
				if r.end().After(end) {
					end = r.end()
				}
				if r.TS.Before(o.TS) {
					o.TS = r.TS
				}
				if end.After(o.TS) {
					o.To = end
				}
				continue
			}
			idx[k] = len(out)
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b Record) int {
		return cmp.Or(cmp.Compare(a.Provider, b.Provider), cmp.Compare(a.Home, b.Home), a.TS.Compare(b.TS), cmp.Compare(a.Kind, b.Kind))
	})
	return out
}

// Mark is a harvest watermark for one file or database.
type Mark struct {
	Size    int64     `json:"size"`
	MtimeNs int64     `json:"mtimeNs,omitempty"`
	Off     int64     `json:"off"`
	Last    time.Time `json:"last,omitzero"` // ts of the last timestamped line read
	Aux     string    `json:"aux,omitempty"` // per-harvester resume context (no ids)
}

// Span is one live probe interval (store.Interval without the store import).
type Span struct {
	Provider string
	Home     string
	Acct     string
	From, To time.Time
}

// --- index ---

type sample struct {
	ts   time.Time
	acct string
	src  string
	q    string
}

type sessRec struct {
	ts   time.Time
	acct string
	src  string
}

type span struct {
	proc     string
	from, to time.Time
}

type era struct {
	from, to time.Time
	acct     string
}

type window struct {
	from, to time.Time
	acct     string
	proc     string
}

type gemRec struct {
	ts   time.Time
	acct string
	sole bool
}

// prov is the evidence for one (provider, home).
type prov struct {
	provider string
	samples  []sample    // sorted by ts
	bounds   []time.Time // sorted
	orgs     map[string]string
	sess     map[string][]sessRec // stream -> records, sorted
	spans    map[string][]span    // grok: stream -> process spans
	procUser map[string][]sample  // grok: proc -> user_info samples
	eras     []era                // codex plan lineage
	windows  []window             // cursor log folders
	winOwner map[string][]sample  // cursor: folder -> owner lines
	gemini   []gemRec
	accts    map[string]bool
	first    time.Time // earliest log evidence (grok chain)
}

// Index answers attribution queries from evidence. It is safe for
// concurrent use.
type Index struct {
	provs map[string]*prov

	mu        sync.Mutex
	conflicts map[string]int
}

func provKey(provider, home string) string { return provider + "\x00" + home }

// Build indexes records and live probe spans.
func Build(recs []Record, spans []Span) *Index {
	ix := &Index{provs: map[string]*prov{}, conflicts: map[string]int{}}
	get := func(provider, home string) *prov {
		k := provKey(provider, home)
		p := ix.provs[k]
		if p == nil {
			p = &prov{provider: provider, orgs: map[string]string{}, sess: map[string][]sessRec{}, spans: map[string][]span{},
				procUser: map[string][]sample{}, winOwner: map[string][]sample{}, accts: map[string]bool{}}
			ix.provs[k] = p
		}
		return p
	}
	// Org maps first, so org-only samples resolve to accounts.
	for _, r := range recs {
		if r.Kind == KindOrgMap && r.Org != "" && r.Acct != "" {
			get(r.Provider, r.Home).orgs[r.Org] = r.Acct
		}
	}
	var plans = map[string][]Record{}
	for _, r := range recs {
		p := get(r.Provider, r.Home)
		acct := r.Acct
		if acct == "" && r.Org != "" {
			acct = p.resolveOrg(r.Org)
		}
		switch r.Kind {
		case KindSample:
			if acct == "" {
				continue
			}
			p.accts[acct] = true
			s := sample{ts: r.TS, acct: acct, src: r.Source, q: r.Q}
			switch {
			case r.Source == SrcGrokUserInfo && r.Proc != "":
				p.procUser[r.Proc] = append(p.procUser[r.Proc], s)
			case r.Source == SrcCursorRetrieval && r.Proc != "":
				p.winOwner[r.Proc] = append(p.winOwner[r.Proc], s)
			case r.Source == SrcGeminiAccounts:
				p.gemini = append(p.gemini, gemRec{ts: r.TS, acct: acct, sole: r.Sole})
			}
			p.samples = append(p.samples, s)
			if e := r.end(); e.After(r.TS) {
				s.ts = e
				p.samples = append(p.samples, s)
			}
			p.noteFirst(r)
		case KindSession:
			if acct == "" || r.Stream == "" {
				continue
			}
			p.accts[acct] = true
			p.sess[r.Stream] = append(p.sess[r.Stream], sessRec{ts: r.TS, acct: acct, src: r.Source})
			// A stream's own record is also a sample of its time.
			p.samples = append(p.samples, sample{ts: r.TS, acct: acct, src: r.Source, q: QExact})
		case KindBoundary:
			p.bounds = append(p.bounds, r.TS)
			p.noteFirst(r)
		case KindSpan:
			if r.Stream != "" && r.Proc != "" {
				p.spans[r.Stream] = append(p.spans[r.Stream], span{proc: r.Proc, from: r.TS, to: r.end()})
				p.noteFirst(r)
			}
		case KindWindow:
			p.windows = append(p.windows, window{from: r.TS, to: r.end(), acct: acct, proc: r.Proc})
			if acct != "" {
				p.accts[acct] = true
			}
		case KindPlan:
			if r.Plan != "" {
				plans[provKey(r.Provider, r.Home)] = append(plans[provKey(r.Provider, r.Home)], r)
			}
		}
	}
	for _, s := range spans {
		if s.Acct == "" {
			continue
		}
		p := get(s.Provider, s.Home)
		p.accts[s.Acct] = true
		p.samples = append(p.samples, sample{ts: s.From, acct: s.Acct, src: SrcProbe, q: QStrong})
		if s.To.After(s.From) {
			p.samples = append(p.samples, sample{ts: s.To, acct: s.Acct, src: SrcProbe, q: QStrong})
		}
	}
	for k, p := range ix.provs {
		slices.SortStableFunc(p.samples, func(a, b sample) int { return a.ts.Compare(b.ts) })
		slices.SortFunc(p.bounds, time.Time.Compare)
		for _, l := range p.sess {
			slices.SortStableFunc(l, func(a, b sessRec) int { return a.ts.Compare(b.ts) })
		}
		for _, l := range p.procUser {
			slices.SortStableFunc(l, func(a, b sample) int { return a.ts.Compare(b.ts) })
		}
		for _, l := range p.winOwner {
			slices.SortStableFunc(l, func(a, b sample) int { return a.ts.Compare(b.ts) })
		}
		slices.SortStableFunc(p.gemini, func(a, b gemRec) int { return a.ts.Compare(b.ts) })
		slices.SortStableFunc(p.windows, func(a, b window) int { return a.from.Compare(b.from) })
		p.eras = buildEras(plans[k], p)
	}
	return ix
}

func (p *prov) noteFirst(r Record) {
	switch r.Source {
	case SrcGrokUserInfo, SrcGrokSession, SrcGrokLogin:
		if p.first.IsZero() || r.TS.Before(p.first) {
			p.first = r.TS
		}
	}
}

// resolveOrg maps an org hash to its account, or "" when unmapped.
func (p *prov) resolveOrg(org string) string {
	if a, ok := p.orgs[org]; ok {
		return a
	}
	// Unmapped: the org hash stands in for the account (SPEC: hashed with
	// native id "org:<uuid>").
	return org
}

func (ix *Index) prov(provider, home string) *prov { return ix.provs[provKey(provider, home)] }

func (ix *Index) conflict(provider string) {
	ix.mu.Lock()
	ix.conflicts[provider]++
	ix.mu.Unlock()
}

// Conflicts returns the conflict counts per provider so far (disagreeing
// samples inside one interval, disagreeing recorded sources).
func (ix *Index) Conflicts() map[string]int {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	out := make(map[string]int, len(ix.conflicts))
	for k, v := range ix.conflicts {
		out[k] = v
	}
	return out
}

// boundaryBetween reports a login boundary in (from, to].
func (p *prov) boundaryBetween(from, to time.Time) bool {
	if !from.Before(to) {
		return false
	}
	i, _ := slices.BinarySearchFunc(p.bounds, from, time.Time.Compare)
	for ; i < len(p.bounds); i++ {
		b := p.bounds[i]
		if !b.After(from) {
			continue
		}
		return !b.After(to)
	}
	return false
}

// Recorded is step 1 of the attribution order: the stream's own identity
// record (the parser's hint, a session record or, for Grok, the process's
// user_info), with no login boundary between it and ts. ok is false when
// there is none. An org hint that is not in the org map gives "bounded".
func (ix *Index) Recorded(provider, home string, ts time.Time, sessionID string, h sources.Hint) (acct, q string, ok bool) {
	p := ix.prov(provider, home)
	type cand struct {
		acct, q string
		at      time.Time
	}
	var cands []cand
	if !h.IsZero() {
		a, hq := h.ID, model.AcctRecorded
		if h.Kind == sources.HintOrg {
			a = h.ID
			if p != nil {
				if m, ok := p.orgs[h.ID]; ok {
					a = m
				} else {
					hq = model.AcctBounded
				}
			} else {
				hq = model.AcctBounded
			}
		}
		if p == nil || !p.boundaryBetween(h.At, ts) {
			cands = append(cands, cand{a, hq, h.At})
		}
	}
	if p != nil && sessionID != "" {
		sk := model.SessionKey(provider, sessionID)
		if l := p.sess[sk]; len(l) > 0 {
			r := l[0]
			for _, x := range l {
				if !x.ts.After(ts.Add(time.Minute)) {
					r = x
				}
			}
			if !p.boundaryBetween(r.ts, ts) {
				cands = append(cands, cand{r.acct, model.AcctRecorded, r.ts})
			}
		}
		if l := p.spans[sk]; len(l) > 0 {
			if a, at, ok := p.grokUser(l, ts); ok {
				cands = append(cands, cand{a, model.AcctRecorded, at})
			}
		}
	}
	if p != nil && provider == "cursor" {
		if a, at, ok := p.cursorWindow(ts); ok {
			cands = append(cands, cand{a, model.AcctRecorded, at})
		}
	}
	if len(cands) == 0 {
		return "", "", false
	}
	best := cands[0]
	for _, c := range cands[1:] {
		if c.acct != best.acct {
			ix.conflict(provider)
		}
		// The later record wins; at equal times the stronger one.
		if c.at.After(best.at) || (c.at.Equal(best.at) && model.AcctRank(c.q) > model.AcctRank(best.q)) {
			best = c
		}
	}
	return best.acct, best.q, true
}

// grokUser joins a session's process spans to that process's user_info.
func (p *prov) grokUser(spans []span, ts time.Time) (string, time.Time, bool) {
	const slack = 2 * time.Minute
	pick := -1
	for i, s := range spans {
		if !ts.Before(s.from.Add(-slack)) && !ts.After(s.to.Add(slack)) {
			if pick < 0 || s.from.After(spans[pick].from) {
				pick = i
			}
		}
	}
	if pick < 0 {
		for i, s := range spans {
			if !s.from.After(ts) && (pick < 0 || s.from.After(spans[pick].from)) {
				pick = i
			}
		}
	}
	if pick < 0 {
		for i, s := range spans {
			if pick < 0 || s.from.Before(spans[pick].from) {
				pick = i
			}
		}
	}
	s := spans[pick]
	var best *sample
	for i, u := range p.procUser[s.proc] {
		// user_info is written when the process starts; a pid seen a day
		// earlier is another process.
		if u.ts.After(s.to.Add(slack)) || u.ts.Before(s.from.Add(-24*time.Hour)) {
			continue
		}
		best = &p.procUser[s.proc][i]
	}
	if best == nil {
		return "", time.Time{}, false
	}
	return best.acct, best.ts, true
}

// cursorWindow is the account of the log folder covering ts: the folder's
// only owner, or in a folder naming several, the owner line at or before ts.
func (p *prov) cursorWindow(ts time.Time) (string, time.Time, bool) {
	for i := len(p.windows) - 1; i >= 0; i-- {
		w := p.windows[i]
		if ts.Before(w.from) || ts.After(w.to) {
			continue
		}
		if w.acct != "" {
			return w.acct, w.from, true
		}
		var best *sample
		for j, o := range p.winOwner[w.proc] {
			if !o.ts.After(ts) {
				best = &p.winOwner[w.proc][j]
			}
		}
		if best != nil {
			return best.acct, best.ts, true
		}
	}
	return "", time.Time{}, false
}

// Fallback runs steps 3 to 6 of the attribution order: the login interval
// (bounded), the provider's lineage rule, the nearest sample (inferred) and
// unknown. ok is false only when there is no evidence at all, so the caller
// can keep its own inferred answer.
func (ix *Index) Fallback(provider, home string, ts time.Time) (acct, q string, ok bool) {
	p := ix.prov(provider, home)
	if p == nil {
		return "", model.AcctUnknown, false
	}
	switch provider {
	case model.ProviderAnthropic:
		// Login intervals need login evidence: with no boundary known at
		// all (no history.jsonl), nothing bounds anything.
		if len(p.bounds) > 0 {
			if a, q, ok := ix.interval(p, ts); ok {
				return a, q, true
			}
		}
	case "google":
		if a, ok := p.geminiBounded(ts); ok {
			return a, model.AcctBounded, true
		}
	}
	if a, ok := p.lineage(ts); ok {
		return a, model.AcctLineage, true
	}
	if s, ok := nearest(p.samples, ts); ok {
		return s.acct, model.AcctInferred, true
	}
	if len(p.eras) > 0 || len(p.windows) > 0 {
		return "", model.AcctUnknown, true
	}
	return "", model.AcctUnknown, false
}

// interval is step 3: the samples between the login boundaries around ts.
func (ix *Index) interval(p *prov, ts time.Time) (string, string, bool) {
	lo, hi := time.Time{}, time.Time{}
	i, _ := slices.BinarySearchFunc(p.bounds, ts, time.Time.Compare)
	// bounds[i] >= ts; an interval is [bounds[k], bounds[k+1]).
	if i < len(p.bounds) && p.bounds[i].Equal(ts) {
		lo = p.bounds[i]
		if i+1 < len(p.bounds) {
			hi = p.bounds[i+1]
		}
	} else {
		if i > 0 {
			lo = p.bounds[i-1]
		}
		if i < len(p.bounds) {
			hi = p.bounds[i]
		}
	}
	// p.samples is sorted: the interval's samples are one slice of it.
	first, last := 0, len(p.samples)
	if !lo.IsZero() {
		first, _ = slices.BinarySearchFunc(p.samples, lo, func(s sample, t time.Time) int { return s.ts.Compare(t) })
	}
	if !hi.IsZero() {
		last, _ = slices.BinarySearchFunc(p.samples, hi, func(s sample, t time.Time) int { return s.ts.Compare(t) })
	}
	if first >= last {
		return "", "", false
	}
	in := p.samples[first:last]
	if len(in) == 0 {
		return "", "", false
	}
	agree := true
	for _, s := range in[1:] {
		if s.acct != in[0].acct {
			agree = false
			break
		}
	}
	if agree {
		return in[0].acct, model.AcctBounded, true
	}
	// Disagreeing samples: split at them.
	var prev, next *sample
	for j := range in {
		if !in[j].ts.After(ts) {
			prev = &in[j]
		} else if next == nil {
			next = &in[j]
		}
	}
	if prev != nil && next != nil && prev.acct == next.acct {
		return prev.acct, model.AcctBounded, true
	}
	ix.conflict(p.provider)
	s, _ := nearest(in, ts)
	return s.acct, model.AcctInferred, true
}

// geminiBounded is M1: google_accounts.json names the active account; the
// last write at or before ts holds, and a file whose "old" list is empty
// was the only login, so it holds for earlier events too.
func (p *prov) geminiBounded(ts time.Time) (string, bool) {
	if len(p.gemini) == 0 {
		return "", false
	}
	var best *gemRec
	for i, g := range p.gemini {
		if !g.ts.After(ts) {
			best = &p.gemini[i]
		}
	}
	if best != nil {
		return best.acct, true
	}
	if p.gemini[0].sole {
		return p.gemini[0].acct, true
	}
	return "", false
}

// lineage is step 4, per provider.
func (p *prov) lineage(ts time.Time) (string, bool) {
	switch p.provider {
	case model.ProviderOpenAI:
		for _, e := range p.eras {
			if e.acct != "" && !ts.Before(e.from) && !ts.After(e.to) {
				return e.acct, true
			}
		}
	case model.ProviderXAI:
		return p.grokChain(ts)
	case "cursor":
		return p.between(ts)
	}
	return "", false
}

// grokChain is G3: an unbroken token chain (no interactive login between
// the event and the nearest identity sample) carries that sample's account.
// It only applies inside the span the harvested log covers.
func (p *prov) grokChain(ts time.Time) (string, bool) {
	if p.first.IsZero() || ts.Before(p.first) {
		return "", false
	}
	var best *sample
	var bestD time.Duration
	for i, s := range p.samples {
		lo, hi := s.ts, ts
		if hi.Before(lo) {
			lo, hi = hi, lo
		}
		if p.boundaryBetween(lo, hi) {
			continue
		}
		d := hi.Sub(lo)
		if best == nil || d < bestD {
			best, bestD = &p.samples[i], d
		}
	}
	if best == nil {
		return "", false
	}
	return best.acct, true
}

// between is K3: an event between two samples of the same account (with no
// other account between) continues that account; before the first or after
// the last sample, only a tool that ever named one account continues it.
func (p *prov) between(ts time.Time) (string, bool) {
	var prev, next *sample
	for i := range p.samples {
		if !p.samples[i].ts.After(ts) {
			prev = &p.samples[i]
		} else if next == nil {
			next = &p.samples[i]
		}
	}
	switch {
	case prev != nil && next != nil:
		if prev.acct == next.acct {
			return prev.acct, true
		}
		return "", false
	case len(p.accts) == 1:
		for a := range p.accts {
			return a, true
		}
	}
	return "", false
}

// nearest is the sample closest in time to ts.
func nearest(list []sample, ts time.Time) (sample, bool) {
	if len(list) == 0 {
		return sample{}, false
	}
	i, _ := slices.BinarySearchFunc(list, ts, func(s sample, t time.Time) int { return s.ts.Compare(t) })
	switch {
	case i == 0:
		return list[0], true
	case i >= len(list):
		return list[len(list)-1], true
	}
	if ts.Sub(list[i-1].ts) <= list[i].ts.Sub(ts) {
		return list[i-1], true
	}
	return list[i], true
}

// buildEras is X4: plan marks carried forward per rollout. A plan change
// between consecutive marks breaks lineage unless one live rollout shows
// both plans across that change (an upgrade flap or a dip inside one
// rollout). An era gets the account of the recorded anchors inside it when
// they all agree.
func buildEras(plans []Record, p *prov) []era {
	if len(plans) == 0 {
		return nil
	}
	type stream struct {
		first, last time.Time
		plans       map[string]bool
	}
	streams := map[string]*stream{}
	type mark struct {
		ts   time.Time
		plan string
	}
	var marks []mark
	for _, r := range plans {
		s := streams[r.Stream]
		if s == nil {
			s = &stream{first: r.TS, last: r.end(), plans: map[string]bool{}}
			streams[r.Stream] = s
		}
		if r.TS.Before(s.first) {
			s.first = r.TS
		}
		if r.end().After(s.last) {
			s.last = r.end()
		}
		s.plans[r.Plan] = true
		marks = append(marks, mark{r.TS, r.Plan}, mark{r.end(), r.Plan})
	}
	slices.SortStableFunc(marks, func(a, b mark) int { return a.ts.Compare(b.ts) })
	linked := func(a, b string, t1, t2 time.Time) bool {
		for _, s := range streams {
			if s.plans[a] && s.plans[b] && !s.first.After(t2) && !s.last.Before(t1) {
				return true
			}
		}
		return false
	}
	var eras []era
	cur := era{from: marks[0].ts, to: marks[0].ts}
	for i := 1; i < len(marks); i++ {
		m1, m2 := marks[i-1], marks[i]
		if m1.plan != m2.plan && !linked(m1.plan, m2.plan, m1.ts, m2.ts) {
			eras = append(eras, cur)
			cur = era{from: m2.ts, to: m2.ts}
			continue
		}
		cur.to = m2.ts
	}
	eras = append(eras, cur)
	for i := range eras {
		e := &eras[i]
		var anchors map[string]bool
		for _, l := range p.sess {
			for _, r := range l {
				if !r.ts.Before(e.from) && !r.ts.After(e.to) {
					if anchors == nil {
						anchors = map[string]bool{}
					}
					anchors[r.acct] = true
				}
			}
		}
		for _, s := range p.samples {
			if s.q == QExact && s.src != SrcProbe && !s.ts.Before(e.from) && !s.ts.After(e.to) {
				if anchors == nil {
					anchors = map[string]bool{}
				}
				anchors[s.acct] = true
			}
		}
		if len(anchors) == 1 {
			for a := range anchors {
				e.acct = a
			}
		}
	}
	return eras
}

// Counts summarises records per provider and kind (status output).
func Counts(recs []Record) map[string]map[string]int {
	out := map[string]map[string]int{}
	for _, r := range recs {
		m := out[r.Provider]
		if m == nil {
			m = map[string]int{}
			out[r.Provider] = m
		}
		m[r.Kind]++
	}
	return out
}
