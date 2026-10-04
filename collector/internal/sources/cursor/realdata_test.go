package cursor

import (
	"os"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
)

// TestRealData parses this machine's real Cursor database from scratch and
// logs only aggregate numbers (never text, emails or ids), plus parse time
// and memory, because the tick budget is 50 s. Run with D0M1_REALDATA=1.
func TestRealData(t *testing.T) {
	if os.Getenv("D0M1_REALDATA") != "1" {
		t.Skip("set D0M1_REALDATA=1 to parse this machine's real Cursor database")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	env := &sources.Env{
		Home:        home,
		Machine:     "realdata",
		Attribute:   func(string, time.Time, string, sources.Hint) (string, string) { return "", model.AcctUnknown },
		Label:       func(string) string { return "" },
		TZOffsetMin: func(ts time.Time) int { _, off := ts.In(time.Local).Zone(); return off / 60 },
		Prompts:     true,
	}
	src := New()
	files, err := src.Files(env)
	if err != nil || len(files) != 1 {
		t.Fatalf("files = %v %v", files, err)
	}
	st, _ := os.Stat(files[0])
	var ms runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms)
	sysBefore := ms.Sys
	start := time.Now()
	b, cur, err := src.Parse(env, files[0], sources.Cursor{})
	elapsed := time.Since(start)
	runtime.ReadMemStats(&ms)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("db=%d bytes parsed in %s; cursor offset=%d; Go heap peak≈%d MB (sys %d→%d MB)",
		st.Size(), elapsed.Round(time.Millisecond), cur.Offset, ms.HeapSys/1e6, sysBefore/1e6, ms.Sys/1e6)
	// Only the shape of the account probe: never its values.
	acctID, acctLabel, ok := Account(home)
	t.Logf("account: found=%v idLen=%d labelLooksLikeEmail=%v", ok, len(acctID), strings.Contains(acctLabel, "@"))

	type agg struct {
		events, in, out, calls, prompts, actWith, actNo int64
	}
	months := map[string]*agg{}
	month := func(ts time.Time) *agg {
		m := ts.In(time.Local).Format("2006-01")
		if months[m] == nil {
			months[m] = &agg{}
		}
		return months[m]
	}
	merged := map[string]model.UsageEvent{}
	dupIDs := 0
	for _, u := range b.Usage {
		if m, ok := merged[u.ID]; ok {
			dupIDs++
			m.Tokens.Max(u.Tokens)
			if u.TS.Before(m.TS) {
				m.TS = u.TS
			}
			merged[u.ID] = m
		} else {
			merged[u.ID] = u
		}
	}
	var total agg
	var first, last time.Time
	models := map[string]int{}
	noModel, bySession := 0, map[string]bool{}
	for _, u := range merged {
		a := month(u.TS)
		for _, x := range []*agg{a, &total} {
			x.events++
			x.in += u.In
			x.out += u.Out
			x.calls += u.Calls
		}
		if first.IsZero() || u.TS.Before(first) {
			first = u.TS
		}
		if u.TS.After(last) {
			last = u.TS
		}
		models[u.Model]++
		if u.Model == "" {
			noModel++
		}
		bySession[u.Session] = true
	}
	for _, a := range b.Activity {
		x := month(a.TS)
		if a.HasUsage {
			x.actWith++
			total.actWith++
		} else {
			x.actNo++
			total.actNo++
		}
		if first.IsZero() || a.TS.Before(first) {
			first = a.TS
		}
		if a.TS.After(last) {
			last = a.TS
		}
	}
	promptNoModel, promptNoWS, truncated := 0, 0, 0
	for _, p := range b.Prompts {
		month(p.TS).prompts++
		total.prompts++
		if p.Model == "" {
			promptNoModel++
		}
		if p.Workspace == "" {
			promptNoWS++
		}
		if len(p.Text) >= model.MaxPromptBytes-100 {
			truncated++
		}
	}
	t.Logf("usage: raw=%d merged=%d (dupIds=%d) in=%d out=%d calls=%d models=%d noModel=%d sessions=%d",
		len(b.Usage), len(merged), dupIDs, total.in, total.out, total.calls, len(models), noModel, len(bySession))
	t.Logf("activity=%d (hasUsage=%d, noUsage=%d) prompts=%d (noModel=%d, noWorkspace=%d, truncated=%d) firstTs=%s lastTs=%s",
		len(b.Activity), total.actWith, total.actNo, len(b.Prompts), promptNoModel, promptNoWS, truncated, first.Format(time.RFC3339), last.Format(time.RFC3339))
	keys := make([]string, 0, len(months))
	for m := range months {
		keys = append(keys, m)
	}
	sort.Strings(keys)
	for _, m := range keys {
		a := months[m]
		t.Logf("  %s: events=%d in=%d out=%d prompts=%d act+=%d act-=%d", m, a.events, a.in, a.out, a.prompts, a.actWith, a.actNo)
	}
	type mc struct {
		name string
		n    int
	}
	var ml []mc
	for m, n := range models {
		ml = append(ml, mc{m, n})
	}
	sort.Slice(ml, func(i, j int) bool { return ml[i].n > ml[j].n })
	for _, m := range ml {
		t.Logf("  model %q: %d events", m.name, m.n)
	}
}
