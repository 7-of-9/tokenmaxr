package configfix

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetCleanupPeriodDays(t *testing.T) {
	cases := []struct {
		name, in, want string
		changed        bool
	}{
		{"raise existing", "{\n  \"model\": \"x\",\n  \"cleanupPeriodDays\": 30,\n  \"hooks\": {}\n}\n",
			"{\n  \"model\": \"x\",\n  \"cleanupPeriodDays\": 3650,\n  \"hooks\": {}\n}\n", true},
		{"already high", "{\"cleanupPeriodDays\": 99999}", "{\"cleanupPeriodDays\": 99999}", false},
		{"exactly 3650", "{\"cleanupPeriodDays\":3650}", "{\"cleanupPeriodDays\":3650}", false},
		{"zero", "{\"cleanupPeriodDays\": 0}", "{\"cleanupPeriodDays\": 3650}", true},
		{"float", "{\"cleanupPeriodDays\": 30.5 }", "{\"cleanupPeriodDays\": 3650 }", true},
		{"insert pretty", "{\n    \"model\": \"x\"\n}\n", "{\n    \"cleanupPeriodDays\": 3650,\n    \"model\": \"x\"\n}\n", true},
		{"insert compact", "{\"model\":\"x\"}", "{\"cleanupPeriodDays\":3650,\"model\":\"x\"}", true},
		{"insert empty", "{}\n", "{\n  \"cleanupPeriodDays\": 3650\n}\n", true},
		{"crlf", "{\r\n  \"a\": 1\r\n}\r\n", "{\r\n  \"cleanupPeriodDays\": 3650,\r\n  \"a\": 1\r\n}\r\n", true},
		{"nested same key untouched", "{\n  \"x\": {\"cleanupPeriodDays\": 1},\n  \"cleanupPeriodDays\": 7\n}",
			"{\n  \"x\": {\"cleanupPeriodDays\": 1},\n  \"cleanupPeriodDays\": 3650\n}", true},
		{"bom", "\xef\xbb\xbf{\"cleanupPeriodDays\": 5}", "\xef\xbb\xbf{\"cleanupPeriodDays\": 3650}", true},
	}
	for _, c := range cases {
		out, changed, err := SetCleanupPeriodDays([]byte(c.in), 3650)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if changed != c.changed || string(out) != c.want {
			t.Errorf("%s:\n got %q (changed=%v)\nwant %q", c.name, out, changed, c.want)
		}
	}
	for _, bad := range []string{"{\"a\": 1,}", "// comment\n{}", "[1,2]", "null", ""} {
		if _, _, err := SetCleanupPeriodDays([]byte(bad), 3650); err == nil {
			t.Errorf("%q: expected refusal", bad)
		}
	}
}

func TestClaudeRetentionBackupOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".claude")
	if st, _ := ClaudeRetention(dir, true); st != Skipped {
		t.Fatalf("no .claude dir: %s", st)
	}
	os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, "settings.json")
	orig := "{\n  \"cleanupPeriodDays\": 30,\n  \"theme\": \"dark\"\n}\n"
	os.WriteFile(p, []byte(orig), 0o644)

	if st, _ := ClaudeRetention(dir, false); st != Skipped {
		t.Fatalf("no-fix: %s", st)
	}
	if b, _ := os.ReadFile(p); string(b) != orig {
		t.Fatal("no-fix edited the file")
	}
	if st, _ := ClaudeRetention(dir, true); st != Fixed {
		t.Fatalf("fix: %s", st)
	}
	var m map[string]any
	b, _ := os.ReadFile(p)
	if json.Unmarshal(b, &m) != nil || m["cleanupPeriodDays"] != float64(3650) || m["theme"] != "dark" {
		t.Fatalf("after fix: %s", b)
	}
	if bak, _ := os.ReadFile(p + ".d0m1-backup"); string(bak) != orig {
		t.Fatal("backup is not the original")
	}
	if st, _ := ClaudeRetention(dir, true); st != OK {
		t.Fatalf("second run: %s", st)
	}
	// A later lowering is fixed again, but the first backup is kept.
	os.WriteFile(p, []byte(`{"cleanupPeriodDays": 10}`), 0o644)
	ClaudeRetention(dir, true)
	if bak, _ := os.ReadFile(p + ".d0m1-backup"); string(bak) != orig {
		t.Fatal("backup was overwritten")
	}
	// Unparseable settings are never edited.
	os.WriteFile(p, []byte(`{"cleanupPeriodDays": 10,,}`), 0o644)
	if st, _ := ClaudeRetention(dir, true); st != Failed {
		t.Fatalf("invalid json: %s", st)
	}
	if b, _ := os.ReadFile(p); string(b) != `{"cleanupPeriodDays": 10,,}` {
		t.Fatal("invalid file was edited")
	}
	// Missing settings.json is created.
	os.Remove(p)
	if st, _ := ClaudeRetention(dir, true); st != Fixed {
		t.Fatalf("create: %s", st)
	}
}

func TestGrokTOML(t *testing.T) {
	in := strings.Join([]string{
		"[ui]",
		"cleanup_ttl_days = 5 # not storage",
		"",
		"[storage]",
		"  cleanup_ttl_days   =  30   # keep a month",
		"other = 1",
		"[storage.nested]",
		"cleanup_ttl_days = 9",
	}, "\r\n")
	out, changed := SetTOMLInt([]byte(in), "storage", "cleanup_ttl_days", func(v int64) bool { return v != 0 }, 0)
	want := strings.Replace(in, "  cleanup_ttl_days   =  30   # keep a month", "  cleanup_ttl_days   =  0   # keep a month", 1)
	if !changed || string(out) != want {
		t.Fatalf("got %q", out)
	}
	if _, changed := SetTOMLInt(out, "storage", "cleanup_ttl_days", func(v int64) bool { return v != 0 }, 0); changed {
		t.Fatal("second edit changed again")
	}
	dotted := "storage.cleanup_ttl_days = 14\n[cli]\nx = 1\n"
	out, changed = SetTOMLInt([]byte(dotted), "storage", "cleanup_ttl_days", func(v int64) bool { return v != 0 }, 0)
	if !changed || !strings.HasPrefix(string(out), "storage.cleanup_ttl_days = 0\n") {
		t.Fatalf("dotted: %q", out)
	}

	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	os.WriteFile(p, []byte("[storage]\ncleanup_ttl_days = 7\n"), 0o644)
	if st, _ := GrokRetention(p, true); st != Fixed {
		t.Fatalf("GrokRetention: %s", st)
	}
	if b, _ := os.ReadFile(p); string(b) != "[storage]\ncleanup_ttl_days = 0\n" {
		t.Fatalf("grok file: %q", b)
	}
	if st, _ := GrokRetention(p, true); st != OK {
		t.Fatalf("second: %s", st)
	}
}

func TestCodexHistory(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	os.WriteFile(p, []byte("model = \"x\"\n[history]\npersistence = \"none\"\n"), 0o644)
	if st, _ := CodexHistory(p); st != Warn {
		t.Fatalf("none: %s", st)
	}
	os.WriteFile(p, []byte("[history]\npersistence = \"save-all\"\n"), 0o644)
	if st, _ := CodexHistory(p); st != OK {
		t.Fatalf("save-all: %s", st)
	}
	if st, _ := CodexHistory(filepath.Join(dir, "missing.toml")); st != OK {
		t.Fatalf("missing: %s", st)
	}
}
