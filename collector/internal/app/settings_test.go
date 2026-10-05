package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/ghpub"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

// settingsPage starts the page for a and returns its base URL.
func settingsPage(t *testing.T, a *App, changed func()) (*Settings, string) {
	s := NewSettings(a, changed)
	t.Cleanup(s.Close)
	base, err := s.URL()
	if err != nil {
		t.Fatal(err)
	}
	return s, base
}

func postJSON(t *testing.T, base, path string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, base+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", strings.SplitN(base, "/s/", 2)[0])
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func getState(t *testing.T, base string) (settingsState, string) {
	t.Helper()
	res, err := http.Get(base + "api/state")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var st settingsState
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("state %s: %v", raw, err)
	}
	return st, string(raw)
}

func waitState(t *testing.T, base, what string, ok func(settingsState) bool) settingsState {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, raw := getState(t, base)
		if ok(st) {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s: %s", what, raw)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func localKey(t *testing.T, a *App) []byte {
	k := make([]byte, 32)
	rand.Read(k)
	if err := store.SaveSecrets(a.Home, store.Secrets{K: base64.StdEncoding.EncodeToString(k)}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := store.LoadConfig(a.Home)
	cfg.Endpoint, cfg.MachineLabel = store.NoServer, "box"
	store.SaveConfig(a.Home, cfg)
	return k
}

// The page is reachable only as http://127.0.0.1:P/s/<token>/, and changes
// only from its own origin with JSON bodies.
func TestSettingsGuards(t *testing.T) {
	a, _, _ := newTestApp(t)
	k := localKey(t, a)
	var kicked atomic.Int32
	_, base := settingsPage(t, a, func() { kicked.Add(1) })
	origin, _, _ := strings.Cut(base, "/s/")
	if !strings.HasPrefix(base, "http://127.0.0.1:") {
		t.Fatalf("base %q", base)
	}

	res, err := http.Get(base)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(page), "tokenmaxr settings") || !strings.Contains(res.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("page %d %q", res.StatusCode, res.Header)
	}
	for _, f := range []string{"settings.js", "settings.css", "icon.png"} {
		if r, _ := http.Get(base + f); r.StatusCode != 200 {
			t.Fatalf("%s: %d", f, r.StatusCode)
		}
	}

	if r, _ := http.Get(origin + "/s/not-the-token/api/state"); r.StatusCode != 404 {
		t.Fatalf("wrong token: %d", r.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, base+"api/state", nil)
	req.Host = "attacker.example"
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("rebinding host: %d", r.StatusCode)
	}
	for name, hdr := range map[string][2]string{
		"no origin":      {"", "application/json"},
		"foreign origin": {"http://evil.example", "application/json"},
		"form post":      {origin, "text/plain"},
	} {
		req, _ := http.NewRequest(http.MethodPost, base+"api/sync", strings.NewReader("{}"))
		if hdr[0] != "" {
			req.Header.Set("Origin", hdr[0])
		}
		req.Header.Set("Content-Type", hdr[1])
		if r, _ := http.DefaultClient.Do(req); r.StatusCode != http.StatusForbidden {
			t.Errorf("%s: %d", name, r.StatusCode)
		}
	}
	// A plain GET of a state-changing call (a leaked URL) is refused.
	for _, p := range []string{"api/sync", "api/github/logout", "api/github/cancel", "api/server/cancel"} {
		if r, _ := http.Get(base + p); r.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("GET %s: %d", p, r.StatusCode)
		}
	}
	if kicked.Load() != 0 {
		t.Fatal("a refused request ran")
	}
	if code, _ := postJSON(t, base, "api/sync", struct{}{}); code != 200 || kicked.Load() != 1 {
		t.Fatalf("sync: %d", code)
	}

	st, raw := getState(t, base)
	if !st.SetUp || st.GitHub.On || st.Server.Enrolled || st.Server.URL != "" || st.GitHub.PublishEveryMinutes != 30 {
		t.Fatalf("state %s", raw)
	}
	if strings.Contains(raw, base64.StdEncoding.EncodeToString(k)) {
		t.Fatal("the state leaks the fleet key")
	}
}

// The whole GitHub sign-in from the page: code, the two guided steps, done;
// then the options and signing out.
func TestSettingsGitHubSignIn(t *testing.T) {
	f, exists := useFakeGitHub(t)
	a, _, _ := newTestApp(t)
	localKey(t, a)
	var opened []string
	a.OpenURL = func(u string) error { opened = append(opened, u); return nil }
	f.PollPlan = []string{"pending", "ok"}
	f.NoInstall = true
	var kicked atomic.Int32
	_, base := settingsPage(t, a, func() { kicked.Add(1) })

	if code, out := postJSON(t, base, "api/github/login", map[string]string{"label": "   "}); code != 200 {
		t.Fatalf("login: %d %v", code, out)
	}
	st := waitState(t, base, "the create step", func(s settingsState) bool {
		return s.Login != nil && strings.HasPrefix(s.Login.Step, "Create")
	})
	if st.Login.Code != "ABCD-1234" || !st.Login.Running || !strings.Contains(st.Login.StepURL, "template_name=") {
		t.Fatalf("login %+v", st.Login)
	}
	// A second click while signing in is harmless.
	if code, _ := postJSON(t, base, "api/github/login", map[string]string{}); code != 200 {
		t.Fatal("second login click")
	}
	*exists = true
	waitState(t, base, "the install step", func(s settingsState) bool {
		return s.Login != nil && strings.HasPrefix(s.Login.Step, "Install")
	})
	f.Files[ghpub.MarkerFile] = `{"tokenmaxr":1}`
	f.NoInstall = false
	st = waitState(t, base, "signed in", func(s settingsState) bool { return s.Login != nil && !s.Login.Running })
	if st.Login.Err != "" || st.Login.Result == nil || !st.GitHub.On || st.GitHub.Repo != "octo/agent-usage" || st.GitHub.Login != "octo" || st.GitHub.PagesURL == "" {
		t.Fatalf("after sign-in %+v / %+v", st.Login, st.GitHub)
	}
	if !strings.HasPrefix(st.GitHub.Label, "machine-") || kicked.Load() != 1 {
		t.Fatalf("label %q kicked %d", st.GitHub.Label, kicked.Load())
	}
	if len(opened) != 3 {
		t.Fatalf("opened %v", opened)
	}

	// Options.
	if code, out := postJSON(t, base, "api/github/options", map[string]any{"label": "desk", "publishEveryMinutes": 5}); code != 400 || !strings.Contains(out["error"].(string), "10 minutes") {
		t.Fatalf("too often: %d %v", code, out)
	}
	stt, _ := store.LoadState(a.Home)
	stt.GitHub.LastAttempt, stt.GitHub.LastPublish = time.Now(), time.Now()
	store.SaveState(a.Home, stt)
	if code, out := postJSON(t, base, "api/github/options", map[string]any{"label": "desk", "publishEveryMinutes": 60}); code != 200 {
		t.Fatalf("options: %d %v", code, out)
	}
	cfg, _ := store.LoadConfig(a.Home)
	if cfg.GitHub.Label != "desk" || cfg.GitHub.PublishEveryMinutes != 60 {
		t.Fatalf("config %+v", cfg.GitHub)
	}
	if stt, _ = store.LoadState(a.Home); !stt.GitHub.LastAttempt.IsZero() || !stt.GitHub.LastPublish.IsZero() {
		t.Fatal("a new label must publish now, meta included")
	}
	// The country is off until switched on; switching it republishes meta.
	if st, _ = getState(t, base); st.GitHub.ShowCountry || cfg.GitHub.ShowCountry {
		t.Fatal("the country must be off by default")
	}
	stt.GitHub.LastAttempt, stt.GitHub.LastPublish = time.Now(), time.Now()
	store.SaveState(a.Home, stt)
	if code, out := postJSON(t, base, "api/github/options", map[string]any{"label": "desk", "publishEveryMinutes": 60, "showCountry": true}); code != 200 {
		t.Fatalf("country option: %d %v", code, out)
	}
	if cfg, _ = store.LoadConfig(a.Home); !cfg.GitHub.ShowCountry {
		t.Fatal("country option not saved")
	}
	if stt, _ = store.LoadState(a.Home); !stt.GitHub.LastPublish.IsZero() {
		t.Fatal("a changed country choice must republish meta")
	}
	if st, _ = getState(t, base); !st.GitHub.ShowCountry {
		t.Fatal("the page must show the country choice")
	}
	// The page's checkbox starts unchecked.
	if html := embeddedSettingsPage(t); !strings.Contains(html, `<input type="checkbox" id="gh-country">`) {
		t.Fatal("the country checkbox must exist and be unchecked by default")
	}
	// No quota-meter or account-history toggles: both are always published.
	if html := embeddedSettingsPage(t); strings.Contains(html, "gh-quota") || strings.Contains(html, "gh-history") {
		t.Fatal("the quota and account-history options are gone")
	}

	// Signing out keeps the fleet key; the page offers sign-in again.
	if code, _ := postJSON(t, base, "api/github/logout", struct{}{}); code != 200 {
		t.Fatal("logout")
	}
	if st, _ = getState(t, base); st.GitHub.On || !st.SetUp {
		t.Fatalf("after logout %+v", st.GitHub)
	}
}

func TestSettingsGitHubSignInFailsAndClears(t *testing.T) {
	f, _ := useFakeGitHub(t)
	a, _, _ := newTestApp(t)
	localKey(t, a)
	a.OpenURL = func(string) error { return nil }
	f.PollPlan = []string{"denied"}
	_, base := settingsPage(t, a, nil)
	postJSON(t, base, "api/github/login", map[string]string{})
	st := waitState(t, base, "the refusal", func(s settingsState) bool { return s.Login != nil && !s.Login.Running })
	if st.Login.Err == "" || st.GitHub.On {
		t.Fatalf("denied: %+v", st.Login)
	}
	postJSON(t, base, "api/github/cancel", struct{}{}) // Back
	if st, _ = getState(t, base); st.Login != nil {
		t.Fatal("Back must clear the failed sign-in")
	}
}

// Adding a server from the page enrols this machine (join code here; the
// browser link otherwise), re-reads its history for the server, and can be
// switched off and on again without enrolling again.
func TestSettingsServer(t *testing.T) {
	api, srv := newFakeAPI(t)
	a, _, _ := newTestApp(t)
	localKey(t, a)
	ctx := context.Background()
	if _, err := a.Tick(ctx, TickOptions{}); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.LoadState(a.Home); len(st.Cursors) == 0 {
		t.Fatal("the local tick read nothing")
	}
	opened := 0
	a.OpenURL = func(string) error { opened++; return nil }
	_, base := settingsPage(t, a, nil)

	if code, _ := postJSON(t, base, "api/server", map[string]string{"server": "not a url"}); code != 400 {
		t.Fatal("bad URL accepted")
	}
	if code, out := postJSON(t, base, "api/server", map[string]string{"server": srv.URL, "join": "D0M1-first"}); code != 200 {
		t.Fatalf("connect: %v", out)
	}
	st := waitState(t, base, "connected", func(s settingsState) bool { return s.Connect != nil && !s.Connect.Running })
	if st.Connect.Err != "" || !st.Server.Enrolled || st.Server.URL != srv.URL || st.Server.MachineID != "m_first" {
		t.Fatalf("connect %+v server %+v", st.Connect, st.Server)
	}
	if s, _ := store.LoadState(a.Home); len(s.Cursors) != 0 {
		t.Fatal("history must be re-read for the new server")
	}
	if _, err := a.Tick(ctx, TickOptions{}); err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	got := len(api.usage)
	api.mu.Unlock()
	if got == 0 {
		t.Fatal("the server got no history")
	}

	if code, _ := postJSON(t, base, "api/server", map[string]string{"server": "off"}); code != 200 {
		t.Fatal("off")
	}
	if cfg, _ := store.LoadConfig(a.Home); !cfg.ServerOff || cfg.Endpoint != srv.URL {
		t.Fatalf("off must keep the URL: %+v", cfg)
	}
	if st, _ = getState(t, base); st.Server.Enrolled || st.Server.URL != "" {
		t.Fatalf("off: %+v", st.Server)
	}
	if sec, _ := store.LoadSecrets(a.Home); !sec.Enrolled() {
		t.Fatal("switching off dropped the enrolment")
	}
	// Back on: the same server, no browser, no new enrolment.
	postJSON(t, base, "api/server", map[string]string{"server": srv.URL})
	st = waitState(t, base, "back on", func(s settingsState) bool { return s.Connect != nil && !s.Connect.Running && s.Server.Enrolled })
	if opened != 0 || st.Server.MachineID != "m_first" {
		t.Fatalf("back on: opened %d %+v", opened, st.Server)
	}
}

func TestFirstRunSettings(t *testing.T) {
	a, _, _ := newTestApp(t)
	localKey(t, a)
	if !a.needsFirstRunSettings() {
		t.Fatal("a machine publishing nowhere should see the settings once")
	}
	a.markFirstRunSettings()
	if a.needsFirstRunSettings() {
		t.Fatal("only once")
	}
	b, _, _ := newTestApp(t)
	f, _ := useFakeGitHub(t)
	githubOnly(t, b, f)
	if b.needsFirstRunSettings() {
		t.Fatal("a GitHub machine needs no first-run settings")
	}
}

func TestDesktopOpensSettings(t *testing.T) {
	a, _, _ := newTestApp(t)
	localKey(t, a)
	d := a.newDesktop(context.Background())
	var got string
	d.open = func(u string) error { got = u; return nil }
	d.showSettings() // headless: the page
	if !strings.HasPrefix(got, "http://127.0.0.1:") || !strings.Contains(got, "/s/") {
		t.Fatalf("opened %q", got)
	}
	if r, err := http.Get(got + "api/state"); err != nil || r.StatusCode != 200 {
		t.Fatalf("page not served: %v", err)
	}
	d.stop()
	if _, err := http.Get(got + "api/state"); err == nil {
		t.Fatal("the page outlived the app")
	}
}

// embeddedSettingsPage is the settings page as the binary serves it.
func embeddedSettingsPage(t *testing.T) string {
	t.Helper()
	b, err := settingsFiles.ReadFile("settings/index.html")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The page fetches the dashboard's owner key (to hand to the dashboard it
// opened, or for an unlock link) only with a POST from itself, and only when
// this machine publishes to GitHub with a dashboard and a fleet key. The key
// never shows in the page's state.
func TestSettingsUnlockKey(t *testing.T) {
	a, _, _ := newTestApp(t)
	k := localKey(t, a)
	_, base := settingsPage(t, a, nil)
	origin, _, _ := strings.Cut(base, "/s/")
	want := base64.RawURLEncoding.EncodeToString(ghpub.OwnerKey(k))
	unlock := func() (int, map[string]any) { return postJSON(t, base, "api/github/unlock", map[string]any{}) }

	// Not publishing to GitHub.
	if code, out := unlock(); code != 400 || out["key"] != nil || !strings.Contains(fmt.Sprint(out["error"]), "not publishing") {
		t.Fatalf("not publishing: %d %v", code, out)
	}
	sec, _ := store.LoadSecrets(a.Home)
	sec.GitHub = &store.GitHubSecrets{Token: "ghu_test", Login: "octo", UserID: 42}
	store.SaveSecrets(a.Home, sec)
	cfg, _ := store.LoadConfig(a.Home)
	cfg.GitHub = &store.GitHubConfig{Repo: "octo/agent-usage", Label: "laptop"}
	store.SaveConfig(a.Home, cfg)
	// No dashboard address yet.
	if code, out := unlock(); code != 400 || out["key"] != nil || !strings.Contains(fmt.Sprint(out["error"]), "no address") {
		t.Fatalf("no dashboard: %d %v", code, out)
	}
	if st, _ := getState(t, base); st.GitHub.CanUnlock {
		t.Fatal("offered without a dashboard")
	}
	st, _ := store.LoadState(a.Home)
	st.GitHub.PagesURL = "https://octo.github.io/agent-usage/"
	store.SaveState(a.Home, st)
	got, raw := getState(t, base)
	if !got.GitHub.CanUnlock || strings.Contains(raw, want) {
		t.Fatalf("state %s", raw)
	}
	if code, out := unlock(); code != 200 || out["key"] != want {
		t.Fatalf("unlock: %d %v", code, out)
	}

	// Only a POST from the page itself: not a GET, another origin, a form
	// post or another token.
	if r, _ := http.Get(base + "api/github/unlock"); r.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", r.StatusCode)
	}
	for name, mod := range map[string]func(*http.Request){
		"no origin":      func(r *http.Request) { r.Header.Del("Origin") },
		"another origin": func(r *http.Request) { r.Header.Set("Origin", "https://octo.github.io") },
		"form":           func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") },
	} {
		req, _ := http.NewRequest(http.MethodPost, base+"api/github/unlock", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		mod(req)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusForbidden || strings.Contains(string(body), want) {
			t.Fatalf("%s: %d %s", name, res.StatusCode, body)
		}
	}
	if code, _ := postJSON(t, origin+"/s/not-the-token/", "api/github/unlock", map[string]any{}); code != 404 {
		t.Fatalf("wrong token: %d", code)
	}

	// No fleet key.
	sec, _ = store.LoadSecrets(a.Home)
	sec.K = ""
	store.SaveSecrets(a.Home, sec)
	if code, out := unlock(); code != 400 || out["key"] != nil {
		t.Fatalf("no fleet key: %d %v", code, out)
	}
	if st, _ := getState(t, base); st.GitHub.CanUnlock {
		t.Fatal("offered without a fleet key")
	}
	// A dashboard address that is not https is never handed the key.
	sec.K = base64.StdEncoding.EncodeToString(k)
	store.SaveSecrets(a.Home, sec)
	st.GitHub.PagesURL = "http://octo.github.io/agent-usage/"
	store.SaveState(a.Home, st)
	if code, _ := unlock(); code != 400 {
		t.Fatalf("http dashboard: %d", code)
	}
}
