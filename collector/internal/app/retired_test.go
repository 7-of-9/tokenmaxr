package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/ghpub"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/rollup"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

// cells is a rollup's every cell, by date and key.
func cells(t *testing.T, ru *rollup.Rollup) map[string]rollup.Cell {
	t.Helper()
	out := map[string]rollup.Cell{}
	for date, cs := range ru.Days {
		for k, c := range cs {
			out[date+" "+k] = *c
		}
	}
	return out
}

// A rollup file written by 0.4.16 (version 6) loads as it is, with no
// rebuild, and a re-read of every log loses nothing and counts nothing
// twice: sealed Claude Code, Codex and Grok history, and the cells of the
// retired Claude channels (native-claude, web-claude, chat-claude, sealed
// and not, with their chat id sets and a live chat event), which stay
// Anthropic usage and are still published. The retired readers' state and
// the web capture's leftovers go.
func TestA0416RollupKeepsEveryCellThroughARescan(t *testing.T) {
	f, _ := useFakeGitHub(t)
	a, _, _ := newTestApp(t)
	user := t.TempDir()
	for _, src := range []string{"claude", "codex", "grok"} {
		if err := os.CopyFS(user, os.DirFS(filepath.Join("..", "sources", src, "testdata", "home"))); err != nil {
			t.Fatal(err)
		}
	}
	a.UserHome, a.CodexHome, a.Sources = user, filepath.Join(user, ".codex"), DefaultSources
	githubOnly(t, a, f)
	ctx := context.Background()
	if _, err := a.Tick(ctx, TickOptions{}); err != nil {
		t.Fatal(err)
	}
	ru, err := rollup.Load(paths.Rollup(a.Home))
	if err != nil {
		t.Fatal(err)
	}
	read := map[string]bool{}
	for _, r := range ru.Rows() {
		read[r.Provider+"|"+r.Source] = true
	}
	for _, want := range []string{"anthropic|claude-code", "openai|codex", "xai|grok-cli"} {
		if !read[want] {
			t.Fatalf("the fixtures gave no %s rows: %v", want, read)
		}
	}
	if ru.Sealed == "" {
		t.Fatal("the fixtures' dates were not sealed")
	}

	// What 0.4.16 added to the file: a chat turn swept from long ago (exempt
	// from the seal, its id in the chat sets), a live Claude Web turn, and a
	// desktop-app turn filed as native-claude under its claude-code id.
	now := time.Now()
	const old = "2025-01-15"
	oldTS := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	ru.AddUsage(model.UsageEvent{ID: "chat-1", Provider: "anthropic", Source: model.SourceClaudeChat, IDSource: model.IDSourceClaudeChat,
		TS: oldTS, Model: "claude-x", Acct: "a_9", Q: model.QualityEstimated, Tokens: model.Tokens{In: 500, Out: 50, Calls: 1}}, now)
	ru.AddUsage(model.UsageEvent{ID: "web-1", Provider: "anthropic", Source: model.SourceClaudeWeb, IDSource: model.IDSourceClaudeChat,
		TS: now.Add(-30 * time.Minute), Model: "claude-x", Acct: "a_9", Q: model.QualityEstimated, Tokens: model.Tokens{In: 300, Out: 30, Calls: 1}}, now)
	ru.AddUsage(model.UsageEvent{ID: "desk-1", Provider: "anthropic", Source: model.SourceClaudeNative, IDSource: model.SourceClaudeCode,
		TS: now.Add(-40 * time.Minute), Model: "claude-x", Acct: "a_9", Q: model.QualityExact, Tokens: model.Tokens{In: 70, Out: 7, Calls: 1}}, now)
	if err := ru.Save(paths.Rollup(a.Home)); err != nil {
		t.Fatal(err)
	}
	// And sealed history of every source on a date whose logs are gone.
	var file map[string]json.RawMessage
	b, _ := os.ReadFile(paths.Rollup(a.Home))
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatal(err)
	}
	var days map[string]map[string][10]int64
	if err := json.Unmarshal(file["days"], &days); err != nil {
		t.Fatal(err)
	}
	if days[old] == nil {
		days[old] = map[string][10]int64{}
	}
	for k, c := range map[string][10]int64{
		"anthropic|claude-code|claude-x|a_1|":   {1000, 10, 1, 500, 40, 4, 3, 12, 1, 12},
		"anthropic|native-claude|claude-x|a_1|": {200, 0, 0, 100, 20, 0, 2, 2, 0, 2},
		"anthropic|web-claude|claude-x|a_1|e":   {300, 0, 0, 3000, 30, 0, 1, 1, 0, 1},
		"openai|codex|gpt-5|a_2|":               {700, 0, 0, 7000, 70, 7, 5, 5, 0, 5},
		"xai|grok-cli|grok-4|a_3|":              {90, 0, 0, 900, 9, 0, 1, 1, 0, 1},
	} {
		days[old][k] = c
	}
	file["days"], _ = json.Marshal(days)
	if _, ok := file["chatSeen"]; !ok {
		t.Fatal("no chat id sets in the 0.4.16 file")
	}
	b, _ = json.Marshal(file)
	if err := os.WriteFile(paths.Rollup(a.Home), b, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := rollup.Load(paths.Rollup(a.Home))
	if err != nil {
		t.Fatal(err)
	}
	want := cells(t, before)

	// 0.4.16's state: the retired readers' cursors and status rows, and the
	// web capture's folders. Every other cursor goes, so all logs are read
	// again.
	st, _ := store.LoadState(a.Home)
	st.Cursors = map[string]map[string]store.FileCursor{model.SourceClaudeNative: {"cowork.jsonl": {}}, model.SourceClaudeWeb: {"spool.jsonl": {}}}
	st.Sources[model.SourceClaudeNative] = store.SourceState{Files: 1}
	st.Sources[model.SourceClaudeWeb] = store.SourceState{Files: 1}
	if err := store.SaveState(a.Home, st); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(paths.WebClaude(a.Home), "spool", "2026-10-25.jsonl"), filepath.Join(paths.ChromeExtension(a.Home), "manifest.json")} {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("{}\n"), 0o600)
	}

	a.webCleaned = false // a new process
	if _, err := a.Tick(ctx, TickOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	st, _ = store.LoadState(a.Home)
	if st.GitHub.Rebuild {
		t.Fatal("the version 6 file was rebuilt")
	}
	after, err := rollup.Load(paths.Rollup(a.Home))
	if err != nil {
		t.Fatal(err)
	}
	got := cells(t, after)
	for k, c := range want {
		if got[k] != c {
			t.Errorf("%s: %+v after the rescan, %+v before", k, got[k], c)
		}
	}
	for k, c := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s: new cell %+v", k, c)
		}
	}
	if after.Sealed != before.Sealed {
		t.Fatalf("sealed %s, was %s", after.Sealed, before.Sealed)
	}
	b, _ = os.ReadFile(paths.Rollup(a.Home))
	var saved struct {
		V        int               `json:"v"`
		ChatSeen map[string]string `json:"chatSeen"`
	}
	if err := json.Unmarshal(b, &saved); err != nil || saved.V != rollup.Version || rollup.Version != 6 || saved.ChatSeen[old] == "" {
		t.Fatalf("saved v%d (version %d), chat ids %v, %v", saved.V, rollup.Version, saved.ChatSeen, err)
	}

	// Published as before: every source under its provider.
	dir := ghpub.MachineDir(st.GitHub.MachineID)
	pub := map[string]float64{}
	for _, r := range usageRows(t, f, dir) {
		pub[r["provider"].(string)+"|"+r["source"].(string)] += r["in"].(float64)
	}
	var wantIn = map[string]float64{}
	for _, r := range after.Rows() {
		wantIn[r.Provider+"|"+r.Source] += float64(r.In)
	}
	for _, k := range []string{"anthropic|claude-code", "anthropic|native-claude", "anthropic|web-claude", "anthropic|chat-claude", "openai|codex", "xai|grok-cli"} {
		if pub[k] == 0 || pub[k] != wantIn[k] {
			t.Errorf("published %s in %v, rollup %v", k, pub[k], wantIn[k])
		}
	}

	for _, n := range retiredSources {
		if _, ok := st.Sources[n]; ok {
			t.Errorf("status row of %s kept", n)
		}
		if _, ok := st.Cursors[n]; ok {
			t.Errorf("cursors of %s kept", n)
		}
	}
	for _, d := range []string{paths.WebClaude(a.Home), paths.ChromeExtension(a.Home)} {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("%s left behind", d)
		}
	}
}

// A machine upgraded in the middle of a rollup rebuild that 0.4.16 began
// keeps that rebuild's cursors of the remaining readers, not of the retired
// ones.
func TestDropRetiredSourcesClearsRebuildCursors(t *testing.T) {
	st := &store.State{
		Cursors: map[string]map[string]store.FileCursor{model.SourceClaudeCode: {"a.jsonl": {}}, model.SourceClaudeNative: {"cowork.jsonl": {}}},
		Sources: map[string]store.SourceState{model.SourceClaudeCode: {Files: 1}, model.SourceClaudeWeb: {Files: 1}},
	}
	st.GitHub.StartRebuild()
	st.GitHub.RebuildCursors[model.SourceClaudeCode] = map[string]store.FileCursor{"a.jsonl": {}}
	st.GitHub.RebuildCursors[model.SourceClaudeNative] = map[string]store.FileCursor{"cowork.jsonl": {}}
	st.GitHub.RebuildCursors[model.SourceClaudeWeb] = map[string]store.FileCursor{"spool.jsonl": {}}
	dropRetiredSources(st)
	for _, n := range retiredSources {
		_, c := st.Cursors[n]
		_, s := st.Sources[n]
		_, r := st.GitHub.RebuildCursors[n]
		if c || s || r {
			t.Errorf("%s kept: cursors %v, status %v, rebuild cursors %v", n, c, s, r)
		}
	}
	if st.Cursors[model.SourceClaudeCode] == nil || st.Sources[model.SourceClaudeCode].Files != 1 || st.GitHub.RebuildCursors[model.SourceClaudeCode] == nil || !st.GitHub.Rebuild {
		t.Fatalf("claude-code state lost: %+v %+v %+v", st.Cursors, st.Sources, st.GitHub.RebuildCursors)
	}
	dropRetiredSources(&store.State{}) // no maps: nothing to do
}
