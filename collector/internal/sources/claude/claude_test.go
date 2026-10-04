package claude

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/jsonl/sourcetest"
)

const (
	s1 = "11111111-1111-4111-8111-111111111111"
	s2 = "22222222-2222-4222-8222-222222222222"
	s3 = "33333333-3333-4333-8333-333333333333"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t
}

func setNow(t *testing.T, ts string) {
	t.Helper()
	old := now
	now = func() time.Time { return at(ts) }
	t.Cleanup(func() { now = old })
}

func transcriptPath(home string) string {
	return filepath.Join(home, ".claude", "projects", "C--work-demo", s1+".jsonl")
}

func usageID(key string) string {
	return model.EventID(model.KindUsage, model.ProviderAnthropic, model.SourceClaudeCode, key)
}

func promptID(key string) string {
	return model.EventID(model.KindPrompt, model.ProviderAnthropic, model.SourceClaudeCode, key)
}

func activityID(key string) string {
	return model.EventID(model.KindActivity, model.ProviderAnthropic, model.SourceClaudeCode, key)
}

// assistantLine is a synthetic reply that names a model after the last prompt.
const assistantLine = `{"type":"assistant","uuid":"as-11","timestamp":"2026-09-28T10:00:21.000Z","sessionId":"` + s1 + `","cwd":"C:\\work\\demo","message":{"id":"msg_F","model":"claude-opus-5-5","role":"assistant","content":[],"usage":{"input_tokens":7,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":8}},"requestId":"req_F"}` + "\n"

func TestTranscriptUsageAndPrompts(t *testing.T) {
	setNow(t, "2026-09-28T10:05:00Z")
	home := sourcetest.Home(t)
	env := sourcetest.Env(home)
	src := New()
	b, cur := sourcetest.Parse(t, src, env, transcriptPath(home), sources.Cursor{})

	// Growing output_tokens across streamed lines collapse into one event per key.
	ids := map[string]int{}
	for _, u := range b.Usage {
		ids[u.ID]++
	}
	for id, n := range ids {
		if n != 1 {
			t.Errorf("usage %s emitted %d times in one read", id, n)
		}
	}
	m := sourcetest.Merge(b)
	want := map[string]model.Tokens{
		"msg_A":       {In: 10, CacheW: 100, CacheW1h: 60, CacheR: 1000, Out: 42, Reasoning: 3, Calls: 1},
		"msg_B":       {In: 3, CacheR: 1100, Out: 9, Calls: 1},
		"msg_C":       {In: 5, Out: 6, Calls: 1},
		"req_D":       {In: 1, Out: 2, Calls: 1},
		s1 + ":as-09": {In: 1, Out: 1, Calls: 1},
		"msg_E":       {In: 2, Out: 3, Calls: 1},
	}
	if len(m.Usage) != len(want) {
		t.Errorf("got %d usage events, want %d (synthetic must be skipped)", len(m.Usage), len(want))
	}
	for key, tok := range want {
		u, ok := m.Usage[usageID(key)]
		if !ok {
			t.Errorf("usage %s missing", key)
			continue
		}
		if u.Tokens != tok {
			t.Errorf("usage %s = %+v, want %+v (iterations[] must be ignored)", key, u.Tokens, tok)
		}
	}
	a := m.Usage[usageID("msg_A")]
	if !a.TS.Equal(at("2026-09-28T10:00:02Z")) || a.Model != "claude-opus-5-5" || a.Q != model.QualityExact ||
		a.Session != model.SessionKey(model.ProviderAnthropic, s1) || a.Acct != "a_anthropic" || a.TZOffsetMin != 60 || a.PV != pv {
		t.Errorf("msg_A event = %+v", a)
	}

	// Prompts: only typed text; the last prompt waits in the carry for a model.
	wantPrompts := map[string][2]string{
		"u-01": {"Write a haiku about tests", "claude-opus-5-5"},
		"u-08": {"<b>bold</b> what does this tag do?", "claude-sonnet-5"},
		"u-09": {"What is in this picture?", "claude-opus-5-5"},
	}
	if len(m.Prompts) != len(wantPrompts) {
		t.Errorf("got %d prompts, want %d", len(m.Prompts), len(wantPrompts))
	}
	for key, w := range wantPrompts {
		p, ok := m.Prompts[promptID(key)]
		if !ok {
			t.Errorf("prompt %s missing", key)
			continue
		}
		if p.Text != w[0] || p.Model != w[1] || p.Workspace != `C:\work\demo` || p.Machine != "test-machine" || p.AcctLabel != "label:a_anthropic" {
			t.Errorf("prompt %s = %+v", key, p)
		}
	}
	for _, key := range []string{"u-01", "u-08", "u-09", "u-10"} {
		if a, ok := m.Activity[activityID(key)]; !ok || !a.HasUsage {
			t.Errorf("activity %s = %+v, present %v", key, a, ok)
		}
	}
	if len(m.Activity) != 4 {
		t.Errorf("got %d activity events, want 4", len(m.Activity))
	}
	if !bytes.Contains(cur.Carry, []byte("u-10")) && !bytes.Contains(cur.Carry, []byte(promptID("u-10"))) {
		t.Errorf("carry does not hold the pending prompt: %s", cur.Carry)
	}
	if !b.LastEventTS.Equal(at("2026-09-28T10:00:20Z")) {
		t.Errorf("LastEventTS = %s", b.LastEventTS)
	}
}

func TestPendingPromptCarry(t *testing.T) {
	setNow(t, "2026-09-28T10:05:00Z")
	home := sourcetest.Home(t)
	env := sourcetest.Env(home)
	src := New()
	path := transcriptPath(home)
	_, cur := sourcetest.Parse(t, src, env, path, sources.Cursor{})

	// Nothing new yet: still pending.
	b, cur := sourcetest.Parse(t, src, env, path, cur)
	if len(b.Prompts) != 0 {
		t.Fatalf("prompt emitted before its model is known: %+v", b.Prompts)
	}
	// The next assistant line names the model.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(assistantLine)
	f.Close()
	b, cur = sourcetest.Parse(t, src, env, path, cur)
	if len(b.Prompts) != 1 || b.Prompts[0].ID != promptID("u-10") || b.Prompts[0].Model != "claude-opus-5-5" ||
		b.Prompts[0].Text != "One more thing: café ünïcödé" {
		t.Fatalf("prompts after reply = %+v", b.Prompts)
	}
	if len(b.Activity) != 0 {
		t.Errorf("activity re-emitted: %+v", b.Activity)
	}
	if bytes.Contains(cur.Carry, []byte("pending")) {
		t.Errorf("carry still has pending prompts: %s", cur.Carry)
	}
}

func TestPendingPromptTimeout(t *testing.T) {
	setNow(t, "2026-09-28T10:05:00Z")
	home := sourcetest.Home(t)
	env := sourcetest.Env(home)
	src := New()
	path := transcriptPath(home)
	_, cur := sourcetest.Parse(t, src, env, path, sources.Cursor{})
	setNow(t, "2026-09-28T10:10:21Z")
	b, _ := sourcetest.Parse(t, src, env, path, cur)
	if len(b.Prompts) != 1 || b.Prompts[0].ID != promptID("u-10") || b.Prompts[0].Model != "" {
		t.Fatalf("timed-out prompt = %+v", b.Prompts)
	}
}

func TestSplitReads(t *testing.T) {
	setNow(t, "2026-09-28T10:05:00Z")
	home := sourcetest.Home(t)
	env := sourcetest.Env(home)
	path := transcriptPath(home)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, assistantLine...)
	sourcetest.SplitCheck(t, New(), env, path, data)
	sourcetest.Growing(t, New(), env, path, data, 97)
}

// A settled file (its last usage line long past) carries nothing, and split
// reads of it still merge to the full read: re-emitted keys merge by max.
// Prompts are off because a prompt cut off from its reply for over ten
// minutes is sent without a model by design (TestPendingPromptTimeout).
func TestSplitReadsSettled(t *testing.T) {
	setNow(t, "2026-09-29T09:00:00Z")
	home := sourcetest.Home(t)
	env := sourcetest.Env(home)
	env.Prompts = false
	path := transcriptPath(home)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, assistantLine...)
	_, cur := sourcetest.Parse(t, New(), env, path, sources.Cursor{})
	if len(cur.Carry) != 0 {
		t.Fatalf("settled file keeps carry: %s", cur.Carry)
	}
	sourcetest.SplitCheck(t, New(), env, path, data)
	sourcetest.Growing(t, New(), env, path, data, 97)
}

// In-flight usage keys stay in the carry while the file can still grow, and
// the first call after recentTTL clears them even though nothing changed.
func TestRecentCarryExpires(t *testing.T) {
	setNow(t, "2026-09-28T10:05:00Z")
	home := sourcetest.Home(t)
	env := sourcetest.Env(home)
	src := New()
	path := transcriptPath(home)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(assistantLine)
	f.Close()
	_, cur := sourcetest.Parse(t, src, env, path, sources.Cursor{})
	if !bytes.Contains(cur.Carry, []byte(`"recent"`)) || !bytes.Contains(cur.Carry, []byte("msg_F")) || bytes.Contains(cur.Carry, []byte("pending")) {
		t.Fatalf("live carry = %s", cur.Carry)
	}
	// The last usage line is at 10:00:21: still live at 10:30:20.
	setNow(t, "2026-09-28T10:30:20Z")
	b, cur := sourcetest.Parse(t, src, env, path, cur)
	if len(b.Usage)+len(b.Activity)+len(b.Prompts) != 0 || !bytes.Contains(cur.Carry, []byte("msg_F")) {
		t.Fatalf("carry dropped early: %s (events %+v)", cur.Carry, b)
	}
	setNow(t, "2026-09-28T10:30:21Z")
	b, cur = sourcetest.Parse(t, src, env, path, cur)
	if len(cur.Carry) != 0 || len(b.Usage) != 0 {
		t.Fatalf("settled carry = %s, usage %+v", cur.Carry, b.Usage)
	}
	// A late line for a carried key (after the carry expired) re-emits only
	// what it holds; the server's max merge keeps the full value.
	late := strings.Replace(assistantLine, `"output_tokens":8`, `"output_tokens":9`, 1)
	late = strings.Replace(late, `"as-11"`, `"as-12"`, 1)
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(late)
	f.Close()
	b, _ = sourcetest.Parse(t, src, env, path, cur)
	if len(b.Usage) != 1 || b.Usage[0].ID != usageID("msg_F") || b.Usage[0].Out != 9 || b.Usage[0].In != 7 {
		t.Fatalf("late line usage = %+v", b.Usage)
	}
}

func TestPartialTrailingLine(t *testing.T) {
	setNow(t, "2026-09-28T10:05:00Z")
	home := sourcetest.Home(t)
	env := sourcetest.Env(home)
	src := New()
	path := transcriptPath(home)
	data, _ := os.ReadFile(path)
	partial := `{"type":"user","uuid":"u-11","timestamp":"2026-09-28T10:00:30.000Z","sessionId":"` + s1 + `","message":{"role":"user","content":"half written"}}`
	os.WriteFile(path, append(append([]byte{}, data...), partial...), 0o644)
	b, cur := sourcetest.Parse(t, src, env, path, sources.Cursor{})
	if cur.Offset != int64(len(data)) {
		t.Fatalf("offset %d, want %d (partial line must not be consumed)", cur.Offset, len(data))
	}
	for _, a := range b.Activity {
		if a.ID == activityID("u-11") {
			t.Fatal("partial line was parsed")
		}
	}
	os.WriteFile(path, append(append(append([]byte{}, data...), partial...), '\n'), 0o644)
	b, cur = sourcetest.Parse(t, src, env, path, cur)
	if len(b.Activity) != 1 || b.Activity[0].ID != activityID("u-11") {
		t.Fatalf("completed line activity = %+v", b.Activity)
	}
	if cur.Offset != int64(len(data)+len(partial)+1) {
		t.Fatalf("offset %d after completing the line", cur.Offset)
	}
}

func TestSidechainTranscript(t *testing.T) {
	setNow(t, "2026-09-28T10:05:00Z")
	home := sourcetest.Home(t)
	env := sourcetest.Env(home)
	path := filepath.Join(home, ".claude", "projects", "C--work-demo", s1, "subagents", "workflows", "wf_1", "agent-a1.jsonl")
	b, _ := sourcetest.Parse(t, New(), env, path, sources.Cursor{})
	if len(b.Usage) != 1 || b.Usage[0].ID != usageID("msg_S") || b.Usage[0].Model != "claude-haiku-5" {
		t.Errorf("sidechain usage = %+v", b.Usage)
	}
	if len(b.Prompts) != 0 || len(b.Activity) != 0 {
		t.Errorf("sidechain prompts must be skipped: %+v %+v", b.Prompts, b.Activity)
	}
}

func TestFiles(t *testing.T) {
	home := sourcetest.Home(t)
	files, err := New().Files(sourcetest.Env(home))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		names = append(names, filepath.Base(f))
	}
	got := strings.Join(names, ",")
	for _, w := range []string{s1 + ".jsonl", "agent-a1.jsonl", "history.jsonl"} {
		if !strings.Contains(got, w) {
			t.Errorf("Files() = %s, missing %s", got, w)
		}
	}
}

func TestHistory(t *testing.T) {
	setNow(t, "2026-09-28T10:05:00Z")
	home := sourcetest.Home(t)
	env := sourcetest.Env(home)
	src := New()
	if err := src.Prepare(env); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "history.jsonl")
	b, cur := sourcetest.Parse(t, src, env, path, sources.Cursor{})
	m := sourcetest.Merge(b)
	key := func(sid, ts string) string {
		return "hist:" + sid + ":" + strconv.FormatInt(at(ts).UnixMilli(), 10)
	}
	want := map[string]string{
		key(s2, "2026-08-01T09:00:00Z"): "Summarise this line one\nline two\nline three please",
		key(s2, "2026-08-01T09:02:00Z"): "see cached paste",
	}
	if len(m.Prompts) != len(want) || len(m.Activity) != len(want) {
		t.Errorf("got %d prompts / %d activity, want %d", len(m.Prompts), len(m.Activity), len(want))
	}
	for k, text := range want {
		p := m.Prompts[promptID(k)]
		if p.Text != text || p.Model != "" || p.Workspace != `C:\old` || p.Session != model.SessionKey(model.ProviderAnthropic, s2) {
			t.Errorf("history prompt %s = %+v", k, p)
		}
		if a, ok := m.Activity[activityID(k)]; !ok || a.HasUsage {
			t.Errorf("history activity %s = %+v (hasUsage must be false)", k, a)
		}
	}
	// The session with a transcript is suppressed.
	if _, ok := m.Prompts[promptID(key(s1, "2026-09-28T10:00:00Z"))]; ok {
		t.Error("history entry of a session with a transcript was emitted")
	}
	// The two-minute-old entry is left unconsumed for a later scan.
	data, _ := os.ReadFile(path)
	lastLine := bytes.LastIndexByte(data[:len(data)-1], '\n') + 1
	if cur.Offset != int64(lastLine) {
		t.Fatalf("offset %d, want %d (recent entry deferred)", cur.Offset, lastLine)
	}
	setNow(t, "2026-09-28T10:20:00Z")
	b, cur = sourcetest.Parse(t, src, env, path, cur)
	if len(b.Prompts) != 1 || b.Prompts[0].ID != promptID(key(s3, "2026-09-28T10:03:00Z")) || cur.Offset != int64(len(data)) {
		t.Fatalf("deferred entry = %+v, offset %d", b.Prompts, cur.Offset)
	}
}

func TestPromptTruncation(t *testing.T) {
	setNow(t, "2026-09-28T12:00:00Z")
	home := sourcetest.Home(t)
	env := sourcetest.Env(home)
	path := filepath.Join(home, ".claude", "projects", "C--work-demo", "44444444-4444-4444-8444-444444444444.jsonl")
	long := "x" + strings.Repeat("é", model.MaxPromptBytes)
	line := `{"type":"user","uuid":"u-long","timestamp":"2026-09-28T10:00:00.000Z","sessionId":"s4","message":{"role":"user","content":"` + long + `"}}` + "\n"
	os.WriteFile(path, []byte(line), 0o644)
	b, _ := sourcetest.Parse(t, New(), env, path, sources.Cursor{})
	if len(b.Prompts) != 1 {
		t.Fatalf("prompts = %d", len(b.Prompts))
	}
	text := b.Prompts[0].Text
	if len(text) > model.MaxPromptBytes || !utf8.ValidString(text) || !strings.Contains(text, "[truncated by the collector") {
		t.Errorf("truncated prompt: len %d, valid %v", len(text), utf8.ValidString(text))
	}
}

func TestQueuedCommandPrompts(t *testing.T) {
	setNow(t, "2026-09-28T12:00:00Z")
	home := sourcetest.Home(t)
	env := sourcetest.Env(home)
	path := filepath.Join(home, ".claude", "projects", "C--work-demo", "55555555-5555-4555-8555-555555555555.jsonl")
	head := `{"isSidechain":false,"cwd":"C:\\work\\demo","sessionId":"s5","timestamp":"2026-09-28T10:00:0`
	lines := []string{
		// Typed while a tool ran: a prompt, with the model of the reply that follows.
		head + `1.000Z","uuid":"q-01","type":"attachment","attachment":{"type":"queued_command","prompt":"also check the tests","commandMode":"prompt","origin":{"kind":"human"}}}`,
		// Text blocks with an image.
		head + `2.000Z","uuid":"q-02","type":"attachment","attachment":{"type":"queued_command","prompt":[{"type":"text","text":"and this screenshot"},{"type":"image"}],"commandMode":"prompt","origin":{"kind":"human"}}}`,
		// Not prompts: a task notification, a coordinator message, a sidechain copy.
		head + `3.000Z","uuid":"q-03","type":"attachment","attachment":{"type":"queued_command","prompt":"<task-notification>done</task-notification>","commandMode":"task-notification"}}`,
		head + `4.000Z","uuid":"q-04","type":"attachment","attachment":{"type":"queued_command","prompt":"coordinator says hi","isMeta":true,"origin":{"kind":"coordinator"}}}`,
		`{"isSidechain":true,"sessionId":"s5","timestamp":"2026-09-28T10:00:05.000Z","uuid":"q-05","type":"attachment","attachment":{"type":"queued_command","prompt":"sidechain copy","commandMode":"prompt","origin":{"kind":"human"}}}`,
		head + `6.000Z","uuid":"as-q","type":"assistant","message":{"id":"msg_Q","model":"claude-opus-5-5","content":[],"usage":{"input_tokens":1,"output_tokens":2}}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, _ := sourcetest.Parse(t, New(), env, path, sources.Cursor{})
	m := sourcetest.Merge(b)
	want := map[string]string{"q-01": "also check the tests", "q-02": "and this screenshot"}
	if len(m.Prompts) != len(want) || len(m.Activity) != len(want) {
		t.Fatalf("got %d prompts / %d activity, want %d", len(m.Prompts), len(m.Activity), len(want))
	}
	for key, text := range want {
		p := m.Prompts[promptID(key)]
		if p.Text != text || p.Model != "claude-opus-5-5" || p.Workspace != `C:\work\demo` {
			t.Errorf("queued prompt %s = %+v", key, p)
		}
		if a, ok := m.Activity[activityID(key)]; !ok || !a.HasUsage {
			t.Errorf("queued activity %s = %+v, present %v", key, a, ok)
		}
	}
}
