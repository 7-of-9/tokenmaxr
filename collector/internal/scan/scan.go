// Package scan walks every source's files with persisted cursors
// (docs/agents/SPEC.md "Cursors"): stat every file each tick, reparse from 0
// on shrink, head change or parser-version bump, and hand events to the
// caller to persist before the matching cursors are committed. Since v1.3 a
// scan covers several homes (SPEC "Scan roots"): each home brings its own
// parser instances and Env, and cursors stay keyed by absolute path.
package scan

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/fsx"
	"github.com/7-of-9/tokenmaxr/collector/internal/limits"
	"github.com/7-of-9/tokenmaxr/collector/internal/logx"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

// HeadBytes is how much of a file's start the head hash covers.
const HeadBytes = 4096

// idleAfter is how long an unchanged file with parser carry keeps being
// re-offered to its parser: longer than sources.CarryTTL, so a parser that
// drops settled carry (Claude's in-flight usage keys, a pending prompt after
// 10 min) gets that call before the file goes idle with its carry frozen.
const idleAfter = sources.CarryTTL + 5*time.Minute

// Home is one scan root: its Env (attribution is per home) and its own
// parser instances (Prepare builds per-home indexes inside them).
type Home struct {
	Env     *sources.Env
	Sources []sources.Source
}

type Options struct {
	// Sources and Env describe a single home; Homes, when set, wins.
	Sources []sources.Source
	Env     *sources.Env
	Homes   []Home
	// KeepCursor reports whether the cursor of a file that no source listed
	// this tick must be kept anyway (a file inside a WSL distro that is not
	// running: unreachable now, unchanged, back later). Others are pruned.
	KeepCursor func(path string) bool
	// Deadline stops the scan between files; zero means no limit. Files not
	// reached keep their cursors and are picked up next tick.
	Deadline time.Time
	Log      *logx.Logger
	Now      func() time.Time
	// Flush must durably persist b (the outbox) before returning. Only then
	// are the cursors of the files that produced b committed.
	Flush func(b sources.Batch) error
	// Save persists the committed cursors (state.json).
	Save func() error
	// FlushEvents and FlushEvery bound how much work a crash can repeat.
	FlushEvents int
	FlushEvery  time.Duration
	// LimitsSent is snapshot id -> limits.SentMark of what was last queued. A
	// snapshot is re-queued when it changed, or when its provider re-reported
	// it (observedAt moved forward) at least limits.ResendAfter later.
	// Run updates it. Nil queues every snapshot.
	LimitsSent map[string]string
}

// SourceStats is one source's scan outcome, summed over the homes.
type SourceStats struct {
	Files       int
	Parsed      int
	Pending     int
	Events      int
	LastEventTS time.Time
	Err         string
}

type Stats struct {
	Sources  map[string]*SourceStats
	Complete bool
	Events   int
	// HomeFiles counts the files listed under each home path.
	HomeFiles map[string]int
}

// Decision is what to do with one file this tick.
type Decision int

const (
	Skip   Decision = iota // unchanged and settled
	Resume                 // parse from the cursor
	Reset                  // parse from offset 0
)

// Decide applies the cursor rules. head returns the hash of the file's first
// n bytes; it is only called for files that changed.
func Decide(cur store.FileCursor, known bool, pv int, size, mtimeNs int64, head func(n int64) string) Decision {
	if !known {
		return Reset
	}
	if cur.PV != pv || size < cur.Size || cur.Offset > size {
		return Reset
	}
	if size == cur.Size && mtimeNs == cur.MtimeNs {
		// Parser carry (a pending prompt) or unconsumed bytes (a deferred
		// history entry, a Grok usage.json waiting for its session to go
		// quiet) keep an unchanged file on offer until it goes idle.
		if cur.Idle || (len(cur.Carry) == 0 && cur.Offset >= size) {
			return Skip
		}
		return Resume
	}
	if cur.HeadHash != "" && head(min(HeadBytes, cur.Size)) != cur.HeadHash {
		return Reset
	}
	return Resume
}

// headReader reads a file's first HeadBytes once and hashes prefixes of it.
type headReader struct {
	path string
	buf  []byte
	err  error
	done bool
}

func (h *headReader) hash(n int64) string {
	if !h.done {
		h.done = true
		f, err := fsx.Open(h.path)
		if err != nil {
			h.err = err
		} else {
			h.buf, h.err = io.ReadAll(io.LimitReader(f, HeadBytes))
			f.Close()
		}
	}
	if h.err != nil {
		return ""
	}
	n = min(n, int64(len(h.buf)))
	sum := sha256.Sum256(h.buf[:n])
	return hex.EncodeToString(sum[:])
}

type job struct {
	path     string
	size     int64
	mtimeNs  int64
	decision Decision
	cur      store.FileCursor
	head     *headReader
	// src and env are the home's parser instance and Env for this file.
	src sources.Source
	env *sources.Env
}

type staged struct {
	source string
	path   string
	cur    store.FileCursor
}

// Run scans all sources of all homes, updating cursors (source -> path ->
// cursor) in place as batches are flushed.
func Run(o Options, cursors map[string]map[string]store.FileCursor) (Stats, error) {
	stats := Stats{Sources: map[string]*SourceStats{}, Complete: true, HomeFiles: map[string]int{}}
	if o.FlushEvents <= 0 {
		o.FlushEvents = 2000
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	homes := o.Homes
	if len(homes) == 0 {
		homes = []Home{{Env: o.Env, Sources: o.Sources}}
	}
	var pending sources.Batch
	var stage []staged
	var prune []staged
	lastFlush := now()
	flush := func() error {
		if len(pending.Usage) > 0 || len(pending.Activity) > 0 || len(pending.Prompts) > 0 || len(pending.Limits) > 0 {
			if err := o.Flush(pending); err != nil {
				return err
			}
		}
		for _, s := range stage {
			if cursors[s.source] == nil {
				cursors[s.source] = map[string]store.FileCursor{}
			}
			cursors[s.source][s.path] = s.cur
		}
		for _, s := range prune {
			delete(cursors[s.source], s.path)
		}
		pending, stage, prune = sources.Batch{}, nil, nil
		lastFlush = now()
		if o.Save != nil {
			return o.Save()
		}
		return nil
	}

	// Meters are current state, read once per home, not from a file cursor.
	for _, h := range homes {
		if h.Env == nil {
			continue
		}
		pending.Limits = append(pending.Limits, limits.Collect(h.Env)...)
	}
	pending.Limits = limits.Dedupe(pending.Limits)
	if o.LimitsSent != nil {
		fresh := pending.Limits[:0]
		for _, s := range pending.Limits {
			sent := limits.SentMark(s)
			if !limits.ShouldResend(o.LimitsSent[s.ID], sent) {
				continue
			}
			o.LimitsSent[s.ID] = sent
			fresh = append(fresh, s)
		}
		pending.Limits = fresh
	}

	// List every home first, per source name, so a cursor is pruned only
	// when no home lists its file any more.
	var names []string
	jobs := map[string][]job{}
	listed := map[string]map[string]bool{}
	failed := map[string]bool{} // a listing failed: never prune that source
	for _, h := range homes {
		for _, src := range h.Sources {
			name := src.Name()
			ss := stats.Sources[name]
			if ss == nil {
				ss = &SourceStats{}
				stats.Sources[name] = ss
				names = append(names, name)
				listed[name] = map[string]bool{}
			}
			if err := src.Prepare(h.Env); err != nil {
				ss.Err = "prepare: " + err.Error()
				failed[name] = true
				o.Log.Printf("scan %s: %s", name, ss.Err)
				continue
			}
			files, err := src.Files(h.Env)
			if err != nil {
				ss.Err = "list: " + err.Error()
				failed[name] = true
				o.Log.Printf("scan %s: %s", name, ss.Err)
				continue
			}
			ss.Files += len(files)
			if h.Env != nil {
				stats.HomeFiles[h.Env.Home] += len(files)
			}
			known := cursors[name]
			for _, p := range files {
				listed[name][p] = true
				st, err := os.Stat(p)
				if err != nil || st.IsDir() {
					continue
				}
				cur, ok := known[p]
				j := job{path: p, size: st.Size(), mtimeNs: st.ModTime().UnixNano(), cur: cur, head: &headReader{path: p}, src: src, env: h.Env}
				j.decision = Decide(cur, ok, src.PV(), j.size, j.mtimeNs, j.head.hash)
				if j.decision != Skip {
					jobs[name] = append(jobs[name], j)
				}
			}
		}
	}
	for _, name := range names {
		if failed[name] {
			continue
		}
		for p := range cursors[name] {
			if !listed[name][p] && (o.KeepCursor == nil || !o.KeepCursor(p)) {
				prune = append(prune, staged{source: name, path: p})
			}
		}
	}

	for _, name := range names {
		ss := stats.Sources[name]
		js := jobs[name]
		// Newest first, so recent days fill in before a long backfill ends.
		slices.SortStableFunc(js, func(a, b job) int {
			if c := cmpBool(a.cur.Idle, b.cur.Idle); c != 0 {
				return c
			}
			return -cmpInt(a.mtimeNs, b.mtimeNs)
		})
		for i, j := range js {
			if !o.Deadline.IsZero() && now().After(o.Deadline) {
				ss.Pending = len(js) - i
				stats.Complete = false
				break
			}
			start := sources.Cursor{}
			if j.decision == Resume {
				start = j.cur.Cursor
			}
			b, next, err := j.src.Parse(j.env, j.path, start)
			if err != nil {
				ss.Err = filepath.Base(j.path) + ": " + err.Error()
				o.Log.Printf("scan %s: parse %s", name, ss.Err)
				continue
			}
			ss.Parsed++
			n := len(b.Usage) + len(b.Activity) + len(b.Prompts)
			ss.Events += n
			stats.Events += n
			if b.LastEventTS.After(ss.LastEventTS) {
				ss.LastEventTS = b.LastEventTS
			}
			next.Size, next.MtimeNs, next.PV = j.size, j.mtimeNs, j.src.PV()
			next.HeadHash = j.head.hash(min(HeadBytes, j.size))
			fc := store.FileCursor{Cursor: next}
			// An unchanged file whose parser produced nothing new and whose
			// carry is unchanged has settled; stop re-opening it.
			unchanged := j.decision == Resume && j.size == j.cur.Size && j.mtimeNs == j.cur.MtimeNs
			if unchanged && n == 0 && next.Offset == j.cur.Offset && bytes.Equal(next.Carry, j.cur.Carry) &&
				now().Sub(time.Unix(0, j.mtimeNs)) > idleAfter {
				fc.Idle = true
			}
			pending.Add(b)
			stage = append(stage, staged{source: name, path: j.path, cur: fc})
			if len(pending.Usage)+len(pending.Activity)+len(pending.Prompts)+len(pending.Limits) >= o.FlushEvents ||
				(o.FlushEvery > 0 && now().Sub(lastFlush) >= o.FlushEvery) {
				if err := flush(); err != nil {
					return stats, err
				}
			}
		}
	}
	return stats, flush()
}

func cmpBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}

func cmpInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
