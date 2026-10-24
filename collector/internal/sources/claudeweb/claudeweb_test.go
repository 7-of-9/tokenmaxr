package claudeweb

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/jsonl/sourcetest"
	"github.com/7-of-9/tokenmaxr/collector/internal/webclaude"
)

const (
	org  = "11111111-2222-4333-8444-555555555555"
	conv = "73c9aaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
)

var t0 = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func uuid(n int) string { return "a0a0aaaa-0000-4000-8000-0000000000" + twoDigits(n) }

func twoDigits(n int) string { return string([]byte{byte('0' + n/10), byte('0' + n%10)}) }

func rec(kind string, n int, ts time.Time) webclaude.Record {
	text := "prompt text"
	return webclaude.Record{V: 1, Kind: kind, Platform: webclaude.PlatformWeb, Org: org, Conv: conv,
		Human: uuid(n), Assistant: uuid(n + 1), TS: ts.Format(time.RFC3339Nano), PromptTS: ts.Add(-2 * time.Second).Format(time.RFC3339Nano),
		Model: "claude-fable-5-1", ChatPlatform: "CLAUDE_AI", Stop: "end_turn", Calls: 2,
		Est: webclaude.Estimate{In: 1000, Out: 50, Algo: webclaude.Algorithm}, Text: &text,
		At: ts.Add(time.Minute).Format(time.RFC3339Nano)}
}

// setup writes records to a spool and returns an Env for it.
func setup(t *testing.T, set webclaude.Settings, recs ...webclaude.Record) (*sources.Env, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "web-claude")
	if _, err := webclaude.Enrol(dir, t0); err != nil {
		t.Fatal(err)
	}
	if err := webclaude.SaveSettings(dir, set); err != nil {
		t.Fatal(err)
	}
	var lines [][]byte
	var claims []string
	for _, r := range recs {
		b, _ := json.Marshal(r)
		lines = append(lines, b)
		if r.Kind == webclaude.KindLive {
			claims = append(claims, webclaude.UsageID(r.Assistant))
		}
	}
	if err := webclaude.Append(dir, time.Now(), lines, claims); err != nil {
		t.Fatal(err)
	}
	env := sourcetest.Env(t.TempDir())
	env.WebClaude = dir
	return env, dir
}

func parseAll(t *testing.T, env *sources.Env) sources.Batch {
	t.Helper()
	s := New()
	if err := s.Prepare(env); err != nil {
		t.Fatal(err)
	}
	files, err := s.Files(env)
	if err != nil || len(files) != 1 {
		t.Fatalf("files %v %v", files, err)
	}
	b, cur, err := s.Parse(env, files[0], sources.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(files[0])
	if cur.Offset != st.Size() {
		t.Fatalf("offset %d of %d", cur.Offset, st.Size())
	}
	return b
}

func TestLiveRecordIsEstimatedWebUsageWithCanonicalIDs(t *testing.T) {
	r := rec(webclaude.KindLive, 1, time.Date(2026, 10, 23, 7, 39, 9, 0, time.UTC))
	env, _ := setup(t, webclaude.Settings{Prompts: webclaude.PromptsLive}, r)
	b := parseAll(t, env)
	if len(b.Usage) != 1 || len(b.Activity) != 1 || len(b.Prompts) != 1 {
		t.Fatalf("%d usage %d activity %d prompts", len(b.Usage), len(b.Activity), len(b.Prompts))
	}
	u := b.Usage[0]
	if u.Source != model.SourceClaudeWeb || u.Provider != model.ProviderAnthropic || u.Q != model.QualityEstimated || u.PV != webclaude.PVLive {
		t.Fatalf("usage %+v", u)
	}
	if u.ID != model.EventID(model.KindUsage, model.ProviderAnthropic, model.IDSourceClaudeChat, "msg:"+r.Assistant) {
		t.Fatal("usage id is not the canonical Claude chat id")
	}
	if u.In != 1000 || u.Out != 50 || u.Calls != 2 || u.CacheR != 0 || u.CacheW != 0 || u.Model != "claude-fable-5-1" {
		t.Fatalf("tokens %+v", u.Tokens)
	}
	if u.Session != model.SessionKey(model.ProviderAnthropic, conv) {
		t.Fatal("session is not the hashed conversation")
	}
	// Attribution follows the organisation, hashed as "org:<uuid>".
	if want := model.AccountHash([]byte("test-key"), model.ProviderAnthropic, "org:"+org); u.Acct != want || u.AcctQ != model.AcctRecorded {
		t.Fatalf("acct %s %s", u.Acct, u.AcctQ)
	}
	a := b.Activity[0]
	if !a.HasUsage || a.Source != model.SourceClaudeWeb || a.ID != model.EventID(model.KindActivity, model.ProviderAnthropic, model.IDSourceClaudeChat, "prompt:"+r.Human) {
		t.Fatalf("activity %+v", a)
	}
	if !a.TS.Equal(u.TS.Add(-2 * time.Second)) {
		t.Fatal("activity is not at the prompt time")
	}
	p := b.Prompts[0]
	if p.Text != "prompt text" || p.Model != "claude-fable-5-1" || p.Source != model.SourceClaudeWeb || p.Workspace != "" {
		t.Fatalf("prompt %+v", p)
	}
	// Off the wire, every event names its chat id source (the rollup keeps
	// Claude chat out of its seal by it).
	if u.IDSource != model.IDSourceClaudeChat || a.IDSource != model.IDSourceClaudeChat || p.IDSource != model.IDSourceClaudeChat {
		t.Fatalf("id sources %q %q %q", u.IDSource, a.IDSource, p.IDSource)
	}
	for _, s := range []string{u.Session, u.Acct, a.Session} {
		if s == conv || s == org {
			t.Fatal("raw id in an event")
		}
	}
}

func TestReplayGivesTheSameEvents(t *testing.T) {
	r := rec(webclaude.KindLive, 1, time.Date(2026, 10, 23, 7, 39, 9, 0, time.UTC))
	dup := r
	env, _ := setup(t, webclaude.Settings{}, r, dup)
	first := sourcetest.Merge(parseAll(t, env))
	second := sourcetest.Merge(parseAll(t, env), parseAll(t, env))
	if len(first.Usage) != 1 || len(second.Usage) != 1 || len(first.Activity) != 1 {
		t.Fatalf("usage %d/%d activity %d", len(first.Usage), len(second.Usage), len(first.Activity))
	}
	for id, u := range first.Usage {
		if second.Usage[id].Tokens != u.Tokens {
			t.Fatal("tokens changed on replay")
		}
	}
}

func TestPromptRecordOnlyWhenCaptureIsOn(t *testing.T) {
	env, _ := setup(t, webclaude.Settings{Prompts: webclaude.PromptsAll}, rec(webclaude.KindLive, 1, time.Date(2026, 10, 23, 7, 39, 9, 0, time.UTC)))
	env.Prompts = false
	if b := parseAll(t, env); len(b.Prompts) != 0 || len(b.Usage) != 1 {
		t.Fatalf("prompts %d", len(b.Prompts))
	}
}

// Chat text in the spool (an older host kept it) is uploaded only as the
// chat prompt setting allows for its kind, even with prompt capture on.
func TestPromptRecordFollowsTheChatPromptSetting(t *testing.T) {
	live := rec(webclaude.KindLive, 1, time.Date(2026, 10, 23, 7, 39, 9, 0, time.UTC))
	old := rec(webclaude.KindSweep, 10, time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC))
	old.Platform = ""
	for mode, want := range map[string]int{"": 0, webclaude.PromptsOff: 0, webclaude.PromptsLive: 1, webclaude.PromptsAll: 2} {
		env, _ := setup(t, webclaude.Settings{Prompts: mode}, live, old)
		b := parseAll(t, env)
		if len(b.Prompts) != want || len(b.Usage) != 2 {
			t.Fatalf("%q: prompts %d usage %d", mode, len(b.Prompts), len(b.Usage))
		}
		if want == 1 && b.Prompts[0].Source != model.SourceClaudeWeb {
			t.Fatalf("%q kept the swept prompt", mode)
		}
	}
}

func TestDesktopPlatformIsNative(t *testing.T) {
	r := rec(webclaude.KindLive, 1, time.Date(2026, 10, 23, 7, 39, 9, 0, time.UTC))
	r.Platform = webclaude.PlatformDesktop
	env, _ := setup(t, webclaude.Settings{}, r)
	b := parseAll(t, env)
	if b.Usage[0].Source != model.SourceClaudeNative || b.Activity[0].Source != model.SourceClaudeNative || b.Usage[0].Q != model.QualityEstimated {
		t.Fatalf("%+v", b.Usage[0])
	}
}

func TestSweepClassification(t *testing.T) {
	after := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	before := time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)
	live := rec(webclaude.KindLive, 1, after)
	claimed := rec(webclaude.KindSweep, 1, after) // same turn as live
	claimed.Platform = ""
	old := rec(webclaude.KindSweep, 10, before)
	old.Platform = ""
	app := rec(webclaude.KindSweep, 20, after)
	app.Platform = ""
	app.At = after.Add(7 * time.Hour).Format(time.RFC3339Nano) // swept after it settled
	unsettled := rec(webclaude.KindSweep, 30, after)
	unsettled.At = after.Add(time.Hour).Format(time.RFC3339Nano)
	env, _ := setup(t, webclaude.Settings{}, live, claimed, old, app, unsettled)
	b := parseAll(t, env)
	got := map[string]string{}
	pv := map[string]int{}
	for _, u := range b.Usage {
		got[u.ID] = u.Source
		pv[u.ID] = u.PV
	}
	want := map[string]string{
		webclaude.UsageID(live.Assistant): model.SourceClaudeWeb,
		webclaude.UsageID(old.Assistant):  model.SourceClaudeChat,
		webclaude.UsageID(app.Assistant):  model.SourceClaudeNative,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for id, src := range want {
		if got[id] != src {
			t.Fatalf("%s: %s, want %s", id, got[id], src)
		}
	}
	if pv[webclaude.UsageID(live.Assistant)] != webclaude.PVLive || pv[webclaude.UsageID(app.Assistant)] != webclaude.PVSweep {
		t.Fatal("pv")
	}
	// The claimed sweep record emitted nothing: one activity per remaining turn.
	if len(b.Activity) != 3 {
		t.Fatalf("activity %d", len(b.Activity))
	}

	off := false
	env, _ = setup(t, webclaude.Settings{InferApps: &off}, app, old)
	b = parseAll(t, env)
	if len(b.Usage) != 1 || b.Usage[0].Source != model.SourceClaudeChat {
		t.Fatalf("inference off: %+v", b.Usage)
	}
}

// The fleet's capture start (ChatBefore) is earlier than this machine's T0:
// a turn between them may have been recorded live by another machine, so the
// sweep never reports it, whether or not the app inference is on.
func TestSweepSkipsTurnsBetweenTheFleetStartAndT0(t *testing.T) {
	fleet := t0.Add(-10 * 24 * time.Hour)
	off := false
	for _, set := range []webclaude.Settings{{ChatBefore: fleet}, {ChatBefore: fleet, InferApps: &off}} {
		cls := func(ts time.Time) Class {
			r := rec(webclaude.KindSweep, 1, ts)
			r.Platform = ""
			return Classify(&r, nil, nil, t0, set)
		}
		if c := cls(fleet.Add(-time.Hour)); c.Source != model.SourceClaudeChat || c.Skip {
			t.Fatalf("before the fleet start: %+v", c)
		}
		if c := cls(t0.Add(-time.Hour)); !c.Skip {
			t.Fatalf("before this machine's t0, infer %v: %+v", set.Infer(), c)
		}
	}
	// A ChatBefore later than t0 changes nothing.
	r := rec(webclaude.KindSweep, 1, t0.Add(-time.Hour))
	if c := Classify(&r, nil, nil, t0, webclaude.Settings{ChatBefore: t0.Add(time.Hour)}); c.Source != model.SourceClaudeChat {
		t.Fatalf("%+v", c)
	}
}

func TestClassifyWithoutEnrolmentIsClientNotRecorded(t *testing.T) {
	r := rec(webclaude.KindSweep, 1, time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC))
	if c := Classify(&r, nil, nil, time.Time{}, webclaude.Settings{}); c.Source != model.SourceClaudeChat || c.Skip {
		t.Fatalf("%+v", c)
	}
}

func TestInvalidSpoolLinesAreSkipped(t *testing.T) {
	good := rec(webclaude.KindLive, 1, time.Date(2026, 10, 23, 7, 39, 9, 0, time.UTC))
	bad := good
	bad.Org = "not-a-uuid"
	env, dir := setup(t, webclaude.Settings{}, bad, good)
	f, _ := os.OpenFile(filepath.Join(webclaude.SpoolDir(dir), time.Now().UTC().Format("2006-01-02")+".jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("{not json}\n{\"v\":1,\"kind\":\"live\"") // and a partial last line
	f.Close()
	b := parseAll2(t, env)
	if len(b.Usage) != 1 {
		t.Fatalf("usage %d", len(b.Usage))
	}
}

// parseAll2 is parseAll for a spool whose last line is still being written.
func parseAll2(t *testing.T, env *sources.Env) sources.Batch {
	t.Helper()
	s := New()
	_ = s.Prepare(env)
	files, _ := s.Files(env)
	b, cur, err := s.Parse(env, files[0], sources.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(files[0])
	if cur.Offset >= st.Size() {
		t.Fatal("consumed a partial line")
	}
	return b
}

func TestNoSpoolOutsideTheOSHome(t *testing.T) {
	env := sourcetest.Env(t.TempDir())
	if files, err := New().Files(env); err != nil || files != nil {
		t.Fatal(files, err)
	}
}

// The fixed context per call (tool definitions and the system prompt floor)
// is reported as estimated cache reads, once per model call of the turn.
func TestFixedContextIsEstimatedCacheReads(t *testing.T) {
	r := rec(webclaude.KindLive, 1, time.Date(2026, 10, 23, 7, 39, 9, 0, time.UTC))
	r.Est.Ctx = 31_000
	env, _ := setup(t, webclaude.Settings{}, r)
	u := parseAll(t, env).Usage[0]
	if u.CacheR != 62_000 || u.In != 1000 || u.Out != 50 || u.CacheW != 0 || u.Q != model.QualityEstimated {
		t.Fatalf("tokens %+v", u.Tokens)
	}
	r.Est.Ctx = webclaude.MaxCtx + 1
	if r.Validate(time.Date(2026, 10, 23, 8, 0, 0, 0, time.UTC), false) == nil {
		t.Fatal("an oversized ctx validated")
	}
}

// A turn this browser saw end but whose live record never came (the tab
// closed during the read-back) is not inferred as an app turn: its sweep
// record reports it in the client's channel, below a live record's pv.
func TestPendingClaimKeepsABrowserTurnInItsChannel(t *testing.T) {
	after := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	web := rec(webclaude.KindSweep, 40, after)
	desk := rec(webclaude.KindSweep, 50, after)
	unsettled := rec(webclaude.KindSweep, 60, after)
	for _, r := range []*webclaude.Record{&web, &desk} {
		r.Platform = ""
		r.At = after.Add(7 * time.Hour).Format(time.RFC3339Nano)
	}
	unsettled.Platform = ""
	off := false
	env, dir := setup(t, webclaude.Settings{InferApps: &off}, web, desk, unsettled)
	if err := webclaude.AppendPending(dir, []webclaude.Pending{{ID: webclaude.UsageID(web.Assistant)},
		{ID: webclaude.UsageID(desk.Assistant), Platform: webclaude.PlatformDesktop},
		{ID: webclaude.UsageID(unsettled.Assistant), Platform: webclaude.PlatformWeb}}); err != nil {
		t.Fatal(err)
	}
	b := parseAll(t, env)
	got := map[string]string{}
	for _, u := range b.Usage {
		got[u.ID] = u.Source
		if u.PV != webclaude.PVSweep {
			t.Fatalf("pv %d", u.PV)
		}
	}
	want := map[string]string{
		webclaude.UsageID(web.Assistant):       model.SourceClaudeWeb,
		webclaude.UsageID(desk.Assistant):      model.SourceClaudeNative,
		webclaude.UsageID(unsettled.Assistant): model.SourceClaudeWeb,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for id, src := range want {
		if got[id] != src {
			t.Fatalf("%s: %s, want %s", id, got[id], src)
		}
	}
}

// Prompt text leaves the spool once the collector's cursor has passed it:
// the read after the one that emitted the prompt deletes it, keeping the
// file's size and time. A dry run (no ScrubSpool) changes nothing.
func TestPromptTextIsDeletedOnceTheCursorPassedIt(t *testing.T) {
	r := rec(webclaude.KindLive, 1, time.Date(2026, 10, 23, 7, 39, 9, 0, time.UTC))
	env, _ := setup(t, webclaude.Settings{Prompts: webclaude.PromptsLive}, r)
	s := New()
	if err := s.Prepare(env); err != nil {
		t.Fatal(err)
	}
	files, _ := s.Files(env)
	p := files[0]
	read := func() string { b, _ := os.ReadFile(p); return string(b) }

	for _, dry := range []bool{true, false} {
		env.ScrubSpool = !dry
		b, cur, err := s.Parse(env, p, sources.Cursor{})
		if err != nil || len(b.Prompts) != 1 || len(cur.Carry) == 0 {
			t.Fatalf("first read: %d prompts, carry %s, %v", len(b.Prompts), cur.Carry, err)
		}
		before, _ := os.Stat(p)
		_, next, err := s.Parse(env, p, cur)
		if err != nil {
			t.Fatal(err)
		}
		after, _ := os.Stat(p)
		has := strings.Contains(read(), "prompt text")
		if dry {
			if !has || len(next.Carry) == 0 {
				t.Fatalf("a dry run changed the spool (carry %s)", next.Carry)
			}
			continue
		}
		if has || len(next.Carry) != 0 {
			t.Fatalf("text kept (carry %s):\n%s", next.Carry, read())
		}
		if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) || next.Offset != cur.Offset {
			t.Fatal("the file changed shape")
		}
		// A full re-read still finds the turn, now without a prompt record.
		b, _, err = s.Parse(env, p, sources.Cursor{})
		if err != nil || len(b.Usage) != 1 || len(b.Prompts) != 0 {
			t.Fatalf("re-read: %d usage, %d prompts, %v", len(b.Usage), len(b.Prompts), err)
		}
	}
}
