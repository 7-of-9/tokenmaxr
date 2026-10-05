package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/ghapi"
	"github.com/7-of-9/tokenmaxr/collector/internal/ghapi/ghapitest"
	"github.com/7-of-9/tokenmaxr/collector/internal/ghpub"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

// ownerItems decrypts machine dir's owner.json as the dashboard does.
func ownerItems(t *testing.T, a *App, f *ghapitest.Fake, id string) []map[string]any {
	t.Helper()
	file, ok := f.Files[ghpub.OwnerPath(id)]
	if !ok {
		t.Fatalf("no owner.json: %v", keys(f.Files))
	}
	pt, err := ghpub.OpenOwner(mustKey(t, a), id, []byte(file))
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		V     int              `json:"v"`
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(pt, &body); err != nil || body.V != 1 {
		t.Fatalf("plaintext %s: %v", pt, err)
	}
	return body.Items
}

// writeClaudeMeter gives the test home's Claude login a weekly meter.
func writeClaudeMeter(t *testing.T, a *App, used float64) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{
		"oauthAccount": map[string]any{"accountUuid": "uuid-1", "emailAddress": "someone@example.com", "organizationName": "Org", "organizationRateLimitTier": "default_claude_max_20x"},
		"cachedUsageUtilization": map[string]any{"fetchedAtMs": time.Now().UnixMilli(), "accountUuid": "uuid-1",
			"utilization": map[string]any{"seven_day": map[string]any{"utilization": used, "resets_at": time.Now().Add(72 * time.Hour).UTC().Format(time.RFC3339)}}},
	})
	if err := os.WriteFile(filepath.Join(a.UserHome, ".claude.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Quota meters are published only encrypted, in owner.json: the rows the
// owner's server would show, with the account email, that open with the
// owner key. quota.json (published in the clear before) is deleted. A meter
// only read again does not commit by itself; a changed one does; turning
// quota meters off deletes owner.json.
func TestGitHubPublishesQuotaMetersOnlyEncrypted(t *testing.T) {
	f, _ := useFakeGitHub(t)
	a, _, _ := newTestApp(t)
	githubOnly(t, a, f)
	writeClaudeMeter(t, a, 47)
	st, _ := store.LoadState(a.Home)
	st.GitHub.MachineID = "m_0123456789ab"
	quota := ghpub.QuotaPath(st.GitHub.MachineID)
	f.Files[quota] = `{"schema":2,"machine":"m_0123456789ab","meters":[{"plan":"Max (20x)"}]}`
	st.GitHub.Published = map[string]string{quota: ghpub.Hash([]byte(f.Files[quota]))}
	if err := store.SaveState(a.Home, st); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tick(t, a, TickOptions{})

	id := st.GitHub.MachineID
	if _, ok := f.Files[quota]; ok {
		t.Fatal("quota.json not deleted")
	}
	items := ownerItems(t, a, f, id)
	var week map[string]any
	for _, it := range items {
		if it["window"] == "week" {
			week = it
		}
	}
	if week == nil || week["label"] != "someone@example.com" || week["plan"] != "Max (20x)" || week["name"] != "Org" || week["usedPercent"] != 47.0 || week["acctQ"] != "recorded" {
		t.Fatalf("owner rows %v", items)
	}
	for p, c := range f.Files {
		if strings.HasPrefix(p, ghpub.MachineDir(id)) && (strings.Contains(c, "someone@example.com") || strings.Contains(c, "Max (20x)")) {
			t.Fatalf("%s publishes the plan or email in the clear", p)
		}
	}
	st, _ = store.LoadState(a.Home)
	if st.GitHub.Owner.Data == "" || st.GitHub.Owner.Meters == "" || st.GitHub.LastError != "" {
		t.Fatalf("owner state %+v, error %q", st.GitHub.Owner, st.GitHub.LastError)
	}
	if b, _ := os.ReadFile(filepath.Join(a.Home, "state.json")); strings.Contains(string(b), "someone@example.com") {
		t.Fatal("state.json holds the email")
	}

	// Read again, unchanged: nothing is committed.
	writeClaudeMeter(t, a, 47)
	head := f.Head
	if _, err := a.Tick(ctx, TickOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if f.Head != head {
		t.Fatal("a meter only read again made a commit")
	}
	// Changed: committed.
	writeClaudeMeter(t, a, 61)
	if _, err := a.Tick(ctx, TickOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if f.Head == head {
		t.Fatal("a changed meter was not committed")
	}
	found := false
	for _, it := range ownerItems(t, a, f, id) {
		found = found || it["window"] == "week" && it["usedPercent"] == 61.0
	}
	if !found {
		t.Fatal("owner.json not updated")
	}

	// Quota meters off: owner.json goes.
	cfg, _ := store.LoadConfig(a.Home)
	cfg.GitHub.NoQuota = true
	store.SaveConfig(a.Home, cfg)
	if _, err := a.Tick(ctx, TickOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Files[ghpub.OwnerPath(id)]; ok {
		t.Fatal("owner.json kept with quota meters off")
	}
	if st, _ := store.LoadState(a.Home); st.GitHub.Owner != (store.OwnerState{}) {
		t.Fatalf("owner state kept: %+v", st.GitHub.Owner)
	}
}

// Every quota.json in the repository goes once, whether this machine's state
// lists it or not (a sign-in again resets that list) and a retired machine's
// too; later publishes do not look again.
func TestGitHubSweepsQuotaFilesOnce(t *testing.T) {
	f, _ := useFakeGitHub(t)
	a, _, _ := newTestApp(t)
	githubOnly(t, a, f)
	st, _ := store.LoadState(a.Home)
	st.GitHub.MachineID = "m_0123456789ab"
	if err := store.SaveState(a.Home, st); err != nil {
		t.Fatal(err)
	}
	own, retired := ghpub.QuotaPath(st.GitHub.MachineID), ghpub.QuotaPath("m_retired00000")
	f.Files[own], f.Files[retired] = `{"schema":2}`, `{"schema":2}`
	f.Files[ghpub.MachineDir("m_retired00000")+"/meta.json"] = `{}`
	tick(t, a, TickOptions{})
	if _, ok := f.Files[own]; ok {
		t.Fatal("this machine's quota.json not deleted")
	}
	if _, ok := f.Files[retired]; ok {
		t.Fatal("the retired machine's quota.json not deleted")
	}
	if _, ok := f.Files[ghpub.MachineDir("m_retired00000")+"/meta.json"]; !ok {
		t.Fatal("the retired machine's other files were touched")
	}
	st, _ = store.LoadState(a.Home)
	if st.GitHub.QuotaSwept != "octo/agent-usage@main" || st.GitHub.LastError != "" {
		t.Fatalf("swept %q, error %q", st.GitHub.QuotaSwept, st.GitHub.LastError)
	}
	for p := range st.GitHub.Published {
		if strings.HasSuffix(p, "/quota.json") {
			t.Fatalf("%s still recorded as published", p)
		}
	}
	// Swept: one put back by hand stays.
	f.Files[retired] = `{"schema":2}`
	if _, err := a.Tick(context.Background(), TickOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Files[retired]; !ok {
		t.Fatal("swept again")
	}
}

// A meter whose account is no longer signed in takes the email from the
// account's local label; meters only read again ride along with a commit
// that happens anyway; no fleet key, no owner.json.
func TestOwnerFileRows(t *testing.T) {
	k := make([]byte, 32)
	cfg := store.DefaultConfig()
	cfg.GitHub = &store.GitHubConfig{Repo: "octo/agent-usage", Label: "laptop"}
	cfg.AccountLabels = map[string]string{"a_0123456789abcdef": "old@example.com · Old Org"}
	sec := store.Secrets{K: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}
	st := store.NewState()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	used := 12.0
	meters := []model.LimitSnapshot{{ID: strings.Repeat("1", 32), Provider: "anthropic", Source: "claude-code", Acct: "a_0123456789abcdef", AcctQ: "recorded",
		Window: "week", UsedPercent: &used, ObservedAt: now.Add(-time.Minute)}}
	id := "m_0123456789ab"
	file, says, err := ownerFile(&cfg, sec, st, id, meters, nil, false, now)
	if err != nil || file == nil || file.Delete {
		t.Fatalf("owner file %+v: %v", file, err)
	}
	pt, err := ghpub.OpenOwner(k, id, file.Content)
	if err != nil || !strings.Contains(string(pt), `"label":"old@example.com"`) || strings.Contains(string(pt), "Old Org") {
		t.Fatalf("plaintext %s: %v", pt, err)
	}
	st.GitHub.Published = map[string]string{ghpub.OwnerPath(id): ghpub.Hash(file.Content)}
	st.GitHub.Owner = says

	again := []model.LimitSnapshot{meters[0]}
	again[0].ObservedAt = now
	if f, _, _ := ownerFile(&cfg, sec, st, id, again, nil, false, now); f != nil {
		t.Fatal("read again: committed by itself")
	}
	usage := []ghapi.File{{Path: ghpub.MachineDir(id) + "/usage-2026-10.json", Content: []byte("{}")}}
	if f, _, _ := ownerFile(&cfg, sec, st, id, again, usage, false, now); f == nil {
		t.Fatal("read again: not with another file's commit")
	}
	if f, _, _ := ownerFile(&cfg, sec, st, id, again, nil, true, now); f == nil {
		t.Fatal("read again: not with the daily meta.json refresh")
	}
	if f, _, _ := ownerFile(&cfg, store.Secrets{}, st, id, again, usage, false, now); f == nil || !f.Delete {
		t.Fatalf("no fleet key: %+v", f)
	}
	if f, _, _ := ownerFile(&cfg, sec, st, id, nil, usage, false, now); f == nil || !f.Delete {
		t.Fatalf("no meters: %+v", f)
	}
}
