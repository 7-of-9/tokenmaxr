package cursor

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/jsonl/sourcetest"
)

const (
	compA = "aaaaaaaa-0000-4000-8000-00000000000a"
	compB = "bbbbbbbb-0000-4000-8000-00000000000b"
	compC = "cccccccc-0000-4000-8000-00000000000c"
	compD = "dddddddd-0000-4000-8000-00000000000d"
	compE = "eeeeeeee-0000-4000-8000-00000000000e"
	compF = "ffffffff-0000-4000-8000-00000000000f"

	createdA = int64(1767225600000) // 2026-01-01T00:00:00Z, epoch ms
	createdF = "2026-01-10T00:00:00.000Z"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t
}

func js(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func obj(kv ...any) map[string]any {
	m := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func hdr(pairs ...any) []map[string]any {
	var out []map[string]any
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, obj("bubbleId", pairs[i], "type", pairs[i+1]))
	}
	return out
}

func tokens(in, out int64) map[string]any { return obj("inputTokens", in, "outputTokens", out) }

type row struct {
	key string
	val any // string JSON, nil for a NULL value
}

// fixture builds a synthetic state.vscdb (plus a workspaceStorage entry
// mapping composer B to a folder) and points DBEnv at it.
func fixture(t *testing.T) (string, *sources.Env) {
	t.Helper()
	home := t.TempDir()
	userDir := filepath.Join(home, "cursor-user")
	global := filepath.Join(userDir, "globalStorage")
	os.MkdirAll(global, 0o755)
	db := filepath.Join(global, dbFile)
	t.Setenv(DBEnv, db)

	jwt := "eyJhbGciOiJIUzI1NiJ9." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"auth0|user_synthetic","exp":1}`)) + ".sig"
	items := []row{
		{"cursorAuth/accessToken", jwt},
		{"cursorAuth/cachedEmail", "synthetic@example.com"},
	}
	rows := []row{
		// A: epoch-ms createdAt, composer model, full conversation order.
		{composerPrefix + compA, js(obj(
			"composerId", compA, "createdAt", createdA, "unifiedMode", "agent",
			"modelConfig", obj("modelName", "gpt-x", "maxMode", false),
			"fullConversationHeadersOnly", hdr("u1", 1, "a1", 2, "u2", 1, "a2", 2, "a3", 2, "u3", 1, "u4", 1, "a4", 2, "a5", 2),
		))},
		// ISO createdAt, own model, usageUuid.
		{bubblePrefix + compA + ":a1", js(obj("type", 2, "createdAt", "2026-01-01T10:00:00.000Z", "tokenCount", tokens(100, 20), "usageUuid", "uu-1", "modelInfo", obj("modelName", "claude-y"), "text", "", "images", []any{}))},
		// No createdAt (composer's), no usageUuid (composerId:bubbleId), no model (composer's).
		{bubblePrefix + compA + ":a2", js(obj("type", 2, "tokenCount", tokens(50, 5), "usageUuid", nil, "text", "reply", "images", []any{}))},
		// Duplicate usageUuid with a larger out: merged by fieldwise max.
		{bubblePrefix + compA + ":a3", js(obj("type", 2, "createdAt", "2026-01-01T10:05:00.000Z", "tokenCount", tokens(100, 30), "usageUuid", "uu-1", "text", "", "images", []any{}))},
		// Zero tokenCount and missing tokenCount: no usage.
		{bubblePrefix + compA + ":a4", js(obj("type", 2, "createdAt", "2026-01-01T10:06:00.000Z", "tokenCount", tokens(0, 0), "usageUuid", "uu-zero", "images", []any{}))},
		{bubblePrefix + compA + ":a5", js(obj("type", 2, "createdAt", "2026-01-01T10:07:00.000Z", "images", []any{}))},
		{bubblePrefix + compA + ":u1", js(obj("type", 1, "createdAt", "2026-01-01T09:59:00.000Z", "text", "  hello  ", "tokenCount", tokens(0, 0), "workspaceProjectDir", `C:\work\a`, "images", []any{}))},
		{bubblePrefix + compA + ":u2", js(obj("type", 1, "text", "again", "images", []any{}))},
		// Image-only prompt with no assistant after it.
		{bubblePrefix + compA + ":u3", js(obj("type", 1, "createdAt", "2026-01-01T10:08:00.000Z", "text", "", "images", []any{obj("uuid", "img")}))},
		// Empty prompt without images: skipped.
		{bubblePrefix + compA + ":u4", js(obj("type", 1, "createdAt", "2026-01-01T10:09:00.000Z", "text", " ", "images", []any{}))},

		// B: no createdAt, no model, no headers; folder comes from workspaceStorage.
		{composerPrefix + compB, js(obj("composerId", compB))},
		{bubblePrefix + compB + ":b1", js(obj("type", 2, "createdAt", 1767312000000, "tokenCount", tokens(7, 3), "images", []any{}))},
		// No timestamp anywhere: dropped.
		{bubblePrefix + compB + ":b2", js(obj("type", 1, "text", "lost", "images", []any{}))},
		{bubblePrefix + compB + ":b3", js(obj("type", 1, "createdAt", "2026-01-02T01:00:00.000Z", "text", "in b", "images", []any{}))},

		// C: bubble whose composer row does not exist.
		{bubblePrefix + compC + ":c1", js(obj("type", 2, "createdAt", "2026-01-03T00:00:00.000Z", "tokenCount", tokens(1, 1), "usageUuid", "uu-c", "modelInfo", obj("modelName", "gemini-z"), "images", []any{}))},

		// D: NULL values are skipped.
		{composerPrefix + compD, nil},
		{bubblePrefix + compD + ":d1", nil},

		// E: prompt-only session (no tokens anywhere): activity hasUsage=false.
		{composerPrefix + compE, js(obj("composerId", compE, "createdAt", 1767484800000, "modelConfig", obj("modelName", "grok-w"), "fullConversationHeadersOnly", hdr("e1", 1)))},
		{bubblePrefix + compE + ":e1", js(obj("type", 1, "createdAt", "2026-01-04T00:00:00.000Z", "text", "just asking", "images", []any{}))},

		// F: ISO composer createdAt, epoch-seconds bubble createdAt.
		{composerPrefix + compF, js(obj("composerId", compF, "createdAt", createdF, "fullConversationHeadersOnly", hdr("f1", 1, "f2", 2)))},
		{bubblePrefix + compF + ":f1", js(obj("type", 1, "text", "f prompt", "images", []any{}))},
		{bubblePrefix + compF + ":f2", js(obj("type", 2, "createdAt", 1768003200, "tokenCount", tokens(9, 9), "modelInfo", obj("modelName", "m-f"), "images", []any{}))},
		// Other key kinds are ignored.
		{"checkpointId:" + compA + ":x", js(obj("type", 2, "tokenCount", tokens(999, 999)))},
	}
	writeDB(t, db, rows, items)

	ws := filepath.Join(userDir, "workspaceStorage", "deadbeef")
	os.MkdirAll(ws, 0o755)
	os.WriteFile(filepath.Join(ws, "workspace.json"), []byte(`{"folder":"file:///c%3A/work/b%20space"}`), 0o644)
	writeDB(t, filepath.Join(ws, dbFile), nil, []row{{"composer.composerData", js(obj("allComposers", []any{obj("composerId", compB)}))}})
	return db, sourcetest.Env(home)
}

func writeDB(t *testing.T, path string, kv, items []row) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range []string{
		"PRAGMA journal_mode=WAL",
		"CREATE TABLE IF NOT EXISTS ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)",
		"CREATE TABLE IF NOT EXISTS cursorDiskKV (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)",
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	for table, rows := range map[string][]row{"ItemTable": items, "cursorDiskKV": kv} {
		for _, r := range rows {
			if _, err := db.Exec("INSERT INTO "+table+" (key, value) VALUES (?, ?)", r.key, r.val); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func appendRow(t *testing.T, path string, r row) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("INSERT INTO cursorDiskKV (key, value) VALUES (?, ?)", r.key, r.val); err != nil {
		t.Fatal(err)
	}
}

func id(kind, key string) string { return model.EventID(kind, ProviderName, SourceName, key) }

type wantUsage struct {
	tokens model.Tokens
	model  string
	ts     string
	sess   string
}

var wantUsageAll = map[string]wantUsage{
	"uu-1":        {model.Tokens{In: 100, Out: 30, Calls: 1}, "claude-y", "2026-01-01T10:00:00Z", compA},
	compA + ":a2": {model.Tokens{In: 50, Out: 5, Calls: 1}, "gpt-x", "2026-01-01T00:00:00Z", compA},
	compB + ":b1": {model.Tokens{In: 7, Out: 3, Calls: 1}, "", "2026-01-02T00:00:00Z", compB},
	"uu-c":        {model.Tokens{In: 1, Out: 1, Calls: 1}, "gemini-z", "2026-01-03T00:00:00Z", compC},
	compF + ":f2": {model.Tokens{In: 9, Out: 9, Calls: 1}, "m-f", "2026-01-10T00:00:00Z", compF},
}

type wantPrompt struct {
	text, model, ts, workspace string
	hasUsage                   bool
}

var wantPromptAll = map[string]wantPrompt{
	compA + ":u1": {"hello", "claude-y", "2026-01-01T09:59:00Z", `C:\work\a`, true},
	compA + ":u2": {"again", "gpt-x", "2026-01-01T00:00:00Z", "", true},
	compA + ":u3": {"[image]", "gpt-x", "2026-01-01T10:08:00Z", "", true},
	compB + ":b3": {"in b", "", "2026-01-02T01:00:00Z", filepath.FromSlash("c:/work/b space"), true},
	compE + ":e1": {"just asking", "grok-w", "2026-01-04T00:00:00Z", "", false},
	compF + ":f1": {"f prompt", "m-f", "2026-01-10T00:00:00Z", "", true},
}

func check(t *testing.T, label string, m sourcetest.Merged) {
	t.Helper()
	if len(m.Usage) != len(wantUsageAll) {
		t.Errorf("%s: got %d usage events, want %d", label, len(m.Usage), len(wantUsageAll))
	}
	for key, w := range wantUsageAll {
		u, ok := m.Usage[id(model.KindUsage, key)]
		if !ok {
			t.Errorf("%s: usage %s missing", label, key)
			continue
		}
		if u.Tokens != w.tokens || u.Model != w.model || !u.TS.Equal(at(w.ts)) || u.Session != model.SessionKey(ProviderName, w.sess) ||
			u.Provider != ProviderName || u.Source != SourceName || u.Q != model.QualityExact || u.PV != pv || u.Acct != "a_cursor" || u.TZOffsetMin != 60 {
			t.Errorf("%s: usage %s = %+v, want %+v", label, key, u, w)
		}
	}
	if len(m.Prompts) != len(wantPromptAll) || len(m.Activity) != len(wantPromptAll) {
		t.Errorf("%s: got %d prompts / %d activity, want %d", label, len(m.Prompts), len(m.Activity), len(wantPromptAll))
	}
	for key, w := range wantPromptAll {
		p, ok := m.Prompts[id(model.KindPrompt, key)]
		if !ok {
			t.Errorf("%s: prompt %s missing", label, key)
			continue
		}
		if p.Text != w.text || p.Model != w.model || !p.TS.Equal(at(w.ts)) || p.Workspace != w.workspace ||
			p.Machine != "test-machine" || p.AcctLabel != "label:a_cursor" || p.Session == "" {
			t.Errorf("%s: prompt %s = %+v, want %+v", label, key, p, w)
		}
		a, ok := m.Activity[id(model.KindActivity, key)]
		if !ok || a.HasUsage != w.hasUsage || !a.TS.Equal(at(w.ts)) || a.Session != p.Session {
			t.Errorf("%s: activity %s = %+v, want hasUsage=%v ts=%s", label, key, a, w.hasUsage, w.ts)
		}
	}
}

func TestParse(t *testing.T) {
	for _, sqlJSON := range []bool{true, false} {
		old := useSQLJSON
		useSQLJSON = sqlJSON
		t.Cleanup(func() { useSQLJSON = old })
		label := map[bool]string{true: "json_extract", false: "go decode"}[sqlJSON]
		db, env := fixture(t)
		src := New()
		files, err := src.Files(env)
		if err != nil || len(files) != 1 || files[0] != db {
			t.Fatalf("%s: files = %v %v", label, files, err)
		}
		b, cur := sourcetest.Parse(t, src, env, db, sources.Cursor{})
		st, _ := os.Stat(db)
		if cur.Offset != st.Size() || len(cur.Carry) != 0 {
			t.Errorf("%s: cursor = %+v, want offset %d and no carry", label, cur, st.Size())
		}
		one := sourcetest.Merge(b)
		check(t, label, one)
		if b.LastEventTS.IsZero() || !b.LastEventTS.Equal(at("2026-01-10T00:00:00Z")) {
			t.Errorf("%s: lastEventTs = %s", label, b.LastEventTS)
		}
		// Re-reading the whole file (what every change triggers) changes nothing.
		b2, _ := sourcetest.Parse(t, src, env, db, cur)
		sourcetest.Equal(t, label+" reparse", sourcetest.Merge(b, b2), one)
		if len(b2.Usage) != len(b.Usage) || len(b2.Prompts) != len(b.Prompts) || len(b2.Activity) != len(b.Activity) {
			t.Errorf("%s: reparse emitted %d/%d/%d, first pass %d/%d/%d", label, len(b2.Usage), len(b2.Activity), len(b2.Prompts), len(b.Usage), len(b.Activity), len(b.Prompts))
		}
	}
}

// A malformed value makes json_extract fail the statement; the parser then
// falls back to decoding rows in Go and skips the bad one.
func TestMalformedRowFallsBack(t *testing.T) {
	db, env := fixture(t)
	appendRow(t, db, row{bubblePrefix + compA + ":bad", `{"type": 2, "tokenCount": {"inputTokens": 5`})
	b, _ := sourcetest.Parse(t, New(), env, db, sources.Cursor{})
	check(t, "fallback", sourcetest.Merge(b))
}

func TestNoPrompts(t *testing.T) {
	db, env := fixture(t)
	env.Prompts = false
	b, _ := sourcetest.Parse(t, New(), env, db, sources.Cursor{})
	if len(b.Prompts) != 0 || len(b.Activity) != len(wantPromptAll) || len(b.Usage) == 0 {
		t.Errorf("prompts off: %d prompts, %d activity, %d usage", len(b.Prompts), len(b.Activity), len(b.Usage))
	}
}

func TestMissingDB(t *testing.T) {
	home := t.TempDir()
	t.Setenv(DBEnv, filepath.Join(home, "missing.vscdb"))
	files, err := New().Files(sourcetest.Env(home))
	if err != nil || files != nil {
		t.Errorf("files = %v %v", files, err)
	}
	if _, _, ok := Account(home); ok {
		t.Error("account found without a database")
	}
}

func TestAccount(t *testing.T) {
	_, env := fixture(t)
	idv, label, ok := Account(env.Home)
	if !ok || idv != "auth0|user_synthetic" || label != "synthetic@example.com" {
		t.Errorf("account = %q %q %v", idv, label, ok)
	}
}

func TestParseTS(t *testing.T) {
	cases := map[string]string{
		`1767225600000`:              "2026-01-01T00:00:00Z",
		`1767225600`:                 "2026-01-01T00:00:00Z",
		`"2026-01-01T00:00:00.250Z"`: "2026-01-01T00:00:00.25Z",
		`null`:                       "",
		`""`:                         "",
		`"yesterday"`:                "",
		`0`:                          "",
	}
	for in, want := range cases {
		got := parseTS(json.RawMessage(in))
		if want == "" {
			if !got.IsZero() {
				t.Errorf("parseTS(%s) = %s, want zero", in, got)
			}
		} else if !got.Equal(at(want)) {
			t.Errorf("parseTS(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestURIPath(t *testing.T) {
	if got := uriPath("file:///c%3A/Users/x/proj"); got != filepath.FromSlash("c:/Users/x/proj") {
		t.Errorf("uriPath = %q", got)
	}
	if got := uriPath("file:///Users/x/proj"); got != filepath.FromSlash("/Users/x/proj") {
		t.Errorf("uriPath = %q", got)
	}
	if got := uriPath("vscode-remote://wsl%2Bubuntu/home/x"); got != "vscode-remote://wsl%2Bubuntu/home/x" {
		t.Errorf("uriPath = %q", got)
	}
}
