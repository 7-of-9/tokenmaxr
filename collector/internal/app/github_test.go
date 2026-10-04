package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
	"github.com/7-of-9/tokenmaxr/collector/internal/ghapi"
	"github.com/7-of-9/tokenmaxr/collector/internal/ghapi/ghapitest"
	"github.com/7-of-9/tokenmaxr/collector/internal/ghpub"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/rollup"
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
