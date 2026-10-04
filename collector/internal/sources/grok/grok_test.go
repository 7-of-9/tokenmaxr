package grok

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/jsonl/sourcetest"
)

const (
	sidA = "a0a0a0a0-0000-4000-8000-00000000000a"
	sidB = "b0b0b0b0-0000-4000-8000-00000000000b"
	sidC = "c0c0c0c0-0000-4000-8000-00000000000c"
	sidD = "d0d0d0d0-0000-4000-8000-00000000000d"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t
}

func setup(t *testing.T, ts string) (string, *sources.Env) {
	t.Helper()
	old := now
	now = func() time.Time { return at(ts) }
	t.Cleanup(func() { now = old })
	home := sourcetest.Home(t)
	// Fixture files are freshly written; make every session look quiet.
	old8 := at("2026-09-20T09:00:00Z")
	filepath.WalkDir(filepath.Join(home, ".grok"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			os.Chtimes(p, old8, old8)
		}
		return nil
	})
	return home, sourcetest.Env(home)
}

func sessionDir(home, sid string) string {
	return filepath.Join(home, ".grok", "sessions", "C%3A%5Cwork%5Cgrok", sid)
}

func id(kind, key string) string {
	return model.EventID(kind, model.ProviderXAI, model.SourceGrokCLI, key)
}

type wantUsage struct {
	tokens model.Tokens
	model  string
	ts     string
}

func checkUsage(t *testing.T, m sourcetest.Merged, want map[string]wantUsage) {
	t.Helper()
	if len(m.Usage) != len(want) {
		t.Errorf("got %d usage events, want %d", len(m.Usage), len(want))
	}
	for key, w := range want {
		u, ok := m.Usage[id(model.KindUsage, key)]
		if !ok {
			t.Errorf("usage %s missing", key)
			continue
		}
		if u.Tokens != w.tokens || u.Model != w.model || !u.TS.Equal(at(w.ts)) {
			t.Errorf("usage %s = %+v %s %s, want %+v %s %s", key, u.Tokens, u.Model, u.TS, w.tokens, w.model, w.ts)
		}
	}
}

var wantA = map[string]wantUsage{
	// modelUsage splits one turn into one event per model.
	sidA + "-5:grok-4.6-build": {model.Tokens{In: 250, CacheW: 50, CacheR: 600, Out: 150, Reasoning: 80, Calls: 2}, "grok-4.6-build", "2026-09-28T10:00:05Z"},
	sidA + "-5:grok-code-fast": {model.Tokens{In: 100, Out: 50, Calls: 1}, "grok-code-fast", "2026-09-28T10:00:05Z"},
	// No modelUsage: the model the prompt named.
	sidA + "-10:grok-4.7": {model.Tokens{In: 200, CacheR: 100, Out: 40, Reasoning: 10, Calls: 1}, "grok-4.7", "2026-09-28T10:00:30Z"},
}

func TestUpdates(t *testing.T) {
	home, env := setup(t, "2026-09-28T10:05:00Z")
	path := filepath.Join(sessionDir(home, sidA), "updates.jsonl")
	b, cur := sourcetest.Parse(t, New(), env, path, sources.Cursor{})
	m := sourcetest.Merge(b)
	checkUsage(t, m, wantA)
	for _, u := range m.Usage {
		if u.Session != model.SessionKey(model.ProviderXAI, sidA) || u.Q != model.QualityExact || u.Acct != "a_xai" {
			t.Errorf("usage event = %+v", u)
		}
	}
	// A message is one text block plus the images after it; the second text
	// block is the next message, written back to back.
	prompts := map[string][3]string{
		sidA + "-1": {"Please review @notes.txt", "grok-4.6", "2026-09-28T10:00:00.25Z"},
		sidA + "-3": {"And this screenshot", "grok-4.6", "2026-09-28T10:00:00.25Z"},
		sidA + "-8": {"Second question", "grok-4.7", "2026-09-28T10:00:20.25Z"},
	}
	if len(m.Prompts) != len(prompts) || len(m.Activity) != len(prompts) {
		t.Errorf("got %d prompts / %d activity, want %d (hidden message skipped, last one pending)", len(m.Prompts), len(m.Activity), len(prompts))
	}
	for key, w := range prompts {
		p := m.Prompts[id(model.KindPrompt, key)]
		if p.Text != w[0] || p.Model != w[1] || !p.TS.Equal(at(w[2])) || p.Workspace != `C:\work\grok` {
			t.Errorf("prompt %s = %+v", key, p)
		}
		if a, ok := m.Activity[id(model.KindActivity, key)]; !ok || !a.HasUsage {
			t.Errorf("activity %s = %+v", key, a)
		}
	}
	if !bytes.Contains(cur.Carry, []byte("Still typing")) {
		t.Errorf("carry does not hold the unfinished message: %s", cur.Carry)
	}
	// Ten minutes later the unfinished message is flushed.
	now = func() time.Time { return at("2026-09-28T10:10:41Z") }
	b, _ = sourcetest.Parse(t, New(), env, path, cur)
	if len(b.Prompts) != 1 || b.Prompts[0].Text != "Still typing" || b.Prompts[0].Model != "grok-4.7" {
		t.Errorf("flushed prompt = %+v", b.Prompts)
	}
}

func TestUsageJSONOnlyWithoutTurnCompleted(t *testing.T) {
	home, env := setup(t, "2026-09-28T10:05:00Z")
	// A has turn_completed lines, so its (double-counting) usage.json is ignored.
	for _, sid := range []string{sidA, sidD} {
		path := filepath.Join(sessionDir(home, sid), "usage.json")
		b, cur := sourcetest.Parse(t, New(), env, path, sources.Cursor{})
		st, _ := os.Stat(path)
		if len(b.Usage) != 0 || cur.Offset != st.Size() {
			t.Errorf("%s usage.json: %d events, offset %d", sid, len(b.Usage), cur.Offset)
		}
	}
	// B has none: usage.json turns are the fallback.
	path := filepath.Join(sessionDir(home, sidB), "usage.json")
	b, cur := sourcetest.Parse(t, New(), env, path, sources.Cursor{})
	checkUsage(t, sourcetest.Merge(b), map[string]wantUsage{
		sidB + ":1:2026-09-20T08:00:00.123456+00:00": {model.Tokens{In: 300, CacheR: 200, Out: 60, Reasoning: 20, Calls: 2}, "grok-4.5-build", "2026-09-20T08:00:00.123456Z"},
		sidB + ":2:2026-09-20T08:05:00.654321+00:00": {model.Tokens{In: 190, CacheW: 10, CacheR: 100, Out: 30, Reasoning: 5, Calls: 1}, "grok-4.5", "2026-09-20T08:05:00.654321Z"},
	})
	if st, _ := os.Stat(path); cur.Offset != st.Size() {
		t.Errorf("offset %d, want the whole file", cur.Offset)
	}
	// A session that is still active is not trusted yet: its first
	// turn_completed line may not have landed.
	fresh := at("2026-09-28T10:04:00Z")
	os.Chtimes(filepath.Join(sessionDir(home, sidB), "updates.jsonl"), fresh, fresh)
	b, cur = sourcetest.Parse(t, New(), env, path, sources.Cursor{})
	if len(b.Usage) != 0 || cur.Offset != 0 {
		t.Errorf("active session fallback: %d events, offset %d", len(b.Usage), cur.Offset)
	}
}

func TestAllSessionsNoDoubleCount(t *testing.T) {
	_, env := setup(t, "2026-09-28T10:05:00Z")
	src := New()
	if err := src.Prepare(env); err != nil {
		t.Fatal(err)
	}
	files, err := src.Files(env)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 7 {
		t.Errorf("Files() = %d files, want 4 updates.jsonl + 3 usage.json", len(files))
	}
	var batches []sources.Batch
	for _, f := range files {
		b, _ := sourcetest.Parse(t, src, env, f, sources.Cursor{})
		batches = append(batches, b)
	}
	m := sourcetest.Merge(batches...)
	want := map[string]wantUsage{
		sidB + ":1:2026-09-20T08:00:00.123456+00:00": {model.Tokens{In: 300, CacheR: 200, Out: 60, Reasoning: 20, Calls: 2}, "grok-4.5-build", "2026-09-20T08:00:00.123456Z"},
		sidB + ":2:2026-09-20T08:05:00.654321+00:00": {model.Tokens{In: 190, CacheW: 10, CacheR: 100, Out: 30, Reasoning: 5, Calls: 1}, "grok-4.5", "2026-09-20T08:05:00.654321Z"},
		// The fork's own turn; its replayed copy of A's turn shares A's ids.
		sidC + "-3:grok-code-fast": {model.Tokens{In: 400, Out: 20, Calls: 1}, "grok-code-fast", "2026-09-28T10:01:00Z"},
		// No modelUsage and no prompt in the file: summary.json's model.
		sidD + "-1:grok-4.6": {model.Tokens{In: 10, Out: 1, Calls: 1}, "grok-4.6", "2026-09-28T10:01:10Z"},
	}
	for k, v := range wantA {
		want[k] = v
	}
	checkUsage(t, m, want)
	// The fork's delegated prompt is not a prompt; B's old prompt is.
	if _, ok := m.Prompts[id(model.KindPrompt, sidC+"-1")]; ok {
		t.Error("subagent fork prompt was emitted")
	}
	if p, ok := m.Prompts[id(model.KindPrompt, sidB+"-1")]; !ok || p.Text != "Old session prompt" || p.Model != "grok-4.5" {
		t.Errorf("session B prompt = %+v", p)
	}
}

func TestSplitReads(t *testing.T) {
	home, env := setup(t, "2026-09-28T10:05:00Z")
	for _, sid := range []string{sidA, sidC} {
		path := filepath.Join(sessionDir(home, sid), "updates.jsonl")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sourcetest.SplitCheck(t, New(), env, path, data)
		sourcetest.Growing(t, New(), env, path, data, 83)
	}
}

func TestMidTurnMessagesAndReminders(t *testing.T) {
	home, env := setup(t, "2026-09-28T10:05:00Z")
	const sid = "e0e0e0e0-0000-4000-8000-00000000000e"
	dir := sessionDir(home, sid)
	os.MkdirAll(dir, 0o755)
	chunk := func(eid, content, meta string) string {
		return `{"timestamp":1790589600,"method":"session/update","params":{"sessionId":"` + sid + `","update":{"sessionUpdate":"user_message_chunk","content":` + content +
			`,"_meta":{"modelId":"grok-4.6"` + meta + `}},"_meta":{"eventId":"` + eid + `","agentTimestampMs":1790589600250}}}`
	}
	other := func(kind, eid string) string {
		return `{"timestamp":1790589601,"method":"session/update","params":{"sessionId":"` + sid + `","update":{"sessionUpdate":"` + kind + `"},"_meta":{"eventId":"` + eid + `"}}}`
	}
	img := `{"type":"image","data":"iVBORw0KGgo=","mimeType":"image/png"}`
	lines := []string{
		// Three messages typed while a turn ran, flushed back to back.
		chunk("m-1", `{"type":"text","text":"first follow-up","_meta":{"displayText":"first follow-up"}}`, `,"interjection":true`),
		chunk("m-2", `{"type":"text","text":"second follow-up"}`, `,"interjection":true`),
		chunk("m-3", img, `,"interjection":true`),
		chunk("m-4", `{"type":"text","text":"third follow-up"}`, `,"interjection":true`),
		other("agent_message_chunk", "m-5"),
		// A reminder injected after a goal update, not hidden: not a prompt.
		other("goal_updated", "m-6"),
		chunk("m-7", `{"type":"text","text":"<system-reminder>goal changed</system-reminder>"}`, ``),
		other("turn_completed", "m-8"),
		// A hidden reminder written right before a typed message.
		chunk("m-9", `{"type":"text","text":"<system-reminder>background task done</system-reminder>"}`, `,"hideFromScrollback":true`),
		chunk("m-10", `{"type":"text","text":"typed after the reminder"}`, ``),
		other("agent_message_chunk", "m-11"),
	}
	path := filepath.Join(dir, "updates.jsonl")
	data := []byte(strings.Join(lines, "\n") + "\n")
	os.WriteFile(path, data, 0o644)
	b, _ := sourcetest.Parse(t, New(), env, path, sources.Cursor{})
	m := sourcetest.Merge(b)
	want := map[string]string{"m-1": "first follow-up", "m-2": "second follow-up", "m-4": "third follow-up", "m-10": "typed after the reminder"}
	if len(m.Prompts) != len(want) || len(m.Activity) != len(want) {
		t.Fatalf("got %d prompts / %d activity, want %d", len(m.Prompts), len(m.Activity), len(want))
	}
	for key, text := range want {
		if p := m.Prompts[id(model.KindPrompt, key)]; p.Text != text {
			t.Errorf("prompt %s = %+v", key, p)
		}
	}
	sourcetest.SplitCheck(t, New(), env, path, data)
}
