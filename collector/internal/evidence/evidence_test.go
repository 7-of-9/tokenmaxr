package evidence_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/accounts"
	"github.com/7-of-9/tokenmaxr/collector/internal/evidence"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/claude"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/codex"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/grok"
)

var key = []byte("0123456789abcdef0123456789abcdef")

func hash(provider, id string) string  { return model.AccountHash(key, provider, id) }
func org(provider, uuid string) string { return hash(provider, "org:"+uuid) }

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func write(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// env attributes with Resolve over ix and no live timeline.
func env(home string, ix *evidence.Index) *sources.Env {
	return &sources.Env{
		Home:    home,
		Machine: "test",
		Attribute: func(provider string, ts time.Time, sessionID string, h sources.Hint) (string, string) {
			return accounts.Resolve(nil, ix, "", provider, ts, sessionID, h)
		},
		HashID:      hash,
		Label:       func(string) string { return "" },
		TZOffsetMin: func(time.Time) int { return 0 },
		Prompts:     true,
	}
}

func harvest(t *testing.T, home string) []evidence.Record {
	t.Helper()
	res := evidence.Harvest(evidence.Options{Home: home, Hash: hash}, map[string]evidence.Mark{})
	if !res.Complete {
		t.Fatal("harvest incomplete")
	}
	return evidence.Merge(nil, res.Records)
}

const (
	orgA  = "aaaaaaaa-0000-4000-8000-000000000001"
	orgB  = "bbbbbbbb-0000-4000-8000-000000000002"
	acctA = "11111111-0000-4000-8000-00000000000a"
	acctB = "22222222-0000-4000-8000-00000000000b"
)

func assistant(id, ts string) string {
	return `{"type":"assistant","sessionId":"s1","uuid":"u` + id + `","timestamp":"` + ts + `","message":{"id":"m` + id +
		`","model":"claude-x","usage":{"input_tokens":1,"output_tokens":2}}}`
}

func credOrg(o, ts string) string {
	return `{"type":"attachment","sessionId":"s1","uuid":"c` + ts + `","timestamp":"` + ts + `","attachment":{"type":"credential_org","organizationUuid":"` + o + `"}}`
}

// A credential_org written mid-file (a /login in another terminal) splits
// the file's events between the two accounts, each recorded.
func TestCredentialOrgSwitchSplitsFile(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", "projects", "p", "s1.jsonl")
	write(t, path,
		credOrg(orgA, "2026-09-29T10:00:00Z"),
		assistant("1", "2026-09-29T10:01:00Z"),
		credOrg(orgB, "2026-09-29T11:00:00Z"),
		assistant("2", "2026-09-29T11:01:00Z"),
	)
	recs := []evidence.Record{
		{Provider: model.ProviderAnthropic, Kind: evidence.KindOrgMap, Source: evidence.SrcClaudeBackup, Org: org(model.ProviderAnthropic, orgA), Acct: hash(model.ProviderAnthropic, acctA)},
		{Provider: model.ProviderAnthropic, Kind: evidence.KindOrgMap, Source: evidence.SrcClaudeBackup, Org: org(model.ProviderAnthropic, orgB), Acct: hash(model.ProviderAnthropic, acctB)},
	}
	ix := evidence.Build(recs, nil)
	b, _, err := claude.New().Parse(env(home, ix), path, sources.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, u := range b.Usage {
		got[u.TS.Format("15:04")] = u.Acct + "/" + u.AcctQ
	}
	if got["10:01"] != hash(model.ProviderAnthropic, acctA)+"/recorded" || got["11:01"] != hash(model.ProviderAnthropic, acctB)+"/recorded" {
		t.Errorf("split: %v", got)
	}
}

// bridge-session and autoreact-ledger records have no timestamp: they take
// the last timestamped line's, skipping untimed lines in between.
func TestBridgeSessionTakesPreviousTimestamp(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".claude", "projects", "p", "s2.jsonl"),
		`{"type":"user","sessionId":"s2","timestamp":"2026-09-10T08:00:00Z","message":{"content":"hi"}}`,
		`{"type":"atis-latch","sessionId":"s2"}`,
		`{"type":"bridge-session","sessionId":"s2","bridgeSessionId":"cse_x","ownerAccountUuid":"`+acctA+`","ownerOrganizationUuid":"`+orgA+`"}`,
		`{"type":"artifact-autoreact-ledger","v":1,"sessionId":"s2","accountUuid":"`+acctA+`","artifacts":{}}`,
	)
	recs := harvest(t, home)
	var bridge, ledger, maps int
	for _, r := range recs {
		switch {
		case r.Source == evidence.SrcClaudeBridge && r.Kind == evidence.KindSample:
			bridge++
			if !r.TS.Equal(at("2026-09-10T08:00:00Z")) || r.Acct != hash(model.ProviderAnthropic, acctA) {
				t.Errorf("bridge sample %+v", r)
			}
		case r.Source == evidence.SrcClaudeLedger:
			ledger++
			if !r.TS.Equal(at("2026-09-10T08:00:00Z")) {
				t.Errorf("ledger sample %+v", r)
			}
		case r.Kind == evidence.KindOrgMap && r.Org == org(model.ProviderAnthropic, orgA) && r.Acct == hash(model.ProviderAnthropic, acctA):
			maps++
		}
	}
	if bridge != 1 || ledger != 1 || maps != 1 {
		t.Errorf("bridge %d ledger %d orgmap %d", bridge, ledger, maps)
	}
}

func sample(provider, acct, ts string) evidence.Record {
	return evidence.Record{Provider: provider, Kind: evidence.KindSample, Source: evidence.SrcClaudeBackup, Q: evidence.QStrong, Acct: acct, TS: at(ts)}
}

func login(ts string) evidence.Record {
	return evidence.Record{Provider: model.ProviderAnthropic, Kind: evidence.KindBoundary, Source: evidence.SrcClaudeLogin, TS: at(ts)}
}

// /login boundaries confine bounded attribution to the interval holding
// the sample; an interval with no sample is not bounded.
func TestLoginBoundariesConfineBounded(t *testing.T) {
	p := model.ProviderAnthropic
	a := hash(p, acctA)
	ix := evidence.Build([]evidence.Record{
		login("2026-03-01T00:00:00Z"),
		sample(p, a, "2026-03-05T00:00:00Z"),
		login("2026-03-10T00:00:00Z"),
		login("2026-03-20T00:00:00Z"),
	}, nil)
	if acct, q, _ := ix.Fallback(p, "", at("2026-03-08T00:00:00Z")); acct != a || q != model.AcctBounded {
		t.Errorf("inside: %s %s", acct, q)
	}
	if acct, q, _ := ix.Fallback(p, "", at("2026-03-15T00:00:00Z")); acct != a || q != model.AcctInferred {
		t.Errorf("next interval: %s %s, want inferred", acct, q)
	}
	// A hint older than a /login no longer counts as recorded.
	h := sources.Hint{Kind: sources.HintAccount, ID: a, At: at("2026-03-09T00:00:00Z")}
	if _, _, ok := ix.Recorded(p, "", at("2026-03-11T00:00:00Z"), "", h); ok {
		t.Error("hint across a /login still recorded")
	}
	if acct, q, ok := ix.Recorded(p, "", at("2026-03-09T12:00:00Z"), "", h); !ok || acct != a || q != model.AcctRecorded {
		t.Errorf("hint: %s %s %v", acct, q, ok)
	}
}

// Disagreeing samples in one interval: between two agreeing samples is
// still bounded; otherwise the nearest sample, inferred, and a conflict.
func TestDisagreeingSamplesConflict(t *testing.T) {
	p := model.ProviderAnthropic
	a, b := hash(p, acctA), hash(p, acctB)
	ix := evidence.Build([]evidence.Record{
		login("2026-04-01T00:00:00Z"),
		sample(p, a, "2026-04-02T00:00:00Z"),
		sample(p, a, "2026-04-04T00:00:00Z"),
		sample(p, b, "2026-04-06T00:00:00Z"),
	}, nil)
	if acct, q, _ := ix.Fallback(p, "", at("2026-04-03T00:00:00Z")); acct != a || q != model.AcctBounded {
		t.Errorf("between agreeing: %s %s", acct, q)
	}
	if acct, q, _ := ix.Fallback(p, "", at("2026-04-05T12:00:00Z")); acct != b || q != model.AcctInferred {
		t.Errorf("between disagreeing: %s %s", acct, q)
	}
	if n := ix.Conflicts()[p]; n != 1 {
		t.Errorf("conflicts %d", n)
	}
}

// An org that is not in the map stands in for the account, one level down.
func TestUnmappedOrgDowngraded(t *testing.T) {
	p := model.ProviderAnthropic
	ix := evidence.Build(nil, nil)
	h := sources.Hint{Kind: sources.HintOrg, ID: org(p, orgB), At: at("2026-05-01T00:00:00Z")}
	acct, q, ok := ix.Recorded(p, "", at("2026-05-01T01:00:00Z"), "", h)
	if !ok || acct != org(p, orgB) || q != model.AcctBounded {
		t.Errorf("unmapped: %s %s %v", acct, q, ok)
	}
	ix = evidence.Build([]evidence.Record{{Provider: p, Kind: evidence.KindOrgMap, Source: evidence.SrcClaudeDesktop, Org: org(p, orgB), Acct: hash(p, acctB)}}, nil)
	if acct, q, _ := ix.Recorded(p, "", at("2026-05-01T01:00:00Z"), "", h); acct != hash(p, acctB) || q != model.AcctRecorded {
		t.Errorf("mapped: %s %s", acct, q)
	}
}

func tokenCount(ts, plan string) string {
	return `{"timestamp":"` + ts + `","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"limit_id":"codex","plan_type":"` + plan + `"}}}`
}

func meta(id, ts, creator string) string {
	c := ""
	if creator != "" {
		c = `,"creator_account_id":"` + creator + `"`
	}
	return `{"timestamp":"` + ts + `","type":"session_meta","payload":{"id":"` + id + `","cwd":"/w"` + c + `}}`
}

func rollout(t *testing.T, home, id string, lines ...string) {
	write(t, filepath.Join(home, ".codex", "sessions", "2026", "03", "01", "rollout-2026-03-01T00-00-00-"+id+".jsonl"), lines...)
}

// Codex X4: a plan flap or dip inside one rollout keeps the era (and its
// anchor's account); a plan change between rollouts with no anchor breaks it.
func TestCodexPlanLineage(t *testing.T) {
	p := model.ProviderOpenAI
	home := t.TempDir()
	const (
		r1 = "00000000-0000-4000-8000-000000000001"
		r2 = "00000000-0000-4000-8000-000000000002"
		r3 = "00000000-0000-4000-8000-000000000003"
		r4 = "00000000-0000-4000-8000-000000000004"
	)
	// Era 1: free (r1), then a change to plus between rollouts: a break.
	rollout(t, home, r1, meta(r1, "2026-01-01T00:00:00Z", ""), tokenCount("2026-01-01T00:01:00Z", "free"), tokenCount("2026-01-02T00:00:00Z", "free"))
	// Era 2: plus, flapping to pro inside r2, a free dip inside r3, then
	// pro into r4, whose creator id anchors the era.
	rollout(t, home, r2, meta(r2, "2026-02-01T00:00:00Z", ""),
		tokenCount("2026-02-01T00:01:00Z", "plus"), tokenCount("2026-02-03T00:00:00Z", "pro"), tokenCount("2026-02-03T00:01:00Z", "plus"), tokenCount("2026-02-04T00:00:00Z", "pro"))
	rollout(t, home, r3, meta(r3, "2026-02-05T00:00:00Z", ""),
		tokenCount("2026-02-05T00:01:00Z", "pro"), tokenCount("2026-02-06T00:00:00Z", "free"), tokenCount("2026-02-07T00:00:00Z", "pro"))
	rollout(t, home, r4, meta(r4, "2026-03-01T00:00:00Z", acctA), tokenCount("2026-03-01T00:01:00Z", "pro"))
	ix := evidence.Build(harvest(t, home), nil)
	a := hash(p, acctA)
	for _, c := range []struct {
		ts   string
		acct string
		q    string
	}{
		{"2026-02-02T00:00:00Z", a, model.AcctLineage},  // plus, before the flap
		{"2026-02-06T12:00:00Z", a, model.AcctLineage},  // inside the free dip
		{"2026-01-01T12:00:00Z", a, model.AcctInferred}, // the free era before the break: no anchor
		{"2026-02-20T00:00:00Z", a, model.AcctLineage},  // a gap with no rollout, inside the era
		{"2026-03-01T00:05:00Z", a, model.AcctInferred}, // after the last plan mark
		{"2025-12-01T00:00:00Z", a, model.AcctInferred}, // no plan data at all
	} {
		if acct, q, _ := ix.Fallback(p, "", at(c.ts)); acct != c.acct || q != c.q {
			t.Errorf("%s: %s %s, want %s", c.ts, acct, q, c.q)
		}
	}
	// The rollout's own creator id is recorded, through the session key.
	if acct, q, ok := ix.Recorded(p, "", at("2026-03-01T00:05:00Z"), r4, sources.Hint{}); !ok || acct != a || q != model.AcctRecorded {
		t.Errorf("creator: %s %s %v", acct, q, ok)
	}
}

// Grok: a session's events take the user_info of the process they ran in
// (recorded); a process with no user_info falls back to the token chain
// (lineage) when no interactive login lies between it and the next sample.
func TestGrokPidJoinAndChain(t *testing.T) {
	p := model.ProviderXAI
	home := t.TempDir()
	write(t, filepath.Join(home, ".grok", "logs", "unified.jsonl"),
		`{"ts":"2026-09-10T05:11:47Z","src":"shell","pid":100,"sid":"sess-login","lvl":"info","msg":"auth started","ctx":{"method":"grok.com"}}`,
		`{"ts":"2026-09-10T05:15:00Z","src":"shell","pid":100,"sid":"sess-login","lvl":"info","msg":"session update"}`,
		`{"ts":"2026-09-10T05:32:47Z","src":"shell","pid":200,"lvl":"info","msg":"auth init user_info check","ctx":{"user_id":"user-x","needs_user_info":false,"key_prefix":"SECRETKEY","rt_prefix":"SECRETRT"}}`,
		`{"ts":"2026-09-10T05:32:48Z","src":"shell","pid":200,"sid":"sess-ok","lvl":"info","msg":"auth started","ctx":{"method":"cached_token"}}`,
		`{"ts":"2026-09-10T06:00:00Z","src":"shell","pid":200,"sid":"sess-ok","lvl":"info","msg":"session update"}`,
	)
	ix := evidence.Build(harvest(t, home), nil)
	x := hash(p, "user-x")
	if acct, q, ok := ix.Recorded(p, "", at("2026-09-10T05:40:00Z"), "sess-ok", sources.Hint{}); !ok || acct != x || q != model.AcctRecorded {
		t.Errorf("pid join: %s %s %v", acct, q, ok)
	}
	if _, _, ok := ix.Recorded(p, "", at("2026-09-10T05:14:00Z"), "sess-login", sources.Hint{}); ok {
		t.Error("a process without user_info is not recorded")
	}
	if acct, q, _ := ix.Fallback(p, "", at("2026-09-10T05:14:00Z")); acct != x || q != model.AcctLineage {
		t.Errorf("chain: %s %s", acct, q)
	}
	// Before the interactive login the chain is broken.
	if _, q, _ := ix.Fallback(p, "", at("2026-09-10T05:00:00Z")); q == model.AcctLineage {
		t.Error("chain crossed an interactive login")
	}
	// The Grok parser attributes through the session id.
	sess := filepath.Join(home, ".grok", "sessions", "%2Fw", "sess-ok")
	write(t, filepath.Join(sess, "updates.jsonl"),
		`{"timestamp":1789104000,"params":{"update":{"sessionUpdate":"turn_completed","usage":{"inputTokens":3,"outputTokens":4,"modelCalls":1}},"_meta":{"eventId":"e1"}}}`)
	b, _, err := grok.New().Parse(env(home, ix), filepath.Join(sess, "updates.jsonl"), sources.Cursor{})
	if err != nil || len(b.Usage) != 1 {
		t.Fatalf("grok parse: %v %d", err, len(b.Usage))
	}
	if u := b.Usage[0]; u.Acct != x || u.AcctQ != model.AcctRecorded {
		t.Errorf("grok usage: %s %s", u.Acct, u.AcctQ)
	}
}

// Gemini M1: google_accounts.json written once ("old" empty) bounds every
// event to its active account.
func TestGeminiWrittenOnceBounded(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".gemini", "google_accounts.json")
	write(t, path, `{"active":"person@example.com","old":[]}`)
	when := at("2026-01-08T05:16:29Z")
	os.Chtimes(path, when, when)
	ix := evidence.Build(harvest(t, home), nil)
	a := hash("google", "person@example.com")
	for _, ts := range []string{"2026-01-01T00:00:00Z", "2026-06-01T00:00:00Z"} {
		if acct, q, _ := ix.Fallback("google", "", at(ts)); acct != a || q != model.AcctBounded {
			t.Errorf("%s: %s %s", ts, acct, q)
		}
	}
}

// Cursor K3: a tool that only ever named one account continues it as
// lineage outside the log windows; inside a window it is recorded.
func TestCursorSoleAccountLineage(t *testing.T) {
	home := t.TempDir()
	logs := evidence.CursorLogsDir(home)
	folder := filepath.Join(logs, "20260219T120000")
	logPath := filepath.Join(folder, "window1", "exthost", "anysphere.cursor-retrieval", "Cursor Indexing & Retrieval.log")
	write(t, logPath, "2026-02-19 12:00:05.000 [info] Starting RepoIndexWatcher for repo: r1, owner: auth0|user_ONE")
	end := time.Date(2026, 2, 19, 13, 0, 0, 0, time.Local)
	os.Chtimes(logPath, end, end)
	ix := evidence.Build(harvest(t, home), nil)
	a := hash("cursor", "auth0|user_ONE")
	inside := time.Date(2026, 2, 19, 12, 30, 0, 0, time.Local).UTC()
	if acct, q, ok := ix.Recorded("cursor", "", inside, "", sources.Hint{}); !ok || acct != a || q != model.AcctRecorded {
		t.Errorf("window: %s %s %v", acct, q, ok)
	}
	if acct, q, _ := ix.Fallback("cursor", "", at("2025-06-01T00:00:00Z")); acct != a || q != model.AcctLineage {
		t.Errorf("sole account: %s %s", acct, q)
	}
	// A second account in the evidence ends the sole-account rule.
	ix = evidence.Build(append(harvest(t, home), evidence.Record{Provider: "cursor", Kind: evidence.KindSample, Source: evidence.SrcCursorRetrieval,
		Q: evidence.QExact, Acct: hash("cursor", "github|user_TWO"), TS: at("2026-01-30T00:00:00Z")}), nil)
	if _, q, _ := ix.Fallback("cursor", "", at("2025-06-01T00:00:00Z")); q != model.AcctInferred {
		t.Errorf("two accounts: %s, want inferred", q)
	}
}

// The parsers keep their stream hint across incremental reads: a read that
// resumes after the identity line still attributes by it.
func TestHintSurvivesIncrementalRead(t *testing.T) {
	home := t.TempDir()
	p := model.ProviderAnthropic
	recs := []evidence.Record{{Provider: p, Kind: evidence.KindOrgMap, Source: evidence.SrcClaudeBackup, Org: org(p, orgA), Acct: hash(p, acctA)}}
	ix := evidence.Build(recs, nil)
	path := filepath.Join(home, ".claude", "projects", "p", "s1.jsonl")
	write(t, path, credOrg(orgA, "2026-09-29T10:00:00Z"), assistant("1", "2026-09-29T10:01:00Z"))
	src := claude.New()
	_, cur, err := src.Parse(env(home, ix), path, sources.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(assistant("2", "2026-09-29T10:05:00Z") + "\n")
	f.Close()
	b, _, err := src.Parse(env(home, ix), path, cur)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, u := range b.Usage {
		if u.TS.Equal(at("2026-09-29T10:05:00Z")) {
			found = true
			if u.Acct != hash(p, acctA) || u.AcctQ != model.AcctRecorded {
				t.Errorf("claude resumed: %s %s", u.Acct, u.AcctQ)
			}
		}
	}
	if !found {
		t.Error("claude resumed read emitted no usage")
	}

	// Codex: the creator id from session_meta carries over too.
	o := model.ProviderOpenAI
	const id = "00000000-0000-4000-8000-0000000000c1"
	rpath := filepath.Join(home, ".codex", "sessions", "2026", "09", "28", "rollout-2026-09-28T00-00-00-"+id+".jsonl")
	write(t, rpath, meta(id, "2026-09-28T09:00:00Z", acctB))
	cs := codex.New()
	cenv := env(home, evidence.Build(nil, nil))
	_, ccur, err := cs.Parse(cenv, rpath, sources.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	f, _ = os.OpenFile(rpath, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"timestamp":"2026-09-28T09:05:00Z","type":"token_usage_record","payload":{"response_id":"r1","usage":{"input_tokens":5,"output_tokens":1}}}` + "\n")
	f.Close()
	b, _, err = cs.Parse(cenv, rpath, ccur)
	if err != nil || len(b.Usage) != 1 {
		t.Fatalf("codex resumed: %v %d", err, len(b.Usage))
	}
	if u := b.Usage[0]; u.Acct != hash(o, acctB) || u.AcctQ != model.AcctRecorded {
		t.Errorf("codex resumed: %s %s", u.Acct, u.AcctQ)
	}
}
