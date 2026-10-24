package claude

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/jsonl/sourcetest"
)

// epUser and epAssistant are one prompt and one reply of a session whose lines
// carry entrypoint ep ("" leaves the field out, as older lines do).
func epUser(uuid, ts, ep, text string) string {
	return `{"type":"user","uuid":"` + uuid + `","timestamp":"` + ts + `","sessionId":"` + s1 + `","cwd":"C:\\work\\demo",` +
		epField(ep) + `"message":{"role":"user","content":"` + text + `"}}` + "\n"
}

func epAssistant(id, ts, ep string, out int) string {
	return `{"type":"assistant","uuid":"as-` + id + `","timestamp":"` + ts + `","sessionId":"` + s1 + `","cwd":"C:\\work\\demo",` +
		epField(ep) + `"message":{"id":"` + id + `","model":"claude-opus-5-5","role":"assistant","content":[],` +
		`"usage":{"input_tokens":3,"cache_creation_input_tokens":10,"cache_read_input_tokens":100,"output_tokens":` + itoa(out) + `}},"requestId":"req-` + id + `"}` + "\n"
}

func epField(ep string) string {
	if ep == "" {
		return ""
	}
	return `"entrypoint":"` + ep + `",`
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func parseText(t *testing.T, env *sources.Env, text string) sourcetest.Merged {
	t.Helper()
	path := filepath.Join(env.Home, ".claude", "projects", "C--work-demo", s1+".jsonl")
	writeNative(t, path, text)
	b, _ := sourcetest.Parse(t, New(), env, path, sources.Cursor{})
	return sourcetest.Merge(b)
}

func TestEntrypointRoutesDesktopLinesWithStableIDs(t *testing.T) {
	setNow(t, "2026-10-23T12:00:00Z")
	for _, tc := range []struct{ ep, want string }{
		{"cli", model.SourceClaudeCode},
		{"", model.SourceClaudeCode},
		{"claude-vscode", model.SourceClaudeCode},
		{"sdk-ts", model.SourceClaudeCode},
		{"remote_cowork", model.SourceClaudeCode},
		{"claude-desktop", model.SourceClaudeNative},
		{"claude-desktop-3p", model.SourceClaudeNative},
		{"local-agent", model.SourceClaudeNative},
	} {
		t.Run(tc.ep, func(t *testing.T) {
			env := sourcetest.Env(t.TempDir())
			env.Prompts = true
			m := parseText(t, env, epUser("u-1", "2026-10-23T10:00:00Z", tc.ep, "hello")+
				epAssistant("msg_1", "2026-10-23T10:00:05Z", tc.ep, 7))
			// The id is the one claude-code always gave the line, so a turn
			// already uploaded as Claude Code is replaced, never added again.
			u, ok := m.Usage[usageID("msg_1")]
			if !ok || len(m.Usage) != 1 {
				t.Fatalf("usage ids changed: %v", m.Usage)
			}
			if u.Source != tc.want || u.Q != model.QualityExact || u.PV != pv || u.Out != 7 || u.CacheR != 100 {
				t.Fatalf("usage = %+v, want source %s exact", u, tc.want)
			}
			a, ok := m.Activity[activityID("u-1")]
			if !ok || a.Source != tc.want || !a.HasUsage {
				t.Fatalf("activity = %+v (ok %v)", a, ok)
			}
			p, ok := m.Prompts[promptID("u-1")]
			if !ok || p.Source != tc.want || p.Model != "claude-opus-5-5" {
				t.Fatalf("prompt = %+v (ok %v)", p, ok)
			}
			// A routed line says where its id came from (off the wire), so the
			// rollup's rebuild compares all three under claude-code.
			wantID := ""
			if tc.want != model.SourceClaudeCode {
				wantID = model.SourceClaudeCode
			}
			if u.IDSource != wantID || a.IDSource != wantID || p.IDSource != wantID {
				t.Fatalf("id sources usage %q activity %q prompt %q, want %q", u.IDSource, a.IDSource, p.IDSource, wantID)
			}
		})
	}
}

func TestEntrypointMixedSession(t *testing.T) {
	setNow(t, "2026-10-23T12:00:00Z")
	env := sourcetest.Env(t.TempDir())
	// A desktop session resumed from the CLI, with lines that name no
	// entrypoint following each: those take the latest one.
	text := epUser("u-1", "2026-10-23T10:00:00Z", "claude-desktop", "one") +
		epAssistant("msg_1", "2026-10-23T10:00:05Z", "", 5) +
		epUser("u-2", "2026-10-23T10:01:00Z", "cli", "two") +
		epAssistant("msg_2", "2026-10-23T10:01:05Z", "", 6) +
		epUser("u-3", "2026-10-23T10:02:00Z", "", "three") +
		epAssistant("msg_3", "2026-10-23T10:02:05Z", "claude-desktop", 8)
	m := parseText(t, env, text)
	want := map[string]string{"msg_1": model.SourceClaudeNative, "msg_2": model.SourceClaudeCode, "msg_3": model.SourceClaudeNative}
	for key, src := range want {
		if u := m.Usage[usageID(key)]; u.Source != src {
			t.Errorf("%s source = %q, want %q", key, u.Source, src)
		}
	}
	for key, src := range map[string]string{"u-1": model.SourceClaudeNative, "u-2": model.SourceClaudeCode, "u-3": model.SourceClaudeCode} {
		if a := m.Activity[activityID(key)]; a.Source != src {
			t.Errorf("%s activity source = %q, want %q", key, a.Source, src)
		}
	}
}

// A read that resumes after the last desktop entrypoint keeps routing lines
// without one, and a message streamed across two reads keeps its channel.
func TestEntrypointCarriesAcrossReads(t *testing.T) {
	setNow(t, "2026-10-23T10:05:00Z")
	env := sourcetest.Env(t.TempDir())
	path := filepath.Join(env.Home, ".claude", "projects", "C--work-demo", s1+".jsonl")
	first := epUser("u-1", "2026-10-23T10:00:00Z", "claude-desktop", "one") + epAssistant("msg_1", "2026-10-23T10:00:05Z", "claude-desktop", 2)
	second := epAssistant("msg_1", "2026-10-23T10:00:06Z", "", 9) + epAssistant("msg_2", "2026-10-23T10:00:07Z", "", 4)
	writeNative(t, path, first)
	b1, cur := sourcetest.Parse(t, New(), env, path, sources.Cursor{})
	if !strings.Contains(string(cur.Carry), `"ep":"claude-desktop"`) {
		t.Fatalf("carry = %s", cur.Carry)
	}
	writeNative(t, path, first+second)
	b2, _ := sourcetest.Parse(t, New(), env, path, cur)
	m := sourcetest.Merge(b1, b2)
	if u := m.Usage[usageID("msg_1")]; u.Source != model.SourceClaudeNative || u.Out != 9 {
		t.Errorf("msg_1 = %+v", u)
	}
	if u := m.Usage[usageID("msg_2")]; u.Source != model.SourceClaudeNative {
		t.Errorf("msg_2 = %+v", u)
	}

	// A plain CLI file carries nothing once settled.
	setNow(t, "2026-10-24T10:05:00Z")
	writeNative(t, path, epUser("u-9", "2026-10-23T10:00:00Z", "cli", "x")+epAssistant("msg_9", "2026-10-23T10:00:05Z", "cli", 1))
	if _, cur := sourcetest.Parse(t, New(), env, path, sources.Cursor{}); len(cur.Carry) != 0 {
		t.Errorf("cli carry = %s", cur.Carry)
	}
}

// The Cowork reader is unchanged: its own ids and source, whatever the lines'
// entrypoint says.
func TestCoworkReaderIgnoresEntrypoint(t *testing.T) {
	setNow(t, "2026-10-23T12:00:00Z")
	env := sourcetest.Env(t.TempDir())
	path := filepath.Join(NativeDir(env.Home), s2, s3, "local_task", ".claude", "projects", "work", s1+".jsonl")
	writeNative(t, path, epUser("u-1", "2026-10-23T10:00:00Z", "local-agent", "x")+epAssistant("msg_1", "2026-10-23T10:00:05Z", "cli", 3))
	b, _, err := NewNative().Parse(env, path, sources.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	m := sourcetest.Merge(b)
	want := model.EventID(model.KindUsage, model.ProviderAnthropic, model.SourceClaudeNative, "msg_1")
	if u, ok := m.Usage[want]; !ok || u.Source != model.SourceClaudeNative {
		t.Fatalf("cowork usage = %v", m.Usage)
	}
}
