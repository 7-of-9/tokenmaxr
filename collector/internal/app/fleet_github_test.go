package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/fleetshare"
	"github.com/7-of-9/tokenmaxr/collector/internal/ghapi/ghapitest"
	"github.com/7-of-9/tokenmaxr/collector/internal/ghpub"
	"github.com/7-of-9/tokenmaxr/collector/internal/joincode"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

// fleetFixture is a fleet on the fake server: machine a ("m_first") signed
// in to the fake GitHub, so its sign-in is shared; more machines join with
// join(). Every machine runs on the shared clock *now.
type fleetFixture struct {
	gh  *ghapitest.Fake
	api *fakeAPI
	srv *httptest.Server
	a   *App
	k   []byte
	now *time.Time
}

func newFleet(t *testing.T) *fleetFixture {
	t.Helper()
	gh, _ := useFakeGitHub(t)
	api, srv := newFakeAPI(t)
	now := time.Now()
	fx := &fleetFixture{gh: gh, api: api, srv: srv, now: &now}
	a, _, out := newTestApp(t)
	a.Now = fx.clock
	a.OpenURL = func(string) error { return nil }
	ctx := context.Background()
	if err := a.Install(ctx, InstallOptions{Join: "D0M1-first", Endpoint: srv.URL, Label: "studio", Yes: true, NoAutostart: true}); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	gh.PollPlan = []string{"ok"}
	gh.Files[ghpub.MarkerFile] = `{"tokenmaxr":1}`
	res, err := a.GitHubLogin(ctx, &loginUI{onStep: func(string) { t.Fatal("no step expected") }}, "desk")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Shared {
		t.Fatalf("the sign-in was not shared: %+v", res)
	}
	fx.a, fx.k = a, mustKey(t, a)
	return fx
}

func (fx *fleetFixture) clock() time.Time { return *fx.now }

func (fx *fleetFixture) advance(d time.Duration) { *fx.now = fx.now.Add(d) }

// join enrols another machine of the fleet (the same fleet key) with label as
// its public name on the server.
func (fx *fleetFixture) join(t *testing.T, invite, label string) *App {
	t.Helper()
	b, _, out := newTestApp(t)
	b.Now = fx.clock
	if err := b.Install(context.Background(), InstallOptions{Join: joincode.Format(invite, fx.k), Endpoint: fx.srv.URL, Label: label, Yes: true, NoAutostart: true}); err != nil {
		t.Fatalf("join: %v\n%s", err, out)
	}
	return b
}

func tick(t *testing.T, a *App, o TickOptions) TickReport {
	t.Helper()
	rep, err := a.Tick(context.Background(), o)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	return rep
}

func (f *fakeAPI) fleetCounts() (gets, puts, deletes int, share *fakeFleetGitHub) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fleetGitHub != nil {
		c := *f.fleetGitHub
		share = &c
	}
	return f.fleetGets, f.fleetPuts, f.fleetDeletes, share
}

// statusOf is a's status output, its whitespace collapsed.
func statusOf(t *testing.T, a *App) string {
	t.Helper()
	var buf bytes.Buffer
	prev := a.Out
	a.Out = &buf
	defer func() { a.Out = prev }()
	if err := a.Status(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(strings.Fields(buf.String()), " ") // rows wrap
}

// Signing in shares the sign-in through the server, sealed with the fleet
// key; a second machine of the fleet adopts it, rebuilds its rollup from all
// history without sending anything to the server again, and publishes its own
// machine folder under its existing public name.
func TestFleetSharesTheSignInAndAnotherMachineAdoptsIt(t *testing.T) {
	fx := newFleet(t)
	_, puts, _, share := fx.api.fleetCounts()
	if puts != 1 || share == nil || share.By != "m_first" {
		t.Fatalf("share after sign-in: %d puts, %+v", puts, share)
	}
	raw, _ := base64.StdEncoding.DecodeString(share.Blob)
	if strings.Contains(share.Blob, "ghu_test") || bytes.Contains(raw, []byte("ghu_test")) || bytes.Contains(raw, []byte("agent-usage")) {
		t.Fatal("the server can read the shared sign-in")
	}
	s, err := fleetshare.Open(fx.k, share.Blob)
	if err != nil || s.Token != "ghu_test" || s.Login != "octo" || s.UserID != 42 || s.Repo != "octo/agent-usage" {
		t.Fatalf("shared %+v: %v", s, err)
	}
	stA, _ := store.LoadState(fx.a.Home)
	if stA.GitHub.Fleet.Shared == "" || stA.GitHub.Fleet.LastError != "" {
		t.Fatalf("sharer state %+v", stA.GitHub.Fleet)
	}
	if b, _ := os.ReadFile(paths.State(fx.a.Home)); bytes.Contains(b, []byte("ghu_test")) {
		t.Fatal("state.json holds the token")
	}
	if out := statusOf(t, fx.a); !strings.Contains(out, "sign-in shared with your other machines") {
		t.Fatalf("sharer status:\n%s", out)
	}
	// The sharer's ticks do not share an unchanged sign-in again within a day.
	tick(t, fx.a, TickOptions{})
	if _, puts, _, _ = fx.api.fleetCounts(); puts != 1 {
		t.Fatalf("re-shared an unchanged sign-in: %d puts", puts)
	}

	b := fx.join(t, "second", "Laptop B")
	rep := tick(t, b, TickOptions{}) // uploads its history, then adopts
	if rep.Upload.Err != nil || rep.Queued == 0 {
		t.Fatalf("first tick %+v", rep)
	}
	sec, _ := store.LoadSecrets(b.Home)
	cfg, _ := store.LoadConfig(b.Home)
	st, _ := store.LoadState(b.Home)
	if sec.GitHub == nil || sec.GitHub.Token != "ghu_test" || sec.GitHub.Login != "octo" || sec.GitHub.UserID != 42 {
		t.Fatalf("adopted secrets %+v", sec.GitHub)
	}
	if cfg.GitHub == nil || !cfg.GitHub.Adopted || cfg.GitHub.Repo != "octo/agent-usage" || cfg.GitHub.Label != "Laptop B" || cfg.GitHub.SharesWithFleet() {
		t.Fatalf("adopted config %+v", cfg.GitHub)
	}
	if !st.GitHub.Rebuild || !strings.HasPrefix(st.GitHub.MachineID, "m_") || st.GitHub.Fleet.LastError != "" {
		t.Fatalf("adopted state %+v", st.GitHub)
	}
	fx.api.mu.Lock()
	ingests := fx.api.ingests
	fx.api.mu.Unlock()

	rep = tick(t, b, TickOptions{})
	fx.api.mu.Lock()
	after := fx.api.ingests
	fx.api.mu.Unlock()
	if rep.Queued != 0 || after != ingests {
		t.Fatalf("history sent to the server again: queued %d, ingests %d -> %d", rep.Queued, ingests, after)
	}
	st, _ = store.LoadState(b.Home)
	dir := ghpub.MachineDir(st.GitHub.MachineID)
	if st.GitHub.Rebuild || st.GitHub.LastError != "" || len(usageRows(t, fx.gh, dir)) == 0 {
		t.Fatalf("not published: rebuild %v error %q files %v", st.GitHub.Rebuild, st.GitHub.LastError, keys(fx.gh.Files))
	}
	if meta := fx.gh.Files[dir+"/meta.json"]; !strings.Contains(meta, `"Laptop B"`) {
		t.Fatalf("meta %s", meta)
	}
	if stA.GitHub.MachineID == st.GitHub.MachineID {
		t.Fatal("the two machines share a folder")
	}
	// An adopted machine never shares again.
	if _, puts, _, share := fx.api.fleetCounts(); puts != 1 || share.By != "m_first" {
		t.Fatalf("the adopted machine shared: %d puts by %s", puts, share.By)
	}
	out := statusOf(t, b)
	if !strings.Contains(out, "signed in through your fleet (shared by octo)") || !strings.Contains(out, "github logout) to opt this") {
		t.Fatalf("adopted status:\n%s", out)
	}
	if strings.Contains(out, "ghu_test") {
		t.Fatal("status shows the token")
	}

	// The poll keeps its hour: no request within it, one after it.
	gets, _, _, _ := fx.api.fleetCounts()
	fx.advance(30 * time.Minute)
	tick(t, b, TickOptions{})
	if g, _, _, _ := fx.api.fleetCounts(); g != gets {
		t.Fatalf("polled within the hour: %d gets", g-gets)
	}
	fx.advance(31 * time.Minute)
	tick(t, b, TickOptions{})
	if g, _, _, _ := fx.api.fleetCounts(); g != gets+1 {
		t.Fatalf("hourly poll: %d gets", g-gets)
	}
	if cfg, _ := store.LoadConfig(b.Home); cfg.GitHub == nil || !cfg.GitHub.Adopted {
		t.Fatal("an unchanged share changed the adopted sign-in")
	}
}

// A machine of another fleet (another key) cannot open the share: it neither
// adopts it nor calls GitHub, and records why.
func TestFleetShareOfAnotherFleetIsIgnored(t *testing.T) {
	fx := newFleet(t)
	_, _, _, share := fx.api.fleetCounts()
	api2, srv2 := newFakeAPI(t)
	api2.fleetGitHub = share // the other server holds this fleet's blob
	c, _, out := newTestApp(t)
	c.Now = fx.clock
	if err := c.Install(context.Background(), InstallOptions{Join: "D0M1-other", Endpoint: srv2.URL, Label: "other", Yes: true, NoAutostart: true}); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if bytes.Equal(mustKey(t, c), fx.k) {
		t.Fatal("test needs another key")
	}
	calls := len(fx.gh.AuthSeen)
	tick(t, c, TickOptions{})
	sec, _ := store.LoadSecrets(c.Home)
	cfg, _ := store.LoadConfig(c.Home)
	st, _ := store.LoadState(c.Home)
	if sec.GitHub != nil || cfg.GitHub != nil || len(fx.gh.AuthSeen) != calls {
		t.Fatalf("adopted another fleet's sign-in: %+v %+v, %d GitHub calls", sec.GitHub, cfg.GitHub, len(fx.gh.AuthSeen)-calls)
	}
	if !strings.Contains(st.GitHub.Fleet.LastError, "does not decrypt") {
		t.Fatalf("error %q", st.GitHub.Fleet.LastError)
	}
	if api2.fleetPuts != 0 || api2.fleetDeletes != 0 || api2.fleetGitHub == nil {
		t.Fatal("the share was changed")
	}
	// Retried only with the next hourly poll.
	gets := api2.fleetGets
	fx.advance(20 * time.Minute)
	tick(t, c, TickOptions{})
	if api2.fleetGets != gets {
		t.Fatal("polled again within the hour after a failure")
	}
}

// The repository's fleet key must be this machine's: a share pointing at a
// repository of another fleet is refused.
func TestFleetAdoptionRefusesARepositoryOfAnotherFleet(t *testing.T) {
	fx := newFleet(t)
	other := make([]byte, 32)
	rand.Read(other)
	fx.gh.Vars[ghpub.FleetVariable] = base64.StdEncoding.EncodeToString(other)
	b := fx.join(t, "second", "Laptop B")
	tick(t, b, TickOptions{})
	cfg, _ := store.LoadConfig(b.Home)
	sec, _ := store.LoadSecrets(b.Home)
	st, _ := store.LoadState(b.Home)
	if cfg.GitHub != nil || sec.GitHub != nil || !strings.Contains(st.GitHub.Fleet.LastError, "different fleet") {
		t.Fatalf("adopted: %+v, error %q", cfg.GitHub, st.GitHub.Fleet.LastError)
	}
	if !bytes.Equal(mustKey(t, b), fx.k) {
		t.Fatal("the machine's fleet key changed")
	}
}

// Stopping publishing on an adopted machine opts it out: it is not adopted
// again, and the sharer's share stays.
func TestFleetOptOutAfterLogoutIsNotAdoptedAgain(t *testing.T) {
	fx := newFleet(t)
	b := fx.join(t, "second", "Laptop B")
	tick(t, b, TickOptions{})
	if cfg, _ := store.LoadConfig(b.Home); cfg.GitHub == nil || !cfg.GitHub.Adopted {
		t.Fatal("not adopted")
	}
	if err := b.GitHubLogout(); err != nil {
		t.Fatal(err)
	}
	cfg, _ := store.LoadConfig(b.Home)
	if cfg.GitHub != nil || !cfg.GitHubFleetOptOut {
		t.Fatalf("after logout %+v opt-out %v", cfg.GitHub, cfg.GitHubFleetOptOut)
	}
	gets, _, deletes, share := fx.api.fleetCounts()
	if deletes != 0 || share == nil {
		t.Fatal("an adopted machine's logout withdrew the share")
	}
	fx.advance(3 * time.Hour)
	tick(t, b, TickOptions{})
	if g, _, _, _ := fx.api.fleetCounts(); g != gets {
		t.Fatal("an opted-out machine polled")
	}
	if cfg, _ := store.LoadConfig(b.Home); cfg.GitHub != nil {
		t.Fatal("adopted again after opting out")
	}
	if out := statusOf(t, b); !strings.Contains(out, "opted out of the sign-in your fleet shares") {
		t.Fatalf("status:\n%s", out)
	}
	// Signing in on it undoes the opt-out (its own sign-in now).
	if _, err := b.GitHubLogin(context.Background(), &loginUI{onStep: func(string) {}}, ""); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := store.LoadConfig(b.Home); cfg.GitHubFleetOptOut || cfg.GitHub.Adopted {
		t.Fatalf("after signing in %+v", cfg)
	}
}

// The sharer's logout withdraws its share; the adopted machines stop
// publishing at their next poll and keep their data. A share another machine
// put there since is left alone.
func TestFleetShareWithdrawnOnTheSharersLogout(t *testing.T) {
	fx := newFleet(t)
	b := fx.join(t, "second", "Laptop B")
	tick(t, b, TickOptions{})
	tick(t, b, TickOptions{}) // the rebuild and first publish
	stB, _ := store.LoadState(b.Home)
	if stB.GitHub.LastPublish.IsZero() {
		t.Fatal("the adopted machine did not publish")
	}

	if err := fx.a.GitHubLogout(); err != nil {
		t.Fatal(err)
	}
	_, _, deletes, share := fx.api.fleetCounts()
	if deletes != 1 || share != nil {
		t.Fatalf("share not withdrawn: %d deletes, %+v", deletes, share)
	}
	if st, _ := store.LoadState(fx.a.Home); st.GitHub.Fleet.Shared != "" {
		t.Fatalf("sharer state %+v", st.GitHub.Fleet)
	}

	fx.advance(fleetPollEvery)
	tick(t, b, TickOptions{})
	cfg, _ := store.LoadConfig(b.Home)
	sec, _ := store.LoadSecrets(b.Home)
	st, _ := store.LoadState(b.Home)
	if cfg.GitHub != nil || sec.GitHub != nil || cfg.GitHubFleetOptOut {
		t.Fatalf("still publishing: %+v %+v opt-out %v", cfg.GitHub, sec.GitHub, cfg.GitHubFleetOptOut)
	}
	if _, err := os.Stat(paths.Rollup(b.Home)); err != nil || st.GitHub.MachineID != stB.GitHub.MachineID || !sec.Enrolled() {
		t.Fatalf("data lost: rollup %v, machine %q", err, st.GitHub.MachineID)
	}

	// Sharing again (here: signing in again on the first machine) reaches it.
	if _, err := fx.a.GitHubLogin(context.Background(), &loginUI{onStep: func(string) {}}, ""); err != nil {
		t.Fatal(err)
	}
	fx.advance(fleetPollEvery)
	tick(t, b, TickOptions{})
	if cfg, _ := store.LoadConfig(b.Home); cfg.GitHub == nil || !cfg.GitHub.Adopted || cfg.GitHub.Label != "Laptop B" {
		t.Fatalf("not adopted again: %+v", cfg.GitHub)
	}

	// Another machine's share is not the sharer's to withdraw.
	fx.api.mu.Lock()
	fx.api.fleetGitHub.By = "m_other"
	fx.api.mu.Unlock()
	if err := fx.a.GitHubLogout(); err != nil {
		t.Fatal(err)
	}
	if _, _, deletes, share := fx.api.fleetCounts(); deletes != 1 || share == nil {
		t.Fatalf("withdrew another machine's share: %d deletes", deletes)
	}
}

// GitHub refusing the adopted sign-in is recorded; the next poll takes the
// renewed sign-in the sharer shared, and publishing resumes with it.
func TestFleetAdoptedMachineTakesTheRenewedSignInAfterA401(t *testing.T) {
	fx := newFleet(t)
	b := fx.join(t, "second", "Laptop B")
	tick(t, b, TickOptions{})
	tick(t, b, TickOptions{})

	// The old sign-in is revoked; the sharer signs in again and shares anew.
	fx.advance(time.Minute)
	fx.gh.Revoked = map[string]bool{"ghu_test": true}
	sec, _ := store.LoadSecrets(fx.a.Home)
	sec.GitHub.Token = "ghu_renewed"
	store.SaveSecrets(fx.a.Home, sec)
	tick(t, fx.a, TickOptions{})
	if _, puts, _, _ := fx.api.fleetCounts(); puts != 2 {
		t.Fatalf("the changed sign-in was not shared again: %d puts", puts)
	}

	fx.advance(10 * time.Minute)
	tick(t, b, TickOptions{Force: true})
	st, _ := store.LoadState(b.Home)
	if !strings.Contains(st.GitHub.LastError, "refused the sign-in shared by octo") {
		t.Fatalf("error %q", st.GitHub.LastError)
	}
	fx.advance(fleetPollEvery)
	tick(t, b, TickOptions{})
	secB, _ := store.LoadSecrets(b.Home)
	st, _ = store.LoadState(b.Home)
	if secB.GitHub == nil || secB.GitHub.Token != "ghu_renewed" || st.GitHub.LastError != "" || st.GitHub.Fleet.LastError != "" {
		t.Fatalf("token not renewed: %+v, errors %q %q", secB.GitHub, st.GitHub.LastError, st.GitHub.Fleet.LastError)
	}
	if !strings.HasSuffix(fx.gh.AuthSeen[len(fx.gh.AuthSeen)-1], "ghu_renewed") {
		t.Fatal("did not publish with the renewed sign-in")
	}
}

// Sharing is on by default (configs without the field included), and the
// settings page switches it off, which withdraws the share with the next
// tick, and on again. An adopted machine's page offers no sharing.
func TestFleetSharingSwitch(t *testing.T) {
	fx := newFleet(t)
	if (&store.GitHubConfig{}).SharesWithFleet() != true {
		t.Fatal("sharing must default to on")
	}
	_, base := settingsPage(t, fx.a, nil)
	st, raw := getState(t, base)
	if !st.GitHub.CanShare || !st.GitHub.ShareWithFleet || st.GitHub.Adopted || !strings.Contains(st.GitHub.Fleet, "shared with your other machines") {
		t.Fatalf("sharer page %s", raw)
	}
	if strings.Contains(raw, "ghu_test") {
		t.Fatal("the page state carries the token")
	}
	if html := embeddedSettingsPage(t); !strings.Contains(html, `<input type="checkbox" id="gh-share" checked> Share this sign-in with my other machines (through your server, encrypted with the fleet key)`) {
		t.Fatal("the sharing checkbox must exist and be checked by default")
	}

	opts := map[string]any{"label": "desk", "publishEveryMinutes": 30, "shareWithFleet": false}
	if code, out := postJSON(t, base, "api/github/options", opts); code != 200 {
		t.Fatalf("options: %d %v", code, out)
	}
	cfg, _ := store.LoadConfig(fx.a.Home)
	if cfg.GitHub.SharesWithFleet() || cfg.GitHub.ShareWithFleet == nil {
		t.Fatalf("not saved: %+v", cfg.GitHub)
	}
	tick(t, fx.a, TickOptions{})
	if _, _, deletes, share := fx.api.fleetCounts(); deletes != 1 || share != nil {
		t.Fatal("switching sharing off did not withdraw the share")
	}
	if st, _ = getState(t, base); st.GitHub.ShareWithFleet || !strings.Contains(st.GitHub.Fleet, "not shared") {
		t.Fatalf("page after switching off %+v", st.GitHub)
	}
	// Options without the field leave the choice as it is.
	if code, _ := postJSON(t, base, "api/github/options", map[string]any{"label": "desk"}); code != 200 {
		t.Fatal("options")
	}
	if cfg, _ = store.LoadConfig(fx.a.Home); cfg.GitHub.SharesWithFleet() {
		t.Fatal("an option save without the field switched sharing on")
	}
	opts["shareWithFleet"] = true
	postJSON(t, base, "api/github/options", opts)
	tick(t, fx.a, TickOptions{})
	if _, puts, _, share := fx.api.fleetCounts(); puts != 2 || share == nil || share.By != "m_first" {
		t.Fatalf("switching sharing on did not share: %d puts", puts)
	}

	b := fx.join(t, "second", "Laptop B")
	tick(t, b, TickOptions{})
	_, baseB := settingsPage(t, b, nil)
	stB, raw := getState(t, baseB)
	if !stB.GitHub.On || !stB.GitHub.Adopted || stB.GitHub.CanShare || stB.GitHub.Login != "octo" || !strings.Contains(stB.GitHub.Fleet, "shared by octo") {
		t.Fatalf("adopted page %s", raw)
	}
	// Asking an adopted machine to share does nothing.
	postJSON(t, baseB, "api/github/options", map[string]any{"label": "Laptop B", "shareWithFleet": true})
	tick(t, b, TickOptions{})
	if _, puts, _, _ := fx.api.fleetCounts(); puts != 2 {
		t.Fatal("an adopted machine shared")
	}
}

// The share is sealed with the fleet key; status says whether the server
// could read it anyway (it keeps the fleet key on a fleet linked from the
// browser, which PUT reports).
func TestFleetShareSaysWhetherTheServerCouldReadIt(t *testing.T) {
	fx := newFleet(t)
	if out := statusOf(t, fx.a); !strings.Contains(out, "your server cannot read it") {
		t.Fatalf("status without escrow:\n%s", out)
	}
	fx.api.mu.Lock()
	fx.api.fleetEscrowed = true
	fx.api.mu.Unlock()
	sec, _ := store.LoadSecrets(fx.a.Home)
	sec.GitHub.Token = "ghu_other"
	store.SaveSecrets(fx.a.Home, sec)
	tick(t, fx.a, TickOptions{})
	out := statusOf(t, fx.a)
	if !strings.Contains(out, "so whoever runs it could read it") || strings.Contains(out, "cannot read it") {
		t.Fatalf("status with escrow:\n%s", out)
	}
}

// Only a share newer than the one a machine took is adopted: the server
// serving an old (or the withdrawn) share again changes nothing.
func TestFleetOldSharesAreNotTakenAgain(t *testing.T) {
	fx := newFleet(t)
	_, _, _, first := fx.api.fleetCounts()
	b := fx.join(t, "second", "Laptop B")
	tick(t, b, TickOptions{})
	if cfg, _ := store.LoadConfig(b.Home); cfg.GitHub == nil {
		t.Fatal("not adopted")
	}

	// An older share (sealed by a fleet-key holder) pointing elsewhere.
	old, err := fleetshare.Seal(fx.k, fleetshare.Share{Token: "ghu_old", Login: "octo", UserID: 42, Repo: "octo/old-usage", Branch: "main", SharedAt: fx.now.Add(-time.Hour).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	fx.api.mu.Lock()
	fx.api.fleetGitHub = &fakeFleetGitHub{Blob: old, UpdatedAt: time.Now(), By: "m_first"}
	fx.api.mu.Unlock()
	fx.advance(fleetPollEvery)
	tick(t, b, TickOptions{})
	cfg, _ := store.LoadConfig(b.Home)
	st, _ := store.LoadState(b.Home)
	if cfg.GitHub == nil || cfg.GitHub.Repo != "octo/agent-usage" || !strings.Contains(st.GitHub.Fleet.LastError, "not newer") {
		t.Fatalf("rolled back: %+v, error %q", cfg.GitHub, st.GitHub.Fleet.LastError)
	}

	// The sharer signs out; the withdrawn share served again is not taken.
	fx.api.mu.Lock()
	fx.api.fleetGitHub = first
	fx.api.mu.Unlock()
	if err := fx.a.GitHubLogout(); err != nil {
		t.Fatal(err)
	}
	fx.advance(fleetPollEvery)
	tick(t, b, TickOptions{})
	if cfg, _ := store.LoadConfig(b.Home); cfg.GitHub != nil {
		t.Fatal("still publishing after the withdrawal")
	}
	fx.api.mu.Lock()
	fx.api.fleetGitHub = first
	fx.api.mu.Unlock()
	fx.advance(fleetPollEvery)
	tick(t, b, TickOptions{})
	cfg, _ = store.LoadConfig(b.Home)
	sec, _ := store.LoadSecrets(b.Home)
	if cfg.GitHub != nil || sec.GitHub != nil || cfg.GitHubFleetOptOut {
		t.Fatalf("the withdrawn share was taken again: %+v", cfg.GitHub)
	}
}

// Uploads switched off keep the enrolment: the sharer's sign-out still
// withdraws its share, and an adopted machine still follows it and stops.
// A machine with uploads off adopts nothing new.
func TestFleetWithdrawalWithTheServerSwitchedOff(t *testing.T) {
	fx := newFleet(t)
	ctx := context.Background()
	b := fx.join(t, "second", "Laptop B")
	tick(t, b, TickOptions{})
	for _, m := range []*App{fx.a, b} {
		if err := m.SetServer(ctx, store.NoServer, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := fx.a.GitHubLogout(); err != nil {
		t.Fatal(err)
	}
	if _, _, deletes, share := fx.api.fleetCounts(); deletes != 1 || share != nil {
		t.Fatalf("not withdrawn with uploads off: %d deletes", deletes)
	}
	fx.advance(fleetPollEvery)
	tick(t, b, TickOptions{Force: true})
	if cfg, _ := store.LoadConfig(b.Home); cfg.GitHub != nil {
		t.Fatalf("the adopted machine kept publishing: %+v", cfg.GitHub)
	}

	fx.advance(time.Minute)
	fx.api.mu.Lock()
	fx.api.fleetGitHub = &fakeFleetGitHub{Blob: mustSeal(t, fx), UpdatedAt: time.Now(), By: "m_first"}
	fx.api.mu.Unlock()
	gets, _, _, _ := fx.api.fleetCounts()
	fx.advance(fleetPollEvery)
	tick(t, b, TickOptions{})
	if g, _, _, _ := fx.api.fleetCounts(); g != gets {
		t.Fatal("a machine with uploads off looked for a sign-in")
	}
	if cfg, _ := store.LoadConfig(b.Home); cfg.GitHub != nil {
		t.Fatal("adopted with uploads off")
	}
}

func mustSeal(t *testing.T, fx *fleetFixture) string {
	t.Helper()
	blob, err := fleetshare.Seal(fx.k, fleetshare.Share{Token: "ghu_test", Login: "octo", UserID: 42, Repo: "octo/agent-usage", SharedAt: fx.now.UTC()})
	if err != nil {
		t.Fatal(err)
	}
	return blob
}

// A sharer that re-enrols (a new server machine id) shares again under it,
// so its sign-out still withdraws the share.
func TestFleetShareFollowsAReEnrolment(t *testing.T) {
	fx := newFleet(t)
	if err := fx.a.SetServer(context.Background(), fx.srv.URL, joincode.Format("third", fx.k)); err != nil {
		t.Fatal(err)
	}
	tick(t, fx.a, TickOptions{})
	if _, puts, _, share := fx.api.fleetCounts(); puts != 2 || share == nil || share.By != "m_third" {
		t.Fatalf("not shared under the new machine id: %d puts, %+v", puts, share)
	}
	if err := fx.a.GitHubLogout(); err != nil {
		t.Fatal(err)
	}
	if _, _, deletes, share := fx.api.fleetCounts(); deletes != 1 || share != nil {
		t.Fatalf("not withdrawn after re-enrolling: %d deletes", deletes)
	}
}

// The daily re-share of an unchanged sign-in leaves a share another machine
// made since in place (here: the old sign-in is dead), and puts its own back
// once that one is withdrawn.
func TestFleetReshareKeepsANewerShareOfAnotherMachine(t *testing.T) {
	fx := newFleet(t)
	ctx := context.Background()
	sec, _ := store.LoadSecrets(fx.a.Home)
	sec.GitHub.Token = "ghu_a"
	store.SaveSecrets(fx.a.Home, sec)
	tick(t, fx.a, TickOptions{})

	b := fx.join(t, "second", "Laptop B")
	fx.advance(time.Minute)
	if _, err := b.GitHubLogin(ctx, &loginUI{onStep: func(string) {}}, "Laptop B"); err != nil {
		t.Fatal(err)
	}
	_, puts, _, share := fx.api.fleetCounts()
	if share == nil || share.By != "m_second" {
		t.Fatalf("b's sign-in not shared: %+v", share)
	}

	fx.gh.Revoked = map[string]bool{"ghu_a": true}
	fx.advance(25 * time.Hour)
	tick(t, fx.a, TickOptions{})
	if _, p, _, share := fx.api.fleetCounts(); p != puts || share.By != "m_second" {
		t.Fatalf("a dead sign-in replaced b's: %d puts, by %s", p-puts, share.By)
	}
	if out := statusOf(t, fx.a); !strings.Contains(out, "another of your machines shared since") {
		t.Fatalf("status:\n%s", out)
	}
	fx.advance(25 * time.Hour)
	tick(t, fx.a, TickOptions{})
	if _, p, _, _ := fx.api.fleetCounts(); p != puts {
		t.Fatal("re-shared over b's on the next day")
	}

	// b signs out: a's sign-in is shared again with its next daily look.
	if err := b.GitHubLogout(); err != nil {
		t.Fatal(err)
	}
	fx.advance(25 * time.Hour)
	tick(t, fx.a, TickOptions{})
	if _, _, _, share := fx.api.fleetCounts(); share == nil || share.By != "m_first" {
		t.Fatalf("a's sign-in not shared again: %+v", share)
	}
}

// A machine whose owner stopped publishing before sign-ins were shared
// (0.4.0: a machine folder, no GitHub settings, nothing adopted) is opted
// out, not adopted; a sign-out on a machine that never published says it
// opts out.
func TestFleetEarlierSignOutStaysAStop(t *testing.T) {
	fx := newFleet(t)
	b := fx.join(t, "second", "Laptop B")
	st, _ := store.LoadState(b.Home)
	st.GitHub.MachineID = ghpub.NewMachineID()
	store.SaveState(b.Home, st)
	tick(t, b, TickOptions{})
	cfg, _ := store.LoadConfig(b.Home)
	if cfg.GitHub != nil || !cfg.GitHubFleetOptOut {
		t.Fatalf("adopted after an earlier sign-out: %+v opt-out %v", cfg.GitHub, cfg.GitHubFleetOptOut)
	}

	c := fx.join(t, "third", "Laptop C")
	var out bytes.Buffer
	c.Out = &out
	if err := c.GitHubLogoutCLI(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "does not take the sign-in your fleet shares") {
		t.Fatalf("logout said %q", out.String())
	}
}
