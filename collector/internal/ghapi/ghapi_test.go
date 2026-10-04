package ghapi

import (
	"context"
	"github.com/7-of-9/tokenmaxr/collector/internal/ghapi/ghapitest"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func client(t *testing.T, f *ghapitest.Fake) *Client {
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := New("ghu_test", "tokenmaxr-test")
	c.API, c.Web = srv.URL, srv.URL
	return c
}

func noSleep(context.Context, time.Duration) error { return nil }

func TestDeviceFlowWaitsSlowsDownAndSucceeds(t *testing.T) {
	f := ghapitest.New()
	f.PollPlan = []string{"pending", "slow_down", "pending", "ok"}
	c := client(t, f)
	d, err := c.StartDevice(context.Background(), "Iv-test")
	if err != nil || d.UserCode != "ABCD-1234" || d.Interval != 5 {
		t.Fatalf("start: %+v %v", d, err)
	}
	var waits []time.Duration
	tok, err := c.PollDevice(context.Background(), "Iv-test", d, func(_ context.Context, w time.Duration) error { waits = append(waits, w); return nil })
	if err != nil || tok != "ghu_test" {
		t.Fatalf("poll: %q %v", tok, err)
	}
	if len(waits) != 4 || waits[0] != 5*time.Second || waits[2] != 10*time.Second {
		t.Fatalf("waits %v: slow_down must adopt GitHub's new interval", waits)
	}
	for _, a := range f.AuthSeen[:2] {
		if a != "" {
			t.Fatal("the OAuth endpoints must not receive a bearer token")
		}
	}
}

func TestDeviceFlowDeniedExpiredAndBadClient(t *testing.T) {
	for plan, want := range map[string]error{"denied": ErrDenied, "expired": ErrExpired} {
		f := ghapitest.New()
		f.PollPlan = []string{plan}
		c := client(t, f)
		if _, err := c.PollDevice(context.Background(), "Iv-test", DeviceCode{DeviceCode: "dev-1", Interval: 5}, noSleep); err != want {
			t.Errorf("%s: %v", plan, err)
		}
	}
	c := client(t, ghapitest.New())
	if _, err := c.StartDevice(context.Background(), "wrong"); err == nil || !strings.Contains(err.Error(), "Device Flow") {
		t.Fatalf("bad client id must explain itself: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.PollDevice(ctx, "Iv-test", DeviceCode{DeviceCode: "dev-1", Interval: 5}, nil); err == nil {
		t.Fatal("a cancelled sign-in must stop polling")
	}
}

func TestCommitWritesFilesInOneCommitAndReportsConflicts(t *testing.T) {
	f := ghapitest.New()
	c := client(t, f)
	ctx := context.Background()
	files := []File{{Path: "data/machines/m1/quota.json", Content: []byte(`{"a":1}`)}, {Path: "tokenmaxr.json", Content: []byte(`{"schema":1}`)}}
	sha, err := c.Commit(ctx, "octo/agent-usage", "main", "publish", files)
	if err != nil || sha == "c0" {
		t.Fatalf("commit: %s %v", sha, err)
	}
	if f.Files["tokenmaxr.json"] != `{"schema":1}` || f.Files["data/machines/m1/quota.json"] != `{"a":1}` {
		t.Fatalf("files %v", f.Files)
	}
	// Same content again: no new commit.
	again, err := c.Commit(ctx, "octo/agent-usage", "main", "publish", files)
	if err != nil || again != sha {
		t.Fatalf("no-op publish made a commit: %s %v", again, err)
	}
	f.MoveOnce = true
	if _, err := c.Commit(ctx, "octo/agent-usage", "main", "publish", []File{{Path: "x.json", Content: []byte("1")}}); err != ErrConflict {
		t.Fatalf("moved branch: %v", err)
	}
	got, err := c.GetFile(ctx, "octo/agent-usage", "tokenmaxr.json")
	if err != nil || string(got) != `{"schema":1}` {
		t.Fatalf("get: %q %v", got, err)
	}
	if got, err := c.GetFile(ctx, "octo/agent-usage", "missing.json"); got != nil || err != nil {
		t.Fatalf("missing file: %q %v", got, err)
	}
}

func TestVariablesPagesInstallations(t *testing.T) {
	f := ghapitest.New()
	c := client(t, f)
	ctx := context.Background()
	if _, ok, err := c.GetVariable(ctx, "octo/agent-usage", "TOKENMAXR_FLEET_KEY"); ok || err != nil {
		t.Fatalf("absent variable: %v %v", ok, err)
	}
	if err := c.CreateVariable(ctx, "octo/agent-usage", "TOKENMAXR_FLEET_KEY", "k1"); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateVariable(ctx, "octo/agent-usage", "TOKENMAXR_FLEET_KEY", "k2"); err != ErrConflict {
		t.Fatalf("second create must conflict, not overwrite: %v", err)
	}
	if v, ok, _ := c.GetVariable(ctx, "octo/agent-usage", "TOKENMAXR_FLEET_KEY"); !ok || v != "k1" {
		t.Fatalf("variable %q", v)
	}
	if u, _ := c.PagesURL(ctx, "octo/agent-usage"); u != "" {
		t.Fatal("pages off")
	}
	if err := c.EnablePages(ctx, "octo/agent-usage"); err != nil {
		t.Fatal(err)
	}
	if err := c.EnablePages(ctx, "octo/agent-usage"); err != nil {
		t.Fatalf("already enabled must be fine: %v", err)
	}
	if u, _ := c.PagesURL(ctx, "octo/agent-usage"); u != "https://octo.github.io/agent-usage/" {
		t.Fatalf("pages url %q", u)
	}
	inst, err := c.Installations(ctx)
	if err != nil || len(inst) != 1 || inst[0].AppSlug != "tokenmaxor" {
		t.Fatalf("installations %+v %v", inst, err)
	}
	repos, err := c.InstallationRepos(ctx, inst[0].ID)
	if err != nil || len(repos) != 1 || repos[0].FullName != "octo/agent-usage" {
		t.Fatalf("repos %+v %v", repos, err)
	}
	if u, err := c.User(ctx); err != nil || u.ID != 42 {
		t.Fatalf("user %+v %v", u, err)
	}
	if f.AuthSeen[len(f.AuthSeen)-1] != "Bearer ghu_test" {
		t.Fatal("API calls must carry the user token")
	}
}
