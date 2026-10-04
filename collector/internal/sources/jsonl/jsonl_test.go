package jsonl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func readAll(t *testing.T, path string, off int64) ([]string, int64) {
	t.Helper()
	r, err := Open(path, off)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var out []string
	for {
		b, ok, err := r.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return out, r.Offset()
		}
		out = append(out, string(b))
	}
}

func TestReaderLongLinesCRLFAndPartialTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.jsonl")
	long := strings.Repeat("a", 3<<20) // longer than the 1 MB read buffer
	data := "one\r\n" + long + "\n\nthree\npartial"
	os.WriteFile(path, []byte(data), 0o644)

	lines, off := readAll(t, path, 0)
	if len(lines) != 4 || lines[0] != "one" || lines[1] != long || lines[2] != "" || lines[3] != "three" {
		t.Fatalf("lines = %d, first %q", len(lines), lines[0])
	}
	if want := int64(len(data) - len("partial")); off != want {
		t.Fatalf("offset %d, want %d (partial tail not consumed)", off, want)
	}
	// Resuming from the offset returns nothing until the line is finished.
	if lines, off2 := readAll(t, path, off); len(lines) != 0 || off2 != off {
		t.Fatalf("resume before newline: %v %d", lines, off2)
	}
	os.WriteFile(path, []byte(data+" done\n"), 0o644)
	lines, off = readAll(t, path, off)
	if len(lines) != 1 || lines[0] != "partial done" || off != int64(len(data)+6) {
		t.Fatalf("resume after newline: %v %d", lines, off)
	}
}

func TestReaderLongPartialTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.jsonl")
	os.WriteFile(path, []byte("ok\n"+strings.Repeat("b", 2<<20)), 0o644)
	lines, off := readAll(t, path, 0)
	if len(lines) != 1 || off != 3 {
		t.Fatalf("lines %d offset %d", len(lines), off)
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("short", 100); got != "short" {
		t.Fatalf("short text changed: %q", got)
	}
	for _, unit := range []string{"é", "€", "😀", "a"} {
		for pad := 0; pad < 4; pad++ {
			s := strings.Repeat("x", pad) + strings.Repeat(unit, 400)
			got := Truncate(s, 200)
			if len(got) > 200 || !utf8.ValidString(got) || !strings.Contains(got, "[truncated by the collector: original") {
				t.Errorf("unit %q pad %d: len %d valid %v %q", unit, pad, len(got), utf8.ValidString(got), got)
			}
			if !strings.HasPrefix(s, got[:strings.Index(got, "\n[truncated")]) {
				t.Errorf("unit %q pad %d: kept text is not a prefix", unit, pad)
			}
		}
	}
}
