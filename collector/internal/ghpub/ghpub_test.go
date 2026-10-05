package ghpub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/ghapi"
	"github.com/7-of-9/tokenmaxr/collector/internal/ghapi/ghapitest"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/rollup"
)

func setup(t *testing.T) (*ghapitest.Fake, *ghapi.Client) {
	f := ghapitest.New()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := ghapi.New("ghu_test", "tokenmaxr-test")
	c.API, c.Web = srv.URL, srv.URL
	return f, c
}

func TestDiscoverGuidesUntilReady(t *testing.T) {
	f, c := setup(t)
	ctx := context.Background()

	f.NoInstall = true
	g, err := Discover(ctx, c, "tokenmaxor", 0, "7-of-9/tokenmaxr-pages")
	if err != nil || g.Ready() || g.Installation != nil || g.User.Login != "octo" {
		t.Fatalf("not installed: %+v %v", g, err)
	}
	if g.InstallURL != "https://github.com/apps/tokenmaxor/installations/new/permissions?target_id=42" {
		t.Fatalf("install url %q", g.InstallURL)
	}
	for _, want := range []string{"template_owner=7-of-9", "template_name=tokenmaxr-pages", "owner=octo", "name=tokenmaxr-usage", "visibility=public"} {
		if !strings.Contains(g.CreateRepoURL, want) {
			t.Errorf("create url %q lacks %q", g.CreateRepoURL, want)
		}
	}

	f.NoInstall = false // installed, but the repository is not a tokenmaxr one yet
	g, _ = Discover(ctx, c, "tokenmaxor", 0, "7-of-9/tokenmaxr-pages")
	if g.Ready() || g.Installation == nil || !strings.HasPrefix(g.InstallURL, "https://github.com/settings/installations/") {
		t.Fatalf("installed, unmarked: %+v", g)
	}

	f.Files[MarkerFile] = `{"tokenmaxr":1}`
	g, _ = Discover(ctx, c, "tokenmaxor", 0, "7-of-9/tokenmaxr-pages")
	if !g.Ready() || g.Repo.FullName != "octo/agent-usage" {
		t.Fatalf("marked repo not found: %+v", g)
	}
	if g, _ = Discover(ctx, c, "another-app", 0, "x/y"); g.Installation != nil {
		t.Fatal("an installation of a different App must not count")
	}
	// A renamed App (new slug) is still found by its id.
	if g, _ = Discover(ctx, c, "tokenmaxr-app", 99, "7-of-9/tokenmaxr-pages"); !g.Ready() {
		t.Fatalf("renamed App not found by id: %+v", g)
	}
	if g, _ = Discover(ctx, c, "tokenmaxr-app", 100, "x/y"); g.Installation != nil {
		t.Fatal("another App's id must not count")
	}
}

func TestJoinSeedsReadsAndGuardsTheFleetKey(t *testing.T) {
	f, c := setup(t)
	ctx := context.Background()
	server := make([]byte, 32)
	server[0] = 7

	// First machine is server-enrolled: it seeds the repository with its key,
	// so account hashes match the private server.
	k, created, err := Join(ctx, c, "octo/agent-usage", server, true)
	if err != nil || !created || k[0] != 7 {
		t.Fatalf("seed: %v %v %v", k, created, err)
	}
	if v := f.Vars[FleetVariable]; v != base64.StdEncoding.EncodeToString(server) {
		t.Fatalf("variable %q", v)
	}
	// A new GitHub-only machine (no key) adopts it.
	k2, created, err := Join(ctx, c, "octo/agent-usage", nil, false)
	if err != nil || created || string(k2) != string(server) {
		t.Fatalf("join: %v %v", created, err)
	}
	// A GitHub-only machine with another local key adopts the fleet's...
	other := make([]byte, 32)
	if k3, created, err := Join(ctx, c, "octo/agent-usage", other, false); err != nil || created || string(k3) != string(server) {
		t.Fatalf("adopt: %v %v", created, err)
	}
	// ...but a server-enrolled one cannot change its key.
	if _, _, err := Join(ctx, c, "octo/agent-usage", other, true); err != ErrFleetMismatch {
		t.Fatalf("mismatch: %v", err)
	}
	// A malformed variable is reported, never used.
	f.Vars[FleetVariable] = "not-a-key"
	if _, _, err := Join(ctx, c, "octo/agent-usage", nil, false); err == nil {
		t.Fatal("malformed fleet variable accepted")
	}
}

func TestJoinGeneratesAKeyWhenNobodyHasOne(t *testing.T) {
	f, c := setup(t)
	k, created, err := Join(context.Background(), c, "octo/agent-usage", nil, false)
	if err != nil || len(k) != 32 || !created || f.Vars[FleetVariable] == "" {
		t.Fatalf("generate: %v %v", created, err)
	}
}

func sampleRows() []rollup.Row {
	r := rollup.New()
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	r.AddUsage(model.UsageEvent{ID: "a1", Provider: "anthropic", Source: "claude-code", Model: "claude-x", Acct: "a_1", TS: time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC), Tokens: model.Tokens{In: 10, Out: 5}}, now)
	r.AddUsage(model.UsageEvent{ID: "a2", Provider: "openai", Source: "codex", Model: "gpt-x", Acct: "a_2", TS: time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC), Tokens: model.Tokens{In: 20}}, now)
	return r.Rows()
}

func TestFilesArePublicSafeAndDeterministic(t *testing.T) {
	m := Machine{ID: "m_abc", Label: "laptop", OS: "windows", Collector: "0.3.0"}
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	files := Files(m, sampleRows(), AccountHistory{}, now)
	paths := []string{}
	all := ""
	for _, f := range files {
		paths = append(paths, f.Path)
		all += string(f.Content)
	}
	// quota.json (plans and reset times in the clear, before 0.4.2) only
	// ever goes: Publish deletes it where it is published.
	want := "data/machines/m_abc/usage-2026-09.json,data/machines/m_abc/usage-2026-10.json,data/machines/m_abc/quota.json,data/machines/m_abc/meta.json"
	if strings.Join(paths, ",") != want {
		t.Fatalf("paths %v", paths)
	}
	if q := fileAt(files, "/quota.json"); q == nil || !q.Delete || q.Content != nil {
		t.Fatalf("quota.json %+v", q)
	}
	for _, public := range []string{`"laptop"`, `"claude-x"`, `"a_1"`} {
		if !strings.Contains(all, public) {
			t.Errorf("published files lack %s", public)
		}
	}
	again := Files(m, sampleRows(), AccountHistory{}, now)
	for i := range files {
		if string(files[i].Content) != string(again[i].Content) {
			t.Fatalf("%s not deterministic", files[i].Path)
		}
	}
	if q := Files(m, nil, AccountHistory{}, now); len(q) != 2 || !strings.HasSuffix(q[1].Path, "/meta.json") || !q[0].Delete {
		t.Fatalf("no data must publish only meta: %v", q)
	}
}

func TestPublishCommitsOnlyChangesAndRetriesConflicts(t *testing.T) {
	f, c := setup(t)
	ctx := context.Background()
	m := Machine{ID: "m_abc", Label: "laptop", OS: "windows", Collector: "0.3.0"}
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	published := map[string]string{}
	apply := func(ch map[string]string) {
		for k, v := range ch {
			published[k] = v
		}
	}

	// A quota.json an older collector published is in the repository.
	quota := "data/machines/m_abc/quota.json"
	f.Files[quota] = `{"schema":2,"meters":[{"plan":"Max (20x)"}]}`
	published[quota] = Hash([]byte(f.Files[quota]))
	files := Files(m, sampleRows(), AccountHistory{}, now)
	if !Pending(files, published) {
		t.Fatal("nothing pending before the first publish")
	}
	ch, err := Publish(ctx, c, "octo/agent-usage", "main", m.Label, files, published, false)
	if err != nil || len(ch) != 4 || f.Files["data/machines/m_abc/meta.json"] == "" || ch[quota] != "" {
		t.Fatalf("first publish: %v %v", ch, err)
	}
	if _, ok := f.Files[quota]; ok {
		t.Fatal("quota.json not deleted")
	}
	apply(ch)
	delete(published, quota)

	// Same data an hour later: nothing to commit (meta alone is not news).
	files = Files(m, sampleRows(), AccountHistory{}, now.Add(time.Hour))
	if Pending(files, published) {
		t.Fatal("unchanged files pending")
	}
	if ch, err := Publish(ctx, c, "octo/agent-usage", "main", m.Label, files, published, false); err != nil || ch != nil {
		t.Fatalf("unchanged publish committed: %v %v", ch, err)
	}
	// ...unless a daily "last seen" refresh is due.
	ch, err = Publish(ctx, c, "octo/agent-usage", "main", m.Label, Files(m, sampleRows(), AccountHistory{}, now.Add(25*time.Hour)), published, true)
	if err != nil || len(ch) != 1 {
		t.Fatalf("meta refresh: %v %v", ch, err)
	}
	apply(ch)

	// A change commits that file and meta only, even after another machine
	// moved the branch once.
	rows := sampleRows()
	rows[0].In += 5
	f.MoveOnce = true
	ch, err = Publish(ctx, c, "octo/agent-usage", "main", m.Label, Files(m, rows, AccountHistory{}, now.Add(26*time.Hour)), published, false)
	if err != nil || len(ch) != 2 || ch["data/machines/m_abc/usage-2026-09.json"] == "" {
		t.Fatalf("change after conflict: %v %v", ch, err)
	}
}

func TestNewMachineID(t *testing.T) {
	a, b := NewMachineID(), NewMachineID()
	if len(a) != 14 || !strings.HasPrefix(a, "m_") || a == b {
		t.Fatalf("ids %q %q", a, b)
	}
}

// readRows reads a usage file's rows by column name, as the dashboard does.
func readRows(t *testing.T, content []byte) (int, []map[string]any) {
	t.Helper()
	var f struct {
		Schema int      `json:"schema"`
		Cols   []string `json:"cols"`
		Rows   [][]any  `json:"rows"`
	}
	if err := json.Unmarshal(content, &f); err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, r := range f.Rows {
		if len(r) != len(f.Cols) {
			t.Fatalf("row %v does not match cols %v", r, f.Cols)
		}
		m := map[string]any{}
		for i, c := range f.Cols {
			m[c] = r[i]
		}
		out = append(out, m)
	}
	return f.Schema, out
}

func TestSchema2UsageColumns(t *testing.T) {
	want := "date,provider,source,model,acct,q,in,cacheW,cacheW1h,cacheR,out,reasoning,events,prompts,promptsNoUsage,modelPrompts"
	if strings.Join(UsageCols, ",") != want {
		t.Fatalf("cols %v", UsageCols)
	}
	r := rollup.New()
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	ts := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	r.AddUsage(model.UsageEvent{ID: "b1", Provider: "anthropic", Source: "claude-code", Model: "claude-x", Acct: "a_1", TS: ts, Q: model.QualityExact,
		Tokens: model.Tokens{In: 10, CacheW: 8, CacheW1h: 6, CacheR: 4, Out: 5, Reasoning: 2}}, now)
	r.AddUsage(model.UsageEvent{ID: "b2", Provider: "anthropic", Source: "claude-code", Model: "claude-x", Acct: "a_1", TS: ts, Q: model.QualityEstimated,
		Tokens: model.Tokens{In: 3}}, now)
	r.AddActivity(model.ActivityEvent{ID: "b3", Provider: "anthropic", Source: "claude-code", Acct: "a_1", TS: ts, HasUsage: true})
	r.AddActivity(model.ActivityEvent{ID: "b4", Provider: "anthropic", Source: "claude-code", Acct: "a_1", TS: ts.AddDate(0, 0, -5)}) // a prompt on a day with no tokens
	r.AddPrompt(model.PromptRecord{ID: "b5", Provider: "anthropic", Source: "claude-code", Model: "claude-x", Acct: "a_1", TS: ts, Text: "hello there"})
	files := Files(Machine{ID: "m_abc", Label: "laptop", OS: "linux", Collector: "0.4.0"}, r.Rows(), AccountHistory{}, now)
	if len(files) != 4 { // two months, quota.json's deletion, meta
		t.Fatalf("files %d", len(files))
	}
	schema, sept := readRows(t, files[0].Content)
	if schema != 2 || len(sept) != 1 || sept[0]["date"] != "2026-09-28" || sept[0]["prompts"] != 1.0 || sept[0]["promptsNoUsage"] != 1.0 || sept[0]["in"] != 0.0 {
		t.Fatalf("september rows %v", sept)
	}
	_, oct := readRows(t, files[1].Content)
	byKey := map[string]map[string]any{}
	for _, row := range oct {
		byKey[row["model"].(string)+"/"+row["q"].(string)] = row
	}
	exact, est, prompts := byKey["claude-x/"], byKey["claude-x/e"], byKey["/"]
	if exact == nil || exact["cacheW1h"] != 6.0 || exact["reasoning"] != 2.0 || exact["cacheR"] != 4.0 || exact["events"] != 1.0 || exact["modelPrompts"] != 1.0 {
		t.Fatalf("exact row %v", exact)
	}
	if est == nil || est["in"] != 3.0 || est["events"] != 1.0 || est["prompts"] != 0.0 {
		t.Fatalf("estimated row %v", est)
	}
	if prompts == nil || prompts["prompts"] != 1.0 || prompts["promptsNoUsage"] != 0.0 {
		t.Fatalf("prompt row %v", prompts)
	}
	for _, f := range files {
		if strings.Contains(string(f.Content), "hello there") {
			t.Fatalf("%s publishes prompt text", f.Path)
		}
	}
}

func metaOf(t *testing.T, files []ghapi.File) (string, map[string]any) {
	t.Helper()
	f := files[len(files)-1]
	if !strings.HasSuffix(f.Path, "/meta.json") {
		t.Fatalf("last file %s", f.Path)
	}
	var m map[string]any
	if err := json.Unmarshal(f.Content, &m); err != nil {
		t.Fatal(err)
	}
	return string(f.Content), m
}

func TestMetaCountryIsOptInAndOnlyACountry(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 30, 0, time.UTC)
	m := Machine{ID: "m_abc", Label: "laptop", OS: "windows", Collector: "0.4.0", LastEventAt: time.Date(2026, 10, 4, 11, 58, 59, 0, time.UTC)}

	raw, meta := metaOf(t, Files(m, sampleRows(), AccountHistory{}, now))
	if _, ok := meta["cc"]; ok || strings.Contains(raw, `"cc"`) {
		t.Fatalf("country published without opting in: %s", raw)
	}
	if meta["schema"] != 2.0 || meta["firstSeenAt"] != "2026-09-30" || meta["lastEventAt"] != "2026-10-04T11:58:00Z" {
		t.Fatalf("meta %s", raw)
	}

	m.CC = "GB"
	if raw, meta = metaOf(t, Files(m, sampleRows(), AccountHistory{}, now)); meta["cc"] != "GB" {
		t.Fatalf("opted-in country missing: %s", raw)
	}
	again, _ := metaOf(t, Files(m, sampleRows(), AccountHistory{}, now))
	if again != raw {
		t.Fatal("meta not deterministic")
	}

	// Anything but a two-letter country (a zone, a Windows zone id, an
	// offset, lower case) is never published.
	for _, bad := range []string{"Europe/London", "GMT Standard Time", "+01:00", "UTC", "gb", "G", "ZZZ"} {
		m.CC = bad
		raw, _ := metaOf(t, Files(m, sampleRows(), AccountHistory{}, now))
		if strings.Contains(raw, `"cc"`) || strings.Contains(raw, bad) {
			t.Fatalf("published %q: %s", bad, raw)
		}
	}

	// An event stamped ahead of the clock is reported as now; none, omitted.
	m.CC, m.LastEventAt = "", now.Add(48*time.Hour)
	if _, meta = metaOf(t, Files(m, sampleRows(), AccountHistory{}, now)); meta["lastEventAt"] != "2026-10-04T12:00:00Z" {
		t.Fatalf("future last event %v", meta["lastEventAt"])
	}
	m.LastEventAt = time.Time{}
	if raw, _ = metaOf(t, Files(m, nil, AccountHistory{}, now)); strings.Contains(raw, "lastEventAt") || strings.Contains(raw, "firstSeenAt") {
		t.Fatalf("empty machine meta %s", raw)
	}
}

func fileAt(files []ghapi.File, suffix string) *ghapi.File {
	for i := range files {
		if strings.HasSuffix(files[i].Path, suffix) {
			return &files[i]
		}
	}
	return nil
}

// account-usage.json carries the newest account totals and the UTC-day ledger
// (read by "ledgerCols" names), sorted so it is deterministic in any input
// order, and is omitted when there is nothing to reconcile.
func TestAccountUsageFile(t *testing.T) {
	m := Machine{ID: "m_abc", Label: "laptop"}
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	at := time.Date(2026, 10, 4, 11, 30, 15, 999, time.FixedZone("x", 3600))
	h := AccountHistory{
		Snapshots: []rollup.AccountTotal{
			{Provider: "openai", Source: "codex", Acct: "a_2", Date: "2026-10-02", TotalTokens: 900, ObservedAt: at},
			{Provider: "openai", Source: "codex", Acct: "a_1", Date: "2026-10-02", TotalTokens: 800, ObservedAt: at},
			{Provider: "openai", Source: "codex", Acct: "a_1", Date: "2026-10-01", TotalTokens: 700, ObservedAt: at},
		},
		Ledger: []rollup.LedgerRow{
			{Date: "2026-10-02", Provider: "openai", Source: "codex", Acct: "", Tokens: 30},
			{Date: "2026-10-01", Provider: "openai", Source: "codex", Acct: "a_1", Tokens: 600},
			{Date: "2026-10-01", Provider: "openai", Source: "codex", Acct: "a_2", Tokens: 0},
		},
	}
	f := fileAt(Files(m, sampleRows(), h, now), "/account-usage.json")
	if f == nil || f.Path != "data/machines/m_abc/account-usage.json" {
		t.Fatalf("no account-usage.json: %v", f)
	}
	var got struct {
		Schema     int               `json:"schema"`
		Machine    string            `json:"machine"`
		Snapshots  []AccountSnapshot `json:"snapshots"`
		LedgerCols []string          `json:"ledgerCols"`
		Ledger     [][]any           `json:"ledger"`
	}
	if err := json.Unmarshal(f.Content, &got); err != nil {
		t.Fatal(err)
	}
	if got.Schema != Schema || got.Machine != "m_abc" || strings.Join(got.LedgerCols, ",") != "date,provider,source,acct,tokens" {
		t.Fatalf("header %+v", got)
	}
	order := []string{}
	for _, s := range got.Snapshots {
		order = append(order, s.Date+"/"+s.Acct)
		if s.ObservedAt != "2026-10-04T10:30:15Z" || s.Provider != "openai" || s.Source != "codex" {
			t.Fatalf("snapshot %+v", s)
		}
	}
	if strings.Join(order, ",") != "2026-10-01/a_1,2026-10-02/a_1,2026-10-02/a_2" {
		t.Fatalf("snapshot order %v", order)
	}
	at0 := map[string]int{}
	for i, c := range got.LedgerCols {
		at0[c] = i
	}
	if len(got.Ledger) != 2 { // the row without tokens is left out
		t.Fatalf("ledger %v", got.Ledger)
	}
	first, second := got.Ledger[0], got.Ledger[1]
	if first[at0["date"]] != "2026-10-01" || first[at0["acct"]] != "a_1" || first[at0["tokens"]] != 600.0 ||
		second[at0["date"]] != "2026-10-02" || second[at0["acct"]] != "" || second[at0["tokens"]] != 30.0 {
		t.Fatalf("ledger rows %v", got.Ledger)
	}

	// Any input order publishes the same bytes.
	shuffled := AccountHistory{Snapshots: []rollup.AccountTotal{h.Snapshots[2], h.Snapshots[0], h.Snapshots[1]},
		Ledger: []rollup.LedgerRow{h.Ledger[1], h.Ledger[2], h.Ledger[0]}}
	if again := fileAt(Files(m, sampleRows(), shuffled, now), "/account-usage.json"); string(again.Content) != string(f.Content) {
		t.Fatalf("not deterministic:\n%s\n%s", f.Content, again.Content)
	}
	// A ledger alone (a signed-in machine whose totals another machine
	// reads) is published; nothing at all is not.
	if only := fileAt(Files(m, nil, AccountHistory{Ledger: h.Ledger[:1]}, now), "/account-usage.json"); only == nil || !strings.Contains(string(only.Content), `"snapshots": []`) {
		t.Fatalf("ledger-only file %v", only)
	}
	if none := fileAt(Files(m, nil, AccountHistory{Ledger: h.Ledger[2:]}, now), "/account-usage.json"); none != nil {
		t.Fatalf("published an empty account-usage.json: %s", none.Content)
	}
	if strings.Contains(string(f.Content), "unledgered") {
		t.Fatalf("unledgered without such dates: %s", f.Content)
	}
	// Dates an older rollup filled in are listed once, in order.
	h.Unledgered = []string{"2025-01-15", "2024-12-31", "2025-01-15"}
	var withGaps struct {
		Unledgered []string `json:"unledgered"`
	}
	json.Unmarshal(fileAt(Files(m, sampleRows(), h, now), "/account-usage.json").Content, &withGaps)
	if strings.Join(withGaps.Unledgered, ",") != "2024-12-31,2025-01-15" {
		t.Fatalf("unledgered %v", withGaps.Unledgered)
	}
}

// A deletion is committed only for a path this machine published, and a path
// that is gone already (removed by hand) never blocks the rest.
func TestPublishDeletesOnlyPublishedPaths(t *testing.T) {
	f, c := setup(t)
	ctx := context.Background()
	m := Machine{ID: "m_abc", Label: "laptop"}
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	h := AccountHistory{Ledger: []rollup.LedgerRow{{Date: "2026-10-01", Provider: "openai", Source: "codex", Tokens: 5}}}
	published := map[string]string{}
	ch, err := Publish(ctx, c, "octo/agent-usage", "main", m.Label, Files(m, sampleRows(), h, now), published, false)
	if err != nil || ch[AccountUsagePath(m.ID)] == "" {
		t.Fatalf("first publish: %v %v", ch, err)
	}
	maps.Copy(published, ch)

	gone := ghapi.File{Path: AccountUsagePath(m.ID), Delete: true}
	files := append(Files(m, sampleRows(), AccountHistory{}, now), gone)
	ch, err = Publish(ctx, c, "octo/agent-usage", "main", m.Label, files, published, false)
	if v, ok := ch[gone.Path]; err != nil || !ok || v != "" {
		t.Fatalf("delete: %v %v", ch, err)
	}
	if _, ok := f.Files[gone.Path]; ok {
		t.Fatal("account-usage.json not deleted")
	}
	delete(published, gone.Path)
	// Not published (any more): no commit.
	head := f.Head
	if ch, err := Publish(ctx, c, "octo/agent-usage", "main", m.Label, files, published, false); err != nil || ch != nil || f.Head != head {
		t.Fatalf("deleted again: %v %v", ch, err)
	}
	// Recorded as published but gone from the repository: the rest commits.
	published[gone.Path] = "old"
	rows := sampleRows()
	rows[0].In++
	ch, err = Publish(ctx, c, "octo/agent-usage", "main", m.Label, append(Files(m, rows, AccountHistory{}, now), gone), published, false)
	if err != nil || ch[gone.Path] != "" || f.Head == head {
		t.Fatalf("delete of a missing path: %v %v", ch, err)
	}
	// One path gone, another still there: that one is deleted all the same.
	there := QuotaPath(m.ID)
	f.Files[there] = "{}"
	published[gone.Path], published[there] = "old", ""
	ch, err = Publish(ctx, c, "octo/agent-usage", "main", m.Label, append(Files(m, rows, AccountHistory{}, now), gone), published, true)
	if _, ok := f.Files[there]; err != nil || ok || ch[there] != "" || ch[gone.Path] != "" {
		t.Fatalf("delete beside a missing path: %v %v", ch, err)
	}
}

func TestStaleQuota(t *testing.T) {
	got := StaleQuota([]string{"data/machines/m_a/quota.json", "data/machines/m_a/meta.json", "data/machines/m_b/quota.json",
		"data/quota.json", "data/machines/m_c/x/quota.json", "pages/quota.json"})
	if strings.Join(got, ",") != "data/machines/m_a/quota.json,data/machines/m_b/quota.json" {
		t.Fatalf("stale %v", got)
	}
}
