package evidence_test

import (
	"crypto/rand"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/accounts"
	"github.com/7-of-9/tokenmaxr/collector/internal/evidence"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/scan"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/claude"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/codex"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/cursor"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/gemini"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/grok"
)

// TestRealDataCoverage attributes this machine's real history from evidence
// harvested into memory and reports usage and prompt counts per quality
// (numbers only). It runs only with D0M1_REALDATA_ATTRIB=1 and reads the
// tools' files read-only.
//
// It asserts the SPEC "Accounts" coverage on the owner's machine: every
// Claude Code usage event is recorded or bounded, and the account that was
// signed in from 2026-09-29T23:27Z to 2026-10-01T00:02Z (not the current
// one) owns the expected usage events in that window.
func TestRealDataCoverage(t *testing.T) {
	if os.Getenv("D0M1_REALDATA_ATTRIB") != "1" {
		t.Skip("set D0M1_REALDATA_ATTRIB=1 to attribute this machine's real history")
	}
	home, err := paths.UserHome()
	if err != nil {
		t.Fatal(err)
	}
	k := make([]byte, 32)
	rand.Read(k)
	hash := func(p, id string) string { return model.AccountHash(k, p, id) }
	start := time.Now()
	res := evidence.Harvest(evidence.Options{Home: home, CodexHome: paths.CodexHome(home), Hash: hash}, map[string]evidence.Mark{})
	t.Logf("harvest: %d records from %d files in %s", len(res.Records), res.Files, time.Since(start).Round(time.Millisecond))
	recs := evidence.Merge(nil, res.Records)
	bySrc := map[string]int{}
	for _, r := range recs {
		bySrc[r.Source+"/"+r.Kind]++
	}
	keys := make([]string, 0, len(bySrc))
	for k := range bySrc {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		t.Logf("  %-40s %6d", k, bySrc[k])
	}
	now := time.Now()
	var spans []evidence.Span
	live := map[string]string{}
	for _, o := range accounts.Probe(home, paths.CodexHome(home)) {
		a := accounts.Hash(k, o)
		live[o.Provider] = a
		spans = append(spans, evidence.Span{Provider: o.Provider, Acct: a, From: now, To: now})
	}
	ix := evidence.Build(recs, spans)
	// Distinct accounts per provider in the evidence, and whether the live
	// probe's account is one of them (counts and booleans only).
	seen := map[string]map[string]bool{}
	for _, r := range recs {
		if r.Acct == "" {
			continue
		}
		if seen[r.Provider] == nil {
			seen[r.Provider] = map[string]bool{}
		}
		seen[r.Provider][r.Acct] = true
	}
	for p, m := range seen {
		_, hasLive := live[p]
		t.Logf("evidence accounts %-10s %d (live probe present: %v, live among them: %v)", p, len(m), hasLive, m[live[p]])
	}
	env := &sources.Env{
		Home:      home,
		CodexHome: paths.CodexHome(home),
		Machine:   "realdata",
		Attribute: func(provider string, ts time.Time, sessionID string, h sources.Hint) (string, string) {
			return accounts.Resolve(nil, ix, "", provider, ts, sessionID, h)
		},
		HashID:      hash,
		Label:       func(string) string { return "" },
		TZOffsetMin: func(time.Time) int { return 0 },
		Prompts:     true,
	}
	srcs := []sources.Source{claude.New(), codex.New(), grok.New(), cursor.New(), gemini.New()}
	rep, err := scan.DryRun(srcs, env, nil, nil, now, os.Stderr)
	if err != nil {
		t.Logf("dry run: %v", err)
	}
	order := []string{model.AcctRecorded, model.AcctSession, model.AcctTimeline, model.AcctBounded, model.AcctLineage, model.AcctInferred, model.AcctUnknown}
	names := make([]string, 0, len(rep.Sources))
	for n := range rep.Sources {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		s := rep.Sources[n]
		for _, kind := range []string{"usage", "prompts"} {
			line := ""
			for _, q := range order {
				if v := s.ByAcctQ[kind][q]; v > 0 {
					line += " " + q + "=" + itoa(v)
				}
			}
			t.Logf("%-12s %-8s%s", n, kind, line)
		}
	}
	t.Logf("conflicts: %v", ix.Conflicts())

	cc := rep.Sources[model.SourceClaudeCode]
	if cc == nil || cc.Usage.Events == 0 {
		t.Skip("no Claude Code history on this machine")
	}
	u := cc.ByAcctQ["usage"]
	if strong := u[model.AcctRecorded] + u[model.AcctBounded]; strong != cc.Usage.Events {
		t.Errorf("claude-code usage recorded+bounded = %d of %d, want all", strong, cc.Usage.Events)
	}

	// The window when the other account was signed in.
	from := time.Date(2026, 9, 29, 23, 27, 14, 0, time.UTC)
	to := time.Date(2026, 10, 1, 0, 2, 13, 0, time.UTC)
	other := map[string]int{}
	for _, e := range scanUsage(t, claude.New(), env) {
		if e.TS.Before(from) || !e.TS.Before(to) {
			continue
		}
		if e.Acct != live[model.ProviderAnthropic] {
			other[e.AcctQ]++
		}
	}
	n := 0
	for _, v := range other {
		n += v
	}
	t.Logf("claude-code usage in the 2026-09-29/10-01 window on the other account: %d %v", n, other)
	// SPEC "Accounts" coverage table: 1,716 usage events. Override with
	// D0M1_REALDATA_HEIMDALL (or "-" to skip) on another machine.
	want := os.Getenv("D0M1_REALDATA_HEIMDALL")
	if want == "" {
		want = "1716"
	}
	if want != "-" && itoa(int64(n)) != want {
		t.Errorf("other-account usage in the window = %d, want %s", n, want)
	}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

// scanUsage parses every file of src and returns its usage events, merged
// by id as the server would (the earliest timestamp wins).
func scanUsage(t *testing.T, src sources.Source, env *sources.Env) []model.UsageEvent {
	t.Helper()
	if err := src.Prepare(env); err != nil {
		t.Fatal(err)
	}
	files, err := src.Files(env)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]model.UsageEvent{}
	for _, p := range files {
		b, cur, err := src.Parse(env, p, sources.Cursor{})
		if err != nil {
			continue
		}
		if len(cur.Carry) > 0 {
			if b2, _, err := src.Parse(env, p, cur); err == nil {
				b.Usage = append(b.Usage, b2.Usage...)
			}
		}
		for _, e := range b.Usage {
			if have, ok := byID[e.ID]; ok {
				scan.MergeUsage(&have, e)
				byID[e.ID] = have
			} else {
				byID[e.ID] = e
			}
		}
	}
	out := make([]model.UsageEvent, 0, len(byID))
	for _, e := range byID {
		out = append(out, e)
	}
	return out
}
