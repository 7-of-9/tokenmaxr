package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/accountusage"
	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
	"github.com/7-of-9/tokenmaxr/collector/internal/ghapi"
	"github.com/7-of-9/tokenmaxr/collector/internal/ghapi/ghapitest"
	"github.com/7-of-9/tokenmaxr/collector/internal/ghpub"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/rollup"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

// useFakeGitHub points the GitHub publisher at an in-memory GitHub.
func useFakeGitHub(t *testing.T) (*ghapitest.Fake, *bool) {
	f := ghapitest.New()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	exists := false
	prevClient, prevExists, prevPoll, prevSleep, prevID := newGitHubClient, repoExists, githubPoll, githubDeviceSleep, buildinfo.GitHubClientID
	newGitHubClient = func(token, _ string) *ghapi.Client {
		c := ghapi.New(token, "tokenmaxr-test")
		c.API, c.Web = srv.URL, srv.URL
		return c
	}
	repoExists = func(context.Context, string) bool { return exists }
	githubPoll = time.Millisecond
	githubDeviceSleep = func(context.Context, time.Duration) error { return nil }
	buildinfo.GitHubClientID = "Iv-test"
	t.Cleanup(func() {
		newGitHubClient, repoExists, githubPoll, githubDeviceSleep, buildinfo.GitHubClientID = prevClient, prevExists, prevPoll, prevSleep, prevID
	})
	return f, &exists
}

type loginUI struct {
	mu     sync.Mutex
	code   string
	steps  []string
	onStep func(text string)
}

func (u *loginUI) Code(code, _ string) { u.mu.Lock(); u.code = code; u.mu.Unlock() }
func (u *loginUI) Step(text, _ string) {
	u.mu.Lock()
	u.steps = append(u.steps, text)
	u.mu.Unlock()
	u.onStep(text)
}
func (u *loginUI) Progress(string) {}

func TestGitHubLoginGuidesThroughEveryStep(t *testing.T) {
	f, exists := useFakeGitHub(t)
	a, _, _ := newTestApp(t)
	opened := []string{}
	a.OpenURL = func(u string) error { opened = append(opened, u); return nil }
	f.PollPlan = []string{"pending", "ok"}
	f.NoInstall = true
	ui := &loginUI{}
	ui.onStep = func(text string) {
		// The user does what each step asks, on GitHub.
		switch {
		case strings.HasPrefix(text, "Create"):
			*exists = true
		case strings.HasPrefix(text, "Install"):
			f.NoInstall = false
			f.Files[ghpub.MarkerFile] = `{"tokenmaxr":1}`
		}
	}
	res, err := a.GitHubLogin(context.Background(), ui, "")
	if err != nil {
		t.Fatal(err)
	}
	if ui.code != "ABCD-1234" || len(ui.steps) != 2 || !strings.HasPrefix(ui.steps[0], "Create") || !strings.HasPrefix(ui.steps[1], "Install") {
		t.Fatalf("code %q steps %q", ui.code, ui.steps)
	}
	if len(opened) != 3 || !strings.Contains(opened[0], "/login/device") || !strings.Contains(opened[1], "template_name=") || !strings.Contains(opened[2], "/installations/new") {
		t.Fatalf("browser opened %v", opened)
	}
	if res.Repo != "octo/agent-usage" || res.Login != "octo" || !res.NewFleet || res.Rehash || res.PagesURL == "" {
		t.Fatalf("result %+v", res)
	}
	sec, _ := store.LoadSecrets(a.Home)
	if sec.GitHub == nil || sec.GitHub.Token != "ghu_test" || sec.GitHub.UserID != 42 || !sec.HasFleet() || sec.Enrolled() {
		t.Fatalf("secrets %+v", sec.GitHub)
	}
	if f.Vars[ghpub.FleetVariable] != sec.K {
		t.Fatal("the fleet key on GitHub must be this machine's key")
	}
	cfg, _ := store.LoadConfig(a.Home)
	if cfg.GitHub == nil || cfg.GitHub.Repo != "octo/agent-usage" || !strings.HasPrefix(cfg.GitHub.Label, "machine-") || cfg.GitHub.Label == cfg.MachineLabel {
		t.Fatalf("config %+v (the public label must not default to the hostname)", cfg.GitHub)
	}
	st, _ := store.LoadState(a.Home)
	if !strings.HasPrefix(st.GitHub.MachineID, "m_") || !f.Pages {
		t.Fatalf("state %+v pages %v", st.GitHub, f.Pages)
	}

	// Signing in again (or on a second machine) needs no steps.
	ui2 := &loginUI{onStep: func(string) { t.Fatal("no step expected once set up") }}
	f.PollPlan = []string{"ok"}
	f.Polls = 0
	if _, err := a.GitHubLogin(context.Background(), ui2, "studio"); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := store.LoadConfig(a.Home); cfg.GitHub.Label != "studio" {
		t.Fatalf("label %q", cfg.GitHub.Label)
	}
}

func TestGitHubLoginCancelledOnGitHub(t *testing.T) {
	f, _ := useFakeGitHub(t)
	a, _, _ := newTestApp(t)
	a.OpenURL = func(string) error { return nil }
	f.PollPlan = []string{"denied"}
	if _, err := a.GitHubLogin(context.Background(), &loginUI{onStep: func(string) {}}, ""); err != ghapi.ErrDenied {
		t.Fatalf("denied: %v", err)
	}
	if sec, _ := store.LoadSecrets(a.Home); sec.GitHub != nil {
		t.Fatal("a cancelled sign-in must save nothing")
	}
}

// githubOnly configures a machine that publishes to GitHub with no server.
func githubOnly(t *testing.T, a *App, f *ghapitest.Fake) {
	k := make([]byte, 32)
	rand.Read(k)
	if err := store.SaveSecrets(a.Home, store.Secrets{K: base64.StdEncoding.EncodeToString(k), GitHub: &store.GitHubSecrets{Token: "ghu_test", Login: "octo", UserID: 42}}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := store.LoadConfig(a.Home)
	cfg.GitHub = &store.GitHubConfig{Repo: "octo/agent-usage", Label: "laptop"}
	if err := store.SaveConfig(a.Home, cfg); err != nil {
		t.Fatal(err)
	}
	f.Files[ghpub.MarkerFile] = `{"tokenmaxr":1}`
}

func TestGitHubOnlyMachineCollectsAndPublishesWithoutAServer(t *testing.T) {
	f, _ := useFakeGitHub(t)
	a, _, _ := newTestApp(t)
	githubOnly(t, a, f)
	ctx := context.Background()

	rep, err := a.Tick(ctx, TickOptions{})
	if err != nil {
		t.Fatalf("a GitHub-only machine must tick: %v", err)
	}
	if rep.UploadSkipped != "no server destination" {
		t.Fatalf("upload skipped %q", rep.UploadSkipped)
	}
	if entries, _ := os.ReadDir(paths.Outbox(a.Home)); len(entries) != 0 {
		t.Fatalf("nothing may be queued for a server that does not exist: %d files", len(entries))
	}
	ru, err := rollup.Load(paths.Rollup(a.Home))
	if err != nil {
		t.Fatal(err)
	}
	var tok, prompts int64
	for _, r := range ru.Rows() {
		tok += r.Tokens()
		prompts += r.Prompts
	}
	if tok != 16 || prompts != 2 {
		t.Fatalf("rollup tokens %d prompts %d, want 16 and 2 (growing re-reads merged once)", tok, prompts)
	}
	st, _ := store.LoadState(a.Home)
	dir := ghpub.MachineDir(st.GitHub.MachineID)
	var usage string
	for p, c := range f.Files {
		if strings.HasPrefix(p, dir+"/usage-") {
			usage = c
		}
	}
	if usage == "" || f.Files[dir+"/meta.json"] == "" || st.GitHub.LastPublish.IsZero() || st.GitHub.LastError != "" {
		t.Fatalf("not published: files %v state %+v", keys(f.Files), st.GitHub)
	}
	var uf struct {
		Rows [][]any `json:"rows"`
	}
	json.Unmarshal([]byte(usage), &uf)
	if len(uf.Rows) == 0 {
		t.Fatal("usage rows missing")
	}
	if strings.Contains(f.Files[dir+"/meta.json"], cfgHost(t, a)) {
		t.Fatal("the hostname must never be published")
	}

	// Within the publish interval nothing is attempted again...
	head := f.Head
	before := st.GitHub.LastAttempt
	if _, err := a.Tick(ctx, TickOptions{}); err != nil {
		t.Fatal(err)
	}
	if st2, _ := store.LoadState(a.Home); !st2.GitHub.LastAttempt.Equal(before) || f.Head != head {
		t.Fatal("published again before the interval")
	}
	// ...and a forced sync with unchanged data commits nothing.
	if _, err := a.Tick(ctx, TickOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if f.Head != head {
		t.Fatal("an unchanged forced publish made a commit")
	}
}

func TestGitHubPublishFailureIsRecordedNotFatal(t *testing.T) {
	f, _ := useFakeGitHub(t)
	a, _, _ := newTestApp(t)
	githubOnly(t, a, f)
	cfg, _ := store.LoadConfig(a.Home)
	cfg.GitHub.Repo = "octo/not-granted"
	store.SaveConfig(a.Home, cfg)
	if _, err := a.Tick(context.Background(), TickOptions{}); err != nil {
		t.Fatalf("a publishing failure must not fail the tick: %v", err)
	}
	st, _ := store.LoadState(a.Home)
	if !strings.Contains(st.GitHub.LastError, "cannot write to the repository") {
		t.Fatalf("last error %q", st.GitHub.LastError)
	}
}

func TestServerAndGitHubTogether(t *testing.T) {
	f, _ := useFakeGitHub(t)
	api, srv := newFakeAPI(t)
	a, _, out := newTestApp(t)
	if err := a.Install(context.Background(), InstallOptions{Join: "D0M1-both", Endpoint: srv.URL, Label: "STUDIO", Yes: true, NoAutostart: true}); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	sec, _ := store.LoadSecrets(a.Home)
	sec.GitHub = &store.GitHubSecrets{Token: "ghu_test", Login: "octo", UserID: 42}
	store.SaveSecrets(a.Home, sec)
	cfg, _ := store.LoadConfig(a.Home)
	cfg.GitHub = &store.GitHubConfig{Repo: "octo/agent-usage", Label: "laptop"}
	store.SaveConfig(a.Home, cfg)
	f.Files[ghpub.MarkerFile] = `{"tokenmaxr":1}`

	if _, err := a.Tick(context.Background(), TickOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	sent := len(api.usage)
	api.mu.Unlock()
	st, _ := store.LoadState(a.Home)
	if sent != 2 || f.Files[ghpub.MachineDir(st.GitHub.MachineID)+"/meta.json"] == "" {
		t.Fatalf("server got %d usage events, GitHub files %v", sent, keys(f.Files))
	}
}

func keys(m map[string]string) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	return out
}

func cfgHost(t *testing.T, a *App) string {
	cfg, _ := store.LoadConfig(a.Home)
	if cfg.MachineLabel == "" {
		t.Fatal("test needs a machine label")
	}
	return cfg.MachineLabel
}

// Signing in to GitHub on a machine that already uploads to a server
// rebuilds the dashboard totals from all history without sending that
// history to the server again.
func TestGitHubRebuildDoesNotReuploadToTheServer(t *testing.T) {
	f, _ := useFakeGitHub(t)
	api, srv := newFakeAPI(t)
	a, _, out := newTestApp(t)
	ctx := context.Background()
	if err := a.Install(ctx, InstallOptions{Join: "D0M1-rb", Endpoint: srv.URL, Label: "STUDIO", Yes: true, NoAutostart: true}); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if _, err := a.Tick(ctx, TickOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	before := api.ingests
	api.mu.Unlock()

	// What GitHubLogin leaves behind on such a machine.
	sec, _ := store.LoadSecrets(a.Home)
	sec.GitHub = &store.GitHubSecrets{Token: "ghu_test", Login: "octo", UserID: 42}
	store.SaveSecrets(a.Home, sec)
	cfg, _ := store.LoadConfig(a.Home)
	cfg.GitHub = &store.GitHubConfig{Repo: "octo/agent-usage", Label: "laptop"}
	store.SaveConfig(a.Home, cfg)
	st, _ := store.LoadState(a.Home)
	st.GitHub.StartRebuild()
	store.SaveState(a.Home, st)
	f.Files[ghpub.MarkerFile] = `{"tokenmaxr":1}`

	rep, err := a.Tick(ctx, TickOptions{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	after := api.ingests
	api.mu.Unlock()
	if rep.Queued != 0 || after != before {
		t.Fatalf("history sent to the server again: queued %d, ingests %d -> %d", rep.Queued, before, after)
	}
	st, _ = store.LoadState(a.Home)
	ru, _ := rollup.Load(paths.Rollup(a.Home))
	if st.GitHub.Rebuild || len(ru.Rows()) == 0 || f.Files[ghpub.MachineDir(st.GitHub.MachineID)+"/meta.json"] == "" {
		t.Fatalf("rebuild %v, %d rollup rows", st.GitHub.Rebuild, len(ru.Rows()))
	}
}

// usageRows reads every published usage row of this machine by column name.
func usageRows(t *testing.T, f *ghapitest.Fake, dir string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for p, c := range f.Files {
		if !strings.HasPrefix(p, dir+"/usage-") {
			continue
		}
		var uf struct {
			Schema int      `json:"schema"`
			Cols   []string `json:"cols"`
			Rows   [][]any  `json:"rows"`
		}
		if err := json.Unmarshal([]byte(c), &uf); err != nil || uf.Schema != ghpub.Schema {
			t.Fatalf("%s: schema %d %v", p, uf.Schema, err)
		}
		for _, r := range uf.Rows {
			m := map[string]any{}
			for i, col := range uf.Cols {
				m[col] = r[i]
			}
			out = append(out, m)
		}
	}
	return out
}

func sumCol(rows []map[string]any, col string) (n float64) {
	for _, r := range rows {
		n += r[col].(float64)
	}
	return
}

// Schema 2 on a real tick: prompts per model and the meta fields, with the
// country published only once the owner opts in, and never the zone.
func TestGitHubPublishesSchema2AndTheCountryOnlyOnOptIn(t *testing.T) {
	f, _ := useFakeGitHub(t)
	a, _, _ := newTestApp(t)
	githubOnly(t, a, f)
	prev := machineCountry
	machineCountry = func() string { return "GB" }
	t.Cleanup(func() { machineCountry = prev })
	ctx := context.Background()

	if _, err := a.Tick(ctx, TickOptions{}); err != nil {
		t.Fatal(err)
	}
	st, _ := store.LoadState(a.Home)
	dir := ghpub.MachineDir(st.GitHub.MachineID)
	rows := usageRows(t, f, dir)
	if sumCol(rows, "prompts") != 2 || sumCol(rows, "modelPrompts") != 2 || sumCol(rows, "promptsNoUsage") != 0 || sumCol(rows, "events") != 2 {
		t.Fatalf("rows %v", rows)
	}
	for p, c := range f.Files {
		if strings.Contains(c, "synthetic prompt") {
			t.Fatalf("%s publishes prompt text", p)
		}
	}
	var meta map[string]any
	json.Unmarshal([]byte(f.Files[dir+"/meta.json"]), &meta)
	if _, ok := meta["cc"]; ok {
		t.Fatalf("country published without opting in: %v", meta)
	}
	if meta["schema"] != 2.0 || meta["firstSeenAt"] == nil || meta["lastEventAt"] == nil {
		t.Fatalf("meta %v", meta)
	}

	// Opting in (as the settings page does) republishes meta with the country.
	cfg, _ := store.LoadConfig(a.Home)
	cfg.GitHub.ShowCountry = true
	store.SaveConfig(a.Home, cfg)
	st.GitHub.LastPublish = time.Time{}
	store.SaveState(a.Home, st)
	if _, err := a.Tick(ctx, TickOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	raw := f.Files[dir+"/meta.json"]
	json.Unmarshal([]byte(raw), &meta)
	if meta["cc"] != "GB" {
		t.Fatalf("opted-in meta %s", raw)
	}
	tz := machineTZ()
	for _, private := range []string{tz.IANA, tz.WindowsID} {
		if private != "" && strings.Contains(raw, private) {
			t.Fatalf("meta publishes the time zone %q", private)
		}
	}
}

// A rollup written by the previous release is rebuilt from all history, and
// published as schema 2 only once the rebuild is complete. Nothing it held is
// lost: a date whose logs are gone keeps the old file's totals (and is marked
// unledgered), while dates the logs still hold are rebuilt, counted once.
func TestGitHubOldRollupRebuildsBeforePublishing(t *testing.T) {
	f, _ := useFakeGitHub(t)
	a, _, _ := newTestApp(t)
	githubOnly(t, a, f)
	ctx := context.Background()
	if _, err := a.Tick(ctx, TickOptions{}); err != nil {
		t.Fatal(err)
	}
	st, _ := store.LoadState(a.Home)
	dir := ghpub.MachineDir(st.GitHub.MachineID)
	before := usageRows(t, f, dir)
	ru, _ := rollup.Load(paths.Rollup(a.Home))

	// The file as the previous release kept it: version 1 cells (in, cacheW,
	// cacheR, out, events, prompts under a 4-part key) for every date it
	// counted, including one whose logs have since been deleted.
	const gone = "2025-01-15"
	days := map[string]map[string][6]int64{gone: {"anthropic|claude-code|claude-x|a_1": {1000, 0, 500, 40, 3, 12}}}
	for _, r := range ru.Rows() {
		if days[r.Date] == nil {
			days[r.Date] = map[string][6]int64{}
		}
		days[r.Date][r.Provider+"|"+r.Source+"|"+r.Model+"|"+r.Acct] = [6]int64{r.In, r.CacheW, r.CacheR, r.Out, r.Events, r.Prompts}
	}
	v1, _ := json.Marshal(map[string]any{"v": 1, "days": days})
	os.WriteFile(paths.Rollup(a.Home), v1, 0o600)
	if _, err := a.Tick(ctx, TickOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	st, _ = store.LoadState(a.Home)
	if st.GitHub.Rebuild {
		t.Fatal("the rebuild did not finish")
	}
	ru, err := rollup.Load(paths.Rollup(a.Home))
	if err != nil {
		t.Fatalf("rebuilt rollup: %v", err)
	}
	// The dates the logs hold: exactly what was published before (re-read
	// once, never twice, and never added to the old file's totals).
	var in, out, events, prompts, modelPrompts float64
	for _, r := range ru.Rows() {
		if r.Date != gone {
			in, out, events = in+float64(r.In), out+float64(r.Out), events+float64(r.Events)
			prompts, modelPrompts = prompts+float64(r.Prompts), modelPrompts+float64(r.ModelPrompts)
		}
	}
	got := map[string]float64{"in": in, "out": out, "events": events, "prompts": prompts, "modelPrompts": modelPrompts}
	for col, v := range got {
		if v != sumCol(before, col) || v == 0 {
			t.Fatalf("%s: %v after the rebuild, %v before", col, v, sumCol(before, col))
		}
	}
	// The deleted logs' date keeps the old totals and is published.
	c := ru.Days[gone][rollup.Key{Provider: "anthropic", Source: "claude-code", Model: "claude-x", Acct: "a_1"}.String()]
	if c == nil || c.In != 1000 || c.CacheR != 500 || c.Out != 40 || c.Events != 3 || c.Prompts != 12 {
		t.Fatalf("the old file's %s: %+v", gone, c)
	}
	if u := ru.Unledgered(); len(u) != 1 || u[0] != (rollup.DaySource{Date: gone, Provider: "anthropic", Source: "claude-code"}) {
		t.Fatalf("unledgered %+v", u)
	}
	after := usageRows(t, f, dir)
	if sumCol(after, "in") != sumCol(before, "in")+1000 || sumCol(after, "prompts") != sumCol(before, "prompts")+12 {
		t.Fatalf("published in %v prompts %v, before %v %v", sumCol(after, "in"), sumCol(after, "prompts"), sumCol(before, "in"), sumCol(before, "prompts"))
	}
	if _, ok := f.Files[dir+"/usage-2025-01.json"]; !ok {
		t.Fatalf("the old date's month was not published: %v", keys(f.Files))
	}
}

// While a rebuild is under way, its partial totals are never published over
// the complete ones.
func TestGitHubDoesNotPublishHalfARebuild(t *testing.T) {
	f, _ := useFakeGitHub(t)
	a, _, _ := newTestApp(t)
	githubOnly(t, a, f)
	ctx := context.Background()
	if _, err := a.Tick(ctx, TickOptions{}); err != nil {
		t.Fatal(err)
	}
	partial := rollup.New()
	partial.AddUsage(model.UsageEvent{ID: "p1", Provider: "anthropic", Source: "claude-code", TS: time.Now().Add(-time.Hour), Tokens: model.Tokens{In: 1}}, time.Now())
	cfg, _ := store.LoadConfig(a.Home)
	sec, _ := store.LoadSecrets(a.Home)
	st, _ := store.LoadState(a.Home)
	st.GitHub.StartRebuild()
	head, attempt := f.Head, st.GitHub.LastAttempt
	a.publishGitHub(ctx, &cfg, sec, st, partial, nil, true)
	if f.Head != head || !st.GitHub.LastAttempt.Equal(attempt) {
		t.Fatal("published part of a rebuild")
	}
	st.GitHub.Rebuild = false // the guard is what held it back
	a.publishGitHub(ctx, &cfg, sec, st, partial, nil, true)
	if f.Head == head {
		t.Fatal("the partial rollup should differ from what was published")
	}
}

// codexAccountReader signs a into Codex and gives it a fake account-history
// reader returning one total of *total for yesterday (UTC), or *fail's error.
// It returns the reader's call count.
func codexAccountReader(a *App, total *int64, fail *error) *int {
	os.MkdirAll(a.CodexHome, 0o700)
	os.WriteFile(a.CodexHome+"/auth.json", []byte(`{"tokens":{"account_id":"account-one"}}`), 0o600)
	calls := new(int)
	a.ReadAccountUsage = func(_ context.Context, _, _ string, key []byte, now time.Time) ([]model.AccountUsageSnapshot, error) {
		*calls++
		if fail != nil && *fail != nil {
			return nil, *fail
		}
		acct := accountusage.Account(a.UserHome, a.CodexHome, key)
		return []model.AccountUsageSnapshot{{ID: "day-1", Provider: model.ProviderOpenAI, Source: model.SourceCodex, Acct: acct,
			AcctQ: model.AcctRecorded, Date: now.UTC().AddDate(0, 0, -1).Format("2006-01-02"), Timezone: "UTC", TotalTokens: *total, ObservedAt: now}}, nil
	}
	return calls
}

// showAccountHistory sets the account-history opt-in in config.json.
func showAccountHistory(t *testing.T, a *App, on bool) {
	t.Helper()
	cfg, _ := store.LoadConfig(a.Home)
	cfg.GitHub.ShowAccountHistory = on
	if err := store.SaveConfig(a.Home, cfg); err != nil {
		t.Fatal(err)
	}
}

// A GitHub-only machine (no server outbox) that opted in reads account
// history on the same schedule and publishes it as account-usage.json;
// nothing is marked as sent to a server.
func TestGitHubPublishesAccountHistoryWithoutAServer(t *testing.T) {
	f, _ := useFakeGitHub(t)
	a, _, _ := newTestApp(t)
	githubOnly(t, a, f)
	showAccountHistory(t, a, true)
	total := int64(5000)
	calls := codexAccountReader(a, &total, nil)
	ctx := context.Background()
	if _, err := a.Tick(ctx, TickOptions{}); err != nil {
		t.Fatal(err)
	}
	st, _ := store.LoadState(a.Home)
	if *calls != 1 || st.AccountHistory.LastSuccess.IsZero() || len(st.AccountHistory.Queued) != 0 {
		t.Fatalf("reader calls %d, history %+v", *calls, st.AccountHistory)
	}
	dir := ghpub.MachineDir(st.GitHub.MachineID)
	raw, ok := f.Files[dir+"/account-usage.json"]
	if !ok {
		t.Fatalf("no account-usage.json in %v", keys(f.Files))
	}
	var file struct {
		Snapshots  []ghpub.AccountSnapshot `json:"snapshots"`
		LedgerCols []string                `json:"ledgerCols"`
		Ledger     [][]any                 `json:"ledger"`
	}
	if err := json.Unmarshal([]byte(raw), &file); err != nil || len(file.Snapshots) != 1 || file.Snapshots[0].TotalTokens != 5000 || file.LedgerCols == nil || file.Ledger == nil {
		t.Fatalf("account-usage.json %s (%v)", raw, err)
	}
	if strings.Contains(raw, "account-one") || strings.Contains(raw, "day-1") {
		t.Fatalf("published the raw account id or snapshot id: %s", raw)
	}
	// The local ledger publishes only the account-history sources (this
	// machine's synthetic Claude Code tokens are not among them).
	if strings.Contains(raw, model.SourceClaudeCode) {
		t.Fatalf("ledger of another source published: %s", raw)
	}

	// The reader keeps its 15-minute schedule; a changed total republishes.
	if _, err := a.Tick(ctx, TickOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if *calls != 1 {
		t.Fatalf("reader ran again before its schedule: %d", *calls)
	}
	ru, _ := rollup.Load(paths.Rollup(a.Home))
	if got := ru.AccountUsage(); len(got) != 1 || got[0].TotalTokens != 5000 {
		t.Fatalf("rollup totals %+v", got)
	}
	total = 6000
	st, _ = store.LoadState(a.Home)
	st.AccountHistory.LastAttempt = time.Now().Add(-accountUsageEvery)
	store.SaveState(a.Home, st)
	if _, err := a.Tick(ctx, TickOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if *calls != 2 || !strings.Contains(f.Files[dir+"/account-usage.json"], `"totalTokens": 6000`) {
		t.Fatalf("changed total not republished (calls %d): %s", *calls, f.Files[dir+"/account-usage.json"])
	}
}

// Account history is opt-in: by default a GitHub-only machine neither reads
// it nor publishes account-usage.json (UTC days next to local dates reveal
// the time zone's offset). Opting out again deletes the published file.
func TestGitHubAccountHistoryIsOptIn(t *testing.T) {
	f, _ := useFakeGitHub(t)
	a, _, _ := newTestApp(t)
	githubOnly(t, a, f)
	total := int64(5000)
	calls := codexAccountReader(a, &total, nil)
	ctx := context.Background()
	if _, err := a.Tick(ctx, TickOptions{}); err != nil {
		t.Fatal(err)
	}
	st, _ := store.LoadState(a.Home)
	path := ghpub.AccountUsagePath(st.GitHub.MachineID)
	if _, ok := f.Files[path]; ok || *calls != 0 || f.Files[ghpub.MachineDir(st.GitHub.MachineID)+"/meta.json"] == "" {
		t.Fatalf("account history without opting in: reader calls %d, files %v", *calls, keys(f.Files))
	}

	showAccountHistory(t, a, true)
	if _, err := a.Tick(ctx, TickOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.Files[path], `"totalTokens": 5000`) || *calls != 1 {
		t.Fatalf("opted in: reader calls %d, %q", *calls, f.Files[path])
	}

	showAccountHistory(t, a, false)
	if _, err := a.Tick(ctx, TickOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	st, _ = store.LoadState(a.Home)
	if _, ok := f.Files[path]; ok || st.GitHub.Published[path] != "" || st.GitHub.LastError != "" {
		t.Fatalf("opted out: file still published (%v), state %+v", keys(f.Files), st.GitHub)
	}
	// Nothing is deleted again on later publishes, and a file someone has
	// removed by hand already never blocks publishing.
	head := f.Head
	if _, err := a.Tick(ctx, TickOptions{Force: true}); err != nil || f.Head != head {
		t.Fatalf("an unchanged publish after opting out committed (%v)", err)
	}
	st, _ = store.LoadState(a.Home)
	st.GitHub.Published[path] = "stale"
	st.GitHub.LastPublish = time.Time{}
	store.SaveState(a.Home, st)
	if _, err := a.Tick(ctx, TickOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if st, _ = store.LoadState(a.Home); st.GitHub.LastError != "" || st.GitHub.Published[path] != "" {
		t.Fatalf("deleting a file that is gone: %+v", st.GitHub)
	}
}

// A rebuild (here: the rollup of an older release) starts without the account
// totals; they are read again in the same tick, and while they cannot be read
// the published account-usage.json is left as it was, never withdrawn.
func TestGitHubRebuildKeepsThePublishedAccountHistory(t *testing.T) {
	f, _ := useFakeGitHub(t)
	a, _, _ := newTestApp(t)
	githubOnly(t, a, f)
	showAccountHistory(t, a, true)
	total := int64(5000)
	var fail error
	calls := codexAccountReader(a, &total, &fail)
	ctx := context.Background()
	if _, err := a.Tick(ctx, TickOptions{}); err != nil {
		t.Fatal(err)
	}
	st, _ := store.LoadState(a.Home)
	path := ghpub.AccountUsagePath(st.GitHub.MachineID)
	published := f.Files[path]
	if !strings.Contains(published, `"totalTokens": 5000`) {
		t.Fatalf("first publish %q", published)
	}

	// The reader fails right after the upgrade: the file stays as it was.
	fail = errors.New("account history unavailable")
	os.WriteFile(paths.Rollup(a.Home), []byte(`{"v":1,"days":{}}`), 0o600)
	if _, err := a.Tick(ctx, TickOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if *calls != 2 {
		t.Fatalf("the rebuild must read the account totals again at once: %d reads", *calls)
	}
	if st, _ = store.LoadState(a.Home); st.GitHub.Rebuild || f.Files[path] != published {
		t.Fatalf("rebuild %v; account history withdrawn: %q", st.GitHub.Rebuild, f.Files[path])
	}
	// The next successful read publishes it again (a changed total shows it).
	fail, total = nil, 7000
	st.AccountHistory.LastAttempt = time.Time{}
	store.SaveState(a.Home, st)
	if _, err := a.Tick(ctx, TickOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.Files[path], `"totalTokens": 7000`) {
		t.Fatalf("after a successful read: %q", f.Files[path])
	}
}

// Prompt records of accounts excluded from prompts are not counted per model:
// the server, which never gets them, does not count them either.
func TestExcludedAccountsPromptsAreNotCountedPerModel(t *testing.T) {
	a, _, _ := newTestApp(t)
	cfg := store.Config{PromptExcludeAccts: []string{"a_private"}}
	ru := rollup.New()
	ts := time.Now().Add(-time.Hour)
	b := sources.Batch{Prompts: []model.PromptRecord{
		{ID: "pp01", Provider: model.ProviderAnthropic, Source: model.SourceClaudeCode, TS: ts, Model: "claude-x", Acct: "a_private"},
		{ID: "pp02", Provider: model.ProviderAnthropic, Source: model.SourceClaudeCode, TS: ts, Model: "claude-x", Acct: "a_work"},
	}}
	if _, err := a.persist(nil, &cfg, nil, b, nil, ru); err != nil {
		t.Fatal(err)
	}
	var counted int64
	for _, r := range ru.Rows() {
		if r.Acct == "a_private" && r.ModelPrompts != 0 {
			t.Fatalf("excluded account counted: %+v", r)
		}
		counted += r.ModelPrompts
	}
	if counted != 1 {
		t.Fatalf("model prompts %d, want 1", counted)
	}
}

// countReads counts reads of path in the fake GitHub.
func countReads(f *ghapitest.Fake, path string) (n int) {
	for _, r := range f.Reads {
		if strings.HasPrefix(r, path+"@") || r == path {
			n++
		}
	}
	return n
}

func TestGitHubKeepsTheDashboardCurrent(t *testing.T) {
	f, _ := useFakeGitHub(t)
	a, _, _ := newTestApp(t)
	githubOnly(t, a, f)
	now := time.Now()
	a.Now = func() time.Time { return now }
	ctx := context.Background()
	// A repository made from the first template: its dashboard predates version.json.
	f.Files[".github/workflows/pages.yml"] = "name: dashboard\n"
	f.Files["site/index.html"] = `<script src="app.js"></script>`
	f.Files["site/app.js"] = "old"
	f.Files["site/style.css"] = "old"
	embedded, files, err := ghpub.Site()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := a.Tick(ctx, TickOptions{}); err != nil {
		t.Fatal(err)
	}
	st, _ := store.LoadState(a.Home)
	if st.GitHub.LastError != "" || st.GitHub.Site != embedded.Hash || !st.GitHub.SiteChecked.Equal(now) {
		t.Fatalf("state after the first publish: %+v", st.GitHub)
	}
	for _, fl := range files {
		if f.Files[fl.Path] != string(fl.Content) {
			t.Errorf("%s is not the embedded build", fl.Path)
		}
	}
	if _, ok := f.Files["site/app.js"]; ok {
		t.Fatal("the old dashboard's app.js must be deleted")
	}
	if f.Files[".github/workflows/pages.yml"] != "name: dashboard\n" || f.Files[ghpub.MarkerFile] != `{"tokenmaxr":1}` || f.Files[ghpub.MachineDir(st.GitHub.MachineID)+"/meta.json"] == "" {
		t.Fatalf("other files changed: %v", keys(f.Files))
	}

	// The next publishes do not read version.json again until a day has passed.
	reads, commits := countReads(f, ghpub.SiteVersionFile), f.Commits
	now = now.Add(time.Hour)
	if _, err := a.Tick(ctx, TickOptions{}); err != nil {
		t.Fatal(err)
	}
	if countReads(f, ghpub.SiteVersionFile) != reads || f.Commits != commits {
		t.Fatalf("checked the dashboard again within the day (%d reads, %d commits)", countReads(f, ghpub.SiteVersionFile)-reads, f.Commits-commits)
	}

	// A collector update carrying a newer build: checked at once, and the
	// files the new build no longer has are deleted.
	newer := embedded.BuiltAt.Add(time.Hour)
	nv := ghpub.SiteVersion{BuiltAt: newer}
	nfiles := []ghapi.File{{Path: "site/index.html", Content: []byte("<p>newer</p>")}}
	nv.Hash = ghpub.SiteHash(nfiles)
	vb, _ := json.Marshal(nv)
	nfiles = append(nfiles, ghapi.File{Path: ghpub.SiteVersionFile, Content: vb})
	prev := siteBuild
	t.Cleanup(func() { siteBuild = prev })
	siteBuild = func() (ghpub.SiteVersion, []ghapi.File, error) { return nv, nfiles, nil }
	now = now.Add(time.Hour)
	if _, err := a.Tick(ctx, TickOptions{}); err != nil {
		t.Fatal(err)
	}
	var site []string
	for p := range f.Files {
		if strings.HasPrefix(p, "site/") {
			site = append(site, p)
		}
	}
	if len(site) != 2 || f.Files["site/index.html"] != "<p>newer</p>" {
		t.Fatalf("newer build not in place: %v", site)
	}
	if st, _ := store.LoadState(a.Home); st.GitHub.Site != nv.Hash {
		t.Fatalf("site state %q", st.GitHub.Site)
	}

	// A machine still on the older release never downgrades it.
	// (A forced tick may still commit data that changed meanwhile: what
	// matters is that no site file moved.)
	siteBuild = prev
	now = now.Add(time.Hour)
	before := map[string]string{}
	for p, c := range f.Files {
		if strings.HasPrefix(p, "site/") {
			before[p] = c
		}
	}
	if _, err := a.Tick(ctx, TickOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	st, _ = store.LoadState(a.Home)
	after := map[string]string{}
	for p, c := range f.Files {
		if strings.HasPrefix(p, "site/") {
			after[p] = c
		}
	}
	if !maps.Equal(before, after) || st.GitHub.Site != embedded.Hash || st.GitHub.LastError != "" {
		t.Fatalf("older build downgraded the dashboard or failed: %v -> %v, %+v", keys(before), keys(after), st.GitHub)
	}

	// A failing update is recorded and retried with the next publish.
	siteBuild = func() (ghpub.SiteVersion, []ghapi.File, error) {
		return ghpub.SiteVersion{BuiltAt: newer.Add(time.Hour)}, []ghapi.File{{Path: "data/x.json"}}, nil
	}
	now = now.Add(time.Hour)
	if _, err := a.Tick(ctx, TickOptions{}); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.LoadState(a.Home); !strings.HasPrefix(st.GitHub.LastError, "dashboard update: ") || st.GitHub.Site != embedded.Hash {
		t.Fatalf("failure not recorded: %+v", st.GitHub)
	}
	if _, ok := f.Files["data/x.json"]; ok {
		t.Fatal("a file outside site/ was committed")
	}
}
