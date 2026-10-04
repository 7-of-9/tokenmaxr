package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/outbox"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
)

// stuckPromptAPI enrolls any machine and takes usage and activity, but
// answers every prompt id with "retry" forever (a server whose prompt store
// is broken). Before the F1 fix sync-now looped on it without end.
func stuckPromptAPI(t *testing.T) (*httptest.Server, func() int) {
	var mu sync.Mutex
	usage := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/enroll":
			json.NewEncoder(w).Encode(model.EnrollResponse{MachineID: "m_sync", Token: "tok"})
		case "/api/ingest":
			var req model.IngestRequest
			json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			usage += len(req.Usage)
			mu.Unlock()
			resp := model.IngestResponse{OK: true, ServerTime: time.Now()}
			for _, p := range req.Prompts {
				resp.Retry = append(resp.Retry, p.ID)
			}
			json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() int { mu.Lock(); defer mu.Unlock(); return usage }
}

func TestSyncNowStopsWhenTickMakesNoProgress(t *testing.T) {
	srv, usageSeen := stuckPromptAPI(t)
	a, _, out := newTestApp(t)
	ctx := context.Background()
	if err := a.Install(ctx, InstallOptions{Join: "D0M1-sync", Endpoint: srv.URL, Label: "box", Yes: true, NoAutostart: true}); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	out.Reset()
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	err := a.SyncNow(cctx, "")
	if err == nil || !strings.Contains(err.Error(), "no progress") || !strings.Contains(err.Error(), "2 events stay queued") {
		t.Fatalf("SyncNow error = %v\n%s", err, out)
	}
	if cctx.Err() != nil {
		t.Fatal("sync-now ran until the context deadline")
	}
	if n := usageSeen(); n != 2 {
		t.Fatalf("server saw %d usage events, want 2", n)
	}
	// Retried counts per request: 3 sends of 2 prompts on tick 1 (the second
	// and third are the two stall checks), then 2 on tick 2.
	if !strings.Contains(out.String(), "tick 1: 7 events queued, 5 accepted, 6 to retry") || !strings.Contains(out.String(), "tick 2: 0 events queued, 0 accepted, 4 to retry") || strings.Contains(out.String(), "tick 3") {
		t.Fatalf("progress lines:\n%s", out)
	}
	ob := outbox.New(paths.Outbox(a.Home))
	names, _ := ob.List()
	if len(names) != 1 {
		t.Fatalf("outbox files: %v", names)
	}
	// The stuck prompts stay queued with their attempt counts for the
	// scheduled task to keep trying (and eventually dead-letter).
	b, _ := ob.Read(names[0])
	if b.Len() != 2 || len(b.Prompts) != 2 || len(b.Attempts) != 2 {
		t.Fatalf("leftover: %d items (%d prompts), attempts %v", b.Len(), len(b.Prompts), b.Attempts)
	}
	for id, n := range b.Attempts {
		if n < 4 {
			t.Fatalf("attempts[%s] = %d after two ticks", id, n)
		}
	}
}

func TestSyncNowReportsInSync(t *testing.T) {
	_, srv := newFakeAPI(t)
	a, _, out := newTestApp(t)
	ctx := context.Background()
	if err := a.Install(ctx, InstallOptions{Join: "D0M1-ok", Endpoint: srv.URL, Label: "box", Yes: true, NoAutostart: true}); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	out.Reset()
	if err := a.SyncNow(ctx, ""); err != nil {
		t.Fatalf("SyncNow: %v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "in sync") || strings.Contains(out.String(), "dead-letter") {
		t.Fatalf("output:\n%s", out)
	}
}
