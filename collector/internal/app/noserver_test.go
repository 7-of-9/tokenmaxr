package app

import (
	"context"
	"strings"
	"testing"

	"github.com/7-of-9/tokenmaxr/collector/internal/outbox"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

// Without a server, install sets the machine up with a local fleet key (no
// browser, no enrolment) and ticks collect without uploading anywhere.
func TestInstallWithoutServer(t *testing.T) {
	a, _, out := newTestApp(t)
	a.OpenURL = func(u string) error { t.Fatalf("no browser without a server: %s", u); return nil }
	ctx := context.Background()

	if err := a.Install(ctx, InstallOptions{Endpoint: "off", Label: "box", Yes: true, NoAutostart: true}); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	sec, _ := store.LoadSecrets(a.Home)
	cfg, _ := store.LoadConfig(a.Home)
	if !sec.HasFleet() || sec.Enrolled() || cfg.Endpoint != store.NoServer || cfg.Server() != "" || !a.Installed() {
		t.Fatalf("secrets enrolled=%v fleet=%v endpoint %q", sec.Enrolled(), sec.HasFleet(), cfg.Endpoint)
	}
	if !strings.Contains(out.String(), "server:    none") {
		t.Fatalf("install output:\n%s", out)
	}
	rep, err := a.Tick(ctx, TickOptions{})
	if err != nil || rep.UploadSkipped != "no server destination" || rep.Queued == 0 {
		t.Fatalf("tick: %+v %v", rep, err)
	}
	if files, _ := outbox.New(paths.Outbox(a.Home)).Count(); files != 0 {
		t.Fatalf("%d outbox files without a server", files)
	}
	// Re-running keeps the key.
	k := sec.K
	if err := a.Install(ctx, InstallOptions{Yes: true, NoAutostart: true}); err != nil {
		t.Fatal(err)
	}
	if s2, _ := store.LoadSecrets(a.Home); s2.K != k {
		t.Fatal("re-install replaced the local fleet key")
	}
	if err := a.Invite(ctx); err == nil || !strings.Contains(err.Error(), "github login") {
		t.Fatalf("invite without a server: %v", err)
	}
	if err := a.Install(ctx, InstallOptions{Join: "D0M1-x", Yes: true, NoAutostart: true}); err == nil {
		t.Fatal("--join without a server accepted")
	}
}

// An enrolled machine whose server is switched off stops uploading but keeps
// its enrolment for when it is switched back on.
func TestServerSwitchedOff(t *testing.T) {
	api, srv := newFakeAPI(t)
	a, _, out := newTestApp(t)
	ctx := context.Background()
	if err := a.Install(ctx, InstallOptions{Join: "D0M1-first", Endpoint: srv.URL, Yes: true, NoAutostart: true}); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	cfg, _ := store.LoadConfig(a.Home)
	cfg.Endpoint = store.NoServer
	store.SaveConfig(a.Home, cfg)
	rep, err := a.Tick(ctx, TickOptions{})
	if err != nil || rep.UploadSkipped != "no server destination" || len(api.usage) != 0 {
		t.Fatalf("tick: %+v %v (server got %d)", rep, err, len(api.usage))
	}
	if sec, _ := store.LoadSecrets(a.Home); !sec.Enrolled() {
		t.Fatal("switching the server off dropped the enrolment")
	}
}
