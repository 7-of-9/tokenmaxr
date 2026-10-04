package upload

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/logx"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/outbox"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

// fakeServer records requests; respond decides each response.
type fakeServer struct {
	mu       sync.Mutex
	requests []model.IngestRequest
	respond  func(n int, req model.IngestRequest) (int, *model.IngestResponse)
}

func (f *fakeServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ingest" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("Authorization header must never be sent")
		}
		if r.Header.Get(TokenHeader) != "tok" {
			t.Errorf("token header = %q", r.Header.Get(TokenHeader))
		}
		body, _ := io.ReadAll(r.Body)
		if len(body) > MaxBody {
			t.Errorf("body %d bytes exceeds 1 MB", len(body))
		}
		var req model.IngestRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("bad body: %v", err)
		}
		if len(req.Usage) > MaxUsage || len(req.Activity) > MaxActivity || len(req.Prompts) > MaxPrompts {
			t.Errorf("caps exceeded: %d/%d/%d", len(req.Usage), len(req.Activity), len(req.Prompts))
		}
		f.mu.Lock()
		f.requests = append(f.requests, req)
		n := len(f.requests)
		f.mu.Unlock()
		code, resp := f.respond(n, req)
		w.WriteHeader(code)
		if resp != nil {
			json.NewEncoder(w).Encode(resp)
		}
	}
}

func okAll(int, model.IngestRequest) (int, *model.IngestResponse) {
	return 200, &model.IngestResponse{OK: true, ServerTime: time.Now()}
}

func setup(t *testing.T, respond func(int, model.IngestRequest) (int, *model.IngestResponse), items int) (*Uploader, *fakeServer, *outbox.Outbox) {
	f := &fakeServer{respond: respond}
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	ob := outbox.New(t.TempDir())
	var b outbox.Batch
	for i := range items {
		b.Usage = append(b.Usage, model.UsageEvent{ID: fmt.Sprintf("u%04d", i), Provider: "anthropic"})
		b.Activity = append(b.Activity, model.ActivityEvent{ID: fmt.Sprintf("a%04d", i)})
	}
	if items > 0 {
		if _, err := ob.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	u := &Uploader{
		Client:  NewClient(srv.URL, "tok", "test"),
		Outbox:  ob,
		Log:     logx.Discard(),
		Version: "test",
		Now:     time.Now,
	}
	return u, f, ob
}

func TestDrainsUnderCapsAndDeletesFile(t *testing.T) {
	u, f, ob := setup(t, okAll, 450)
	var bo store.Backoff
	res := u.Run(context.Background(), time.Now().Add(time.Minute), &bo, &model.Heartbeat{MachineLabel: "m"})
	if res.Err != nil || res.Accepted != 900 || !res.HeartbeatSent {
		t.Fatalf("res = %+v", res)
	}
	if names, _ := ob.List(); len(names) != 0 {
		t.Fatalf("outbox not emptied: %v", names)
	}
	if f.requests[0].Heartbeat == nil || f.requests[1].Heartbeat != nil {
		t.Fatal("heartbeat must ride on the first request only")
	}
	if f.requests[0].V != 1 || f.requests[0].CollectorVersion != "test" {
		t.Fatalf("envelope: %+v", f.requests[0])
	}
}

func TestHeartbeatAloneWhenOutboxEmpty(t *testing.T) {
	u, f, _ := setup(t, okAll, 0)
	var bo store.Backoff
	res := u.Run(context.Background(), time.Now().Add(time.Minute), &bo, &model.Heartbeat{MachineLabel: "m"})
	if !res.HeartbeatSent || len(f.requests) != 1 || f.requests[0].Heartbeat == nil {
		t.Fatalf("res=%+v requests=%d", res, len(f.requests))
	}
}

func TestRetryIdsAreResentAndOutboxRewritten(t *testing.T) {
	// First response asks to retry two ids; everything after succeeds.
	respond := func(n int, req model.IngestRequest) (int, *model.IngestResponse) {
		if n == 1 {
			return 200, &model.IngestResponse{OK: true, Retry: []string{req.Usage[0].ID, req.Activity[1].ID, "not-sent"}}
		}
		return okAll(n, req)
	}
	u, f, ob := setup(t, respond, 5)
	var bo store.Backoff
	res := u.Run(context.Background(), time.Now().Add(time.Minute), &bo, nil)
	if res.Err != nil || res.Retried != 2 || res.Accepted != 10 {
		t.Fatalf("res = %+v", res)
	}
	if len(f.requests) != 2 || len(f.requests[1].Usage) != 1 || f.requests[1].Usage[0].ID != "u0000" || f.requests[1].Activity[0].ID != "a0001" {
		t.Fatalf("second request: %+v", f.requests[1:])
	}
	if names, _ := ob.List(); len(names) != 0 {
		t.Fatal("outbox not emptied")
	}
}

func TestRetryPersistsWhenDeadlinePasses(t *testing.T) {
	respond := func(n int, req model.IngestRequest) (int, *model.IngestResponse) {
		return 200, &model.IngestResponse{OK: true, Retry: []string{req.Usage[0].ID}}
	}
	u, _, ob := setup(t, respond, 3)
	calls := 0
	base := time.Now()
	u.Now = func() time.Time { calls++; return base.Add(time.Duration(calls) * 10 * time.Second) }
	var bo store.Backoff
	u.Run(context.Background(), base.Add(25*time.Second), &bo, nil)
	names, _ := ob.List()
	if len(names) != 1 {
		t.Fatalf("outbox files: %v", names)
	}
	b, _ := ob.Read(names[0])
	if b.Len() != 1 || b.Usage[0].ID != "u0000" {
		t.Fatalf("outbox should hold only the retry id, got %+v", b)
	}
}

// retryPrompts mimics a server that cannot store prompts (e.g. no
// PROMPT_ENC_KEY): it takes usage and activity and returns every prompt id
// in "retry", forever.
func retryPrompts(_ int, req model.IngestRequest) (int, *model.IngestResponse) {
	resp := &model.IngestResponse{OK: true, ServerTime: time.Now()}
	for _, p := range req.Prompts {
		resp.Retry = append(resp.Retry, p.ID)
	}
	return 200, resp
}

// TestStalledFileDoesNotBlockLaterFiles reproduces F1: the first outbox file
// holds one prompt the server keeps returning in "retry"; five later files
// hold only usage. Before the fix, every pass stopped at the first file and
// none of the later usage was ever sent.
func TestStalledFileDoesNotBlockLaterFiles(t *testing.T) {
	u, f, ob := setup(t, retryPrompts, 3)
	first, _ := ob.List()
	b, _ := ob.Read(first[0])
	b.Prompts = []model.PromptRecord{{ID: "p0", Text: "synthetic"}}
	if err := ob.Rewrite(first[0], b); err != nil {
		t.Fatal(err)
	}
	later := map[string]bool{}
	for i := range 5 {
		var lb outbox.Batch
		for j := range 10 {
			id := fmt.Sprintf("later-%d-%d", i, j)
			later[id] = true
			lb.Usage = append(lb.Usage, model.UsageEvent{ID: id, Provider: "anthropic"})
		}
		if _, err := ob.Write(lb); err != nil {
			t.Fatal(err)
		}
	}
	var bo store.Backoff
	var total Result
	for pass := range 5 {
		res := u.Run(context.Background(), time.Now().Add(time.Minute), &bo, nil)
		if res.Err != nil {
			t.Fatalf("pass %d: %+v", pass, res)
		}
		total.Accepted += res.Accepted
		total.Retried += res.Retried
	}
	got := map[string]bool{}
	for _, req := range f.requests {
		for _, e := range req.Usage {
			got[e.ID] = true
		}
	}
	for id := range later {
		if !got[id] {
			t.Fatalf("later usage %s never sent (%d requests, %+v)", id, len(f.requests), total)
		}
	}
	if total.Accepted != 56 {
		t.Fatalf("accepted %d, want 56 (6 from the first file + 50 later)", total.Accepted)
	}
	// The stuck prompt is still queued, alone, with its attempt count, and
	// each pass spent only two requests on it before moving on.
	names, _ := ob.List()
	if len(names) != 1 || names[0] != first[0] {
		t.Fatalf("outbox files: %v", names)
	}
	left, _ := ob.Read(names[0])
	if left.Len() != 1 || left.Prompts[0].ID != "p0" || left.Attempts["p0"] != 11 {
		t.Fatalf("leftover: %d items, attempts %v", left.Len(), left.Attempts)
	}
	if len(f.requests) != 16 {
		t.Fatalf("%d requests, want 16 (8 on the first pass, 2 per pass after)", len(f.requests))
	}
	if files, _ := ob.DeadCount(); files != 0 {
		t.Fatal("dead-lettered before the attempt limit")
	}
}

func TestRetryLimitMovesIdToDeadLetter(t *testing.T) {
	respond := func(n int, req model.IngestRequest) (int, *model.IngestResponse) {
		resp := &model.IngestResponse{OK: true, ServerTime: time.Now()}
		for _, e := range req.Usage {
			if e.ID == "u0000" {
				resp.Retry = append(resp.Retry, e.ID)
			}
		}
		return 200, resp
	}
	u, f, ob := setup(t, respond, 3)
	var bo store.Backoff
	res := u.Run(context.Background(), time.Now().Add(time.Minute), &bo, nil)
	if res.Err != nil || res.Accepted != 5 || res.Retried != 3 || res.DeadLettered != 0 || len(f.requests) != 3 {
		t.Fatalf("pass 1: %+v, %d requests", res, len(f.requests))
	}
	names, _ := ob.List()
	if len(names) != 1 {
		t.Fatalf("outbox files: %v", names)
	}
	if b, _ := ob.Read(names[0]); b.Len() != 1 || b.Attempts["u0000"] != 3 {
		t.Fatalf("attempts must persist across passes: %+v", b)
	}
	dead := 0
	passes := 1
	for ; passes < 20; passes++ {
		res = u.Run(context.Background(), time.Now().Add(time.Minute), &bo, nil)
		if res.Err != nil {
			t.Fatalf("pass %d: %+v", passes+1, res)
		}
		dead += res.DeadLettered
		if n, _ := ob.List(); len(n) == 0 {
			break
		}
	}
	if dead != 1 || res.Retried != 0 {
		t.Fatalf("dead-lettered %d after %d passes, last %+v", dead, passes+1, res)
	}
	if files, _ := ob.Count(); files != 0 {
		t.Fatal("outbox not emptied after dead-lettering")
	}
	dn, _ := ob.ListDead()
	if len(dn) != 1 {
		t.Fatalf("dead files: %v", dn)
	}
	d, err := ob.ReadDead(dn[0])
	if err != nil || d.Len() != 1 || d.Usage[0].ID != "u0000" || d.Reasons["u0000"] != fmt.Sprintf("retry limit: %d attempts", MaxAttempts) || d.At.IsZero() {
		t.Fatalf("dead file: %+v %v", d, err)
	}
	if last := f.requests[len(f.requests)-1]; len(last.Usage) != 1 || last.Usage[0].ID != "u0000" {
		t.Fatalf("last request: %+v", last)
	}
	if files, events := ob.DeadCount(); files != 1 || events != 1 {
		t.Fatalf("DeadCount = %d, %d", files, events)
	}
}

func TestRejectedIdsAreLoggedCountedAndDeadLettered(t *testing.T) {
	type rejected struct {
		ID    *string `json:"id"`
		Error string  `json:"error"`
	}
	// The response carries "rejected", which model.IngestResponse does not
	// know: emit it by hand, with the rejected id also in accepted and in
	// retry the way ingest.js and a confused server might.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req model.IngestRequest
		json.NewDecoder(r.Body).Decode(&req)
		bad := "u0001"
		json.NewEncoder(w).Encode(map[string]any{
			"ok":         true,
			"accepted":   []string{bad},
			"retry":      []string{bad},
			"serverTime": time.Now(),
			"rejected":   []rejected{{ID: &bad, Error: "ts out of range"}, {ID: nil, Error: "heartbeat: machineLabel too long"}},
		})
	}))
	defer srv.Close()
	ob := outbox.New(t.TempDir())
	var b outbox.Batch
	for i := range 3 {
		b.Usage = append(b.Usage, model.UsageEvent{ID: fmt.Sprintf("u%04d", i)})
		b.Activity = append(b.Activity, model.ActivityEvent{ID: fmt.Sprintf("a%04d", i)})
	}
	ob.Write(b)
	var logged strings.Builder
	log := logx.Discard()
	log.Echo = &logged
	u := &Uploader{Client: NewClient(srv.URL, "tok", "test"), Outbox: ob, Log: log, Version: "test", Now: time.Now}
	var bo store.Backoff
	res := u.Run(context.Background(), time.Now().Add(time.Minute), &bo, &model.Heartbeat{MachineLabel: "m"})
	if res.Err != nil || res.Requests != 1 || res.Accepted != 5 || res.Rejected != 1 || res.Retried != 0 || res.DeadLettered != 1 {
		t.Fatalf("res = %+v", res)
	}
	if files, _ := ob.Count(); files != 0 {
		t.Fatal("rejected id must not stay queued")
	}
	dn, _ := ob.ListDead()
	if len(dn) != 1 {
		t.Fatalf("dead files: %v", dn)
	}
	if d, _ := ob.ReadDead(dn[0]); d.Len() != 1 || d.Usage[0].ID != "u0001" || d.Reasons["u0001"] != "rejected: ts out of range" {
		t.Fatalf("dead file: %+v", d)
	}
	for _, want := range []string{"server rejected u0001: ts out of range", "server rejected the heartbeat: heartbeat: machineLabel too long", "1 ids moved to " + filepath.Join(outbox.DeadDir, "0000000001.json")} {
		if !strings.Contains(logged.String(), want) {
			t.Fatalf("log missing %q:\n%s", want, logged.String())
		}
	}
	if strings.Contains(logged.String(), "u0000") {
		t.Fatalf("log must name only rejected ids:\n%s", logged.String())
	}
}

func TestHTTPErrorStopsBeforeLaterFiles(t *testing.T) {
	respond := func(int, model.IngestRequest) (int, *model.IngestResponse) { return 502, nil }
	u, f, ob := setup(t, respond, 2)
	ob.Write(outbox.Batch{Usage: []model.UsageEvent{{ID: "later"}}})
	var bo store.Backoff
	res := u.Run(context.Background(), time.Now().Add(time.Minute), &bo, nil)
	if res.Err == nil || Code(res.Err) != 502 || len(f.requests) != 1 {
		t.Fatalf("res=%+v requests=%d", res, len(f.requests))
	}
	if files, events := ob.Count(); files != 2 || events != 5 {
		t.Fatalf("outbox after 502: %d files, %d events", files, events)
	}
}

func TestPayloadTooLargeHalves(t *testing.T) {
	respond := func(n int, req model.IngestRequest) (int, *model.IngestResponse) {
		if len(req.Usage)+len(req.Activity) > 60 {
			return 413, nil
		}
		return okAll(n, req)
	}
	u, f, ob := setup(t, respond, 100)
	var bo store.Backoff
	res := u.Run(context.Background(), time.Now().Add(time.Minute), &bo, nil)
	if res.Err != nil || res.Accepted != 200 {
		t.Fatalf("res = %+v", res)
	}
	if bo.Failures != 0 || bo.Limit >= MaxLimit {
		t.Fatalf("413 must halve without backoff: %+v", bo)
	}
	if names, _ := ob.List(); len(names) != 0 {
		t.Fatal("outbox not emptied")
	}
	if len(f.requests) < 3 {
		t.Fatalf("expected halving retries, got %d requests", len(f.requests))
	}
}

func TestLoneOversizedItemIsDropped(t *testing.T) {
	respond := func(n int, req model.IngestRequest) (int, *model.IngestResponse) {
		for _, e := range req.Usage {
			if e.ID == "u0001" {
				return 413, nil
			}
		}
		return okAll(n, req)
	}
	u, _, ob := setup(t, respond, 3)
	var bo store.Backoff
	res := u.Run(context.Background(), time.Now().Add(time.Minute), &bo, nil)
	if res.Dropped != 1 || res.Accepted != 5 {
		t.Fatalf("res = %+v", res)
	}
	if names, _ := ob.List(); len(names) != 0 {
		t.Fatal("outbox not emptied")
	}
}

func TestServerErrorsBackOffAndHalveToFloor(t *testing.T) {
	respond := func(int, model.IngestRequest) (int, *model.IngestResponse) { return 503, nil }
	u, _, ob := setup(t, respond, 50)
	bo := store.Backoff{Limit: 40}
	var res Result
	for i := range 6 {
		res = u.Run(context.Background(), time.Now().Add(time.Minute), &bo, nil)
		if res.Err == nil || Code(res.Err) != 503 {
			t.Fatalf("run %d: %+v", i, res)
		}
	}
	if bo.Failures != 6 || bo.Limit != MinLimit || !bo.Until.After(time.Now().Add(15*time.Minute)) {
		t.Fatalf("backoff = %+v", bo)
	}
	if _, events := ob.Count(); events != 100 {
		t.Fatalf("events lost on 5xx: %d", events)
	}
}

func TestTimeoutCountsAsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer srv.Close()
	ob := outbox.New(t.TempDir())
	ob.Write(outbox.Batch{Usage: []model.UsageEvent{{ID: "u1"}}})
	c := NewClient(srv.URL, "tok", "test")
	c.HTTP.Timeout = 200 * time.Millisecond
	u := &Uploader{Client: c, Outbox: ob, Log: logx.Discard(), Version: "test", Now: time.Now}
	var bo store.Backoff
	res := u.Run(context.Background(), time.Now().Add(time.Minute), &bo, nil)
	if res.Err == nil || bo.Failures != 1 {
		t.Fatalf("res=%+v bo=%+v", res, bo)
	}
	if _, events := ob.Count(); events != 1 {
		t.Fatal("event lost on timeout")
	}
}

func TestUnauthorizedStops(t *testing.T) {
	respond := func(int, model.IngestRequest) (int, *model.IngestResponse) { return 401, nil }
	u, f, ob := setup(t, respond, 300)
	var bo store.Backoff
	res := u.Run(context.Background(), time.Now().Add(time.Minute), &bo, nil)
	if !res.Unauthorized || len(f.requests) != 1 || bo.Failures != 0 {
		t.Fatalf("res=%+v requests=%d bo=%+v", res, len(f.requests), bo)
	}
	if _, events := ob.Count(); events != 600 {
		t.Fatal("events lost on 401")
	}
}

func TestPromptByteCap(t *testing.T) {
	var b outbox.Batch
	big := strings.Repeat("x", 100*1024)
	for i := range 12 {
		b.Prompts = append(b.Prompts, model.PromptRecord{ID: fmt.Sprintf("p%d", i), Text: big})
	}
	part, rest := take(b, MaxLimit)
	if len(part.Prompts) != 4 || len(rest.Prompts) != 8 {
		t.Fatalf("took %d prompts, left %d", len(part.Prompts), len(rest.Prompts))
	}
	one := outbox.Batch{Prompts: []model.PromptRecord{{ID: "huge", Text: strings.Repeat("y", 900*1024)}}}
	if part, _ := take(one, MaxLimit); len(part.Prompts) != 1 {
		t.Fatal("a single oversized prompt must still be taken alone")
	}
}

func TestEnrollAndInvite(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/enroll":
			var req model.EnrollRequest
			json.NewDecoder(r.Body).Decode(&req)
			if r.Header.Get(TokenHeader) != "" {
				t.Error("enroll must not send a token")
			}
			if req.KFingerprint == "bad" {
				w.WriteHeader(409)
				w.Write([]byte(`{"error":"fleet key mismatch"}`))
				return
			}
			json.NewEncoder(w).Encode(model.EnrollResponse{MachineID: "m_1", Token: "t"})
		case "/api/invite":
			if r.Header.Get(TokenHeader) != "tok" {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(model.InviteResponse{Invite: "inv", ExpiresAt: time.Now()})
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	if r, err := NewClient(srv.URL, "", "v").Enroll(ctx, model.EnrollRequest{Invite: "i", KFingerprint: "ok"}); err != nil || r.MachineID != "m_1" {
		t.Fatalf("enroll: %+v %v", r, err)
	}
	_, err := NewClient(srv.URL, "", "v").Enroll(ctx, model.EnrollRequest{Invite: "i", KFingerprint: "bad"})
	if Code(err) != 409 || !strings.Contains(err.Error(), "fleet key mismatch") {
		t.Fatalf("409: %v", err)
	}
	if r, err := NewClient(srv.URL, "tok", "v").Invite(ctx); err != nil || r.Invite != "inv" {
		t.Fatalf("invite: %+v %v", r, err)
	}
	if _, err := NewClient(srv.URL, "wrong", "v").Invite(ctx); Code(err) != 401 {
		t.Fatalf("invite 401: %v", err)
	}
}

// The heartbeat carries the homes scanned (v1.3) as "homes" next to the
// shared fields, capped to what the API accepts; a request without a
// heartbeat has no heartbeat key at all.
func TestHeartbeatCarriesHomes(t *testing.T) {
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, b)
		json.NewEncoder(w).Encode(model.IngestResponse{OK: true, ServerTime: time.Now()})
	}))
	t.Cleanup(srv.Close)
	long := strings.Repeat("x", MaxHomeChars+1)
	homes := []string{`C:\Users\dom`, `\wsl$\Ubuntu-22.04\home\dom`, long}
	for i := 0; i < MaxHomes; i++ {
		homes = append(homes, fmt.Sprintf(`D:\extra%02d`, i))
	}
	u := &Uploader{Client: NewClient(srv.URL, "tok", "test"), Outbox: outbox.New(t.TempDir()), Log: logx.Discard(), Version: "test", Now: time.Now, Homes: homes}
	var bo store.Backoff
	if res := u.Run(context.Background(), time.Now().Add(time.Minute), &bo, &model.Heartbeat{MachineLabel: "m", Checks: map[string]string{}}); !res.HeartbeatSent {
		t.Fatalf("res %+v", res)
	}
	var got struct {
		V         int `json:"v"`
		Heartbeat struct {
			MachineLabel string   `json:"machineLabel"`
			Homes        []string `json:"homes"`
		} `json:"heartbeat"`
	}
	if err := json.Unmarshal(bodies[0], &got); err != nil {
		t.Fatal(err)
	}
	if got.V != 1 || got.Heartbeat.MachineLabel != "m" || len(got.Heartbeat.Homes) != MaxHomes || got.Heartbeat.Homes[1] != homes[1] || strings.Contains(string(bodies[0]), long) {
		t.Fatalf("wire heartbeat: %s", bodies[0])
	}
	if err := u.Client.SendHeartbeat(context.Background(), "test", time.Now(), &model.Heartbeat{MachineLabel: "n"}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bodies[1]), `"machineLabel":"n"`) || strings.Contains(string(bodies[1]), `"homes"`) {
		t.Fatalf("heartbeat alone: %s", bodies[1])
	}
	// No heartbeat: no heartbeat key, whatever Homes holds.
	b := newIngestRequest("test", time.Now(), outbox.Batch{}, nil, homes)
	if raw, _ := json.Marshal(b); strings.Contains(string(raw), "heartbeat") {
		t.Fatalf("request without heartbeat: %s", raw)
	}
}

func TestProgressTracksAcknowledgementsAndRetries(t *testing.T) {
	u, _, ob := setup(t, func(n int, req model.IngestRequest) (int, *model.IngestResponse) {
		if n == 1 {
			return 200, &model.IngestResponse{OK: true, Retry: []string{req.Usage[0].ID}}
		}
		return okAll(n, req)
	}, 450)
	var seen []Progress
	u.Progress = func(p Progress) {
		_, durable := ob.Count()
		if p.Remaining != durable {
			t.Errorf("progress remaining %d differs from durable queue %d", p.Remaining, durable)
		}
		seen = append(seen, p)
	}
	var bo store.Backoff
	res := u.Run(context.Background(), time.Now().Add(time.Minute), &bo, nil)
	if res.Err != nil || res.Accepted != 900 {
		t.Fatalf("result %+v", res)
	}
	var active []int
	for _, p := range seen {
		if p.InFlight {
			active = append(active, p.Remaining)
		}
	}
	// First batch has 200 usage and 450 activity; one retry stays queued.
	if fmt.Sprint(active) != "[900 251 51]" {
		t.Fatalf("in-flight remaining counts %v", active)
	}
	last := seen[len(seen)-1]
	if last.InFlight || last.Remaining != 0 || last.Accepted != 900 {
		t.Fatalf("last progress %+v", last)
	}
}

func TestFailedUploadNeverDecreasesProgress(t *testing.T) {
	u, _, _ := setup(t, func(int, model.IngestRequest) (int, *model.IngestResponse) { return 503, nil }, 4)
	var active bool
	var last Progress
	u.Progress = func(p Progress) {
		if p.Remaining != 8 || p.Accepted != 0 {
			t.Errorf("failed request claimed progress: %+v", p)
		}
		active = active || p.InFlight
		last = p
	}
	var bo store.Backoff
	res := u.Run(context.Background(), time.Now().Add(time.Minute), &bo, nil)
	if res.Err == nil || !active || last.InFlight {
		t.Fatalf("result %+v, active %v, last %+v", res, active, last)
	}
}
