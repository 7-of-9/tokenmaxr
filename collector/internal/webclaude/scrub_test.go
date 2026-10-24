package webclaude

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestScrubTextKeepsOffsetsSizeAndTime(t *testing.T) {
	dir := t.TempDir()
	mk := func(n int, text string) []byte {
		r := valid()
		r.Assistant = "a0a0aaaa-0000-4000-8000-00000000001" + string(rune('0'+n))
		r.At = "2026-10-23T07:39:10Z"
		if text != "" {
			r.Text = &text
		}
		b, _ := json.Marshal(r)
		return b
	}
	lines := [][]byte{mk(1, "SECRET one <&>"), mk(2, ""), mk(3, "SECRET two   é")}
	if err := Append(dir, now, lines, nil); err != nil {
		t.Fatal(err)
	}
	files, _ := SpoolFiles(dir)
	p := files[0]
	old := time.Date(2026, 10, 1, 1, 2, 3, 400, time.UTC)
	os.Chtimes(p, old, old)
	before, _ := os.Stat(p)
	// The cursor stands after the first two lines.
	upto := int64(len(lines[0]) + 1 + len(lines[1]) + 1)
	n, left, err := ScrubText(p, upto)
	if err != nil || n != 1 || left {
		t.Fatalf("n=%d left=%v err=%v", n, left, err)
	}
	after, _ := os.Stat(p)
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("size %d->%d, mtime %v->%v", before.Size(), after.Size(), before.ModTime(), after.ModTime())
	}
	b, _ := os.ReadFile(p)
	got := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(got) != 3 || len(got[0]) != len(lines[0]) || strings.Contains(got[0], "SECRET") || !strings.Contains(got[2], "SECRET two") {
		t.Fatalf("spool:\n%s", b)
	}
	for i, l := range got {
		r, err := Decode([]byte(l))
		if err != nil || r.Validate(now, false) != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		if i == 0 && (r.Text != nil || r.Est.In != 12 || r.Assistant != "a0a0aaaa-0000-4000-8000-000000000011") {
			t.Fatalf("scrubbed record %+v", r)
		}
	}
	// Everything: the last line too; a second pass changes nothing.
	if n, left, err := ScrubText(p, 1<<62); n != 1 || left || err != nil {
		t.Fatalf("n=%d left=%v err=%v", n, left, err)
	}
	if n, _, _ := ScrubText(p, 1<<62); n != 0 {
		t.Fatal("scrubbed twice")
	}
	if b, _ := os.ReadFile(p); strings.Contains(string(b), "SECRET") {
		t.Fatalf("text kept:\n%s", b)
	}
	if err := ScrubAll(dir); err != nil {
		t.Fatal(err)
	}
}

func TestPendingClaims(t *testing.T) {
	dir := t.TempDir()
	if len(PendingClaims(dir)) != 0 {
		t.Fatal("pending without a file")
	}
	if err := AppendPending(dir, []Pending{{ID: UsageID("a"), Platform: PlatformDesktop}, {ID: UsageID("b")}}); err != nil {
		t.Fatal(err)
	}
	p := PendingClaims(dir)
	if plat, ok := p[UsageID("a")]; !ok || plat != PlatformDesktop {
		t.Fatalf("pending %v", p)
	}
	if plat, ok := p[UsageID("b")]; !ok || plat != "" {
		t.Fatalf("pending %v", p)
	}
}

func TestClaimRecords(t *testing.T) {
	c := valid()
	c.Kind, c.Human, c.Est = KindClaim, "", Estimate{Algo: Algorithm}
	if err := c.Validate(now, true); err != nil {
		t.Fatal(err)
	}
	if c.Validate(now, false) == nil {
		t.Fatal("a claim is never a spool line")
	}
	c.Est.In = 5
	if c.Validate(now, true) == nil {
		t.Fatal("a claim carries no estimate")
	}
	r := valid()
	r.Est.In = MaxInPerCall + 1
	if err := r.Validate(now, true); err == nil || !strings.Contains(err.Error(), "est.in") {
		t.Fatalf("one call reading more than a context window: %v", err)
	}
	r.Calls = 2
	if err := r.Validate(now, true); err != nil {
		t.Fatal(err)
	}
	r.Est.Out = 2*MaxOutPerCall + 1
	if err := r.Validate(now, true); err == nil || !strings.Contains(err.Error(), "est.out") {
		t.Fatalf("output past the per-call limit: %v", err)
	}
}
