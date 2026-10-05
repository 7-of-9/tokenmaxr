package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/autostart"
	"github.com/7-of-9/tokenmaxr/collector/internal/homes"
	"github.com/7-of-9/tokenmaxr/collector/internal/joincode"
	"github.com/7-of-9/tokenmaxr/collector/internal/logx"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/outbox"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
	"github.com/7-of-9/tokenmaxr/collector/internal/upload"
)

// fakeAPI is a minimal in-memory /api/enroll + /api/ingest + /api/invite +
// /api/fleet/github.
type fakeAPI struct {
	mu          sync.Mutex
	fingerprint string
	token       string            // the last enrolled machine's
	tokens      map[string]string // every enrolled machine's token -> machine id
	usage       map[string]model.UsageEvent
	activity    map[string]model.ActivityEvent
	prompts     map[string]model.PromptRecord
	heartbeats  int
	ingests     int // ingest requests carrying events
	lastHB      *model.Heartbeat
	lastHomes   []string // "homes" of the last heartbeat, read off the raw body
	// fleetGitHub is the fleet's shared GitHub sign-in (nil: none), stored
	// as the server does: an opaque blob it never decrypts.
	fleetGitHub                        *fakeFleetGitHub
	fleetGets, fleetPuts, fleetDeletes int
	// fleetEscrowed: the server keeps the fleet key (a fleet linked from
	// the browser), as PUT reports.
	fleetEscrowed bool
}

type fakeFleetGitHub struct {
	Blob      string    `json:"blob"`
	UpdatedAt time.Time `json:"updatedAt"`
	By        string    `json:"by"`
}

// machine is the enrolled machine whose token r carries ("" for none).
func (f *fakeAPI) machine(r *http.Request) string {
	if r.Header.Get("Authorization") != "" {
		return ""
	}
	return f.tokens[r.Header.Get(upload.TokenHeader)]
}

// revoke rejects every machine token from now on.
func (f *fakeAPI) revoke() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.token, f.tokens = "revoked", map[string]string{}
}

// fleetBlobOK is the server's format check (SPEC "PUT /api/fleet/github"):
// base64 of at most 4096 bytes starting "tmx1". It never decrypts.
func fleetBlobOK(blob string) bool {
	b, err := base64.StdEncoding.DecodeString(blob)
	return err == nil && len(b) <= 4096 && bytes.HasPrefix(b, []byte("tmx1"))
}

func newFakeAPI(t *testing.T) (*fakeAPI, *httptest.Server) {
	f := &fakeAPI{tokens: map[string]string{}, usage: map[string]model.UsageEvent{}, activity: map[string]model.ActivityEvent{}, prompts: map[string]model.PromptRecord{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/api/enroll":
			var req model.EnrollRequest
			json.NewDecoder(r.Body).Decode(&req)
			if f.fingerprint != "" && f.fingerprint != req.KFingerprint {
				w.WriteHeader(409)
				w.Write([]byte(`{"error":"join code must come from d0m1-collector invite on an enrolled machine"}`))
				return
			}
			f.fingerprint = req.KFingerprint
			f.token = "tok-" + req.Invite
			f.tokens[f.token] = "m_" + req.Invite
			json.NewEncoder(w).Encode(model.EnrollResponse{MachineID: "m_" + req.Invite, Token: f.token})
		case "/api/invite":
			if f.machine(r) == "" {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(model.InviteResponse{Invite: "second", ExpiresAt: time.Now().Add(15 * time.Minute)})
		case "/api/fleet/github":
			by := f.machine(r)
			if by == "" {
				w.WriteHeader(401)
				return
			}
			w.Header().Set("Cache-Control", "private, no-store")
			switch r.Method {
			case http.MethodGet:
				f.fleetGets++
				if f.fleetGitHub == nil {
					w.WriteHeader(404)
					w.Write([]byte(`{"error":"no fleet GitHub sign-in"}`))
					return
				}
				json.NewEncoder(w).Encode(f.fleetGitHub)
			case http.MethodPut:
				var in struct{ Blob string }
				if json.NewDecoder(r.Body).Decode(&in) != nil || !fleetBlobOK(in.Blob) {
					w.WriteHeader(400)
					w.Write([]byte(`{"error":"blob must be base64 of a tmx1 blob up to 4096 bytes"}`))
					return
				}
				f.fleetPuts++
				f.fleetGitHub = &fakeFleetGitHub{Blob: in.Blob, UpdatedAt: time.Now().UTC(), By: by}
				json.NewEncoder(w).Encode(map[string]any{"updatedAt": f.fleetGitHub.UpdatedAt, "escrowed": f.fleetEscrowed})
			case http.MethodDelete:
				f.fleetDeletes++
				f.fleetGitHub = nil
				w.WriteHeader(204)
			default:
				w.WriteHeader(405)
			}
		case "/api/ingest":
			if f.machine(r) == "" {
				w.WriteHeader(401)
				return
			}
			body, _ := io.ReadAll(r.Body)
			var req model.IngestRequest
			json.Unmarshal(body, &req)
			if len(req.Usage)+len(req.Activity)+len(req.Prompts) > 0 {
				f.ingests++
			}
			var extra struct {
				Heartbeat *struct {
					Homes []string `json:"homes"`
				} `json:"heartbeat"`
			}
			if json.Unmarshal(body, &extra) == nil && extra.Heartbeat != nil {
				f.lastHomes = extra.Heartbeat.Homes
			}
			for _, e := range req.Usage {
				f.usage[e.ID] = e
			}
			for _, e := range req.Activity {
				f.activity[e.ID] = e
			}
			for _, e := range req.Prompts {
				f.prompts[e.ID] = e
			}
			if req.Heartbeat != nil {
				f.heartbeats++
				f.lastHB = req.Heartbeat
			}
			json.NewEncoder(w).Encode(model.IngestResponse{OK: true, ServerTime: time.Now()})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

// synthSource emits one usage, activity and prompt event per line of a
// synthetic log, attributing through env like the real parsers. With no
// fixed path it reads <home>/synthetic.log of whichever home it scans.
type synthSource struct{ path string }

func (s *synthSource) Name() string               { return model.SourceClaudeCode }
func (s *synthSource) Provider() string           { return model.ProviderAnthropic }
func (s *synthSource) PV() int                    { return 1 }
func (s *synthSource) Prepare(*sources.Env) error { return nil }
func (s *synthSource) Files(env *sources.Env) ([]string, error) {
	p := s.path
	if p == "" {
		p = filepath.Join(env.Home, "synthetic.log")
	}
	if _, err := os.Stat(p); err != nil {
		return nil, nil
	}
	return []string{p}, nil
}
func (s *synthSource) Parse(env *sources.Env, path string, cur sources.Cursor) (sources.Batch, sources.Cursor, error) {
	var b sources.Batch
	data, err := os.ReadFile(path)
	if err != nil {
		return b, cur, err
	}
	end := int64(bytes.LastIndexByte(data, '\n') + 1)
	for _, line := range strings.Split(string(data[cur.Offset:end]), "\n") {
		if line == "" {
			continue
		}
		ts := time.Now().Add(-time.Hour)
		acct, q := env.Attribute(s.Provider(), ts, "sess", sources.Hint{})
		tz := env.TZOffsetMin(ts)
		b.Usage = append(b.Usage, model.UsageEvent{ID: model.EventID("usage", "anthropic", "claude-code", line), Provider: "anthropic", Source: "claude-code",
			TS: ts, TZOffsetMin: tz, Acct: acct, AcctQ: q, Q: "exact", PV: 1, Tokens: model.Tokens{In: 1, Out: 2, Calls: 1}})
		// The same id twice with growing output: merged before upload.
		b.Usage = append(b.Usage, model.UsageEvent{ID: model.EventID("usage", "anthropic", "claude-code", line), Provider: "anthropic", Source: "claude-code",
			TS: ts, TZOffsetMin: tz, Acct: acct, AcctQ: q, Q: "exact", PV: 1, Tokens: model.Tokens{In: 1, Out: 7, Calls: 1}})
		b.Activity = append(b.Activity, model.ActivityEvent{ID: model.EventID("activity", "anthropic", "claude-code", line), TS: ts, Acct: acct, AcctQ: q, HasUsage: true})
		if env.Prompts {
			b.Prompts = append(b.Prompts, model.PromptRecord{ID: model.EventID("prompt", "anthropic", "claude-code", line), TS: ts, Acct: acct,
				AcctLabel: env.Label(acct), Machine: env.Machine, Text: "synthetic prompt " + line})
		}
	}
	cur.Offset = end
	return b, cur, nil
}

// noTerminal makes install non-interactive, as under CI or launchd.
func noTerminal(t *testing.T) {
	old := openTerminal
	openTerminal = func() (*terminal, bool) { return nil, false }
	t.Cleanup(func() { openTerminal = old })
}

// fakeTerminal answers install's questions with input and records them.
func fakeTerminal(t *testing.T, input string) *bytes.Buffer {
	t.Helper()
	asked := &bytes.Buffer{}
	old := openTerminal
	openTerminal = func() (*terminal, bool) {
		return &terminal{in: bufio.NewReader(strings.NewReader(input)), out: asked}, true
	}
	t.Cleanup(func() { openTerminal = old })
	return asked
}

// fakeWSL stands in for wsl.exe and \\wsl$: a temp root with one directory
// per distro. Tests never run the real wsl.exe (and never boot a distro).
type fakeWSL struct {
	root         string
	all, running []string
}

func (f *fakeWSL) wsl() *homes.WSL {
	return &homes.WSL{Root: f.root, List: func(_ context.Context, runningOnly bool) ([]byte, error) {
		names := f.all
		if runningOnly {
			names = f.running
		}
		var out []byte
		for _, n := range names {
			for _, r := range n + "\r\n" {
				out = append(out, byte(r), 0)
			}
		}
		return out, nil
	}}
}

// claudeHome writes a home with a Claude login for uuid and a synthetic log.
func claudeHome(t *testing.T, dir, uuid, email, log string) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"oauthAccount": map[string]any{"accountUuid": uuid, "emailAddress": email, "organizationName": "Org"}})
	os.MkdirAll(filepath.Join(dir, ".claude"), 0o755)
	os.WriteFile(filepath.Join(dir, ".claude.json"), b, 0o600)
	os.WriteFile(filepath.Join(dir, "synthetic.log"), []byte(log), 0o600)
}

func newTestApp(t *testing.T) (*App, string, *bytes.Buffer) {
	noTerminal(t)
	userHome := t.TempDir()
	claudeHome(t, userHome, "uuid-1", "someone@example.com", "line-1\nline-2\n")
	logPath := filepath.Join(userHome, "synthetic.log")
	out := &bytes.Buffer{}
	a := &App{
		Home:      filepath.Join(t.TempDir(), "state"),
		Version:   "dev",
		UserHome:  userHome,
		CodexHome: filepath.Join(userHome, ".codex"),
		Log:       logx.Discard(),
		Out:       out,
		Now:       time.Now,
		Sources:   func() []sources.Source { return []sources.Source{&synthSource{path: logPath}} },
		WSL:       (&fakeWSL{root: t.TempDir()}).wsl(),
		// Tests never touch the machine's autostart entries or PATH.
		Autostart: &autostart.System{GOOS: "test"},
		UserPath:  &fakePath{},
	}
	return a, logPath, out
}

func TestInstallTickInvite(t *testing.T) {
	api, srv := newFakeAPI(t)
	a, logPath, out := newTestApp(t)
	ctx := context.Background()

	err := a.Install(ctx, InstallOptions{Join: "D0M1-first", Endpoint: srv.URL, Label: "test-box", Yes: true, NoAutostart: true})
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	sec, _ := store.LoadSecrets(a.Home)
	if !sec.Enrolled() || sec.MachineID != "m_first" || model.KFingerprint(sec.Key()) != api.fingerprint {
		t.Fatalf("secrets: machine %q enrolled %v", sec.MachineID, sec.Enrolled())
	}
	// Claude settings got the retention fix (synthetic home, never the real one).
	if b, _ := os.ReadFile(filepath.Join(a.UserHome, ".claude", "settings.json")); !strings.Contains(string(b), "3650") {
		t.Fatalf("settings.json not created: %s", b)
	}
	cfg, _ := store.LoadConfig(a.Home)
	acct := model.AccountHash(sec.Key(), "anthropic", "uuid-1")
	if cfg.AccountLabels[acct] != "someone@example.com · Org" || cfg.MachineLabel != "test-box" || cfg.Autostart {
		t.Fatalf("config: %+v", cfg)
	}

	rep, err := a.Tick(ctx, TickOptions{})
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if rep.Upload.Err != nil || rep.Queued != 7 || !rep.Upload.HeartbeatSent {
		t.Fatalf("tick report: %+v", rep)
	}
	if len(api.usage) != 2 || len(api.activity) != 2 || len(api.prompts) != 2 || api.heartbeats != 1 {
		t.Fatalf("server got %d/%d/%d hb %d", len(api.usage), len(api.activity), len(api.prompts), api.heartbeats)
	}
	if hb := api.lastHB; hb.MachineLabel != "test-box" || hb.TZ == nil || hb.TZ.Source == "" || *hb.TZ != *machineTZ() {
		t.Fatalf("heartbeat label %q tz %+v", hb.MachineLabel, hb.TZ)
	}
	if len(api.lastHomes) != 1 || api.lastHomes[0] != a.UserHome {
		t.Fatalf("heartbeat homes %v", api.lastHomes)
	}
	for _, e := range api.usage {
		if e.Out != 7 || e.Acct != acct || e.AcctQ != model.AcctInferred {
			t.Fatalf("usage event: %+v", e)
		}
	}
	for _, p := range api.prompts {
		if p.AcctLabel != "someone@example.com · Org" || p.Machine != "test-box" {
			t.Fatalf("prompt: %+v", p)
		}
	}
	if files, _ := outbox.New(paths.Outbox(a.Home)).Count(); files != 0 {
		t.Fatal("outbox not drained")
	}

	// Excluded account: usage and activity still flow, prompts do not.
	cfg.PromptExcludeAccts = []string{acct}
	store.SaveConfig(a.Home, cfg)
	f, _ := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("line-3\n")
	f.Close()
	rep, _ = a.Tick(ctx, TickOptions{})
	if rep.Queued != 2 || len(api.usage) != 3 || len(api.prompts) != 2 || rep.Upload.HeartbeatSent {
		t.Fatalf("exclude: queued %d usage %d prompts %d", rep.Queued, len(api.usage), len(api.prompts))
	}
	// Nothing new: nothing queued.
	if rep, _ = a.Tick(ctx, TickOptions{}); rep.Queued != 0 {
		t.Fatalf("idle tick queued %d", rep.Queued)
	}

	// Invite prints a join code carrying this fleet's K.
	out.Reset()
	if err := a.Invite(ctx); err != nil {
		t.Fatal(err)
	}
	inv, k, err := joincode.Parse(strings.TrimSpace(out.String()))
	if err != nil || inv != "second" || !bytes.Equal(k, sec.Key()) {
		t.Fatalf("invite output %q: %v", out.String(), err)
	}

	// Re-running install without --join repairs without re-enrolling.
	if err := a.Install(ctx, InstallOptions{Yes: true, NoAutostart: true}); err != nil {
		t.Fatalf("re-install: %v", err)
	}
	if s2, _ := store.LoadSecrets(a.Home); s2.MachineID != "m_first" {
		t.Fatal("re-install re-enrolled")
	}

	// A second machine joining without K (or with another K) is refused.
	b, _, _ := newTestApp(t)
	err = b.Install(ctx, InstallOptions{Join: "D0M1-other", Endpoint: srv.URL, Yes: true, NoAutostart: true})
	if err == nil || !strings.Contains(err.Error(), "join code must come from") {
		t.Fatalf("second bootstrap: %v", err)
	}
	// ...but joins with the printed code.
	if err := b.Install(ctx, InstallOptions{Join: joincode.Format("second", sec.Key()), Endpoint: srv.URL, Yes: true, NoAutostart: true}); err != nil {
		t.Fatalf("join with K: %v", err)
	}
	s3, _ := store.LoadSecrets(b.Home)
	if base64.StdEncoding.EncodeToString(s3.Key()) != sec.K {
		t.Fatal("second machine has a different K")
	}
}

func TestUnauthorizedPausesUploads(t *testing.T) {
	api, srv := newFakeAPI(t)
	a, _, _ := newTestApp(t)
	ctx := context.Background()
	if err := a.Install(ctx, InstallOptions{Join: "D0M1-x", Endpoint: srv.URL, Yes: true, NoAutostart: true, NoPrompts: true}); err != nil {
		t.Fatal(err)
	}
	api.revoke()
	rep, _ := a.Tick(ctx, TickOptions{})
	if !rep.Upload.Unauthorized {
		t.Fatalf("tick: %+v", rep.Upload)
	}
	st, _ := store.LoadState(a.Home)
	if !st.Unauthorized {
		t.Fatal("401 not recorded")
	}
	rep, _ = a.Tick(ctx, TickOptions{})
	if rep.UploadSkipped == "" || rep.Upload.Requests != 0 {
		t.Fatalf("uploads not paused: %+v", rep)
	}
	// A network change's retry keeps the 401 hold-off, but not a backoff.
	rep, _ = a.Tick(ctx, TickOptions{Retry: true})
	if rep.UploadSkipped == "" || rep.Upload.Requests != 0 {
		t.Fatalf("a retry ignored the 401 hold-off: %+v", rep)
	}
	st, _ = store.LoadState(a.Home)
	st.Unauthorized, st.Backoff.Until = false, a.Now().Add(time.Hour)
	store.SaveState(a.Home, st)
	if rep, _ = a.Tick(ctx, TickOptions{}); !strings.HasPrefix(rep.UploadSkipped, "backing off") {
		t.Fatalf("plain tick during a backoff: %+v", rep)
	}
	if rep, _ = a.Tick(ctx, TickOptions{Retry: true}); rep.UploadSkipped != "" || rep.Upload.Requests == 0 {
		t.Fatalf("a retry kept the backoff: %+v", rep)
	}
	if _, events := outbox.New(paths.Outbox(a.Home)).Count(); events != 5 {
		t.Fatalf("collection must continue: %d events queued", events)
	}
	var buf bytes.Buffer
	a.Out = &buf
	a.Status()
	if !strings.Contains(buf.String(), "PAUSED") || strings.Contains(buf.String(), "someone@example.com") {
		t.Fatalf("status output:\n%s", buf.String())
	}
}

func TestCapPrompt(t *testing.T) {
	p := model.PromptRecord{ID: "x", Text: strings.Repeat("é\x01", 200*1024)}
	got := capPrompt(p)
	b, _ := json.Marshal(got)
	if len(b) > maxPromptJSON || !strings.HasSuffix(got.Text, truncMarker) {
		t.Fatalf("capped to %d bytes", len(b))
	}
	small := model.PromptRecord{ID: "y", Text: "short"}
	if capPrompt(small).Text != "short" {
		t.Fatal("short prompt changed")
	}
}

func TestCleanLabel(t *testing.T) {
	for in, want := range map[string]string{
		"  Studio   Mac\t":               "Studio Mac",
		"desk\x00top\x1b[31m":            "desktop[31m",
		"\t \n":                          "",
		strings.Repeat("é", 80):          strings.Repeat("é", maxLabelRunes),
		strings.Repeat("a", 47) + " b c": strings.Repeat("a", 47),
		"Evil\u202eBox\u2066 1\u2069":    "EvilBox 1",
	} {
		if got := CleanLabel(in); got != want {
			t.Errorf("CleanLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInstallLabel(t *testing.T) {
	_, srv := newFakeAPI(t)
	ctx := context.Background()

	// Interactive: the answer becomes the public label.
	a, _, out := newTestApp(t)
	asked := fakeTerminal(t, "  Studio   Mac \n")
	if err := a.Install(ctx, InstallOptions{Join: "D0M1-first", Endpoint: srv.URL, Yes: true, NoAutostart: true}); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if !strings.Contains(asked.String(), "Public name for this machine ("+publicNote+") [") {
		t.Fatalf("question: %q", asked.String())
	}
	if cfg, _ := store.LoadConfig(a.Home); cfg.MachineLabel != "Studio Mac" {
		t.Fatalf("label %q", cfg.MachineLabel)
	}
	// Re-install keeps it without asking; --label replaces it.
	asked = fakeTerminal(t, "should not be read\n")
	if err := a.Install(ctx, InstallOptions{Yes: true, NoAutostart: true}); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := store.LoadConfig(a.Home); cfg.MachineLabel != "Studio Mac" || asked.Len() != 0 {
		t.Fatalf("re-install: label %q, asked %q", cfg.MachineLabel, asked.String())
	}
	if err := a.Install(ctx, InstallOptions{Label: "Desk", Yes: true, NoAutostart: true}); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := store.LoadConfig(a.Home); cfg.MachineLabel != "Desk" {
		t.Fatalf("--label: %q", cfg.MachineLabel)
	}

	// An empty answer (or end of input) takes the hostname.
	b, _, _ := newTestApp(t)
	fakeTerminal(t, "")
	if err := b.Install(ctx, InstallOptions{Join: joincode.Format("second", mustKey(t, a)), Endpoint: srv.URL, Yes: true, NoAutostart: true}); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := store.LoadConfig(b.Home); cfg.MachineLabel != defaultLabel() {
		t.Fatalf("default label %q, want %q", cfg.MachineLabel, defaultLabel())
	}

	// No terminal: the hostname, with a one-line notice that it is public.
	c, _, cout := newTestApp(t)
	if err := c.Install(ctx, InstallOptions{Join: joincode.Format("second", mustKey(t, a)), Endpoint: srv.URL, Yes: true, NoAutostart: true}); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := store.LoadConfig(c.Home); cfg.MachineLabel != defaultLabel() || !strings.Contains(cout.String(), publicNote+"; change it with `tokenmaxr label NEW_NAME`") {
		t.Fatalf("non-interactive: label %q, output:\n%s", cfg.MachineLabel, cout)
	}
}

func mustKey(t *testing.T, a *App) []byte {
	t.Helper()
	sec, err := store.LoadSecrets(a.Home)
	if err != nil || sec.Key() == nil {
		t.Fatalf("no fleet key: %v", err)
	}
	return sec.Key()
}

func TestSetLabel(t *testing.T) {
	api, srv := newFakeAPI(t)
	a, _, out := newTestApp(t)
	ctx := context.Background()
	if err := a.SetLabel(ctx, "early"); err != nil {
		t.Fatalf("label before install: %v", err)
	}
	if err := a.Install(ctx, InstallOptions{Join: "D0M1-x", Endpoint: srv.URL, Yes: true, NoAutostart: true}); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := store.LoadConfig(a.Home); cfg.MachineLabel != "early" {
		t.Fatalf("install replaced the label set before it: %q", cfg.MachineLabel)
	}
	if err := a.SetLabel(ctx, "  Studio \n Mac  "); err != nil {
		t.Fatal(err)
	}
	cfg, _ := store.LoadConfig(a.Home)
	st, _ := store.LoadState(a.Home)
	if cfg.MachineLabel != "Studio Mac" || api.heartbeats != 1 || api.lastHB.MachineLabel != "Studio Mac" || api.lastHB.TZ == nil || st.LastHeartbeat.IsZero() {
		t.Fatalf("label %q heartbeats %d last %+v at %s", cfg.MachineLabel, api.heartbeats, api.lastHB, st.LastHeartbeat)
	}
	if err := a.SetLabel(ctx, " \t "); err == nil {
		t.Fatal("blank label accepted")
	}

	// Offline: saved anyway, and the next tick is due to send the heartbeat.
	srv.Close()
	out.Reset()
	if err := a.SetLabel(ctx, "Offline Box"); err != nil {
		t.Fatalf("offline: %v", err)
	}
	cfg, _ = store.LoadConfig(a.Home)
	st, _ = store.LoadState(a.Home)
	if cfg.MachineLabel != "Offline Box" || !st.LastHeartbeat.IsZero() || !strings.Contains(out.String(), "the next tick sends it") {
		t.Fatalf("offline: label %q last heartbeat %s output %q", cfg.MachineLabel, st.LastHeartbeat, out)
	}
}

// Three homes with three different Claude accounts (the OS home, an
// extraHome and a running WSL distro's home): each home's events carry its
// own account, the config fix runs in each, the heartbeat lists all three,
// and a distro that stops keeps its cursors while a dropped extraHome loses
// them. The stopped distro's tree is never opened.
func TestMultiHomeAttribution(t *testing.T) {
	api, srv := newFakeAPI(t)
	a, _, out := newTestApp(t)
	ctx := context.Background()
	extra := t.TempDir()
	claudeHome(t, extra, "uuid-2", "two@example.com", "b-1\n")
	f := &fakeWSL{root: t.TempDir(), all: []string{"Ubuntu-22.04", "Ubuntu-24.04"}, running: []string{"Ubuntu-22.04"}}
	wslHome := filepath.Join(f.root, "Ubuntu-22.04", "home", "dom")
	claudeHome(t, wslHome, "uuid-3", "three@example.com", "w-1\n")
	stopped := filepath.Join(f.root, "Ubuntu-24.04", "home", "dom")
	claudeHome(t, stopped, "uuid-4", "four@example.com", "s-1\n")
	a.WSL = f.wsl()
	a.Sources = func() []sources.Source { return []sources.Source{&synthSource{}} }

	if err := a.Install(ctx, InstallOptions{Join: "D0M1-multi", Endpoint: srv.URL, Label: "box", Yes: true, NoAutostart: true}); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	cfg, _ := store.LoadConfig(a.Home)
	cfg.ExtraHomes = []string{extra}
	store.SaveConfig(a.Home, cfg)
	// Checks run hourly and install just ran them: make the tick run them
	// again so the new home gets its fix now.
	st, _ := store.LoadState(a.Home)
	st.LastConfigCheck = time.Time{}
	store.SaveState(a.Home, st)
	rep, err := a.Tick(ctx, TickOptions{})
	if err != nil || rep.Upload.Err != nil {
		t.Fatalf("tick: %v %+v", err, rep)
	}
	sec, _ := store.LoadSecrets(a.Home)
	acct := func(uuid string) string { return model.AccountHash(sec.Key(), "anthropic", uuid) }
	want := map[string]string{"line-1": acct("uuid-1"), "line-2": acct("uuid-1"), "b-1": acct("uuid-2"), "w-1": acct("uuid-3")}
	if len(api.usage) != len(want) {
		t.Fatalf("server has %d usage events, want %d", len(api.usage), len(want))
	}
	for line, acct := range want {
		e, ok := api.usage[model.EventID("usage", "anthropic", "claude-code", line)]
		if !ok || e.Acct != acct || e.AcctQ != model.AcctInferred {
			t.Errorf("%s: %+v (present %v), want acct %s", line, e, ok, acct)
		}
	}
	if _, ok := api.usage[model.EventID("usage", "anthropic", "claude-code", "s-1")]; ok {
		t.Fatal("the stopped distro was scanned")
	}
	if got := strings.Join(api.lastHomes, "|"); got != a.UserHome+"|"+extra+"|"+wslHome {
		t.Fatalf("heartbeat homes %q", got)
	}
	// Each home got its own retention fix; the stopped one was left alone.
	for _, h := range []string{a.UserHome, extra, wslHome} {
		if b, _ := os.ReadFile(filepath.Join(h, ".claude", "settings.json")); !strings.Contains(string(b), "3650") {
			t.Errorf("no config fix in %s", h)
		}
	}
	if _, err := os.Stat(filepath.Join(stopped, ".claude", "settings.json")); err == nil {
		t.Fatal("config fix written into the stopped distro")
	}
	st, _ = store.LoadState(a.Home)
	if len(st.Homes) != 3 || st.Homes[2].Kind != "wsl" || st.Homes[2].Distro != "Ubuntu-22.04" || st.Homes[2].Files != 1 || strings.Join(st.WSLSkipped, ",") != "Ubuntu-24.04" {
		t.Fatalf("state homes %+v skipped %v", st.Homes, st.WSLSkipped)
	}
	keys := map[string]bool{}
	for _, iv := range st.Accounts {
		keys[iv.Home+"="+iv.Acct] = true
	}
	if len(st.Accounts) != 3 || !keys["="+acct("uuid-1")] || !keys[extra+"="+acct("uuid-2")] || !keys[wslHome+"="+acct("uuid-3")] {
		t.Fatalf("timeline %+v", st.Accounts)
	}
	// Install fixed the OS and WSL homes; this tick fixed the new extra home.
	if st.Checks["claudeRetention"] != "ok" || st.Checks["claudeRetention[wsl:Ubuntu-22.04]"] != "ok" || st.Checks["claudeRetention[extra]"] != "fixed" {
		t.Fatalf("checks %v", st.Checks)
	}
	out.Reset()
	a.Status()
	for _, s := range []string{"wsl:Ubuntu-22.04", "Ubuntu-24.04", "not running, skipped", extra} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("status lacks %q:\n%s", s, out)
		}
	}

	// The distro stops: its cursor survives, the home is listed as skipped
	// and its checks no longer show.
	f.running = nil
	wslLog := filepath.Join(wslHome, "synthetic.log")
	if _, err := a.Tick(ctx, TickOptions{}); err != nil {
		t.Fatal(err)
	}
	st, _ = store.LoadState(a.Home)
	if _, ok := st.Cursors["claude-code"][wslLog]; !ok {
		t.Fatal("cursor of the stopped distro pruned")
	}
	if len(st.Homes) != 2 || strings.Join(st.WSLSkipped, ",") != "Ubuntu-22.04,Ubuntu-24.04" {
		t.Fatalf("after stop: homes %+v skipped %v", st.Homes, st.WSLSkipped)
	}
	// The extra home is dropped from config: its cursor goes.
	cfg.ExtraHomes = nil
	store.SaveConfig(a.Home, cfg)
	st.LastConfigCheck = time.Time{}
	store.SaveState(a.Home, st)
	if _, err := a.Tick(ctx, TickOptions{}); err != nil {
		t.Fatal(err)
	}
	st, _ = store.LoadState(a.Home)
	if _, ok := st.Cursors["claude-code"][filepath.Join(extra, "synthetic.log")]; ok {
		t.Fatal("cursor of a dropped extraHome kept")
	}
	if _, ok := st.Checks["claudeRetention[extra]"]; ok {
		t.Fatalf("stale home check kept: %v", st.Checks)
	}
	// Everything the server holds is unchanged by the re-scans.
	if len(api.usage) != len(want) {
		t.Fatalf("re-scan changed the server: %d usage events", len(api.usage))
	}
}
