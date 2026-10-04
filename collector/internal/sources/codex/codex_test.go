package codex

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/jsonl/sourcetest"
)

const (
	ridA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	ridB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	ridC = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	ridD = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	ridE = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
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
	t.Setenv("CODEX_HOME", "")
	old := now
	now = func() time.Time { return at(ts) }
	t.Cleanup(func() { now = old })
	home := sourcetest.Home(t)
	return home, sourcetest.Env(home)
}

func rolloutA(home string) string {
	return filepath.Join(home, ".codex", "sessions", "2026", "09", "27", "rollout-2026-09-27T10-00-00-"+ridA+".jsonl")
}

func rolloutB(home string) string {
	return filepath.Join(home, ".codex", "sessions", "2026", "09", "28", "rollout-2026-09-28T09-30-00-"+ridB+".jsonl")
}

func rolloutC(home string) string {
	return filepath.Join(home, ".codex", "archived_sessions", "rollout-2026-01-05T08-00-00-"+ridC+".jsonl")
}

func id(kind, key string) string {
	return model.EventID(kind, model.ProviderOpenAI, model.SourceCodex, key)
}

type wantUsage struct {
	tokens model.Tokens
	model  string
	ts     string
}

func checkUsage(t *testing.T, m sourcetest.Merged, want map[string]wantUsage, session string) {
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
		if u.Tokens != w.tokens || u.Model != w.model || !u.TS.Equal(at(w.ts)) || u.Session != model.SessionKey(model.ProviderOpenAI, session) {
			t.Errorf("usage %s = %+v %s %s, want %+v %s %s", key, u.Tokens, u.Model, u.TS, w.tokens, w.model, w.ts)
		}
	}
}

func TestRollout(t *testing.T) {
	home, env := setup(t, "2026-09-28T10:05:00Z")
	b, cur := sourcetest.Parse(t, New(), env, rolloutA(home), sources.Cursor{})
	m := sourcetest.Merge(b)
	checkUsage(t, m, map[string]wantUsage{
		// Unchanged totals are counted once; info=null is skipped.
		ridA + ":0:100": {model.Tokens{In: 30, CacheR: 50, Out: 20, Reasoning: 5, Calls: 1}, "gpt-5.5", "2026-09-27T10:00:05Z"},
		ridA + ":0:160": {model.Tokens{In: 5, CacheW: 5, CacheR: 40, Out: 10, Reasoning: 2, Calls: 1}, "gpt-5.5", "2026-09-27T10:00:07Z"},
		// The total dropped from 160 to 50: a reset starts epoch 1. These
		// lines were appended by a resumed session a day after the file's
		// directory date, and keep their own timestamps.
		ridA + ":1:50": {model.Tokens{In: 30, CacheR: 10, Out: 10, Calls: 1}, "gpt-5.5-codex", "2026-09-28T09:00:03Z"},
		// The assistant text mentioning token_usage_record must not switch modes.
		ridA + ":1:90": {model.Tokens{In: 30, Out: 10, Calls: 1}, "gpt-5.5-codex", "2026-09-28T09:00:05Z"},
		// After the first token_usage_record, token_count (total 400) is ignored.
		"resp_1": {model.Tokens{In: 40, CacheW: 10, CacheR: 150, Out: 30, Reasoning: 12, Calls: 1}, "gpt-5.5-codex", "2026-09-28T09:00:06Z"},
		"resp_2": {model.Tokens{In: 10, Out: 5, Calls: 1}, "gpt-5.5-codex", "2026-09-28T09:00:08Z"},
	}, ridA)

	prompts := map[string][2]string{
		ridA + ":5":  {"Fix the failing test", "gpt-5.5"},
		ridA + ":11": {"Now refactor it", "gpt-5.5-codex"}, // turn_context came after the message
	}
	if len(m.Prompts) != len(prompts) || len(m.Activity) != len(prompts) {
		t.Errorf("got %d prompts / %d activity, want %d (injected, AGENTS and inherited messages skipped)", len(m.Prompts), len(m.Activity), len(prompts))
	}
	for key, w := range prompts {
		p := m.Prompts[id(model.KindPrompt, key)]
		if p.Text != w[0] || p.Model != w[1] || p.Workspace != `C:\work\codex` || p.AcctLabel != "label:a_openai" {
			t.Errorf("prompt %s = %+v", key, p)
		}
		if a, ok := m.Activity[id(model.KindActivity, key)]; !ok || !a.HasUsage {
			t.Errorf("activity %s = %+v", key, a)
		}
	}
	if !bytes.Contains(cur.Carry, []byte(`"sawRecord":true`)) || !bytes.Contains(cur.Carry, []byte(ridA)) {
		t.Errorf("carry = %s", cur.Carry)
	}
}

func TestSubagentRollout(t *testing.T) {
	home, env := setup(t, "2026-09-28T10:05:00Z")
	b, _ := sourcetest.Parse(t, New(), env, rolloutB(home), sources.Cursor{})
	m := sourcetest.Merge(b)
	// The rollout id is the thread's own id (first session_meta), not the
	// parent's that follows it or the shared session_id.
	checkUsage(t, m, map[string]wantUsage{
		ridB + ":0:70": {model.Tokens{In: 40, CacheR: 20, Out: 10, Calls: 1}, "gpt-5.5-mini", "2026-09-28T09:30:03Z"},
	}, ridB)
	if len(m.Prompts) != 0 || len(m.Activity) != 0 {
		t.Errorf("subagent prompts must be skipped: %+v", m.Prompts)
	}
}

func TestLegacyRolloutOrdinalFallback(t *testing.T) {
	home, env := setup(t, "2026-09-28T10:05:00Z")
	b, _ := sourcetest.Parse(t, New(), env, rolloutC(home), sources.Cursor{})
	m := sourcetest.Merge(b)
	checkUsage(t, m, map[string]wantUsage{
		ridC + ":0:30": {model.Tokens{In: 15, CacheR: 5, Out: 10, Calls: 1}, "gpt-5", "2026-01-05T08:00:03Z"},
	}, ridC)
	p, ok := m.Prompts[id(model.KindPrompt, ridC+":2")]
	if !ok || p.Text != "Explain this function" || p.Model != "gpt-5" || p.Workspace != `C:\legacy` {
		t.Errorf("legacy prompt (line index key) = %+v, present %v", p, ok)
	}
}

func TestSplitReads(t *testing.T) {
	// The clock sits just after the fixture's last line, as for a live file,
	// so no prompt times out between the two reads.
	home, env := setup(t, "2026-09-28T09:01:00Z")
	for _, path := range []string{rolloutA(home), rolloutB(home), rolloutC(home)} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sourcetest.SplitCheck(t, New(), env, path, data)
		sourcetest.Growing(t, New(), env, path, data, 61)
	}
}

func TestPendingPromptTimeout(t *testing.T) {
	home, env := setup(t, "2026-09-28T09:05:00Z")
	path := rolloutA(home)
	data, _ := os.ReadFile(path)
	// Cut right after "Now refactor it", before its turn_context.
	cut := bytes.Index(data, []byte("Now refactor it"))
	cut += bytes.IndexByte(data[cut:], '\n') + 1
	os.WriteFile(path, data[:cut], 0o644)
	b, cur := sourcetest.Parse(t, New(), env, path, sources.Cursor{})
	for _, p := range b.Prompts {
		if p.Text == "Now refactor it" {
			t.Fatal("prompt emitted before its turn_context")
		}
	}
	old := now
	now = func() time.Time { return at("2026-09-28T09:20:00Z") }
	defer func() { now = old }()
	b, _ = sourcetest.Parse(t, New(), env, path, cur)
	if len(b.Prompts) != 1 || b.Prompts[0].Model != "gpt-5.5" {
		t.Fatalf("timed-out prompt = %+v (takes the current model)", b.Prompts)
	}
}

func TestHistory(t *testing.T) {
	home, env := setup(t, "2026-09-28T10:05:00Z")
	src := New()
	if err := src.Prepare(env); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".codex", "history.jsonl")
	b, cur := sourcetest.Parse(t, src, env, path, sources.Cursor{})
	key := "hist:" + ridD + ":1785574800"
	if len(b.Prompts) != 1 || b.Prompts[0].ID != id(model.KindPrompt, key) || b.Prompts[0].Text != "Old prompt without rollout" {
		t.Fatalf("history prompts = %+v", b.Prompts)
	}
	if len(b.Activity) != 1 || b.Activity[0].HasUsage {
		t.Fatalf("history activity = %+v", b.Activity)
	}
	data, _ := os.ReadFile(path)
	if want := bytes.LastIndexByte(data[:len(data)-1], '\n') + 1; cur.Offset != int64(want) {
		t.Fatalf("offset %d, want %d (recent entry deferred)", cur.Offset, want)
	}
	old := now
	now = func() time.Time { return at("2026-09-28T11:00:00Z") }
	defer func() { now = old }()
	b, _ = sourcetest.Parse(t, src, env, path, cur)
	if len(b.Prompts) != 1 || b.Prompts[0].Session != model.SessionKey(model.ProviderOpenAI, ridE) {
		t.Fatalf("deferred history entry = %+v", b.Prompts)
	}
}

func TestFiles(t *testing.T) {
	home, env := setup(t, "2026-09-28T10:05:00Z")
	files, err := New().Files(env)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 4 {
		t.Fatalf("Files() = %v, want 3 rollouts + history", files)
	}
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	env.Home = t.TempDir()
	if files, _ := New().Files(env); len(files) != 4 {
		t.Fatalf("CODEX_HOME not honoured: %v", files)
	}
}

func TestPromptBeforeFirstTurn(t *testing.T) {
	home, env := setup(t, "2026-09-28T10:05:00Z")
	const rid = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	path := filepath.Join(home, ".codex", "sessions", "2026", "09", "28", "rollout-2026-09-28T10-00-00-"+rid+".jsonl")
	data := `{"timestamp":"2026-09-28T10:00:00.000Z","ordinal":0,"type":"session_meta","payload":{"id":"` + rid + `","cwd":"C:\\work"}}
{"timestamp":"2026-09-28T10:00:01.000Z","ordinal":1,"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"First words"}]}}
{"timestamp":"2026-09-28T10:00:02.000Z","ordinal":2,"type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}
{"timestamp":"2026-09-28T10:00:03.000Z","ordinal":3,"type":"turn_context","payload":{"model":"gpt-5.5","turn_id":"t1"}}
`
	os.WriteFile(path, []byte(data), 0o644)
	b, _ := sourcetest.Parse(t, New(), env, path, sources.Cursor{})
	if len(b.Prompts) != 1 || b.Prompts[0].Model != "gpt-5.5" {
		t.Fatalf("prompt before the first turn = %+v", b.Prompts)
	}
	sourcetest.SplitCheck(t, New(), env, path, []byte(data))
}

func TestImagePrompts(t *testing.T) {
	home, env := setup(t, "2026-09-28T10:05:00Z")
	const rid = "abababab-abab-4bab-8bab-abababababab"
	path := filepath.Join(home, ".codex", "sessions", "2026", "09", "28", "rollout-2026-09-28T10-00-00-"+rid+".jsonl")
	img := `{"type":"input_text","text":"<image name=[Image #1]>"},{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgo="},{"type":"input_text","text":"</image>"}`
	data := `{"timestamp":"2026-09-28T10:00:00.000Z","ordinal":0,"type":"session_meta","payload":{"id":"` + rid + `","cwd":"/work"}}
{"timestamp":"2026-09-28T10:00:01.000Z","ordinal":1,"type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}
{"timestamp":"2026-09-28T10:00:01.500Z","ordinal":2,"type":"turn_context","payload":{"model":"gpt-5.5","turn_id":"t1"}}
{"timestamp":"2026-09-28T10:00:02.000Z","ordinal":3,"type":"response_item","payload":{"type":"message","role":"user","content":[` + img + `,{"type":"input_text","text":"Why is this layout broken?"}]}}
{"timestamp":"2026-09-28T10:00:03.000Z","ordinal":4,"type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"ok"}}
{"timestamp":"2026-09-28T10:00:04.000Z","ordinal":5,"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgo="}]}}
{"timestamp":"2026-09-28T10:00:05.000Z","ordinal":6,"type":"response_item","payload":{"type":"message","role":"user","content":[` + img + `]}}
{"timestamp":"2026-09-28T10:00:06.000Z","ordinal":7,"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>x</environment_context>"}]}}
`
	os.WriteFile(path, []byte(data), 0o644)
	b, _ := sourcetest.Parse(t, New(), env, path, sources.Cursor{})
	m := sourcetest.Merge(b)
	// Attached images are prompts (wrapper tags removed); a bare tool image
	// and injected context are not.
	want := map[string]string{rid + ":3": "Why is this layout broken?", rid + ":6": "[image]"}
	if len(m.Prompts) != len(want) || len(m.Activity) != len(want) {
		t.Fatalf("got %d prompts / %d activity, want %d", len(m.Prompts), len(m.Activity), len(want))
	}
	for key, text := range want {
		if p := m.Prompts[id(model.KindPrompt, key)]; p.Text != text || p.Model != "gpt-5.5" {
			t.Errorf("prompt %s = %+v", key, p)
		}
	}
}
