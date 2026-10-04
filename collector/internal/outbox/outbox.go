// Package outbox stores events waiting for upload as NNNNNNNNNN.json batch
// files. Files are written with fsync + rename before cursors are saved, so a
// crash can only cause harmless re-sends, never lost events.
package outbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/fsx"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
)

// Batch is the content of one outbox file.
type Batch struct {
	Usage        []model.UsageEvent           `json:"usage,omitempty"`
	Activity     []model.ActivityEvent        `json:"activity,omitempty"`
	Prompts      []model.PromptRecord         `json:"prompts,omitempty"`
	Limits       []model.LimitSnapshot        `json:"limits,omitempty"`
	AccountUsage []model.AccountUsageSnapshot `json:"accountUsage,omitempty"`
	// Attempts counts, per id, the sends the server answered with "retry";
	// the uploader dead-letters an id once it reaches its limit. Absent in
	// files written before it existed.
	Attempts map[string]int `json:"attempts,omitempty"`
}

func (b *Batch) Len() int {
	return len(b.Usage) + len(b.Activity) + len(b.Prompts) + len(b.Limits) + len(b.AccountUsage)
}

// Keep returns the subset of b whose ids are in ids (with their attempts).
func (b *Batch) Keep(ids map[string]bool) Batch {
	var out Batch
	for _, e := range b.Usage {
		if ids[e.ID] {
			out.Usage = append(out.Usage, e)
		}
	}
	for _, e := range b.Activity {
		if ids[e.ID] {
			out.Activity = append(out.Activity, e)
		}
	}
	for _, e := range b.Prompts {
		if ids[e.ID] {
			out.Prompts = append(out.Prompts, e)
		}
	}
	for _, e := range b.Limits {
		if ids[e.ID] {
			out.Limits = append(out.Limits, e)
		}
	}
	for _, e := range b.AccountUsage {
		if ids[e.ID] {
			out.AccountUsage = append(out.AccountUsage, e)
		}
	}
	out.Attempts = b.attemptsFor(out)
	return out
}

// attemptsFor returns b's attempt counts for the ids present in sub, or nil
// when there are none (so the field stays out of the JSON).
func (b *Batch) attemptsFor(sub Batch) map[string]int {
	var out map[string]int
	for _, id := range sub.IDs() {
		if n := b.Attempts[id]; n > 0 {
			if out == nil {
				out = map[string]int{}
			}
			out[id] = n
		}
	}
	return out
}

// IDs lists every id in b: usage, then activity, then prompts.
func (b *Batch) IDs() []string {
	out := make([]string, 0, b.Len())
	for _, e := range b.Usage {
		out = append(out, e.ID)
	}
	for _, e := range b.Activity {
		out = append(out, e.ID)
	}
	for _, e := range b.Prompts {
		out = append(out, e.ID)
	}
	for _, e := range b.Limits {
		out = append(out, e.ID)
	}
	for _, e := range b.AccountUsage {
		out = append(out, e.ID)
	}
	return out
}

// Split cuts b into batches of at most n items each.
func (b *Batch) Split(n int) []Batch {
	var out []Batch
	cur := Batch{}
	flush := func() {
		if cur.Len() > 0 {
			cur.Attempts = b.attemptsFor(cur)
			out = append(out, cur)
			cur = Batch{}
		}
	}
	for _, e := range b.Usage {
		cur.Usage = append(cur.Usage, e)
		if cur.Len() >= n {
			flush()
		}
	}
	for _, e := range b.Activity {
		cur.Activity = append(cur.Activity, e)
		if cur.Len() >= n {
			flush()
		}
	}
	for _, e := range b.Prompts {
		cur.Prompts = append(cur.Prompts, e)
		if cur.Len() >= n {
			flush()
		}
	}
	for _, e := range b.Limits {
		cur.Limits = append(cur.Limits, e)
		if cur.Len() >= n {
			flush()
		}
	}
	for _, e := range b.AccountUsage {
		cur.AccountUsage = append(cur.AccountUsage, e)
		if cur.Len() >= n {
			flush()
		}
	}
	flush()
	return out
}

type Outbox struct{ Dir string }

func New(dir string) *Outbox { return &Outbox{Dir: dir} }

// List returns batch file names, oldest first.
func (o *Outbox) List() ([]string, error) { return listNumbered(o.Dir) }

// listNumbered lists the NNNNNNNNNN.json files in dir, oldest first, and
// removes stale temp files.
func listNumbered(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range ents {
		if n := e.Name(); !e.IsDir() && isBatchName(n) {
			names = append(names, n)
		} else if strings.HasSuffix(n, ".tmp") {
			// A temp file left by a crash: its batch was never renamed into
			// place, so the cursors were not advanced either.
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > time.Hour {
				os.Remove(filepath.Join(dir, n))
			}
		}
	}
	slices.Sort(names)
	return names, nil
}

// nextName returns the numbered file name after the highest one in dir.
func nextName(dir string) (string, error) {
	names, err := listNumbered(dir)
	if err != nil {
		return "", err
	}
	var next uint64 = 1
	if len(names) > 0 {
		last, _ := strconv.ParseUint(names[len(names)-1][:10], 10, 64)
		next = last + 1
	}
	return fmt.Sprintf("%010d.json", next), nil
}

func isBatchName(n string) bool {
	if len(n) != 15 || !strings.HasSuffix(n, ".json") {
		return false
	}
	_, err := strconv.ParseUint(n[:10], 10, 64)
	return err == nil
}

// Write stores b as the next numbered file (fsync + rename).
func (o *Outbox) Write(b Batch) (string, error) {
	if b.Len() == 0 {
		return "", nil
	}
	if err := os.MkdirAll(o.Dir, 0o700); err != nil {
		return "", err
	}
	name, err := nextName(o.Dir)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	return name, fsx.WriteFileAtomic(filepath.Join(o.Dir, name), data, 0o600)
}

// Read loads one batch. A file that does not parse is renamed to .bad so it
// cannot block the queue.
func (o *Outbox) Read(name string) (Batch, error) {
	var b Batch
	p := filepath.Join(o.Dir, name)
	data, err := os.ReadFile(p)
	if err != nil {
		return b, err
	}
	if err := json.Unmarshal(data, &b); err != nil {
		os.Rename(p, p+".bad")
		return b, fmt.Errorf("outbox %s is corrupt (moved to .bad): %w", name, err)
	}
	return b, nil
}

// Rewrite replaces a batch file with b, or deletes it when b is empty.
func (o *Outbox) Rewrite(name string, b Batch) error {
	p := filepath.Join(o.Dir, name)
	if b.Len() == 0 {
		err := os.Remove(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(p, data, 0o600)
}

// Count returns the number of files and events queued.
func (o *Outbox) Count() (files, events int) {
	names, _ := o.List()
	for _, n := range names {
		if b, err := o.Read(n); err == nil {
			files++
			events += b.Len()
		}
	}
	return files, events
}

// DeadDir is the subdirectory of the outbox holding dead-letter files.
const DeadDir = "dead"

// Dead is one dead-letter file, outbox/dead/NNNNNNNNNN.json: records the
// server would not take (rejected as invalid, or still in "retry" after the
// uploader's attempt limit) with the reason per id. Nothing re-sends them;
// they are kept so status and doctor can report them and the user can
// inspect or delete them.
type Dead struct {
	At      time.Time         `json:"at"`
	Reasons map[string]string `json:"reasons"`
	Batch
}

func (o *Outbox) deadDir() string { return filepath.Join(o.Dir, DeadDir) }

// WriteDead stores b as the next dead-letter file and returns its name
// (relative to the outbox dir).
func (o *Outbox) WriteDead(b Batch, reasons map[string]string, now time.Time) (string, error) {
	if b.Len() == 0 {
		return "", nil
	}
	dir := o.deadDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name, err := nextName(dir)
	if err != nil {
		return "", err
	}
	b.Attempts = nil
	data, err := json.Marshal(Dead{At: now.UTC(), Reasons: reasons, Batch: b})
	if err != nil {
		return "", err
	}
	rel := filepath.Join(DeadDir, name)
	return rel, fsx.WriteFileAtomic(filepath.Join(o.Dir, rel), data, 0o600)
}

// ListDead returns dead-letter file names (relative to the outbox dir),
// oldest first.
func (o *Outbox) ListDead() ([]string, error) {
	names, err := listNumbered(o.deadDir())
	for i, n := range names {
		names[i] = filepath.Join(DeadDir, n)
	}
	return names, err
}

// ReadDead loads one dead-letter file by the name ListDead returned.
func (o *Outbox) ReadDead(name string) (Dead, error) {
	var d Dead
	data, err := os.ReadFile(filepath.Join(o.Dir, name))
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return d, fmt.Errorf("dead-letter %s is corrupt: %w", name, err)
	}
	return d, nil
}

// DeadCount returns the number of dead-letter files and the events in them.
func (o *Outbox) DeadCount() (files, events int) {
	names, _ := o.ListDead()
	for _, n := range names {
		if d, err := o.ReadDead(n); err == nil {
			files++
			events += d.Len()
		}
	}
	return files, events
}
