// Package sourcetest holds helpers shared by the source parser tests:
// fixture homes, a fixed Env, server-style merging and split-read checks.
package sourcetest

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
)

// Env returns a deterministic Env rooted at home.
func Env(home string) *sources.Env {
	return &sources.Env{
		Home:    home,
		Machine: "test-machine",
		// A stream hint wins (as recorded); otherwise every event is the
		// provider's one timeline account.
		Attribute: func(provider string, ts time.Time, sessionID string, h sources.Hint) (string, string) {
			if !h.IsZero() {
				return h.ID, model.AcctRecorded
			}
			return "a_" + provider, model.AcctTimeline
		},
		HashID: func(provider, nativeID string) string {
			return model.AccountHash([]byte("test-key"), provider, nativeID)
		},
		Label:       func(acct string) string { return "label:" + acct },
		TZOffsetMin: func(time.Time) int { return 60 },
		Prompts:     true,
	}
}

// Home copies testdata/home into a fresh temp dir and returns it.
func Home(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	src := filepath.Join("testdata", "home")
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

// Merged is a batch after the server's merge rules.
type Merged struct {
	Usage    map[string]model.UsageEvent
	Activity map[string]model.ActivityEvent
	Prompts  map[string]model.PromptRecord
}

// Merge applies the server rules: usage by fieldwise max with min ts,
// activity by id, prompts by id with an empty model upgraded later.
func Merge(bs ...sources.Batch) Merged {
	m := Merged{
		Usage:    map[string]model.UsageEvent{},
		Activity: map[string]model.ActivityEvent{},
		Prompts:  map[string]model.PromptRecord{},
	}
	for _, b := range bs {
		for _, u := range b.Usage {
			if o, ok := m.Usage[u.ID]; ok {
				o.Tokens.Max(u.Tokens)
				if u.TS.Before(o.TS) {
					o.TS = u.TS
				}
				if o.Model == "" {
					o.Model = u.Model
				}
				m.Usage[u.ID] = o
			} else {
				m.Usage[u.ID] = u
			}
		}
		for _, a := range b.Activity {
			if _, ok := m.Activity[a.ID]; !ok {
				m.Activity[a.ID] = a
			}
		}
		for _, p := range b.Prompts {
			if o, ok := m.Prompts[p.ID]; ok {
				if o.Model == "" {
					o.Model = p.Model
					m.Prompts[p.ID] = o
				}
			} else {
				m.Prompts[p.ID] = p
			}
		}
	}
	return m
}

// Equal reports every difference between two merged results.
func Equal(t *testing.T, label string, got, want Merged) {
	t.Helper()
	for id, w := range want.Usage {
		g, ok := got.Usage[id]
		if !ok {
			t.Errorf("%s: usage %s (model %s) missing", label, id, w.Model)
			continue
		}
		if g.Tokens != w.Tokens || !g.TS.Equal(w.TS) || g.Model != w.Model || g.Session != w.Session {
			t.Errorf("%s: usage %s = %+v %s %s, want %+v %s %s", label, id, g.Tokens, g.TS, g.Model, w.Tokens, w.TS, w.Model)
		}
	}
	for id := range got.Usage {
		if _, ok := want.Usage[id]; !ok {
			t.Errorf("%s: unexpected usage %s", label, id)
		}
	}
	for id, w := range want.Activity {
		g, ok := got.Activity[id]
		if !ok || !g.TS.Equal(w.TS) || g.HasUsage != w.HasUsage {
			t.Errorf("%s: activity %s = %+v (present %v), want %+v", label, id, g, ok, w)
		}
	}
	for id := range got.Activity {
		if _, ok := want.Activity[id]; !ok {
			t.Errorf("%s: unexpected activity %s", label, id)
		}
	}
	for id, w := range want.Prompts {
		g, ok := got.Prompts[id]
		if !ok || g.Text != w.Text || g.Model != w.Model || !g.TS.Equal(w.TS) || g.Workspace != w.Workspace {
			t.Errorf("%s: prompt %s = %q/%q (present %v), want %q/%q", label, id, g.Text, g.Model, ok, w.Text, w.Model)
		}
	}
	for id := range got.Prompts {
		if _, ok := want.Prompts[id]; !ok {
			t.Errorf("%s: unexpected prompt %s", label, id)
		}
	}
}

// Parse runs one Parse call and fails the test on error.
func Parse(t *testing.T, src sources.Source, env *sources.Env, path string, cur sources.Cursor) (sources.Batch, sources.Cursor) {
	t.Helper()
	b, next, err := src.Parse(env, path, cur)
	if err != nil {
		t.Fatalf("parse %s: %v", filepath.Base(path), err)
	}
	return b, next
}

// Cuts returns interesting split points of data: every line boundary, one
// byte either side of it, and the middle of each line.
func Cuts(data []byte) []int {
	set := map[int]bool{0: true, len(data): true}
	start := 0
	for i, c := range data {
		if c != '\n' {
			continue
		}
		for _, p := range []int{i, i + 1, i + 2, start + (i-start)/2} {
			if p >= 0 && p <= len(data) {
				set[p] = true
			}
		}
		start = i + 1
	}
	out := make([]int, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

// SplitCheck writes data[:cut] to path, parses it, writes all of data,
// continues from the returned cursor, and checks the merged result equals a
// single full read. It also checks that cursors stay on line boundaries.
func SplitCheck(t *testing.T, src sources.Source, env *sources.Env, path string, data []byte) {
	t.Helper()
	write(t, path, data)
	full, _ := Parse(t, src, env, path, sources.Cursor{})
	want := Merge(full)
	for _, cut := range Cuts(data) {
		write(t, path, data[:cut])
		b1, c1 := Parse(t, src, env, path, sources.Cursor{})
		checkBoundary(t, data, cut, c1.Offset)
		write(t, path, data)
		b2, c2 := Parse(t, src, env, path, c1)
		checkBoundary(t, data, len(data), c2.Offset)
		Equal(t, "cut "+strconv.Itoa(cut), Merge(b1, b2), want)
	}
}

// Growing appends data to path step bytes at a time, parsing after each
// append from the previous cursor, like a live tail.
func Growing(t *testing.T, src sources.Source, env *sources.Env, path string, data []byte, step int) {
	t.Helper()
	write(t, path, data)
	full, _ := Parse(t, src, env, path, sources.Cursor{})
	var all []sources.Batch
	cur := sources.Cursor{}
	for n := 0; ; n += step {
		n = min(n, len(data))
		write(t, path, data[:n])
		b, next := Parse(t, src, env, path, cur)
		checkBoundary(t, data, n, next.Offset)
		all = append(all, b)
		cur = next
		if n == len(data) {
			break
		}
	}
	Equal(t, "growing", Merge(all...), Merge(full))
}

func checkBoundary(t *testing.T, data []byte, written int, off int64) {
	t.Helper()
	if off < 0 || int(off) > written {
		t.Fatalf("offset %d outside written %d bytes", off, written)
	}
	if off > 0 && data[off-1] != '\n' {
		t.Fatalf("offset %d is not at a line boundary", off)
	}
	// Everything up to the last '\n' that was written must be consumed.
	if last := bytes.LastIndexByte(data[:written], '\n'); int(off) < last+1 {
		t.Fatalf("offset %d stops before the last complete line (%d)", off, last+1)
	}
}

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
