// Package configfix keeps the tools from deleting the logs the collector
// reads (docs/agents/SPEC.md "Config checks"). Edits are minimal text edits
// that preserve everything else, with a one-time backup next to the file.
package configfix

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/7-of-9/tokenmaxr/collector/internal/fsx"
)

// Check results (heartbeat "checks" values).
const (
	OK      = "ok"
	Fixed   = "fixed"
	Failed  = "failed"
	Skipped = "skipped"
	Warn    = "warn"
)

// ClaudeRetentionDays is the cleanupPeriodDays the collector enforces.
const ClaudeRetentionDays = 3650

const backupSuffix = ".d0m1-backup"

type Options struct {
	UserHome  string
	CodexHome string
	// Fix allows edits; false (--no-fix-config) only reports.
	Fix bool
}

// Result is the heartbeat checks map plus human-readable notes for doctor.
// Notes never contain file content.
type Result struct {
	Checks map[string]string
	Notes  []string
}

// Run performs every check.
func Run(o Options) Result {
	r := Result{Checks: map[string]string{}}
	note := func(s string, args ...any) { r.Notes = append(r.Notes, fmt.Sprintf(s, args...)) }

	st, msg := ClaudeRetention(filepath.Join(o.UserHome, ".claude"), o.Fix)
	r.Checks["claudeRetention"] = st
	if msg != "" {
		note("claude: %s", msg)
	}
	st, msg = GrokRetention(filepath.Join(o.UserHome, ".grok", "config.toml"), o.Fix)
	r.Checks["grokRetention"] = st
	if msg != "" {
		note("grok: %s", msg)
	}
	st, msg = CodexHistory(filepath.Join(o.CodexHome, "config.toml"))
	r.Checks["codexHistory"] = st
	if msg != "" {
		note("codex: %s", msg)
	}
	if sac, detail, ok := SmartAppControl(); ok {
		r.Checks["smartAppControl"] = sac
		if detail != "" {
			note("smart app control: %s", detail)
		}
	}
	return r
}

// ClaudeRetention makes sure <claudeDir>/settings.json has
// cleanupPeriodDays >= 3650 (Claude Code deletes transcripts after 30 days by
// default). A settings file that does not parse is never edited.
func ClaudeRetention(claudeDir string, fix bool) (status, msg string) {
	if _, err := os.Stat(claudeDir); err != nil {
		return Skipped, "Claude Code is not installed"
	}
	path := filepath.Join(claudeDir, "settings.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if !fix {
			return Skipped, "settings.json is missing (transcripts are deleted after 30 days); not fixing (--no-fix-config)"
		}
		out := []byte("{\n  \"cleanupPeriodDays\": " + strconv.Itoa(ClaudeRetentionDays) + "\n}\n")
		if err := fsx.WriteFileAtomic(path, out, 0o644); err != nil {
			return Failed, "could not create settings.json: " + err.Error()
		}
		return Fixed, "created settings.json with cleanupPeriodDays=3650"
	}
	if err != nil {
		return Failed, "could not read settings.json: " + err.Error()
	}
	out, changed, err := SetCleanupPeriodDays(data, ClaudeRetentionDays)
	if err != nil {
		return Failed, "settings.json: " + err.Error() + "; not edited"
	}
	if !changed {
		return OK, ""
	}
	if !fix {
		return Skipped, "cleanupPeriodDays is below 3650; not fixing (--no-fix-config)"
	}
	if err := writeWithBackup(path, data, out); err != nil {
		return Failed, err.Error()
	}
	return Fixed, "set cleanupPeriodDays=3650 (backup: settings.json" + backupSuffix + ")"
}

// SetCleanupPeriodDays returns data with the top-level cleanupPeriodDays set
// to at least days, changing only that value (or inserting the key after the
// opening brace). changed is false when the value is already high enough.
func SetCleanupPeriodDays(data []byte, days int) (out []byte, changed bool, err error) {
	bom := []byte{}
	body := data
	if bytes.HasPrefix(body, []byte("\xef\xbb\xbf")) {
		bom, body = body[:3], body[3:]
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, false, errors.New("not valid JSON")
	}
	if top == nil {
		return nil, false, errors.New("not a JSON object")
	}
	const key = "cleanupPeriodDays"
	want := strconv.Itoa(days)
	if raw, ok := top[key]; ok {
		var n float64
		if json.Unmarshal(raw, &n) == nil && n >= float64(days) {
			return data, false, nil
		}
		start, end, err := topLevelValueSpan(body, key)
		if err != nil {
			return nil, false, err
		}
		out = concatBytes(bom, body[:start], []byte(want), body[end:])
	} else {
		open := bytes.IndexByte(body, '{')
		nl := "\n"
		if bytes.Contains(body, []byte("\r\n")) {
			nl = "\r\n"
		}
		rest := body[open+1:]
		trimmed := bytes.TrimLeft(rest, " \t\r\n")
		switch {
		case len(trimmed) > 0 && trimmed[0] == '}':
			// Empty object.
			ins := "{" + nl + "  \"" + key + "\": " + want + nl
			out = concatBytes(bom, body[:open], []byte(ins), trimmed)
		case bytes.ContainsAny(rest[:len(rest)-len(trimmed)], "\n"):
			// Pretty-printed: copy the first member's indentation.
			ws := rest[:len(rest)-len(trimmed)]
			indent := ws[bytes.LastIndexByte(ws, '\n')+1:]
			ins := nl + string(indent) + "\"" + key + "\": " + want + ","
			out = concatBytes(bom, body[:open+1], []byte(ins), rest)
		default:
			ins := "\"" + key + "\":" + want + ","
			out = concatBytes(bom, body[:open+1], []byte(ins), rest)
		}
	}
	// Verify the edit before anyone writes it.
	var check map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimPrefix(out, []byte("\xef\xbb\xbf")), &check); err != nil || string(check[key]) != want || len(check) != len(top)+boolInt(top[key] == nil) {
		return nil, false, errors.New("edit did not verify")
	}
	return out, true, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func concatBytes(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// topLevelValueSpan finds the byte span of key's value in a JSON object.
func topLevelValueSpan(body []byte, key string) (start, end int, err error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return 0, 0, errors.New("not a JSON object")
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return 0, 0, err
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return 0, 0, err
		}
		if k, _ := t.(string); k == key {
			end = int(dec.InputOffset())
			return end - len(raw), end, nil
		}
	}
	return 0, 0, io.EOF
}

// GrokRetention sets [storage] cleanup_ttl_days to 0 (never delete) when it
// is set and non-zero.
func GrokRetention(path string, fix bool) (status, msg string) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if _, derr := os.Stat(filepath.Dir(path)); derr != nil {
			return Skipped, "Grok CLI is not installed"
		}
		return OK, ""
	}
	if err != nil {
		return Failed, "could not read config.toml: " + err.Error()
	}
	out, changed := SetTOMLInt(data, "storage", "cleanup_ttl_days", func(v int64) bool { return v != 0 }, 0)
	if !changed {
		return OK, ""
	}
	if !fix {
		return Skipped, "storage.cleanup_ttl_days is non-zero; not fixing (--no-fix-config)"
	}
	if err := writeWithBackup(path, data, out); err != nil {
		return Failed, err.Error()
	}
	return Fixed, "set [storage] cleanup_ttl_days=0 (backup: config.toml" + backupSuffix + ")"
}

// CodexHistory warns when Codex is told not to persist prompt history.
func CodexHistory(path string) (status, msg string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return OK, ""
	}
	if v, ok := TOMLValue(data, "history", "persistence"); ok && strings.Trim(v, `"'`) == "none" {
		return Warn, `[history] persistence = "none": Codex prompt history is not saved (usage is unaffected)`
	}
	return OK, ""
}

var (
	tomlHeader = regexp.MustCompile(`^\s*\[\[?\s*([^\]]+?)\s*\]\]?\s*(#.*)?$`)
	tomlKV     = regexp.MustCompile(`^(\s*)([A-Za-z0-9_."'-]+?)(\s*=\s*)([^#\r\n]*?)(\s*(#.*)?)(\r?)$`)
)

func normKey(k string) string {
	parts := strings.Split(k, ".")
	for i, p := range parts {
		parts[i] = strings.Trim(strings.TrimSpace(p), `"'`)
	}
	return strings.Join(parts, ".")
}

// tomlLines splits data into lines and calls fn for every key = value line
// whose full dotted key (table + key) equals table.key, with the line's
// regexp submatches. fn may rewrite lines[i].
func tomlLines(data []byte, table, key string, fn func(lines []string, i int, m []string)) []string {
	lines := strings.Split(string(data), "\n")
	cur := ""
	want := table + "." + key
	for i, line := range lines {
		if m := tomlHeader.FindStringSubmatch(line); m != nil {
			cur = normKey(m[1])
			continue
		}
		m := tomlKV.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		full := normKey(m[2])
		if cur != "" {
			full = cur + "." + full
		}
		if full == want {
			fn(lines, i, m)
		}
	}
	return lines
}

// TOMLValue returns the raw value text of table.key.
func TOMLValue(data []byte, table, key string) (string, bool) {
	var v string
	found := false
	tomlLines(data, table, key, func(_ []string, _ int, m []string) { v, found = strings.TrimSpace(m[4]), true })
	return v, found
}

// SetTOMLInt rewrites the integer value of table.key to to when bad(value),
// preserving indentation, spacing and trailing comments.
func SetTOMLInt(data []byte, table, key string, bad func(int64) bool, to int64) ([]byte, bool) {
	changed := false
	lines := tomlLines(data, table, key, func(lines []string, i int, m []string) {
		v, err := strconv.ParseInt(strings.ReplaceAll(strings.TrimSpace(m[4]), "_", ""), 0, 64)
		if err != nil || !bad(v) {
			return
		}
		lines[i] = m[1] + m[2] + m[3] + strconv.FormatInt(to, 10) + m[5] + m[7]
		changed = true
	})
	if !changed {
		return data, false
	}
	return []byte(strings.Join(lines, "\n")), true
}

// writeWithBackup saves a one-time backup of the original bytes, then writes
// out atomically with the original file mode.
func writeWithBackup(path string, orig, out []byte) error {
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	bak := path + backupSuffix
	if _, err := os.Stat(bak); errors.Is(err, fs.ErrNotExist) {
		if err := os.WriteFile(bak, orig, mode); err != nil {
			return fmt.Errorf("could not write backup: %w", err)
		}
	}
	if err := fsx.WriteFileAtomic(path, out, mode); err != nil {
		return fmt.Errorf("could not write %s: %w", filepath.Base(path), err)
	}
	return nil
}
