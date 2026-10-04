package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/7-of-9/tokenmaxr/collector/internal/evidence"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/claude"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	b, _ := json.Marshal(v)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A tick harvests the evidence, attributes each transcript line by its
// credential_org (recorded, mapped to the account through the snapshots),
// labels prompts from the snapshots, and an AttribVersion bump re-reads
// history once so the server can upgrade it.
func TestTickAttributesFromEvidence(t *testing.T) {
	api, srv := newFakeAPI(t)
	a, _, out := newTestApp(t)
	a.Sources = func() []sources.Source { return []sources.Source{claude.New()} }
	const (
		orgX = "aaaaaaaa-0000-4000-8000-00000000000a"
		orgY = "bbbbbbbb-0000-4000-8000-00000000000b"
	)
	oauth := func(uuid, org, email, name string) map[string]any {
		return map[string]any{"oauthAccount": map[string]any{"accountUuid": uuid, "organizationUuid": org, "emailAddress": email, "organizationName": name}}
	}
	writeJSONFile(t, filepath.Join(a.UserHome, ".claude.json"), oauth("uuid-1", orgX, "someone@example.com", "Org"))
	writeJSONFile(t, filepath.Join(a.UserHome, ".claude", "backups", ".claude.json.backup.1790000000000"), oauth("uuid-2", orgY, "other@example.com", "OrgY"))
	lines := []string{
		`{"type":"attachment","sessionId":"s1","uuid":"c1","timestamp":"2026-09-01T10:00:00Z","attachment":{"type":"credential_org","organizationUuid":"` + orgY + `"}}`,
		`{"type":"user","sessionId":"s1","uuid":"p1","timestamp":"2026-09-01T10:01:00Z","cwd":"/w","message":{"role":"user","content":"first"}}`,
		`{"type":"assistant","sessionId":"s1","uuid":"r1","timestamp":"2026-09-01T10:02:00Z","message":{"id":"m1","model":"claude-x","usage":{"input_tokens":1,"output_tokens":2}}}`,
		`{"type":"attachment","sessionId":"s1","uuid":"c2","timestamp":"2026-09-01T11:00:00Z","attachment":{"type":"credential_org","organizationUuid":"` + orgX + `"}}`,
		`{"type":"assistant","sessionId":"s1","uuid":"r2","timestamp":"2026-09-01T11:01:00Z","message":{"id":"m2","model":"claude-x","usage":{"input_tokens":3,"output_tokens":4}}}`,
	}
	tr := filepath.Join(a.UserHome, ".claude", "projects", "p", "s1.jsonl")
	os.MkdirAll(filepath.Dir(tr), 0o755)
	os.WriteFile(tr, []byte(strings.Join(lines, "\n")+"\n"), 0o600)

	ctx := context.Background()
	if err := a.Install(ctx, InstallOptions{Join: "D0M1-attrib", Endpoint: srv.URL, Label: "box", Yes: true, NoAutostart: true}); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if err := a.OpenLog(nil); err != nil {
		t.Fatal(err)
	}
	defer a.Log.Close()
	if _, err := a.Tick(ctx, TickOptions{}); err != nil {
		t.Fatal(err)
	}
	sec, _ := store.LoadSecrets(a.Home)
	x, y := model.AccountHash(sec.Key(), "anthropic", "uuid-1"), model.AccountHash(sec.Key(), "anthropic", "uuid-2")
	m1 := api.usage[model.EventID("usage", "anthropic", "claude-code", "m1")]
	m2 := api.usage[model.EventID("usage", "anthropic", "claude-code", "m2")]
	if m1.Acct != y || m1.AcctQ != model.AcctRecorded || m2.Acct != x || m2.AcctQ != model.AcctRecorded {
		t.Fatalf("usage: m1 %s/%s m2 %s/%s", m1.Acct, m1.AcctQ, m2.Acct, m2.AcctQ)
	}
	p := api.prompts[model.EventID("prompt", "anthropic", "claude-code", "p1")]
	if p.Acct != y || p.AcctQ != model.AcctRecorded || p.AcctLabel != "other@example.com · OrgY" {
		t.Fatalf("prompt: %s/%s label %q", p.Acct, p.AcctQ, p.AcctLabel)
	}
	st, _ := store.LoadState(a.Home)
	if st.AttribVersion != evidence.AttribVersion || len(st.Evidence) == 0 || len(st.Harvest) == 0 {
		t.Fatalf("state: v%d, %d records, %d marks", st.AttribVersion, len(st.Evidence), len(st.Harvest))
	}
	b, _ := os.ReadFile(paths.State(a.Home))
	for _, s := range []string{orgX, orgY, "uuid-1", "uuid-2", "@example.com"} {
		if strings.Contains(string(b), s) {
			t.Errorf("state.json holds %q", s)
		}
	}

	// A state from before this attribution version re-reads history once.
	st.AttribVersion = 0
	store.SaveState(a.Home, st)
	api.mu.Lock()
	api.usage = map[string]model.UsageEvent{}
	api.mu.Unlock()
	if _, err := a.Tick(ctx, TickOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(api.usage) != 2 {
		t.Fatalf("history not re-sent: %d usage events", len(api.usage))
	}
	st, _ = store.LoadState(a.Home)
	if st.AttribVersion != evidence.AttribVersion {
		t.Fatalf("attribVersion %d", st.AttribVersion)
	}
	log, _ := os.ReadFile(paths.Log(a.Home))
	if !strings.Contains(string(log), "re-reading history once") {
		t.Errorf("log: %s", log)
	}
	// And only once.
	api.mu.Lock()
	api.usage = map[string]model.UsageEvent{}
	api.mu.Unlock()
	if _, err := a.Tick(ctx, TickOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(api.usage) != 0 {
		t.Fatalf("history re-sent again: %d", len(api.usage))
	}
}
