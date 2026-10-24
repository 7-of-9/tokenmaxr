package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/jsonl/sourcetest"
)

func writeNative(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

const nativePrompt = `{"type":"user","uuid":"native-user","timestamp":"2026-09-28T10:00:20Z","sessionId":"` + s1 + `","cwd":"/work/project","message":{"content":"Summarise this folder"}}` + "\n"

func TestNativeTranscriptParityAndDiscovery(t *testing.T) {
	setNow(t, "2026-09-28T11:00:00Z")
	env := sourcetest.Env(t.TempDir())
	src := NewNative()
	dir := filepath.Join(NativeDir(env.Home), s2, s3, "local_task")
	path := filepath.Join(dir, ".claude", "projects", "work", s1+".jsonl")
	// Streaming copies share a message ID; only the largest bucket counts.
	stream := nativePrompt + assistantLine + strings.Replace(assistantLine, `"output_tokens":8`, `"output_tokens":12`, 1)
	writeNative(t, path, stream)
	writeNative(t, filepath.Join(dir, "audit.jsonl"), stream)
	writeNative(t, filepath.Join(dir, "plugins", "unrelated.jsonl"), stream)
	writeNative(t, filepath.Join(NativeDir(env.Home), "not-an-account", s3, "task", "audit.jsonl"), stream)
	files, err := src.Files(env)
	if err != nil || len(files) != 1 || files[0] != path {
		t.Fatalf("discovery: %v %v", files, err)
	}
	b, cur, err := src.Parse(env, path, sources.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	m := sourcetest.Merge(b)
	if len(m.Usage) != 1 || len(m.Prompts) != 1 || len(m.Activity) != 1 {
		t.Fatalf("counts: %d %d %d", len(m.Usage), len(m.Prompts), len(m.Activity))
	}
	for _, u := range m.Usage {
		if u.Source != model.SourceClaudeNative || u.In != 7 || u.Out != 12 || u.AcctQ != model.AcctRecorded || u.Acct != env.HashID(model.ProviderAnthropic, s2) {
			t.Fatalf("usage: %+v", u)
		}
		if u.ID == usageID("msg_F") {
			t.Fatal("native and code source IDs must be separate")
		}
	}
	for _, p := range m.Prompts {
		if p.Text != "Summarise this folder" || p.Model != "claude-opus-5-5" || p.Source != model.SourceClaudeNative {
			t.Fatalf("prompt: %+v", p)
		}
	}
	for _, a := range m.Activity {
		if !a.HasUsage {
			t.Fatal("reply must upgrade prompt coverage")
		}
	}
	repeated, _, err := src.Parse(env, path, cur)
	if err != nil || len(repeated.Usage)+len(repeated.Activity)+len(repeated.Prompts) != 0 {
		t.Fatalf("unchanged file emitted events: %v", err)
	}
}

func TestNativeAuditFallbackAndMissingUsage(t *testing.T) {
	setNow(t, "2026-09-28T11:00:00Z")
	env := sourcetest.Env(t.TempDir())
	src := NewNative()
	path := filepath.Join(NativeDir(env.Home), s2, s3, "local_task", "audit.jsonl")
	toAudit := func(s string) string {
		return strings.NewReplacer(`"timestamp":`, `"_audit_timestamp":`, `"sessionId":`, `"session_id":`).Replace(s)
	}
	writeNative(t, path, toAudit(nativePrompt+assistantLine))
	files, err := src.Files(env)
	if err != nil || len(files) != 1 {
		t.Fatalf("audit not discovered: %v", err)
	}
	b, _, err := src.Parse(env, path, sources.Cursor{})
	if err != nil || len(b.Usage) != 1 || len(b.Prompts) != 1 {
		t.Fatalf("audit parse: %v", err)
	}
	if b.Usage[0].Session != model.SessionKey(model.ProviderAnthropic, s1) {
		t.Fatal("audit session lost")
	}
	writeNative(t, path, toAudit(nativePrompt))
	b, _, err = src.Parse(env, path, sources.Cursor{})
	if err != nil || len(b.Usage) != 0 || len(b.Activity) != 1 || b.Activity[0].HasUsage {
		t.Fatal("a prompt without tokens must stay missing")
	}
}

func TestNativeResumeAndPromptPrivacy(t *testing.T) {
	setNow(t, "2026-09-28T10:00:30Z")
	env := sourcetest.Env(t.TempDir())
	env.Prompts = false
	src := NewNative()
	path := filepath.Join(NativeDir(env.Home), s2, s3, "local_task", "audit.jsonl")
	writeNative(t, path, nativePrompt)
	b, cur, err := src.Parse(env, path, sources.Cursor{})
	if err != nil || len(b.Prompts) != 0 || len(b.Activity) != 1 || b.Activity[0].HasUsage {
		t.Fatal("initial coverage or privacy wrong")
	}
	if strings.Contains(string(cur.Carry), "Summarise") || strings.Contains(string(cur.Carry), s2) {
		t.Fatal("carry leaked prompt text or raw account")
	}
	writeNative(t, path, nativePrompt+assistantLine)
	// The reply may arrive after the prompt model timeout.
	setNow(t, "2026-09-28T11:00:30Z")
	b, _, err = src.Parse(env, path, cur)
	if err != nil || len(b.Prompts) != 0 || len(b.Usage) != 1 || len(b.Activity) != 1 || !b.Activity[0].HasUsage {
		t.Fatal("resume did not upgrade coverage without prompt capture")
	}
	// Parser carry is valid JSON and can survive the normal state save.
	var decoded map[string]any
	if err := json.Unmarshal(cur.Carry, &decoded); err != nil {
		t.Fatal(err)
	}
}
