package app

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
	"github.com/7-of-9/tokenmaxr/collector/internal/ghpub"
	"github.com/7-of-9/tokenmaxr/collector/internal/instance"
	"github.com/7-of-9/tokenmaxr/collector/internal/joincode"
	"github.com/7-of-9/tokenmaxr/collector/internal/lock"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
	"github.com/7-of-9/tokenmaxr/collector/internal/tray"
)

// settingsIcon is the page's header icon: the app sheet's header icon (the
// heatmap tile with its green dot), made once.
var settingsIcon = sync.OnceValue(func() []byte { return tray.BrandPNG(tray.Green, 128) })

//go:embed settings
var settingsFiles embed.FS

// Settings is the local settings page: where this machine publishes (GitHub,
// a server, or nowhere), its public label and how often it publishes. It is
// served on 127.0.0.1 under /s/<token>/ while the app runs: the token is
// random per run and never saved, requests must name the loopback host (no
// DNS rebinding), and changes must come from the page itself (same origin,
// JSON bodies), so other local pages cannot drive it.
type Settings struct {
	a *App
	// changed runs after a change that should publish or upload soon.
	changed func()
	// restart, when set (the desktop app with a UI), restarts the app into
	// the display mode config.json now says (a trayOnly change).
	restart func()

	mu      sync.Mutex
	srv     *http.Server
	origin  string
	token   string
	login   *webLogin
	connect *webTask
	ctx     context.Context
	stop    context.CancelFunc
}

// webLogin is a GitHub sign-in driven from the page (GitHubLoginUI).
type webLogin struct {
	Running  bool               `json:"running"`
	Code     string             `json:"code,omitempty"`
	CodeURL  string             `json:"codeUrl,omitempty"`
	Step     string             `json:"step,omitempty"`
	StepURL  string             `json:"stepUrl,omitempty"`
	Progress []string           `json:"progress,omitempty"`
	Err      string             `json:"error,omitempty"`
	Result   *GitHubLoginResult `json:"result,omitempty"`
	cancel   context.CancelFunc
}

// webTask is a server connection in progress (the browser approves it).
type webTask struct {
	Running bool   `json:"running"`
	Server  string `json:"server"`
	Err     string `json:"error,omitempty"`
	Done    bool   `json:"done,omitempty"`
	cancel  context.CancelFunc
}

// NewSettings makes the settings page for a; changed (may be nil) runs after
// a change that should reach a destination soon.
func NewSettings(a *App, changed func()) *Settings {
	ctx, stop := context.WithCancel(context.Background())
	return &Settings{a: a, changed: changed, ctx: ctx, stop: stop}
}

// URL starts the page's server if needed and returns its address.
func (s *Settings) URL() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv == nil {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return "", fmt.Errorf("settings: %w", err)
		}
		b := make([]byte, 24)
		if _, err := rand.Read(b); err != nil {
			ln.Close()
			return "", err
		}
		s.token = base64.RawURLEncoding.EncodeToString(b)
		s.origin = "http://" + ln.Addr().String()
		s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
		go s.srv.Serve(ln)
		s.a.Log.Printf("settings: serving on %s", s.origin)
	}
	return s.origin + "/s/" + s.token + "/", nil
}

// Close stops the page and anything it started.
func (s *Settings) Close() {
	s.stop()
	s.mu.Lock()
	srv := s.srv
	s.srv = nil
	s.mu.Unlock()
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
}

func (s *Settings) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	origin, token := s.origin, s.token
	s.mu.Unlock()
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	if "http://"+r.Host != origin {
		http.Error(w, "wrong host", http.StatusMisdirectedRequest)
		return
	}
	prefix := "/s/" + token + "/"
	rest, ok := strings.CutPrefix(r.URL.Path, prefix)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPost {
		if r.Header.Get("Origin") != origin || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	} else if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	} else if strings.HasPrefix(rest, "api/") && rest != "api/state" {
		// Every API call but the state read changes something: POST only,
		// so the Origin and Content-Type checks above apply (a plain GET of
		// a leaked URL must not stop publishing).
		http.Error(w, "use POST", http.StatusMethodNotAllowed)
		return
	}
	switch rest {
	case "", "index.html":
		s.file(w, "settings/index.html", "text/html; charset=utf-8")
	case "settings.js":
		s.file(w, "settings/settings.js", "text/javascript; charset=utf-8")
	case "settings.css":
		s.file(w, "settings/settings.css", "text/css; charset=utf-8")
	case "icon.png":
		// The app's colour icon for the page's header (tray.BrandIcon).
		w.Header().Set("Content-Type", "image/png")
		w.Write(settingsIcon())
	case "api/state":
		v, err := s.state()
		s.reply(w, v, err)
	case "api/github/login":
		var in struct{ Label string }
		if s.decode(w, r, &in) {
			s.reply(w, nil, s.startLogin(in.Label))
		}
	case "api/github/cancel":
		s.cancelLogin()
		s.reply(w, nil, nil)
	case "api/github/logout":
		s.reply(w, nil, s.a.GitHubLogout())
	case "api/github/unlock":
		// The dashboard's owner key, for the page to hand to the dashboard
		// it opened (Open dashboard signs it in), or to copy as a sign-in
		// link. Never logged.
		var in struct{}
		if s.decode(w, r, &in) {
			key, err := s.unlockKey()
			s.reply(w, map[string]string{"key": key}, err)
		}
	case "api/github/options":
		var in struct {
			Label               string
			PublishEveryMinutes int
			ShowCountry         bool
			ShareWithFleet      *bool // absent: unchanged
		}
		if s.decode(w, r, &in) {
			s.reply(w, nil, s.saveGitHubOptions(in.Label, in.PublishEveryMinutes, in.ShowCountry, in.ShareWithFleet))
		}
	case "api/server":
		var in struct{ Server, Join string }
		if s.decode(w, r, &in) {
			s.reply(w, nil, s.startConnect(in.Server, in.Join))
		}
	case "api/server/cancel":
		s.mu.Lock()
		if s.connect != nil && s.connect.cancel != nil {
			s.connect.cancel()
		}
		s.mu.Unlock()
		s.reply(w, nil, nil)
	case "api/app":
		var in struct{ TrayOnly bool }
		if s.decode(w, r, &in) {
			restart, err := s.saveAppOptions(in.TrayOnly)
			s.reply(w, map[string]bool{"ok": err == nil, "restart": restart}, err)
			if restart {
				// After the reply: the page learns why it goes quiet.
				time.AfterFunc(restartDelay, s.restart)
			}
		}
	case "api/sync":
		if s.changed != nil {
			s.changed()
		}
		s.reply(w, nil, nil)
	default:
		http.NotFound(w, r)
	}
}

func (s *Settings) file(w http.ResponseWriter, name, ctype string) {
	b, err := fs.ReadFile(settingsFiles, name)
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Write(b)
}

func (s *Settings) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "use POST", http.StatusMethodNotAllowed)
		return false
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(v); err != nil {
		s.reply(w, nil, fmt.Errorf("bad request: %w", err))
		return false
	}
	return true
}

func (s *Settings) reply(w http.ResponseWriter, v any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	if v == nil {
		v = map[string]bool{"ok": true}
	}
	json.NewEncoder(w).Encode(v)
}

// settingsState is what the page shows. It never carries a token or key.
type settingsState struct {
	Version   string `json:"version"`
	BuildTime string `json:"buildTime,omitempty"`
	Machine   string `json:"machine"`
	SetUp     bool   `json:"setUp"`
	Fleet     string `json:"fleet,omitempty"`
	GitHub    struct {
		On                  bool   `json:"on"`
		Repo                string `json:"repo,omitempty"`
		Login               string `json:"login,omitempty"`
		Label               string `json:"label,omitempty"`
		PublishEveryMinutes int    `json:"publishEveryMinutes"`
		ShowCountry         bool   `json:"showCountry"`
		// ShowCountryFrom: the label of the machine whose choice the option
		// follows ("" when chosen here).
		ShowCountryFrom string `json:"showCountryFrom,omitempty"`
		LastPublish     string `json:"lastPublish,omitempty"`
		LastError       string `json:"lastError,omitempty"`
		PagesURL        string `json:"pagesUrl,omitempty"`
		// CanUnlock: the dashboard and the fleet key are there, so Open
		// dashboard signs it in (api/github/unlock).
		CanUnlock bool   `json:"canUnlock"`
		App       string `json:"app"`
		// Adopted: signed in through the fleet (the sign-in Login shared).
		Adopted bool `json:"adopted"`
		// CanShare: the sign-in is this machine's own and a server carries
		// it to the fleet; ShareWithFleet is the choice.
		CanShare       bool   `json:"canShare"`
		ShareWithFleet bool   `json:"shareWithFleet"`
		Fleet          string `json:"fleet,omitempty"`
		FleetError     string `json:"fleetError,omitempty"`
	} `json:"github"`
	Server struct {
		URL        string `json:"url,omitempty"`
		Default    string `json:"default,omitempty"`
		Enrolled   bool   `json:"enrolled"`
		MachineID  string `json:"machineId,omitempty"`
		LastUpload string `json:"lastUpload,omitempty"`
		LastError  string `json:"lastError,omitempty"`
	} `json:"server"`
	// App is how the desktop app shows itself.
	App struct {
		// TrayOnly: no main window, taskbar button or Dock icon.
		TrayOnly bool `json:"trayOnly"`
		// CanSwitch: this is the desktop app with a UI, which restarts in
		// the other mode (headless runs have no UI to switch).
		CanSwitch bool `json:"canSwitch"`
		// Tray names the tray: "system tray" or "menu bar".
		Tray string `json:"tray"`
	} `json:"app"`
	Login   *webLogin `json:"login,omitempty"`
	Connect *webTask  `json:"connect,omitempty"`
}

func rfc(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// retryRead retries a read that met a file mid-replace (Windows refuses to
// open a file while an atomic write renames over it).
func retryRead[T any](load func(string) (T, error), home string) (T, error) {
	v, err := load(home)
	for i := 0; err != nil && i < 10; i++ {
		time.Sleep(20 * time.Millisecond)
		v, err = load(home)
	}
	return v, err
}

func (s *Settings) state() (settingsState, error) {
	a := s.a
	var v settingsState
	// The tasks first: one seen finished has saved its files already.
	s.mu.Lock()
	if s.login != nil {
		l := *s.login
		v.Login = &l
	}
	if s.connect != nil {
		c := *s.connect
		v.Connect = &c
	}
	s.mu.Unlock()
	cfg, err := retryRead(store.LoadConfig, a.Home)
	if err != nil {
		return v, err
	}
	sec, err := retryRead(store.LoadSecrets, a.Home)
	if err != nil {
		return v, err
	}
	st, _ := retryRead(store.LoadState, a.Home)
	v.Version, v.BuildTime, v.Machine, v.SetUp = a.Version, a.BuildTime, cfg.MachineLabel, sec.HasFleet()
	v.App.TrayOnly, v.App.CanSwitch, v.App.Tray = cfg.TrayOnly, s.restart != nil, trayName()
	if k := sec.Key(); k != nil {
		v.Fleet = fleetShort(k)
	}
	g := &v.GitHub
	g.App = buildinfo.GitHubAppSlug
	g.PublishEveryMinutes = int((&store.GitHubConfig{}).PublishEvery() / time.Minute)
	if githubEnabled(&cfg, sec) {
		g.On, g.Repo, g.Login, g.Label = true, cfg.GitHub.Repo, sec.GitHub.Login, cfg.GitHub.Label
		g.PublishEveryMinutes = int(cfg.GitHub.PublishEvery() / time.Minute)
		g.ShowCountry = cfg.GitHub.ShowCountry
		g.ShowCountryFrom = cfg.GitHub.PrefFrom(store.PrefShowCountry)
		g.Adopted = cfg.GitHub.Adopted
		g.CanShare, g.ShareWithFleet = !g.Adopted && serverOn(&cfg, sec), cfg.GitHub.SharesWithFleet()
	}
	// The fleet line only where it adds something: the repository line
	// already says whose sign-in an adopted machine uses.
	if !g.Adopted {
		g.Fleet = fleetLine(&cfg, sec, st)
	}
	switch {
	case g.On || !serverOn(&cfg, sec):
	case cfg.GitHubFleetOptOut:
		g.Fleet = "Opted out of your fleet's shared sign-in."
	default:
		g.Fleet = "Signing in on one machine publishes your others too."
	}
	if st != nil {
		g.LastPublish, g.LastError, g.PagesURL = rfc(st.GitHub.LastPublish), st.GitHub.LastError, st.GitHub.PagesURL
		g.FleetError = st.GitHub.Fleet.LastError
		g.CanUnlock = g.On && dashboardURL(g.PagesURL) && sec.Key() != nil
	}
	sv := &v.Server
	sv.URL, sv.Default = cfg.Server(), store.DefaultEndpoint
	if serverOn(&cfg, sec) {
		sv.Enrolled, sv.MachineID = true, sec.MachineID
		if st != nil {
			sv.LastUpload, sv.LastError = rfc(st.LastUploadOK), st.LastUploadErr
		}
	}
	return v, nil
}

// dashboardURL: u is an https address the page may open and hand the key to.
func dashboardURL(u string) bool {
	p, err := url.Parse(u)
	return err == nil && p.Scheme == "https" && p.Host != "" && p.User == nil
}

// unlockKey is the owner key that signs the owner in to this machine's
// GitHub dashboard (ghpub.OwnerKey of the fleet key, base64url; the
// dashboard's code calls it unlocking): the page hands it only to the
// dashboard it opened, or copies it into a sign-in link.
func (s *Settings) unlockKey() (string, error) {
	cfg, err := retryRead(store.LoadConfig, s.a.Home)
	if err != nil {
		return "", err
	}
	sec, err := retryRead(store.LoadSecrets, s.a.Home)
	if err != nil {
		return "", err
	}
	st, err := retryRead(store.LoadState, s.a.Home)
	switch {
	case err != nil:
		return "", err
	case !githubEnabled(&cfg, sec):
		return "", errors.New("not publishing to GitHub")
	case !dashboardURL(st.GitHub.PagesURL):
		return "", errors.New("the dashboard has no address yet (its first build takes a few minutes)")
	case sec.Key() == nil:
		return "", errors.New("this machine has no fleet key")
	}
	return base64.RawURLEncoding.EncodeToString(ghpub.OwnerKey(sec.Key())), nil
}

func fleetShort(k []byte) string {
	f := model.KFingerprint(k)
	return f[:min(8, len(f))]
}

// GitHubLoginUI for the page: each call updates what the page polls.
func (s *Settings) Code(code, url string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.login != nil {
		s.login.Code, s.login.CodeURL = code, url
	}
}

func (s *Settings) Step(text, url string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.login != nil {
		s.login.Step, s.login.StepURL = text, url
	}
}

func (s *Settings) Progress(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.login != nil {
		s.login.Progress = append(s.login.Progress, text)
	}
}

func (s *Settings) startLogin(label string) error {
	label = strings.TrimSpace(label)
	if label != "" {
		if label = CleanLabel(label); label == "" {
			return errors.New("the label needs at least one visible character")
		}
	}
	s.mu.Lock()
	if s.login != nil && s.login.Running {
		s.mu.Unlock()
		return nil // already signing in: the page shows it
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.login = &webLogin{Running: true, cancel: cancel}
	s.mu.Unlock()
	go func() {
		defer cancel()
		res, err := s.a.GitHubLogin(ctx, s, label)
		s.mu.Lock()
		s.login.Running = false
		switch {
		case errors.Is(err, context.Canceled):
			s.login.Err = "Sign-in cancelled."
		case err != nil:
			s.login.Err = githubErrorText(err)
		default:
			s.login.Result = &res
		}
		s.mu.Unlock()
		if err != nil {
			s.a.Log.Printf("settings: github sign-in: %v", err)
			return
		}
		if s.changed != nil {
			s.changed()
		}
	}()
	return nil
}

// cancelLogin stops a sign-in in progress, or clears a finished one.
func (s *Settings) cancelLogin() {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.login == nil:
	case s.login.Running:
		s.login.cancel()
	default:
		s.login = nil
	}
}

// saveGitHubOptions changes the GitHub label, cadence, the country opt-in
// and (nil: unchanged) whether this machine's own sign-in is shared with
// the fleet. A new label or choice is published with the next publish,
// which is made due now; a sharing change reaches the server with the next
// tick. On an adopted sign-in, the country changed here is this machine's
// own from then on: the sharer's choice no longer changes it.
func (s *Settings) saveGitHubOptions(label string, every int, showCountry bool, share *bool) error {
	a := s.a
	label = strings.TrimSpace(label)
	if label != "" {
		if label = CleanLabel(label); label == "" {
			return errors.New("the label needs at least one visible character")
		}
	}
	if every != 0 && (every < 10 || every > 24*60) {
		return errors.New("publish every 10 minutes to 24 hours")
	}
	lk, err := lock.Acquire(paths.Lock(a.Home), settingsLockWait)
	if err != nil {
		return fmt.Errorf("the collector is busy; try again in a minute (%w)", err)
	}
	defer lk.Release()
	cfg, err := store.LoadConfig(a.Home)
	if err != nil {
		return err
	}
	if cfg.GitHub == nil {
		return errors.New("not publishing to GitHub")
	}
	relabel := label != "" && label != cfg.GitHub.Label
	if label != "" {
		cfg.GitHub.Label = label
	}
	if every != 0 {
		cfg.GitHub.PublishEveryMinutes = every
	}
	recountry := showCountry != cfg.GitHub.ShowCountry
	if cfg.GitHub.Adopted && recountry {
		cfg.GitHub.SetLocalPref(store.PrefShowCountry)
	}
	cfg.GitHub.ShowCountry = showCountry
	reshare := share != nil && *share != cfg.GitHub.SharesWithFleet() && !cfg.GitHub.Adopted
	if reshare {
		on := *share
		cfg.GitHub.ShareWithFleet = &on
	}
	if err := store.SaveConfig(a.Home, cfg); err != nil {
		return err
	}
	st, err := store.LoadState(a.Home)
	if err != nil {
		return err
	}
	// Due now; a new label refreshes meta.json even without new data.
	st.GitHub.LastAttempt = time.Time{}
	if relabel || recountry {
		st.GitHub.LastPublish = time.Time{}
	}
	if reshare {
		st.GitHub.Fleet.LastError = "" // share or withdraw with the next tick
	}
	if err := store.SaveState(a.Home, st); err != nil {
		return err
	}
	lk.Release()
	if s.changed != nil {
		s.changed()
	}
	return nil
}

// restartDelay lets the page read the reply before the app restarts.
var restartDelay = 300 * time.Millisecond

// saveAppOptions saves the display mode: trayOnly runs the app in the
// tray / menu bar alone. A change restarts the app into it (restart).
func (s *Settings) saveAppOptions(trayOnly bool) (restart bool, err error) {
	a := s.a
	lk, err := lock.Acquire(paths.Lock(a.Home), settingsLockWait)
	if err != nil {
		return false, fmt.Errorf("the collector is busy; try again in a minute (%w)", err)
	}
	defer lk.Release()
	cfg, err := store.LoadConfig(a.Home)
	if err != nil {
		return false, err
	}
	if cfg.TrayOnly == trayOnly {
		return false, nil
	}
	cfg.TrayOnly = trayOnly
	if err := store.SaveConfig(a.Home, cfg); err != nil {
		return false, err
	}
	a.Log.Printf("settings: tray only %v", trayOnly)
	return s.restart != nil, nil
}

// trayName is what the settings page calls the tray.
func trayName() string {
	if runtime.GOOS == "darwin" {
		return "menu bar"
	}
	return "system tray"
}

// settingsLockWait bounds a settings change's wait for a running tick.
var settingsLockWait = 30 * time.Second

// startConnect points this machine at a server (enrolling through the
// browser, or with a join code) or, with "off", stops using one.
func (s *Settings) startConnect(server, join string) error {
	server, err := store.ParseServer(server)
	if err != nil {
		return err
	}
	if join = strings.TrimSpace(join); join != "" {
		if _, _, err := joincode.Parse(join); err != nil {
			return err
		}
	}
	if server == store.NoServer {
		return s.a.SetServer(context.Background(), server, "")
	}
	s.mu.Lock()
	if s.connect != nil && s.connect.Running {
		s.mu.Unlock()
		return errors.New("already connecting")
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.connect = &webTask{Running: true, Server: server, cancel: cancel}
	s.mu.Unlock()
	go func() {
		defer cancel()
		err := s.a.SetServer(ctx, server, join)
		s.mu.Lock()
		s.connect.Running, s.connect.Done = false, err == nil
		if errors.Is(err, context.Canceled) {
			s.connect.Err = "Cancelled."
		} else if err != nil {
			s.connect.Err = err.Error()
		}
		s.mu.Unlock()
		if err == nil && s.changed != nil {
			s.changed()
		}
	}()
	return nil
}

// SetServer makes server this machine's server: "off" stops uploading (the
// enrolment is kept); a server it is enrolled with is switched back on; any
// other is enrolled with, through the owner's browser (linkJoin) unless a
// join code is given. A server that starts receiving re-reads the history,
// since nothing was queued for it while it was off.
func (a *App) SetServer(ctx context.Context, server, join string) error {
	if server == store.NoServer {
		lk, err := lock.Acquire(paths.Lock(a.Home), settingsLockWait)
		if err != nil {
			return fmt.Errorf("the collector is busy; try again in a minute (%w)", err)
		}
		defer lk.Release()
		cfg, err := store.LoadConfig(a.Home)
		if err != nil {
			return err
		}
		if cfg.Server() == "" {
			return nil
		}
		cfg.ServerOff = true
		a.Log.Printf("settings: server off")
		return store.SaveConfig(a.Home, cfg)
	}
	cfg, err := store.LoadConfig(a.Home)
	if err != nil {
		return err
	}
	sec, err := store.LoadSecrets(a.Home)
	if err != nil {
		return err
	}
	st, _ := store.LoadState(a.Home)
	// Enrolled with this server before (and not rejected): switch it on.
	sameServer := sec.Enrolled() && cfg.Endpoint == server && (st == nil || !st.Unauthorized)
	if serverOn(&cfg, sec) && cfg.Server() == server && join == "" {
		return nil
	}
	wasOn := serverOn(&cfg, sec)
	var invite string
	var k []byte
	if !sameServer || join != "" {
		if join == "" {
			if join, err = a.linkJoin(ctx, server); err != nil {
				return err
			}
		}
		if invite, k, err = joincode.Parse(join); err != nil {
			return err
		}
	}
	lk, err := lock.Acquire(paths.Lock(a.Home), settingsLockWait)
	if err != nil {
		return fmt.Errorf("the collector is busy; try again in a minute (%w)", err)
	}
	defer lk.Release()
	if cfg, err = store.LoadConfig(a.Home); err != nil {
		return err
	}
	if sec, err = store.LoadSecrets(a.Home); err != nil {
		return err
	}
	if st, err = store.LoadState(a.Home); err != nil {
		return err
	}
	if invite != "" {
		if sec, err = a.enrolWith(ctx, server, cfg.MachineLabel, invite, k, sec, st); err != nil {
			return err
		}
	}
	if !wasOn || cfg.Endpoint != server {
		// Nothing was queued for this server: it gets the whole history.
		st.Cursors = map[string]map[string]store.FileCursor{}
	}
	cfg.Endpoint, cfg.ServerOff = server, false
	if err := store.SaveConfig(a.Home, cfg); err != nil {
		return err
	}
	a.Log.Printf("settings: server %s on", server)
	return store.SaveState(a.Home, st)
}

// firstRunMark records that the settings page opened by itself once.
func firstRunMark(home string) string { return filepath.Join(home, "settings-shown") }

// needsFirstRunSettings: a machine that publishes nowhere opens the settings
// page once, so its owner can pick GitHub (or a server).
func (a *App) needsFirstRunSettings() bool {
	if _, err := os.Stat(firstRunMark(a.Home)); err == nil {
		return false
	}
	cfg, err := store.LoadConfig(a.Home)
	if err != nil {
		return false
	}
	sec, err := store.LoadSecrets(a.Home)
	if err != nil {
		return false
	}
	return !githubEnabled(&cfg, sec) && !serverOn(&cfg, sec)
}

func (a *App) markFirstRunSettings() {
	_ = os.WriteFile(firstRunMark(a.Home), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600)
}

// OpenSettingsCLI asks the running app to show its settings: the Settings
// section of its window (tray only: the popup), or the settings page when
// it runs headless (the page lives in the app: it drives its sign-in).
func (a *App) OpenSettingsCLI() error {
	if !instance.Running(a.Home) {
		return errors.New("the app is not running: start it (" + buildinfo.Product + " app), then open Settings from its icon")
	}
	if err := instance.Send(a.Home, instance.Settings); err != nil {
		return err
	}
	a.printf(buildinfo.Product + " is showing its settings.\n")
	return nil
}
