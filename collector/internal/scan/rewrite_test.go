package scan

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/logx"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/claudeweb"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/jsonl/sourcetest"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
	"github.com/7-of-9/tokenmaxr/collector/internal/webclaude"
)

// A source that rewrites its file's head in Parse (the Claude chat spool's
// prompt-text scrub) gets the head hash of the file as Parse left it, so the
// next growth resumes instead of re-reading the whole day.
func TestRewrittenHeadResumes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "web-claude")
	day := time.Now().UTC()
	if _, err := webclaude.Enrol(dir, day.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := webclaude.SaveSettings(dir, webclaude.Settings{Prompts: webclaude.PromptsLive}); err != nil {
		t.Fatal(err)
	}
	env := sourcetest.Env(t.TempDir())
	env.WebClaude = dir
	env.Prompts = true
	env.ScrubSpool = true // a real scan scrubs
	cursors := map[string]map[string]store.FileCursor{}
	tick := func() int {
		got := 0
		_, err := Run(Options{Sources: []sources.Source{claudeweb.New()}, Env: env, Log: logx.Discard(), Now: time.Now,
			Flush: func(b sources.Batch) error { got += len(b.Usage); return nil }, Save: func() error { return nil },
			FlushEvents: 1000}, cursors)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	add := func(n int) {
		text := "prompt text"
		id := func(k int) string { return fmt.Sprintf("a0a0aaaa-0000-4000-8000-%012d", k) }
		ts := day.Add(-time.Duration(60-n) * time.Minute)
		b, _ := json.Marshal(webclaude.Record{V: 1, Kind: webclaude.KindLive, Platform: webclaude.PlatformWeb,
			Org: "11111111-2222-4333-8444-555555555555", Conv: "73c9aaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
			Human: id(n), Assistant: id(n + 1), TS: ts.Format(time.RFC3339Nano), Model: "claude-fable-5-1", Calls: 1,
			Est: webclaude.Estimate{In: 1000, Out: 50, Algo: webclaude.Algorithm}, Text: &text, At: ts.Add(time.Second).Format(time.RFC3339Nano)})
		if err := webclaude.Append(dir, day, [][]byte{b}, nil); err != nil {
			t.Fatal(err)
		}
	}
	for i, n := range []int{10, 20, 30} {
		add(n)
		if got := tick(); got != 1 {
			t.Fatalf("tick %d: %d usage events, want only the new turn", i+1, got)
		}
	}
}
