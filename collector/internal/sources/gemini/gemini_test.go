package gemini

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/jsonl/sourcetest"
)

const (
	sessA = "a398ecf4-0000-4000-8000-000000000001"
	sessB = "f44504ef-0000-4000-8000-000000000002"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t
}

func usageID(key string) string { return model.EventID(model.KindUsage, ProviderName, SourceName, key) }
func activityID(key string) string {
	return model.EventID(model.KindActivity, ProviderName, SourceName, key)
}
func promptID(key string) string {
	return model.EventID(model.KindPrompt, ProviderName, SourceName, key)
}

func sessionA(home string) string {
	return filepath.Join(home, ".gemini", "tmp", "aaaa1111", "chats", "session-2026-01-08T05-16-a398ecf4.json")
}

func TestFiles(t *testing.T) {
	home := sourcetest.Home(t)
	files, err := New().Files(sourcetest.Env(home))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	var names []string
	for _, f := range files {
		names = append(names, filepath.Base(f))
	}
	want := []string{"session-2026-01-08T05-16-a398ecf4.json", "session-2026-01-11T08-36-f44504ef.json", "session-2026-01-12T00-00-broken.json"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("files = %v, want %v", names, want)
	}
	if files, _ := New().Files(sourcetest.Env(t.TempDir())); len(files) != 0 {
		t.Fatalf("empty home lists %v", files)
	}
}

func TestSessionUsageAndPrompts(t *testing.T) {
	home := sourcetest.Home(t)
	env := sourcetest.Env(home)
	src := New()
	b, cur := sourcetest.Parse(t, src, env, sessionA(home), sources.Cursor{})
	st, _ := os.Stat(sessionA(home))
	if cur.Offset != st.Size() || len(cur.Carry) != 0 {
		t.Fatalf("whole-file cursor: offset %d (size %d) carry %q", cur.Offset, st.Size(), cur.Carry)
	}
	m := sourcetest.Merge(b)

	// SPEC mapping: in = max(0, input - cached) + tool, cacheR = cached,
	// out = output + thoughts, reasoning = thoughts, calls = 1.
	wantUsage := map[string]struct {
		tok   model.Tokens
		model string
	}{
		sessA + ":g-1": {model.Tokens{In: 405, CacheR: 600, Out: 50, Reasoning: 10, Calls: 1}, "gemini-2.5-pro"},
		sessA + ":g-2": {model.Tokens{In: 200, Out: 20, Calls: 1}, "gemini-2.5-flash"},
		// cached > input clamps in to 0 + tool.
		sessA + ":g-4": {model.Tokens{In: 0, CacheR: 350, Out: 10, Reasoning: 3, Calls: 1}, "gemini-2.5-pro"},
	}
	if len(m.Usage) != len(wantUsage) {
		t.Fatalf("%d usage events, want %d (an all-zero tokens message must not count)", len(m.Usage), len(wantUsage))
	}
	for key, w := range wantUsage {
		u, ok := m.Usage[usageID(key)]
		if !ok {
			t.Errorf("usage %s missing", key)
			continue
		}
		if u.Tokens != w.tok || u.Model != w.model || u.Q != model.QualityExact || u.PV != pv || u.Session != model.SessionKey(ProviderName, sessA) || u.Provider != ProviderName || u.Source != SourceName {
			t.Errorf("usage %s = %+v", key, u)
		}
	}
	if u := m.Usage[usageID(sessA+":g-1")]; !u.TS.Equal(at("2026-01-08T05:16:35Z")) || u.TZOffsetMin != 60 || u.Acct != "a_google" {
		t.Errorf("usage g-1 ts/tz/acct: %+v", u)
	}

	// Prompts: non-blank user messages, text from a string or text parts,
	// model from the next gemini message, workspace = projectHash.
	wantPrompts := map[string]struct{ text, model string }{
		sessA + ":u-1": {"first synthetic prompt", "gemini-2.5-pro"},
		sessA + ":u-2": {"second\nprompt", "gemini-2.5-pro"},
		sessA + ":u-4": {"last prompt, no reply yet", ""},
	}
	if len(m.Prompts) != len(wantPrompts) || len(m.Activity) != len(wantPrompts) {
		t.Fatalf("%d prompts / %d activity, want %d", len(m.Prompts), len(m.Activity), len(wantPrompts))
	}
	for key, w := range wantPrompts {
		p, ok := m.Prompts[promptID(key)]
		if !ok || p.Text != w.text || p.Model != w.model || p.Workspace != "aaaa1111" || p.Machine != "test-machine" || p.AcctLabel != "label:a_google" {
			t.Errorf("prompt %s = %+v (present %v)", key, p, ok)
		}
		a, ok := m.Activity[activityID(key)]
		if !ok || !a.HasUsage || a.Session != model.SessionKey(ProviderName, sessA) {
			t.Errorf("activity %s = %+v (present %v)", key, a, ok)
		}
	}
	if !b.LastEventTS.Equal(at("2026-01-08T05:19:00Z")) {
		t.Errorf("last event ts %s", b.LastEventTS)
	}

	// Re-reading the rewritten file (whole-file source) yields the same ids
	// and values: the merge is a no-op.
	b2, _ := sourcetest.Parse(t, src, env, sessionA(home), cur)
	sourcetest.Equal(t, "reparse", sourcetest.Merge(b, b2), m)

	// Prompts off: usage and activity still flow.
	env.Prompts = false
	b3, _ := sourcetest.Parse(t, src, env, sessionA(home), sources.Cursor{})
	if len(b3.Prompts) != 0 || len(b3.Usage) != 3 || len(b3.Activity) != 3 {
		t.Fatalf("prompts off: %d/%d/%d", len(b3.Usage), len(b3.Activity), len(b3.Prompts))
	}
}

func TestSessionWithoutReplyAndBrokenFile(t *testing.T) {
	home := sourcetest.Home(t)
	env := sourcetest.Env(home)
	src := New()
	chats := filepath.Join(home, ".gemini", "tmp", "bbbb2222", "chats")
	b, _ := sourcetest.Parse(t, src, env, filepath.Join(chats, "session-2026-01-11T08-36-f44504ef.json"), sources.Cursor{})
	m := sourcetest.Merge(b)
	if len(m.Usage) != 0 || len(m.Activity) != 1 || len(m.Prompts) != 1 {
		t.Fatalf("no-reply session: %d/%d/%d", len(m.Usage), len(m.Activity), len(m.Prompts))
	}
	// A prompt whose session never produced tokens is activity without usage.
	if a := m.Activity[activityID(sessB+":u-9")]; a.HasUsage {
		t.Fatalf("hasUsage set without any gemini tokens: %+v", a)
	}
	if p := m.Prompts[promptID(sessB+":u-9")]; p.Workspace != "bbbb2222" || p.Model != "" {
		t.Fatalf("prompt: %+v", p)
	}

	// A half-written document parses to nothing and is offered again once
	// the file changes (its cursor covers what was read).
	broken := filepath.Join(chats, "session-2026-01-12T00-00-broken.json")
	b, cur := sourcetest.Parse(t, src, env, broken, sources.Cursor{})
	st, _ := os.Stat(broken)
	if len(b.Usage)+len(b.Activity)+len(b.Prompts) != 0 || cur.Offset != st.Size() {
		t.Fatalf("broken file: %d events, offset %d", len(b.Usage)+len(b.Activity)+len(b.Prompts), cur.Offset)
	}
}

func TestSessionIDFallbackAndWorkspaceMap(t *testing.T) {
	home := t.TempDir()
	chats := filepath.Join(home, ".gemini", "tmp", "cafe0001", "chats")
	os.MkdirAll(chats, 0o755)
	os.WriteFile(filepath.Join(home, ".gemini", "projects.json"), []byte(`{"projects":{"C:\\work\\demo":"cafe0001","C:\\other":"zzz"}}`), 0o644)
	doc := `{"messages":[{"id":"u-1","timestamp":"2026-02-01T10:00:00Z","type":"user","content":"hello"},` +
		`{"id":"g-1","timestamp":"2026-02-01T10:00:05Z","type":"gemini","content":"hi","tokens":{"input":10,"output":2,"cached":0,"thoughts":0,"tool":0,"total":12},"model":"gemini-2.5-pro"}]}`
	p := filepath.Join(chats, "session-2026-02-01T10-00-deadbeef.json")
	os.WriteFile(p, []byte(doc), 0o644)
	b, _ := sourcetest.Parse(t, New(), sourcetest.Env(home), p, sources.Cursor{})
	m := sourcetest.Merge(b)
	if _, ok := m.Usage[usageID("deadbeef:g-1")]; !ok {
		t.Fatalf("session id not taken from the file name: %v", m.Usage)
	}
	if pr := m.Prompts[promptID("deadbeef:u-1")]; pr.Workspace != `C:\work\demo` || pr.Model != "gemini-2.5-pro" {
		t.Fatalf("prompt: %+v", pr)
	}
}

func TestAccount(t *testing.T) {
	home := sourcetest.Home(t)
	id, label, ok := Account(home)
	if !ok || id != "tester@example.com" || label != id {
		t.Fatalf("account = %q %q %v", id, label, ok)
	}
	if _, _, ok := Account(t.TempDir()); ok {
		t.Fatal("account found in an empty home")
	}
	os.WriteFile(filepath.Join(home, ".gemini", "google_accounts.json"), []byte(`{"active":"","old":["x"]}`), 0o600)
	if _, _, ok := Account(home); ok {
		t.Fatal("empty active accepted")
	}
}
