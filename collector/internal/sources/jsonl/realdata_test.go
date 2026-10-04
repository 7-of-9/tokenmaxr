package jsonl_test

import (
	"os"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/claude"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/codex"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/cursor"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/grok"
)

// TestRealData parses this machine's real logs from offset 0 and logs only
// aggregate numbers (never text, emails or ids). Run with D0M1_REALDATA=1;
// add D0M1_REALDATA_STABLE=1 to skip files written in the last hour, so the
// totals can be compared with an independent script while tools are running.
func TestRealData(t *testing.T) {
	if os.Getenv("D0M1_REALDATA") != "1" {
		t.Skip("set D0M1_REALDATA=1 to parse this machine's real logs")
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
	for _, src := range []sources.Source{claude.New(), codex.New(), grok.New(), cursor.New()} {
		start := time.Now()
		if err := src.Prepare(env); err != nil {
			t.Fatalf("%s prepare: %v", src.Name(), err)
		}
		files, err := src.Files(env)
		if err != nil {
			t.Fatalf("%s files: %v", src.Name(), err)
		}
		var all sources.Batch
		var bytesRead, carryBytes int64
		carried, errs := 0, 0
		stable := os.Getenv("D0M1_REALDATA_STABLE") == "1"
		for _, f := range files {
			if st, err := os.Stat(f); stable && err == nil && time.Since(st.ModTime()) < time.Hour {
				continue
			}
			b, cur, err := src.Parse(env, f, sources.Cursor{})
			if err != nil {
				errs++
				continue
			}
			bytesRead += cur.Offset
			if len(cur.Carry) > 0 {
				carried++
				carryBytes += int64(len(cur.Carry))
			}
			all.Add(b)
		}
		// Merge usage by id the way the server does.
		merged := map[string]model.UsageEvent{}
		for _, u := range all.Usage {
			if m, ok := merged[u.ID]; ok {
				m.Tokens.Max(u.Tokens)
				if u.TS.Before(m.TS) {
					m.TS = u.TS
				}
				merged[u.ID] = m
			} else {
				merged[u.ID] = u
			}
		}
		var sum model.Tokens
		var first, last time.Time
		models := map[string]bool{}
		for _, u := range merged {
			sum.In += u.In
			sum.CacheW += u.CacheW
			sum.CacheW1h += u.CacheW1h
			sum.CacheR += u.CacheR
			sum.Out += u.Out
			sum.Reasoning += u.Reasoning
			sum.Calls += u.Calls
			if first.IsZero() || u.TS.Before(first) {
				first = u.TS
			}
			if u.TS.After(last) {
				last = u.TS
			}
			models[u.Model] = true
		}
		actIDs := map[string]bool{}
		withUsage, noUsage := 0, 0
		for _, a := range all.Activity {
			if actIDs[a.ID] {
				continue
			}
			actIDs[a.ID] = true
			if a.HasUsage {
				withUsage++
			} else {
				noUsage++
			}
		}
		promptIDs := map[string]bool{}
		noModel, truncated := 0, 0
		for _, p := range all.Prompts {
			if promptIDs[p.ID] {
				continue
			}
			promptIDs[p.ID] = true
			if p.Model == "" {
				noModel++
			}
			if len(p.Text) >= model.MaxPromptBytes-100 {
				truncated++
			}
		}
		t.Logf("%s: files=%d parseErrors=%d bytes=%d carriedFiles=%d carryBytes=%d elapsed=%s", src.Name(), len(files), errs, bytesRead, carried, carryBytes, time.Since(start).Round(time.Millisecond))
		t.Logf("%s: usage events=%d uniqueIds=%d models=%d in=%d cacheW=%d cacheW1h=%d cacheR=%d out=%d reasoning=%d calls=%d effective=%.0f",
			src.Name(), len(all.Usage), len(merged), len(models), sum.In, sum.CacheW, sum.CacheW1h, sum.CacheR, sum.Out, sum.Reasoning, sum.Calls, sum.Effective())
		t.Logf("%s: activity=%d (hasUsage=%d, historyOnly=%d) prompts=%d (noModel=%d, truncated=%d) firstTs=%s lastTs=%s",
			src.Name(), len(actIDs), withUsage, noUsage, len(promptIDs), noModel, truncated, first.Format(time.RFC3339), last.Format(time.RFC3339))
	}
}
