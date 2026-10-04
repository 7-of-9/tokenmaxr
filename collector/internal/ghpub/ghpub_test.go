package ghpub

import (
	"context"
	"encoding/base64"
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
	g, err := Discover(ctx, c, "tokenmaxor", "7-of-9/tokenmaxr-pages")
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
	g, _ = Discover(ctx, c, "tokenmaxor", "7-of-9/tokenmaxr-pages")
	if g.Ready() || g.Installation == nil || !strings.HasPrefix(g.InstallURL, "https://github.com/settings/installations/") {
		t.Fatalf("installed, unmarked: %+v", g)
	}

	f.Files[MarkerFile] = `{"tokenmaxr":1}`
	g, _ = Discover(ctx, c, "tokenmaxor", "7-of-9/tokenmaxr-pages")
	if !g.Ready() || g.Repo.FullName != "octo/agent-usage" {
		t.Fatalf("marked repo not found: %+v", g)
	}
	if g, _ = Discover(ctx, c, "another-app", "x/y"); g.Installation != nil {
		t.Fatal("an installation of a different App must not count")
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

func meters() []model.LimitSnapshot {
	used := 47.0
	reset := time.Date(2026, 10, 6, 15, 0, 0, 0, time.UTC)
	return []model.LimitSnapshot{{ID: "x", Provider: "anthropic", Source: "claude-code", Acct: "a_1", Window: "week", Plan: "Max (20x)",
		Label: "someone@example.com", Name: "Private Org", Detail: "detail", UsedPercent: &used, ResetsAt: &reset,
		ObservedAt: time.Date(2026, 10, 4, 11, 0, 0, 0, time.UTC), Status: "ok"}}
}

func TestFilesArePublicSafeAndDeterministic(t *testing.T) {
	m := Machine{ID: "m_abc", Label: "laptop", OS: "windows", Collector: "0.3.0"}
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	files := Files(m, sampleRows(), meters(), now)
	paths := []string{}
	all := ""
	for _, f := range files {
		paths = append(paths, f.Path)
		all += string(f.Content)
	}
	want := "data/machines/m_abc/usage-2026-09.json,data/machines/m_abc/usage-2026-10.json,data/machines/m_abc/quota.json,data/machines/m_abc/meta.json"
	if strings.Join(paths, ",") != want {
		t.Fatalf("paths %v", paths)
	}
	for _, private := range []string{"someone@example.com", "Private Org", "detail"} {
		if strings.Contains(all, private) {
			t.Fatalf("published files leak %q", private)
		}
	}
	for _, public := range []string{`"laptop"`, `"Max (20x)"`, `"claude-x"`, `"a_1"`} {
		if !strings.Contains(all, public) {
			t.Errorf("published files lack %s", public)
		}
	}
	again := Files(m, sampleRows(), meters(), now)
	for i := range files {
		if string(files[i].Content) != string(again[i].Content) {
			t.Fatalf("%s not deterministic", files[i].Path)
		}
	}
	if q := Files(m, nil, nil, now); len(q) != 1 || !strings.HasSuffix(q[0].Path, "/meta.json") {
		t.Fatalf("no data and no quota must publish only meta: %v", q)
	}
	old := meters()
	old[0].ObservedAt = now.Add(-MeterMaxAge - time.Hour)
	if q := Files(m, nil, old, now); !strings.Contains(string(q[0].Content), `"meters": []`) {
		t.Fatalf("a meter older than a week was published: %s", q[0].Content)
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

	ch, err := Publish(ctx, c, "octo/agent-usage", "main", m.Label, Files(m, sampleRows(), meters(), now), published, false)
	if err != nil || len(ch) != 4 || f.Files["data/machines/m_abc/meta.json"] == "" {
		t.Fatalf("first publish: %v %v", ch, err)
	}
	apply(ch)

	// Same data an hour later: nothing to commit (meta alone is not news).
	if ch, err := Publish(ctx, c, "octo/agent-usage", "main", m.Label, Files(m, sampleRows(), meters(), now.Add(time.Hour)), published, false); err != nil || ch != nil {
		t.Fatalf("unchanged publish committed: %v %v", ch, err)
	}
	// ...unless a daily "last seen" refresh is due.
	ch, err = Publish(ctx, c, "octo/agent-usage", "main", m.Label, Files(m, sampleRows(), meters(), now.Add(25*time.Hour)), published, true)
	if err != nil || len(ch) != 1 {
		t.Fatalf("meta refresh: %v %v", ch, err)
	}
	apply(ch)

	// A quota change commits quota.json and meta only, even after another
	// machine moved the branch once.
	q := meters()
	more := 60.0
	q[0].UsedPercent = &more
	f.MoveOnce = true
	ch, err = Publish(ctx, c, "octo/agent-usage", "main", m.Label, Files(m, sampleRows(), q, now.Add(26*time.Hour)), published, false)
	if err != nil || len(ch) != 2 || ch["data/machines/m_abc/quota.json"] == "" {
		t.Fatalf("change after conflict: %v %v", ch, err)
	}
	if !strings.Contains(f.Files["data/machines/m_abc/quota.json"], "60") {
		t.Fatal("quota change not committed")
	}
}

func TestNewMachineID(t *testing.T) {
	a, b := NewMachineID(), NewMachineID()
	if len(a) != 14 || !strings.HasPrefix(a, "m_") || a == b {
		t.Fatalf("ids %q %q", a, b)
	}
}
