package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
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

func writeTranscript(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Every ~/.claude line is Claude Code, whichever client wrote it: lines of
// the desktop app's Code tab and local Cowork tasks (entrypoint
// claude-desktop, claude-desktop-3p, local-agent) included, under the ids
// claude-code always gave them.
func TestEveryEntrypointCountsAsClaudeCode(t *testing.T) {
	setNow(t, "2026-10-23T12:00:00Z")
	for _, ep := range []string{"cli", "", "claude-vscode", "sdk-ts", "claude-desktop", "claude-desktop-3p", "local-agent"} {
		t.Run(ep, func(t *testing.T) {
			env := sourcetest.Env(t.TempDir())
			env.Prompts = true
			path := filepath.Join(env.Home, ".claude", "projects", "C--work-demo", s1+".jsonl")
			writeTranscript(t, path, epUser("u-1", "2026-10-23T10:00:00Z", ep, "hello")+
				epAssistant("msg_1", "2026-10-23T10:00:05Z", ep, 7))
			b, _ := sourcetest.Parse(t, New(), env, path, sources.Cursor{})
			m := sourcetest.Merge(b)
			u, ok := m.Usage[usageID("msg_1")]
			if !ok || len(m.Usage) != 1 || u.Source != model.SourceClaudeCode || u.PV != pv || u.Out != 7 || u.CacheR != 100 || u.IDSource != "" {
				t.Fatalf("usage = %+v", m.Usage)
			}
			if a, ok := m.Activity[activityID("u-1")]; !ok || a.Source != model.SourceClaudeCode || !a.HasUsage {
				t.Fatalf("activity = %+v (ok %v)", a, ok)
			}
			if p, ok := m.Prompts[promptID("u-1")]; !ok || p.Source != model.SourceClaudeCode || p.Model != "claude-opus-5-5" {
				t.Fatalf("prompt = %+v (ok %v)", p, ok)
			}
		})
	}
}

// A cursor saved by 0.4.16 (same pv) resumes where it was: its carry's
// desktop entrypoint and a streamed key's native-claude channel are ignored,
// and the message is still emitted once, at its max, under its old id.
func TestResumesA0416Carry(t *testing.T) {
	setNow(t, "2026-10-23T10:05:00Z")
	env := sourcetest.Env(t.TempDir())
	path := filepath.Join(env.Home, ".claude", "projects", "C--work-demo", s1+".jsonl")
	first := epUser("u-1", "2026-10-23T10:00:00Z", "claude-desktop", "one") + epAssistant("msg_1", "2026-10-23T10:00:05Z", "claude-desktop", 2)
	second := epAssistant("msg_1", "2026-10-23T10:00:06Z", "", 9)
	writeTranscript(t, path, first+second)
	carry := `{"recent":[{"key":"msg_1","ts":"2026-10-23T10:00:05Z","session":"` + s1 + `","model":"claude-opus-5-5",` +
		`"tokens":{"in":3,"cacheW":10,"cacheR":100,"out":2,"calls":1},"src":"native-claude"}],"last":"2026-10-23T10:00:05Z","ep":"claude-desktop"}`
	cur := sources.Cursor{Offset: int64(len(first)), PV: pv, Carry: json.RawMessage(carry)}
	b, next := sourcetest.Parse(t, New(), env, path, cur)
	m := sourcetest.Merge(b)
	u, ok := m.Usage[usageID("msg_1")]
	if !ok || len(m.Usage) != 1 || u.Source != model.SourceClaudeCode || u.Out != 9 || u.In != 3 {
		t.Fatalf("usage = %+v", m.Usage)
	}
	if next.Offset != int64(len(first+second)) {
		t.Fatalf("offset %d", next.Offset)
	}
	var c map[string]json.RawMessage
	if err := json.Unmarshal(next.Carry, &c); err != nil {
		t.Fatal(err)
	}
	if _, ok := c["ep"]; ok {
		t.Fatalf("carry kept the entrypoint: %s", next.Carry)
	}
}

// A prompt that 0.4.16 left waiting for its model in a cursor's carry was
// saved as native-claude (a desktop-app line); it goes out as claude-code,
// under the claude-code id it already had, once the reply names the model.
func TestA0416PendingPromptCountsAsClaudeCode(t *testing.T) {
	setNow(t, "2026-10-23T10:05:00Z")
	env := sourcetest.Env(t.TempDir())
	env.Prompts = true
	path := filepath.Join(env.Home, ".claude", "projects", "C--work-demo", s1+".jsonl")
	first := epUser("u-1", "2026-10-23T10:00:00Z", "claude-desktop", "one")
	second := epAssistant("msg_1", "2026-10-23T10:00:05Z", "claude-desktop", 4)
	writeTranscript(t, path, first+second)
	carry := `{"pending":[{"id":"` + promptID("u-1") + `","provider":"anthropic","source":"native-claude",` +
		`"ts":"2026-10-23T10:00:00Z","tzOffsetMin":0,"model":"","acct":"","acctQ":"","acctLabel":"",` +
		`"workspace":"","machine":"","session":"","text":"one"}],"ep":"claude-desktop"}`
	cur := sources.Cursor{Offset: int64(len(first)), PV: pv, Carry: json.RawMessage(carry)}
	b, _ := sourcetest.Parse(t, New(), env, path, cur)
	m := sourcetest.Merge(b)
	p, ok := m.Prompts[promptID("u-1")]
	if !ok || len(m.Prompts) != 1 || p.Source != model.SourceClaudeCode || p.Model != "claude-opus-5-5" || p.IDSource != "" {
		t.Fatalf("prompts = %+v", m.Prompts)
	}
	if u, ok := m.Usage[usageID("msg_1")]; !ok || u.Source != model.SourceClaudeCode {
		t.Fatalf("usage = %+v", m.Usage)
	}
}
