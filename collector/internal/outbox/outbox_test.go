package outbox

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
)

func batch(n int) Batch {
	var b Batch
	for i := range n {
		b.Usage = append(b.Usage, model.UsageEvent{ID: fmt.Sprintf("u%03d", i)})
		b.Activity = append(b.Activity, model.ActivityEvent{ID: fmt.Sprintf("a%03d", i)})
		b.Prompts = append(b.Prompts, model.PromptRecord{ID: fmt.Sprintf("p%03d", i), Text: "synthetic"})
	}
	return b
}

func TestWriteListReadRewrite(t *testing.T) {
	o := New(filepath.Join(t.TempDir(), "outbox"))
	if names, err := o.List(); err != nil || len(names) != 0 {
		t.Fatalf("empty outbox: %v %v", names, err)
	}
	n1, err := o.Write(batch(2))
	if err != nil {
		t.Fatal(err)
	}
	n2, _ := o.Write(batch(1))
	if n1 != "0000000001.json" || n2 != "0000000002.json" {
		t.Fatalf("names %s %s", n1, n2)
	}
	if name, _ := o.Write(Batch{}); name != "" {
		t.Fatal("empty batch was written")
	}
	names, _ := o.List()
	if len(names) != 2 || names[0] != n1 {
		t.Fatalf("List = %v", names)
	}
	if files, events := o.Count(); files != 2 || events != 9 {
		t.Fatalf("Count = %d, %d", files, events)
	}
	b, err := o.Read(n1)
	if err != nil || b.Len() != 6 {
		t.Fatalf("Read = %d %v", b.Len(), err)
	}
	keep := b.Keep(map[string]bool{"u001": true, "p000": true})
	if err := o.Rewrite(n1, keep); err != nil {
		t.Fatal(err)
	}
	if b, _ := o.Read(n1); b.Len() != 2 || b.Usage[0].ID != "u001" || b.Prompts[0].ID != "p000" {
		t.Fatalf("rewrite kept %+v", b)
	}
	if err := o.Rewrite(n1, Batch{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(o.Dir, n1)); !os.IsNotExist(err) {
		t.Fatal("empty rewrite did not delete the file")
	}
	// Numbering continues after the highest remaining file.
	if n, _ := o.Write(batch(1)); n != "0000000003.json" {
		t.Fatalf("next name %s", n)
	}
}

func TestNoTempFilesAndCorruptFileSetAside(t *testing.T) {
	o := New(t.TempDir())
	o.Write(batch(3))
	ents, _ := os.ReadDir(o.Dir)
	for _, e := range ents {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("temp file left: %s", e.Name())
		}
	}
	os.WriteFile(filepath.Join(o.Dir, "0000000009.json"), []byte("{truncated"), 0o600)
	if _, err := o.Read("0000000009.json"); err == nil {
		t.Fatal("corrupt file read without error")
	}
	if _, err := os.Stat(filepath.Join(o.Dir, "0000000009.json.bad")); err != nil {
		t.Fatal("corrupt file not moved to .bad")
	}
	if names, _ := o.List(); len(names) != 1 {
		t.Fatalf("List after corrupt = %v", names)
	}
}

func TestAttemptsRoundTripAndFollowIds(t *testing.T) {
	o := New(t.TempDir())
	b := batch(3)
	b.Attempts = map[string]int{"u001": 4, "p002": 1}
	name, err := o.Write(b)
	if err != nil {
		t.Fatal(err)
	}
	got, err := o.Read(name)
	if err != nil || got.Attempts["u001"] != 4 || got.Attempts["p002"] != 1 || len(got.Attempts) != 2 {
		t.Fatalf("Read attempts = %v (%v)", got.Attempts, err)
	}
	keep := got.Keep(map[string]bool{"u001": true, "a000": true})
	if keep.Len() != 2 || keep.Attempts["u001"] != 4 || len(keep.Attempts) != 1 {
		t.Fatalf("Keep attempts = %v", keep.Attempts)
	}
	if none := got.Keep(map[string]bool{"a000": true}); none.Attempts != nil {
		t.Fatalf("Keep without attempts must leave the map nil, got %v", none.Attempts)
	}
	parts := got.Split(4)
	total := map[string]int{}
	for _, p := range parts {
		for id, n := range p.Attempts {
			total[id] += n
		}
	}
	if total["u001"] != 4 || total["p002"] != 1 || len(total) != 2 {
		t.Fatalf("Split attempts = %v", total)
	}
	if ids := got.IDs(); len(ids) != 9 || ids[0] != "u000" || ids[8] != "p002" {
		t.Fatalf("IDs = %v", ids)
	}
	// A file written before attempts existed still reads.
	os.WriteFile(filepath.Join(o.Dir, "0000000007.json"), []byte(`{"usage":[{"id":"old"}]}`), 0o600)
	if old, err := o.Read("0000000007.json"); err != nil || old.Len() != 1 || old.Attempts != nil {
		t.Fatalf("legacy file: %+v %v", old, err)
	}
}

func TestDeadLetter(t *testing.T) {
	o := New(t.TempDir())
	o.Write(batch(1))
	if n, err := o.WriteDead(Batch{}, nil, time.Now()); n != "" || err != nil {
		t.Fatalf("empty dead batch: %q %v", n, err)
	}
	b := batch(2)
	b.Attempts = map[string]int{"u000": 20}
	at := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	n1, err := o.WriteDead(b.Keep(map[string]bool{"u000": true, "p001": true}), map[string]string{"u000": "retry limit: 20 attempts", "p001": "rejected: bad ts"}, at)
	if err != nil || n1 != filepath.Join(DeadDir, "0000000001.json") {
		t.Fatalf("WriteDead = %q %v", n1, err)
	}
	n2, _ := o.WriteDead(b.Keep(map[string]bool{"a000": true}), map[string]string{"a000": "rejected"}, at)
	if n2 != filepath.Join(DeadDir, "0000000002.json") {
		t.Fatalf("second dead file %q", n2)
	}
	// Dead-letter files never show up as queued batches.
	if names, _ := o.List(); len(names) != 1 || names[0] != "0000000001.json" {
		t.Fatalf("List = %v", names)
	}
	if files, events := o.Count(); files != 1 || events != 3 {
		t.Fatalf("Count = %d, %d", files, events)
	}
	dead, err := o.ListDead()
	if err != nil || len(dead) != 2 || dead[0] != n1 {
		t.Fatalf("ListDead = %v %v", dead, err)
	}
	d, err := o.ReadDead(n1)
	if err != nil || d.Len() != 2 || !d.At.Equal(at) || d.Reasons["u000"] != "retry limit: 20 attempts" || d.Reasons["p001"] != "rejected: bad ts" {
		t.Fatalf("ReadDead = %+v %v", d, err)
	}
	if d.Attempts != nil || d.Usage[0].ID != "u000" || d.Prompts[0].ID != "p001" {
		t.Fatalf("dead batch = %+v", d.Batch)
	}
	if files, events := o.DeadCount(); files != 2 || events != 3 {
		t.Fatalf("DeadCount = %d, %d", files, events)
	}
	if files, events := New(filepath.Join(t.TempDir(), "none")).DeadCount(); files != 0 || events != 0 {
		t.Fatalf("DeadCount on a missing dir = %d, %d", files, events)
	}
}

func TestSplit(t *testing.T) {
	b := batch(5) // 15 items
	parts := b.Split(4)
	total := 0
	for _, p := range parts {
		if p.Len() > 4 {
			t.Fatalf("part of %d", p.Len())
		}
		total += p.Len()
	}
	if total != 15 || len(parts) != 4 {
		t.Fatalf("split into %d parts, %d items", len(parts), total)
	}
}
