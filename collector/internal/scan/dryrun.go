package scan

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/limits"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/workspace"
)

// UsageTotals are summed usage fields; the JSON shape is what the integration
// oracle compares, so do not rename fields.
type UsageTotals struct {
	Events    int64   `json:"events"`
	In        int64   `json:"in"`
	CacheW    int64   `json:"cacheW"`
	CacheW1h  int64   `json:"cacheW1h"` // subset of CacheW, never added
	CacheR    int64   `json:"cacheR"`
	Out       int64   `json:"out"`
	Reasoning int64   `json:"reasoning"`
	Calls     int64   `json:"calls"`
	Effective float64 `json:"effective"`
}

func (u *UsageTotals) add(e model.UsageEvent) {
	u.Events++
	u.In += e.In
	u.CacheW += e.CacheW
	u.CacheW1h += e.CacheW1h
	u.CacheR += e.CacheR
	u.Out += e.Out
	u.Reasoning += e.Reasoning
	u.Calls += e.Calls
	// From the integer totals, so the float carries no summation drift.
	u.Effective = float64(u.In+u.CacheW+u.Out) + float64(u.CacheR)/10
}

func (u *UsageTotals) addTotals(o UsageTotals) {
	u.Events += o.Events
	u.In += o.In
	u.CacheW += o.CacheW
	u.CacheW1h += o.CacheW1h
	u.CacheR += o.CacheR
	u.Out += o.Out
	u.Reasoning += o.Reasoning
	u.Calls += o.Calls
	u.Effective = float64(u.In+u.CacheW+u.Out) + float64(u.CacheR)/10
}

type ActivityTotals struct {
	WithUsage int64 `json:"withUsage"`
	NoUsage   int64 `json:"noUsage"`
}

type MonthTotals struct {
	Usage    UsageTotals    `json:"usage"`
	Activity ActivityTotals `json:"activity"`
	Prompts  int64          `json:"prompts"`
}

// WorkspaceTotals is one project folder after the same canonicalisation
// the prompts page applies to the workspace already stored on each prompt.
type WorkspaceTotals struct {
	Usage    UsageTotals    `json:"usage"`
	Activity ActivityTotals `json:"activity"`
	Prompts  int64          `json:"prompts"`
	FirstTS  *time.Time     `json:"firstTs,omitempty"`
	LastTS   *time.Time     `json:"lastTs,omitempty"`
}

type SourceTotals struct {
	Provider string                  `json:"provider"`
	Files    int                     `json:"files"`
	Usage    UsageTotals             `json:"usage"`
	Activity ActivityTotals          `json:"activity"`
	Prompts  int64                   `json:"prompts"`
	FirstTS  *time.Time              `json:"firstTs"`
	LastTS   *time.Time              `json:"lastTs"`
	ByMonth  map[string]*MonthTotals `json:"byMonth"`
	// ByWorkspace is keyed by the folder the prompts page would show.
	// An empty key is events whose log recorded no folder.
	ByWorkspace map[string]*WorkspaceTotals `json:"byWorkspace,omitempty"`
	// ByAcctQ counts events by attribution quality: kind (usage, activity,
	// prompts) -> acctQ -> events. Counts only, never accounts.
	ByAcctQ map[string]map[string]int64 `json:"byAcctQ,omitempty"`
	// Conflicts counts disagreeing identity evidence met while attributing
	// this source's provider (SPEC "Accounts").
	Conflicts int `json:"conflicts,omitempty"`
}

func (st *SourceTotals) countQ(kind, q string) {
	if q == "" {
		q = model.AcctUnknown
	}
	if st.ByAcctQ == nil {
		st.ByAcctQ = map[string]map[string]int64{}
	}
	m := st.ByAcctQ[kind]
	if m == nil {
		m = map[string]int64{}
		st.ByAcctQ[kind] = m
	}
	m[q]++
}

type Report struct {
	GeneratedAt time.Time                `json:"generatedAt"`
	Since       *string                  `json:"since"`
	Until       *string                  `json:"until"`
	Sources     map[string]*SourceTotals `json:"sources"`
	// Homes are the scan roots the report covers (v1.3).
	Homes []string `json:"homes,omitempty"`
	// Limits is the newest plan window each tool has written, per account.
	// Names are organization or plan labels. Emails are not included.
	Limits []model.LimitSnapshot `json:"limits,omitempty"`
}

// Bound is an inclusive time filter given either as a local calendar date
// (YYYY-MM-DD, compared with the event's local date) or an RFC 3339 instant.
type Bound struct {
	Raw  string
	Date string
	TS   time.Time
}

func ParseBound(s string) (*Bound, error) {
	if s == "" {
		return nil, nil
	}
	if _, err := time.Parse("2006-01-02", s); err == nil {
		return &Bound{Raw: s, Date: s}, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return &Bound{Raw: s, TS: t}, nil
	}
	return nil, fmt.Errorf("%q is neither YYYY-MM-DD nor an RFC 3339 timestamp", s)
}

// LocalDate is the event's calendar date in its own UTC offset (the server's
// day bucketing).
func LocalDate(ts time.Time, tzOffsetMin int) string {
	return ts.UTC().Add(time.Duration(tzOffsetMin) * time.Minute).Format("2006-01-02")
}

// InRange reports whether an event at ts/tz passes since (>=) and until (<=).
func InRange(since, until *Bound, ts time.Time, tz int) bool {
	if since != nil {
		if since.Date != "" && LocalDate(ts, tz) < since.Date {
			return false
		}
		if since.Date == "" && ts.Before(since.TS) {
			return false
		}
	}
	if until != nil {
		if until.Date != "" && LocalDate(ts, tz) > until.Date {
			return false
		}
		if until.Date == "" && ts.After(until.TS) {
			return false
		}
	}
	return true
}

// MergeUsage applies the server's merge rule to a (possibly repeated) event.
func MergeUsage(have *model.UsageEvent, e model.UsageEvent) {
	switch {
	case e.PV > have.PV:
		*have = e
		return
	case e.PV < have.PV:
		return
	}
	have.Tokens.Max(e.Tokens)
	earlier := e.TS.Before(have.TS)
	if earlier {
		have.TS, have.TZOffsetMin = e.TS, e.TZOffsetMin
	}
	if have.Model == "" {
		have.Model = e.Model
	}
	if e.Workspace != "" && (have.Workspace == "" || earlier) {
		have.Workspace, have.WS = e.Workspace, e.WS
	}
}

// merged holds one source's events across every home, merged by id the way
// the server does.
type merged struct {
	usage    map[string]model.UsageEvent
	activity map[string]model.ActivityEvent
	prompts  map[string]model.PromptRecord
}

func (m *merged) collect(b sources.Batch) {
	for _, e := range b.Usage {
		if have, ok := m.usage[e.ID]; ok {
			MergeUsage(&have, e)
			m.usage[e.ID] = have
		} else {
			m.usage[e.ID] = e
		}
	}
	for _, e := range b.Activity {
		if have, ok := m.activity[e.ID]; ok {
			if have.TS.Before(e.TS) {
				e.TS, e.TZOffsetMin = have.TS, have.TZOffsetMin
			}
			e.HasUsage = e.HasUsage || have.HasUsage
			if e.Workspace == "" {
				e.Workspace = have.Workspace
			}
		}
		m.activity[e.ID] = e
	}
	for _, e := range b.Prompts {
		have, ok := m.prompts[e.ID]
		if !ok || e.TS.Before(have.TS) {
			if ok && e.Workspace == "" {
				e.Workspace = have.Workspace
			}
			m.prompts[e.ID] = e
		} else if have.Workspace == "" && e.Workspace != "" {
			have.Workspace = e.Workspace
			m.prompts[e.ID] = have
		}
	}
}

// DryRun parses every file of every source from offset 0 without state,
// merges events by id the way the server does, and totals them.
func DryRun(srcs []sources.Source, env *sources.Env, since, until *Bound, now time.Time, warn io.Writer) (*Report, error) {
	return DryRunHomes([]Home{{Env: env, Sources: srcs}}, since, until, now, warn)
}

// DryRunHomes is DryRun over several homes: a source's totals cover every
// home, merged by id, so the same file reachable twice counts once.
func DryRunHomes(homes []Home, since, until *Bound, now time.Time, warn io.Writer) (*Report, error) {
	rep := &Report{GeneratedAt: now.UTC(), Sources: map[string]*SourceTotals{}}
	if since != nil {
		rep.Since = &since.Raw
	}
	if until != nil {
		rep.Until = &until.Raw
	}
	var errs []error
	var names []string
	events := map[string]*merged{}
	for _, h := range homes {
		env := h.Env
		if env != nil && env.Home != "" {
			rep.Homes = append(rep.Homes, env.Home)
			rep.Limits = limits.Dedupe(append(rep.Limits, limits.Collect(env)...))
		}
		for _, src := range h.Sources {
			name := src.Name()
			st := rep.Sources[name]
			if st == nil {
				st = &SourceTotals{Provider: src.Provider(), ByMonth: map[string]*MonthTotals{}, ByWorkspace: map[string]*WorkspaceTotals{}}
				rep.Sources[name] = st
				names = append(names, name)
				events[name] = &merged{usage: map[string]model.UsageEvent{}, activity: map[string]model.ActivityEvent{}, prompts: map[string]model.PromptRecord{}}
			}
			if err := src.Prepare(env); err != nil {
				errs = append(errs, fmt.Errorf("%s prepare: %w", name, err))
				continue
			}
			files, err := src.Files(env)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s files: %w", name, err))
				continue
			}
			st.Files += len(files)
			m := events[name]
			for _, p := range files {
				b, cur, err := src.Parse(env, p, sources.Cursor{})
				if err != nil {
					fmt.Fprintf(warn, "warning: %s: %v\n", p, err)
					continue
				}
				m.collect(b)
				// A second call lets parsers release output held in carry (e.g. a
				// Claude prompt waiting for its model), as a later tick would.
				if len(cur.Carry) > 0 {
					if b, _, err := src.Parse(env, p, cur); err == nil {
						m.collect(b)
					}
				}
			}
		}
	}
	for _, name := range names {
		st := rep.Sources[name]
		usage, activity, prompts := events[name].usage, events[name].activity, events[name].prompts
		month := func(ts time.Time, tz int) *MonthTotals {
			m := LocalDate(ts, tz)[:7]
			if st.ByMonth[m] == nil {
				st.ByMonth[m] = &MonthTotals{}
			}
			return st.ByMonth[m]
		}
		span := func(ts time.Time) {
			t := ts.UTC()
			if st.FirstTS == nil || t.Before(*st.FirstTS) {
				st.FirstTS = &t
			}
			if st.LastTS == nil || t.After(*st.LastTS) {
				st.LastTS = &t
			}
		}
		ws := func(key string) *WorkspaceTotals {
			if st.ByWorkspace[key] == nil {
				st.ByWorkspace[key] = &WorkspaceTotals{}
			}
			return st.ByWorkspace[key]
		}
		touch := func(w *WorkspaceTotals, ts time.Time) {
			t := ts.UTC()
			if w.FirstTS == nil || t.Before(*w.FirstTS) {
				w.FirstTS = &t
			}
			if w.LastTS == nil || t.After(*w.LastTS) {
				w.LastTS = &t
			}
		}
		for _, e := range usage {
			if !InRange(since, until, e.TS, e.TZOffsetMin) {
				continue
			}
			st.Usage.add(e)
			st.countQ("usage", e.AcctQ)
			month(e.TS, e.TZOffsetMin).Usage.add(e)
			w := ws(e.Workspace)
			w.Usage.add(e)
			touch(w, e.TS)
			span(e.TS)
		}
		for _, e := range activity {
			if !InRange(since, until, e.TS, e.TZOffsetMin) {
				continue
			}
			m := month(e.TS, e.TZOffsetMin)
			st.countQ("activity", e.AcctQ)
			w := ws(e.Workspace)
			if e.HasUsage {
				st.Activity.WithUsage++
				m.Activity.WithUsage++
				w.Activity.WithUsage++
			} else {
				st.Activity.NoUsage++
				m.Activity.NoUsage++
				w.Activity.NoUsage++
			}
			touch(w, e.TS)
			span(e.TS)
		}
		for _, e := range prompts {
			if !InRange(since, until, e.TS, e.TZOffsetMin) {
				continue
			}
			st.Prompts++
			st.countQ("prompts", e.AcctQ)
			month(e.TS, e.TZOffsetMin).Prompts++
			w := ws(e.Workspace)
			w.Prompts++
			touch(w, e.TS)
			span(e.TS)
		}
		foldWorkspaces(st)
	}
	return rep, errors.Join(errs...)
}

// foldWorkspaces applies the prompts-page canonicalisation (workspace.Canonicalize)
// to the raw folders already stored on the events. Prompt counts are the
// weights, matching the API.
func foldWorkspaces(st *SourceTotals) {
	if len(st.ByWorkspace) == 0 {
		return
	}
	counts := make(map[string]int, len(st.ByWorkspace))
	for k, w := range st.ByWorkspace {
		counts[k] = int(w.Prompts)
	}
	folded := workspace.Canonicalize(counts)
	merged := map[string]*WorkspaceTotals{}
	for raw, w := range st.ByWorkspace {
		key := folded[raw]
		dst := merged[key]
		if dst == nil {
			dst = &WorkspaceTotals{}
			merged[key] = dst
		}
		dst.Usage.addTotals(w.Usage)
		dst.Activity.WithUsage += w.Activity.WithUsage
		dst.Activity.NoUsage += w.Activity.NoUsage
		dst.Prompts += w.Prompts
		if w.FirstTS != nil && (dst.FirstTS == nil || w.FirstTS.Before(*dst.FirstTS)) {
			t := *w.FirstTS
			dst.FirstTS = &t
		}
		if w.LastTS != nil && (dst.LastTS == nil || w.LastTS.After(*dst.LastTS)) {
			t := *w.LastTS
			dst.LastTS = &t
		}
	}
	st.ByWorkspace = merged
}

// Text renders the report as a human-readable table.
func (r *Report) Text(w io.Writer) {
	names := make([]string, 0, len(r.Sources))
	for n := range r.Sources {
		names = append(names, n)
	}
	slices.Sort(names)
	fmt.Fprintf(w, "dry run at %s", r.GeneratedAt.Format(time.RFC3339))
	if r.Since != nil {
		fmt.Fprintf(w, "  since %s", *r.Since)
	}
	if r.Until != nil {
		fmt.Fprintf(w, "  until %s", *r.Until)
	}
	fmt.Fprintln(w)
	for _, h := range r.Homes {
		fmt.Fprintf(w, "home %s\n", h)
	}
	row := func(label string, u UsageTotals, a ActivityTotals, prompts int64) {
		fmt.Fprintf(w, "  %-9s %8d ev  in %14d  cacheW %14d  (1h %13d)  cacheR %16d  out %13d  reas %12d  calls %9d  eff %16.0f  act %6d/%-6d prompts %6d\n",
			label, u.Events, u.In, u.CacheW, u.CacheW1h, u.CacheR, u.Out, u.Reasoning, u.Calls, u.Effective, a.WithUsage, a.NoUsage, prompts)
	}
	for _, n := range names {
		s := r.Sources[n]
		fmt.Fprintf(w, "\n%s (%s): %d files", n, s.Provider, s.Files)
		if s.FirstTS != nil {
			fmt.Fprintf(w, ", %s .. %s", s.FirstTS.Format(time.RFC3339), s.LastTS.Format(time.RFC3339))
		}
		fmt.Fprintln(w)
		months := make([]string, 0, len(s.ByMonth))
		for m := range s.ByMonth {
			months = append(months, m)
		}
		slices.Sort(months)
		for _, m := range months {
			mt := s.ByMonth[m]
			row(m, mt.Usage, mt.Activity, mt.Prompts)
		}
		row(strings.Repeat("-", 7), s.Usage, s.Activity, s.Prompts)
		if len(s.ByWorkspace) > 0 {
			fmt.Fprintln(w, "  workspaces (same folders as the prompts page)")
			keys := make([]string, 0, len(s.ByWorkspace))
			for k := range s.ByWorkspace {
				keys = append(keys, k)
			}
			slices.SortFunc(keys, func(a, b string) int {
				wa, wb := s.ByWorkspace[a], s.ByWorkspace[b]
				if wa.Usage.Effective != wb.Usage.Effective {
					if wa.Usage.Effective > wb.Usage.Effective {
						return -1
					}
					return 1
				}
				if wa.Prompts != wb.Prompts {
					if wa.Prompts > wb.Prompts {
						return -1
					}
					return 1
				}
				return strings.Compare(a, b)
			})
			for _, k := range keys {
				wst := s.ByWorkspace[k]
				label := k
				if label == "" {
					label = "(no folder)"
				}
				fmt.Fprintf(w, "    %8d ev  in %14d  cacheW %14d  cacheR %16d  out %13d  prompts %6d  %s\n",
					wst.Usage.Events, wst.Usage.In, wst.Usage.CacheW, wst.Usage.CacheR, wst.Usage.Out, wst.Prompts, label)
			}
		}
		for _, kind := range []string{"usage", "prompts"} {
			if m := s.ByAcctQ[kind]; len(m) > 0 {
				fmt.Fprintf(w, "  acctQ %-8s", kind)
				for _, q := range []string{model.AcctRecorded, model.AcctSession, model.AcctTimeline, model.AcctBounded, model.AcctLineage, model.AcctInferred, model.AcctUnknown} {
					if n := m[q]; n > 0 {
						fmt.Fprintf(w, "  %s %d", q, n)
					}
				}
				fmt.Fprintln(w)
			}
		}
		if s.Conflicts > 0 {
			fmt.Fprintf(w, "  attribution conflicts %d\n", s.Conflicts)
		}
	}
	fmt.Fprintln(w, "\n(1h = the 1-hour-TTL part of cacheW; act = activity with usage / without usage; eff = in + cacheW + out + 0.1 x cacheR)")
	writeLimits(w, r.Limits)
}

// writeLimits prints the meters the tools have written, one account's email
// included so the rows can be told apart. It does not print account hashes.
func writeLimits(w io.Writer, snaps []model.LimitSnapshot) {
	fmt.Fprintln(w, "\nlimits (the last meter each tool wrote; not a live fetch)")
	if len(snaps) == 0 {
		fmt.Fprintln(w, "  none")
		return
	}
	snaps = slices.Clone(snaps)
	rankP := map[string]int{model.ProviderAnthropic: 0, model.ProviderOpenAI: 1, model.ProviderXAI: 2}
	rankW := map[string]int{"session": 0, "week": 1, "extra": 2, "plan": 3}
	slices.SortFunc(snaps, func(a, b model.LimitSnapshot) int {
		if rankP[a.Provider] != rankP[b.Provider] {
			return rankP[a.Provider] - rankP[b.Provider]
		}
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		if c := strings.Compare(a.Plan, b.Plan); c != 0 {
			return c
		}
		if rankW[a.Window] != rankW[b.Window] {
			return rankW[a.Window] - rankW[b.Window]
		}
		return strings.Compare(a.Scope, b.Scope)
	})
	for _, s := range snaps {
		pct := "  —"
		if s.UsedPercent != nil {
			pct = fmt.Sprintf("%3.0f%%", *s.UsedPercent)
			if *s.UsedPercent != float64(int64(*s.UsedPercent)) {
				pct = fmt.Sprintf("%5.1f%%", *s.UsedPercent)
			}
		}
		reset := "no reset"
		if s.ResetsAt != nil {
			word := "resets"
			if s.ResetsAt.Before(time.Now()) {
				word = "ended"
			}
			reset = word + " " + s.ResetsAt.Local().Format("2006-01-02 15:04 MST")
		}
		scope := s.Scope
		if scope != "" {
			scope = " " + scope
		}
		who := s.Label
		if who == "" {
			who = s.Plan
		} else if s.Plan != "" {
			who += " · " + s.Plan
		}
		extra := ""
		if s.Status != "" && s.Status != "ok" {
			extra = "  " + s.Status
		}
		if s.Detail != "" {
			extra += "  " + s.Detail
		}
		observed := ""
		if !s.ObservedAt.IsZero() {
			observed = "  as of " + s.ObservedAt.Local().Format("2006-01-02 15:04 MST")
		}
		fmt.Fprintf(w, "  %-12s %-28s %-8s%s  %s  %s%s%s\n", s.Source, who, s.Window, scope, pct, reset, extra, observed)
	}
}
