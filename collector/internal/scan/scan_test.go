package scan

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/logx"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

// lineSource is a fake parser: each complete line "key tokens" is one usage
// event. With carryPending set, a trailing "?" line is held in Carry until
// the next call, like Claude's pending prompt.
type lineSource struct {
	dir          string
	pv           int
	calls        map[string][]int64 // path -> start offsets seen
	carryPending bool
}

func (s *lineSource) Name() string                   { return "fake" }
func (s *lineSource) Provider() string               { return model.ProviderAnthropic }
func (s *lineSource) PV() int                        { return s.pv }
func (s *lineSource) Prepare(env *sources.Env) error { return nil }
func (s *lineSource) Files(env *sources.Env) ([]string, error) {
	return filepath.Glob(filepath.Join(s.dir, "*.log"))
}

func (s *lineSource) Parse(env *sources.Env, path string, cur sources.Cursor) (sources.Batch, sources.Cursor, error) {
	s.calls[path] = append(s.calls[path], cur.Offset)
	var b sources.Batch
	data, err := os.ReadFile(path)
	if err != nil {
		return b, cur, err
	}
	if len(cur.Carry) > 0 {
		// Release what was held.
		b.Prompts = append(b.Prompts, model.PromptRecord{ID: "held:" + filepath.Base(path)})
		cur.Carry = nil
	}
	sc := bufio.NewScanner(bytes.NewReader(data[cur.Offset:]))
	off := cur.Offset
	for sc.Scan() {
		line := sc.Text()
		if int(off)+len(line) >= len(data) || data[int(off)+len(line)] != '\n' {
			break // partial last line
		}
		off += int64(len(line)) + 1
		if line == "?" && s.carryPending {
			cur.Carry = json.RawMessage(`{"pending":true}`)
			continue
		}
		f := strings.Fields(line)
		b.Usage = append(b.Usage, model.UsageEvent{ID: model.EventID("usage", "anthropic", "fake", f[0]), TS: time.Unix(1_790_000_000, 0)})
	}
	cur.Offset = off
	return b, cur, nil
}

type harness struct {
	t       *testing.T
	src     *lineSource
	cursors map[string]map[string]store.FileCursor
	flushed []sources.Batch
	saves   int
	failFl  bool
	now     time.Time
}

func newHarness(t *testing.T) *harness {
	return &harness{
		t:       t,
		src:     &lineSource{dir: t.TempDir(), pv: 1, calls: map[string][]int64{}},
		cursors: map[string]map[string]store.FileCursor{},
		now:     time.Now(),
	}
}

func (h *harness) write(name, content string) string {
	p := filepath.Join(h.src.dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		h.t.Fatal(err)
	}
	return p
}

func (h *harness) run(deadline time.Time) Stats {
	h.src.calls = map[string][]int64{}
	st, err := Run(Options{
		Sources:  []sources.Source{h.src},
		Env:      &sources.Env{},
		Deadline: deadline,
		Log:      logx.Discard(),
		Now:      func() time.Time { return h.now },
		Flush: func(b sources.Batch) error {
			if h.failFl {
				return errors.New("disk full")
			}
			h.flushed = append(h.flushed, b)
			return nil
		},
		Save:        func() error { h.saves++; return nil },
		FlushEvents: 1000,
	}, h.cursors)
	if err != nil && !h.failFl {
		h.t.Fatal(err)
	}
	return st
}

func (h *harness) usage() int {
	n := 0
	for _, b := range h.flushed {
		n += len(b.Usage)
	}
	h.flushed = nil
	return n
}

func TestCursorRules(t *testing.T) {
	h := newHarness(t)
	p := h.write("a.log", "k1 1\nk2 2\npartial")
	old := time.Now().Add(-time.Hour)
	os.Chtimes(p, old, old)
	h.run(time.Time{})
	if n := h.usage(); n != 2 {
		t.Fatalf("first scan: %d events", n)
	}
	cur := h.cursors["fake"][p]
	if cur.Offset != int64(len("k1 1\nk2 2\n")) || cur.Size != int64(len("k1 1\nk2 2\npartial")) || cur.PV != 1 || cur.HeadHash == "" {
		t.Fatalf("cursor after first scan: %+v", cur)
	}

	// Unchanged with a partial last line: offered again from the cursor until
	// the file has been quiet long enough to go idle, then not even opened.
	h.run(time.Time{})
	if got := h.src.calls[p]; len(got) != 1 || got[0] != cur.Offset {
		t.Fatalf("unconsumed tail: parse offsets %v, want [%d]", got, cur.Offset)
	}
	if n := h.usage(); n != 0 || !h.cursors["fake"][p].Idle {
		t.Fatalf("unconsumed tail: %d events, idle %v", n, h.cursors["fake"][p].Idle)
	}
	h.run(time.Time{})
	if len(h.src.calls[p]) != 0 {
		t.Fatalf("unchanged idle file parsed: %v", h.src.calls[p])
	}

	// Append completes the partial line: resume from the cursor.
	h.write("a.log", "k1 1\nk2 2\npartial 3\nk4 4\n")
	h.run(time.Time{})
	if got := h.src.calls[p]; len(got) != 1 || got[0] != cur.Offset {
		t.Fatalf("append: parse offsets %v, want [%d]", got, cur.Offset)
	}
	if n := h.usage(); n != 2 {
		t.Fatalf("append: %d events", n)
	}

	// Shrink: reparse from 0.
	h.write("a.log", "k1 1\n")
	h.run(time.Time{})
	if got := h.src.calls[p]; len(got) != 1 || got[0] != 0 {
		t.Fatalf("shrink: offsets %v", got)
	}
	h.usage()

	// Same-length-or-longer file with a different head: reparse from 0.
	h.write("a.log", "z1 1\nz2 2\n")
	h.run(time.Time{})
	if got := h.src.calls[p]; len(got) != 1 || got[0] != 0 {
		t.Fatalf("head change: offsets %v", got)
	}
	if n := h.usage(); n != 2 {
		t.Fatalf("head change: %d events", n)
	}

	// Parser version bump: reparse from 0 even though nothing changed.
	h.src.pv = 2
	h.run(time.Time{})
	if got := h.src.calls[p]; len(got) != 1 || got[0] != 0 {
		t.Fatalf("pv bump: offsets %v", got)
	}
	if h.cursors["fake"][p].PV != 2 {
		t.Fatal("pv not recorded")
	}

	// Deleted file: cursor pruned.
	os.Remove(p)
	h.run(time.Time{})
	if _, ok := h.cursors["fake"][p]; ok {
		t.Fatal("cursor of deleted file kept")
	}
}

func TestCarryKeepsFileActiveUntilIdle(t *testing.T) {
	h := newHarness(t)
	h.src.carryPending = true
	p := h.write("b.log", "k1 1\n?\n")
	old := time.Now().Add(-time.Hour)
	os.Chtimes(p, old, old)
	h.run(time.Time{})
	if len(h.cursors["fake"][p].Carry) == 0 {
		t.Fatal("carry not stored")
	}
	h.flushed = nil
	// Unchanged but carrying: parsed again, which releases the held record.
	h.run(time.Time{})
	if len(h.src.calls[p]) != 1 || len(h.flushed) != 1 || len(h.flushed[0].Prompts) != 1 {
		t.Fatalf("carry release: calls %v flushed %+v", h.src.calls[p], h.flushed)
	}
	if h.cursors["fake"][p].Idle {
		t.Fatal("idle set on a call that produced output")
	}
	h.run(time.Time{}) // no carry any more: skipped
	if len(h.src.calls[p]) != 0 {
		t.Fatal("settled file parsed")
	}
}

// A file whose carry never changes goes idle after one fruitless call.
func TestIdleAfterFruitlessCall(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "d.log")
	os.WriteFile(p, []byte("k1 1\n"), 0o600)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(p, old, old)
	st, _ := os.Stat(p)
	cur := store.FileCursor{Cursor: sources.Cursor{Size: st.Size(), MtimeNs: st.ModTime().UnixNano(), Offset: st.Size(), PV: 1, Carry: json.RawMessage(`{"x":1}`)}}
	if d := Decide(cur, true, 1, st.Size(), st.ModTime().UnixNano(), nil); d != Resume {
		t.Fatalf("carry on unchanged file: %v", d)
	}
	cur.Idle = true
	if d := Decide(cur, true, 1, st.Size(), st.ModTime().UnixNano(), nil); d != Skip {
		t.Fatalf("idle file: %v", d)
	}
	// No carry but bytes left unconsumed (a deferred entry, a whole-file
	// source waiting for its session to go quiet): offered again until idle.
	short := store.FileCursor{Cursor: sources.Cursor{Size: st.Size(), MtimeNs: st.ModTime().UnixNano(), Offset: 0, PV: 1}}
	if d := Decide(short, true, 1, st.Size(), st.ModTime().UnixNano(), nil); d != Resume {
		t.Fatalf("unconsumed bytes on unchanged file: %v", d)
	}
	short.Offset = st.Size()
	if d := Decide(short, true, 1, st.Size(), st.ModTime().UnixNano(), nil); d != Skip {
		t.Fatalf("settled file: %v", d)
	}

	src := &constCarrySource{path: p}
	cursors := map[string]map[string]store.FileCursor{"const": {p: {Cursor: cur.Cursor}}}
	_, err := Run(Options{Sources: []sources.Source{src}, Env: &sources.Env{}, Log: logx.Discard(), Now: time.Now,
		Flush: func(sources.Batch) error { return nil }}, cursors)
	if err != nil {
		t.Fatal(err)
	}
	if !cursors["const"][p].Idle || src.calls != 1 {
		t.Fatalf("idle=%v calls=%d", cursors["const"][p].Idle, src.calls)
	}
	Run(Options{Sources: []sources.Source{src}, Env: &sources.Env{}, Log: logx.Discard(), Now: time.Now,
		Flush: func(sources.Batch) error { return nil }}, cursors)
	if src.calls != 1 {
		t.Fatal("idle file parsed again")
	}
}

type constCarrySource struct {
	path  string
	calls int
}

func (s *constCarrySource) Name() string                         { return "const" }
func (s *constCarrySource) Provider() string                     { return "anthropic" }
func (s *constCarrySource) PV() int                              { return 1 }
func (s *constCarrySource) Prepare(*sources.Env) error           { return nil }
func (s *constCarrySource) Files(*sources.Env) ([]string, error) { return []string{s.path}, nil }
func (s *constCarrySource) Parse(_ *sources.Env, _ string, c sources.Cursor) (sources.Batch, sources.Cursor, error) {
	s.calls++
	return sources.Batch{}, c, nil
}

// ttlSource keeps a carry until sources.CarryTTL after the file's last
// write, like Claude's in-flight usage keys.
type ttlSource struct {
	path  string
	now   func() time.Time
	calls int
}

func (s *ttlSource) Name() string                         { return "ttl" }
func (s *ttlSource) Provider() string                     { return "anthropic" }
func (s *ttlSource) PV() int                              { return 1 }
func (s *ttlSource) Prepare(*sources.Env) error           { return nil }
func (s *ttlSource) Files(*sources.Env) ([]string, error) { return []string{s.path}, nil }
func (s *ttlSource) Parse(_ *sources.Env, p string, c sources.Cursor) (sources.Batch, sources.Cursor, error) {
	s.calls++
	st, err := os.Stat(p)
	if err != nil {
		return sources.Batch{}, c, err
	}
	c.Offset = st.Size()
	c.Carry = json.RawMessage(`{"recent":1}`)
	if s.now().Sub(st.ModTime()) >= sources.CarryTTL {
		c.Carry = nil
	}
	return sources.Batch{}, c, nil
}

// An unchanged file whose carry is still live is not frozen as idle before
// the parser gets the call that clears it; afterwards it is never opened.
func TestCarryClearedBeforeIdle(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.log")
	os.WriteFile(p, []byte("x\n"), 0o600)
	written := time.Now().Add(-time.Minute)
	os.Chtimes(p, written, written)
	now := written
	src := &ttlSource{path: p, now: func() time.Time { return now }}
	cursors := map[string]map[string]store.FileCursor{}
	run := func() {
		t.Helper()
		if _, err := Run(Options{Sources: []sources.Source{src}, Env: &sources.Env{}, Log: logx.Discard(),
			Now: func() time.Time { return now }, Flush: func(sources.Batch) error { return nil }}, cursors); err != nil {
			t.Fatal(err)
		}
	}
	for m := 1; m < 30; m++ {
		now = written.Add(time.Duration(m) * time.Minute)
		run()
		if c := cursors["ttl"][p]; c.Idle || len(c.Carry) == 0 {
			t.Fatalf("minute %d: idle %v carry %s", m, c.Idle, c.Carry)
		}
	}
	now = written.Add(sources.CarryTTL)
	run()
	if c := cursors["ttl"][p]; len(c.Carry) != 0 {
		t.Fatalf("carry kept after the TTL: %s", c.Carry)
	}
	calls := src.calls
	now = now.Add(time.Hour)
	run()
	if src.calls != calls {
		t.Fatal("settled file without carry was parsed again")
	}
}

func TestFlushBeforeCursorCommit(t *testing.T) {
	h := newHarness(t)
	p := h.write("a.log", "k1 1\n")
	h.failFl = true
	h.run(time.Time{})
	if _, ok := h.cursors["fake"][p]; ok {
		t.Fatal("cursor committed although the outbox write failed")
	}
	if h.saves != 0 {
		t.Fatal("state saved although the outbox write failed")
	}
	h.failFl = false
	h.run(time.Time{})
	if n := h.usage(); n != 1 || h.cursors["fake"][p].Offset != 5 || h.saves != 1 {
		t.Fatalf("retry: %d events, cursor %+v, saves %d", n, h.cursors["fake"][p], h.saves)
	}
}

func TestDeadlineLeavesPendingFiles(t *testing.T) {
	h := newHarness(t)
	for _, n := range []string{"1.log", "2.log", "3.log"} {
		h.write(n, "k"+n+" 1\n")
	}
	st := h.run(h.now.Add(-time.Second)) // already past: nothing parsed
	if st.Complete || st.Sources["fake"].Pending != 3 || len(h.cursors["fake"]) != 0 {
		t.Fatalf("stats %+v cursors %d", st.Sources["fake"], len(h.cursors["fake"]))
	}
	st = h.run(time.Time{})
	if !st.Complete || h.usage() != 3 || len(h.cursors["fake"]) != 3 {
		t.Fatalf("second run: %+v", st.Sources["fake"])
	}
}

func TestDryRunMergesAndBuckets(t *testing.T) {
	ts := time.Date(2026, 8, 31, 23, 30, 0, 0, time.UTC)
	src := &batchSource{b: sources.Batch{
		Usage: []model.UsageEvent{
			{ID: "u1", TS: ts, TZOffsetMin: 60, PV: 1, Workspace: "/work/demo", Tokens: model.Tokens{In: 10, Out: 5, Calls: 1}},
			{ID: "u1", TS: ts.Add(time.Second), TZOffsetMin: 60, PV: 1, Workspace: "/work/demo", Tokens: model.Tokens{In: 10, Out: 9, CacheR: 100, CacheW: 4, CacheW1h: 3, Calls: 1}},
			{ID: "u2", TS: ts.Add(-24 * time.Hour), TZOffsetMin: 0, PV: 1, Workspace: "/work/other", Tokens: model.Tokens{In: 1, Calls: 1}},
		},
		Activity: []model.ActivityEvent{{ID: "a1", TS: ts, TZOffsetMin: 60, HasUsage: true, Workspace: "/work/demo"}, {ID: "a1", TS: ts, TZOffsetMin: 60, HasUsage: true, Workspace: "/work/demo"}, {ID: "a2", TS: ts, HasUsage: false}},
		Prompts:  []model.PromptRecord{{ID: "p1", TS: ts, TZOffsetMin: 60, Workspace: "/work/demo"}},
	}}
	rep, err := DryRun([]sources.Source{src}, &sources.Env{}, nil, nil, time.Now(), os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	s := rep.Sources["fake"]
	if s.Usage.Events != 2 || s.Usage.In != 11 || s.Usage.Out != 9 || s.Usage.CacheR != 100 || s.Usage.CacheW != 4 || s.Usage.CacheW1h != 3 || s.Usage.Effective != 34 {
		t.Fatalf("usage totals %+v", s.Usage)
	}
	// u1 at 23:30Z with +60 is 2026-09-01 local.
	if s.ByMonth["2026-09"] == nil || s.ByMonth["2026-09"].Usage.Events != 1 || s.ByMonth["2026-08"].Usage.Events != 1 {
		t.Fatalf("months %+v", s.ByMonth)
	}
	if s.Activity.WithUsage != 1 || s.Activity.NoUsage != 1 || s.Prompts != 1 || s.Files != 1 {
		t.Fatalf("activity %+v prompts %d", s.Activity, s.Prompts)
	}
	demo, other := s.ByWorkspace["/work/demo"], s.ByWorkspace["/work/other"]
	if demo == nil || demo.Usage.Events != 1 || demo.Usage.Out != 9 || demo.Prompts != 1 || demo.Activity.WithUsage != 1 {
		t.Fatalf("demo workspace %+v", demo)
	}
	if other == nil || other.Usage.Events != 1 || other.Usage.In != 1 || other.Prompts != 0 {
		t.Fatalf("other workspace %+v", other)
	}
	if s.ByWorkspace[""] == nil || s.ByWorkspace[""].Activity.NoUsage != 1 {
		t.Fatalf("no-folder activity %+v", s.ByWorkspace[""])
	}
	since, _ := ParseBound("2026-09-01")
	rep, _ = DryRun([]sources.Source{src}, &sources.Env{}, since, nil, time.Now(), os.Stderr)
	if rep.Sources["fake"].Usage.Events != 1 || *rep.Since != "2026-09-01" {
		t.Fatalf("since filter: %+v", rep.Sources["fake"].Usage)
	}
	until, _ := ParseBound("2026-08-31T00:00:00Z")
	rep, _ = DryRun([]sources.Source{src}, &sources.Env{}, nil, until, time.Now(), os.Stderr)
	if rep.Sources["fake"].Usage.Events != 1 || rep.Sources["fake"].Prompts != 0 {
		t.Fatalf("until filter: %+v", rep.Sources["fake"])
	}
	b, _ := json.Marshal(rep)
	for _, k := range []string{`"generatedAt"`, `"since":null`, `"until"`, `"byMonth"`, `"withUsage"`, `"effective"`, `"cacheW1h"`, `"firstTs"`, `"lastTs"`, `"provider":"anthropic"`} {
		if !bytes.Contains(b, []byte(k)) {
			t.Errorf("JSON lacks %s: %s", k, b)
		}
	}
}

type batchSource struct{ b sources.Batch }

func (s *batchSource) Name() string                         { return "fake" }
func (s *batchSource) Provider() string                     { return "anthropic" }
func (s *batchSource) PV() int                              { return 1 }
func (s *batchSource) Prepare(*sources.Env) error           { return nil }
func (s *batchSource) Files(*sources.Env) ([]string, error) { return []string{"x"}, nil }
func (s *batchSource) Parse(_ *sources.Env, _ string, c sources.Cursor) (sources.Batch, sources.Cursor, error) {
	return s.b, c, nil
}

// Two homes scanned together: one source name, one cursor map, files of
// both; a home that is unreachable next tick (a stopped WSL distro) keeps
// its cursors through KeepCursor, any other unlisted file is pruned.
func TestMultiHomeScan(t *testing.T) {
	h1 := &lineSource{dir: t.TempDir(), pv: 1, calls: map[string][]int64{}}
	h2 := &lineSource{dir: t.TempDir(), pv: 1, calls: map[string][]int64{}}
	p1 := filepath.Join(h1.dir, "a.log")
	p2 := filepath.Join(h2.dir, "a.log")
	os.WriteFile(p1, []byte("k1 1\n"), 0o600)
	os.WriteFile(p2, []byte("k2 2\n"), 0o600)
	cursors := map[string]map[string]store.FileCursor{}
	var flushed int
	run := func(homes []Home, keep func(string) bool) Stats {
		t.Helper()
		st, err := Run(Options{Homes: homes, KeepCursor: keep, Log: logx.Discard(), Now: time.Now,
			Flush: func(b sources.Batch) error { flushed += len(b.Usage); return nil }}, cursors)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	both := []Home{{Env: &sources.Env{Home: h1.dir}, Sources: []sources.Source{h1}}, {Env: &sources.Env{Home: h2.dir}, Sources: []sources.Source{h2}}}
	st := run(both, nil)
	if flushed != 2 || st.Sources["fake"].Files != 2 || st.HomeFiles[h1.dir] != 1 || st.HomeFiles[h2.dir] != 1 {
		t.Fatalf("both homes: flushed %d stats %+v homes %v", flushed, st.Sources["fake"], st.HomeFiles)
	}
	if _, ok := cursors["fake"][p1]; !ok {
		t.Fatal("home 1 cursor missing")
	}
	if _, ok := cursors["fake"][p2]; !ok {
		t.Fatal("home 2 cursor missing")
	}
	// Home 2 unreachable: its cursor is kept when KeepCursor says so.
	only1 := both[:1]
	run(only1, func(p string) bool { return strings.HasPrefix(p, h2.dir) })
	if _, ok := cursors["fake"][p2]; !ok {
		t.Fatal("cursor of the unreachable home pruned")
	}
	// ...and pruned otherwise.
	run(only1, nil)
	if _, ok := cursors["fake"][p2]; ok {
		t.Fatal("cursor of a dropped home kept")
	}
	// Back again: reparsed from 0 (the id rules keep that idempotent).
	h2.calls = map[string][]int64{}
	run(both, nil)
	if got := h2.calls[p2]; len(got) != 1 || got[0] != 0 {
		t.Fatalf("returning home: parse offsets %v", got)
	}
}
