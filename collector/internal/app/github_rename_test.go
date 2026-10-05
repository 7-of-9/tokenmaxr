package app

import (
	"bytes"
	"context"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/fleetshare"
	"github.com/7-of-9/tokenmaxr/collector/internal/ghapi/ghapitest"
	"github.com/7-of-9/tokenmaxr/collector/internal/ghpub"
	"github.com/7-of-9/tokenmaxr/collector/internal/logx"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

const (
	oldDashboard = "https://octo.github.io/agent-usage/"
	newDashboard = "https://octo.github.io/tokens/"
)

// republish publishes a now with something to commit, as new data would.
func republish(t *testing.T, a *App) {
	t.Helper()
	st, _ := store.LoadState(a.Home)
	st.GitHub.Published = nil
	if err := store.SaveState(a.Home, st); err != nil {
		t.Fatal(err)
	}
	tick(t, a, TickOptions{Force: true})
}

// repoGets counts the requests for the repository itself by name.
func repoGets(f *ghapitest.Fake, name string) int {
	n := 0
	for _, r := range f.Requests {
		if strings.EqualFold(r, "GET /repos/"+name) {
			n++
		}
	}
	return n
}

// The owner renames the usage repository on github.com (owner direction
// 2026-10-05: "Rename the repo to tokens"). The next publish is redirected
// by GitHub; the machine follows by itself: its config names the new
// repository, the dashboard address is the new one (GitHub Pages does not
// redirect the old), the repository's website and README link it, and
// status, Settings and the app's account line say so at once. What was
// published stays; later publishes go straight to the new name.
func TestGitHubFollowsARenamedRepository(t *testing.T) {
	f, _ := useFakeGitHub(t)
	a, _, _ := newTestApp(t)
	githubOnly(t, a, f)
	now := time.Now()
	a.Now = func() time.Time { return now }
	var logs bytes.Buffer
	a.Log = &logx.Logger{Echo: &logs}
	f.Pages = true
	f.Files["README.md"] = "# usage\n\nData.\n"

	tick(t, a, TickOptions{})
	st, _ := store.LoadState(a.Home)
	if st.GitHub.RepoID != ghapitest.RepoID || st.GitHub.RepoName != "octo/agent-usage" || st.GitHub.PagesURL != oldDashboard || f.Homepage != oldDashboard {
		t.Fatalf("before the rename: %+v, website %q", st.GitHub, f.Homepage)
	}

	f.Rename("octo/tokens")
	now = now.Add(time.Hour)
	republish(t, a)
	cfg, _ := store.LoadConfig(a.Home)
	st, _ = store.LoadState(a.Home)
	if cfg.GitHub.Repo != "octo/tokens" || st.GitHub.RepoName != "octo/tokens" || st.GitHub.RepoID != ghapitest.RepoID {
		t.Fatalf("not followed: config %q, state %q/%d", cfg.GitHub.Repo, st.GitHub.RepoName, st.GitHub.RepoID)
	}
	if st.GitHub.PagesURL != newDashboard || st.GitHub.RenamedFrom != "octo/agent-usage" || st.GitHub.FormerPagesURL != oldDashboard || !st.GitHub.RenamedAt.Equal(now) {
		t.Fatalf("dashboard state %+v", st.GitHub)
	}
	if st.GitHub.LastError != "" || !st.GitHub.LastPublish.Equal(now) || len(st.GitHub.Published) == 0 {
		t.Fatalf("publishing stopped: %q, last %v", st.GitHub.LastError, st.GitHub.LastPublish)
	}
	if st.GitHub.Linked != "octo/tokens@main" || st.GitHub.QuotaSwept != "octo/tokens@main" || !st.GitHub.SiteChecked.Equal(now) {
		t.Fatalf("link %q, sweep %q, site checked %v", st.GitHub.Linked, st.GitHub.QuotaSwept, st.GitHub.SiteChecked)
	}
	if f.Homepage != newDashboard || !strings.Contains(f.Files["README.md"], ghpub.DashboardLine(newDashboard)) || strings.Contains(f.Files["README.md"], oldDashboard) {
		t.Fatalf("links not moved: website %q, README %q", f.Homepage, f.Files["README.md"])
	}
	if n := strings.Count(logs.String(), "the usage repository octo/agent-usage is octo/tokens on GitHub now"); n != 1 {
		t.Fatalf("%d rename lines in the log:\n%s", n, logs.String())
	}

	out := statusOf(t, a)
	for _, want := range []string{"octo/tokens as", newDashboard, "renamed on GitHub from octo/agent-usage"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	v, err := NewSettings(a, nil).state()
	if err != nil || v.GitHub.Repo != "octo/tokens" || v.GitHub.PagesURL != newDashboard || v.GitHub.RenamedFrom != "octo/agent-usage" {
		t.Fatalf("settings %+v %v", v.GitHub, err)
	}
	d := a.newDesktop(context.Background())
	d.load(nil)
	if d.in.GitHub != "octo/tokens" || d.in.PagesURL != newDashboard {
		t.Fatalf("app account line: %q %q", d.in.GitHub, d.in.PagesURL)
	}

	// Straight to the new name from now on; a week later the status no
	// longer mentions the rename.
	redirects, sets := len(f.Redirects), f.HomepageSets
	now = now.Add(time.Hour)
	republish(t, a)
	if len(f.Redirects) != redirects || f.HomepageSets != sets {
		t.Fatalf("redirected again: %q", f.Redirects[redirects:])
	}
	now = now.Add(8 * 24 * time.Hour)
	if out := statusOf(t, a); strings.Contains(out, "renamed on GitHub") || !strings.Contains(out, "octo/tokens as") {
		t.Fatalf("status a week later:\n%s", out)
	}
}

// The check costs one request a day: ticks in between ask GitHub nothing.
func TestGitHubRepositoryCheckBudget(t *testing.T) {
	f, _ := useFakeGitHub(t)
	a, _, _ := newTestApp(t)
	githubOnly(t, a, f)
	now := time.Now()
	a.Now = func() time.Time { return now }
	f.Pages = true
	f.Files["README.md"] = "# usage\n"

	tick(t, a, TickOptions{})
	st, _ := store.LoadState(a.Home)
	if st.GitHub.RepoID != ghapitest.RepoID || !st.GitHub.RepoChecked.Equal(now) {
		t.Fatalf("id not learned: %+v", st.GitHub)
	}
	base, n := repoGets(f, "octo/agent-usage"), len(f.Requests)
	for i := 0; i < 8; i++ {
		now = now.Add(31 * time.Minute)
		tick(t, a, TickOptions{})
	}
	if len(f.Requests) != n {
		t.Fatalf("ticks within the day asked GitHub: %q", f.Requests[n:])
	}
	if st2, _ := store.LoadState(a.Home); !st2.GitHub.LastAttempt.After(st.GitHub.LastAttempt) {
		t.Fatal("the ticks did not publish")
	}
	now = now.Add(20 * time.Hour)
	tick(t, a, TickOptions{})
	if got := repoGets(f, "octo/agent-usage"); got != base+1 {
		t.Fatalf("a day later: %d repository reads, want one", got-base)
	}
	if len(f.Redirects) != 0 || f.ByID != 0 {
		t.Fatalf("redirects %q, %d reads by id", f.Redirects, f.ByID)
	}
}

// When the old name no longer leads to the repository (GitHub dropped the
// redirect), the publish's 404 finds it by its id, and the publish lands
// under the new name in the same tick.
func TestGitHubFindsARenamedRepositoryByID(t *testing.T) {
	f, _ := useFakeGitHub(t)
	a, _, _ := newTestApp(t)
	githubOnly(t, a, f)
	now := time.Now()
	a.Now = func() time.Time { return now }
	tick(t, a, TickOptions{})

	f.Rename("octo/tokens")
	f.NoRedirect = true
	now = now.Add(time.Hour)
	republish(t, a)
	cfg, _ := store.LoadConfig(a.Home)
	st, _ := store.LoadState(a.Home)
	if cfg.GitHub.Repo != "octo/tokens" || f.ByID == 0 || len(f.Redirects) != 0 {
		t.Fatalf("not found by id: %q, %d by id, redirects %q", cfg.GitHub.Repo, f.ByID, f.Redirects)
	}
	if st.GitHub.LastError != "" || !st.GitHub.LastPublish.Equal(now) {
		t.Fatalf("not published in the same tick: %q %v", st.GitHub.LastError, st.GitHub.LastPublish)
	}
}

// A transfer is followed like a rename while the App's installation still
// covers the repository. Moved out of its reach, the public repository
// still reads under its new name (and its old one redirects), but nothing
// changes: the publish fails and says what to check, it is looked at again
// hourly (not with every publish), and it is followed once the App is
// installed for it.
func TestGitHubFollowsATransferOnlyWithinTheInstallation(t *testing.T) {
	f, _ := useFakeGitHub(t)
	a, _, _ := newTestApp(t)
	githubOnly(t, a, f)
	f.Pages = true
	tick(t, a, TickOptions{})
	f.Rename("octo-org/agent-usage")
	republish(t, a)
	cfg, _ := store.LoadConfig(a.Home)
	st, _ := store.LoadState(a.Home)
	if cfg.GitHub.Repo != "octo-org/agent-usage" || st.GitHub.PagesURL != "https://octo-org.github.io/agent-usage/" || st.GitHub.LastError != "" {
		t.Fatalf("transfer: %q %q %q", cfg.GitHub.Repo, st.GitHub.PagesURL, st.GitHub.LastError)
	}

	g, _ := useFakeGitHub(t)
	b, _, _ := newTestApp(t)
	githubOnly(t, b, g)
	now := time.Now()
	b.Now = func() time.Time { return now }
	var logs bytes.Buffer
	b.Log = &logx.Logger{Echo: &logs}
	tick(t, b, TickOptions{})
	g.Rename("someone-else/agent-usage")
	g.Uncovered = true
	now = now.Add(time.Minute)
	republish(t, b)
	cfg, _ = store.LoadConfig(b.Home)
	st, _ = store.LoadState(b.Home)
	if cfg.GitHub.Repo != "octo/agent-usage" || st.GitHub.RenamedFrom != "" || !strings.Contains(st.GitHub.LastError, "cannot write to the repository") {
		t.Fatalf("followed out of the installation: %q, %q", cfg.GitHub.Repo, st.GitHub.LastError)
	}
	if st.GitHub.RepoOutside != "someone-else/agent-usage" || strings.Count(logs.String(), "installation does not cover") != 1 {
		t.Fatalf("outside: %q, log:\n%s", st.GitHub.RepoOutside, logs.String())
	}
	installs := func() int {
		n := 0
		for _, r := range g.Requests {
			if strings.HasPrefix(r, "GET /user/installations") {
				n++
			}
		}
		return n
	}
	n := installs()
	now = now.Add(30 * time.Minute)
	republish(t, b)
	if installs() != n || strings.Count(logs.String(), "installation does not cover") != 1 {
		t.Fatalf("looked again within the hour: %d installation reads", installs()-n)
	}
	// The App installed for it: followed with the next look.
	g.Uncovered = false
	now = now.Add(31 * time.Minute)
	republish(t, b)
	cfg, _ = store.LoadConfig(b.Home)
	st, _ = store.LoadState(b.Home)
	if cfg.GitHub.Repo != "someone-else/agent-usage" || st.GitHub.LastError != "" || st.GitHub.RepoOutside != "" || st.GitHub.RenamedFrom != "octo/agent-usage" {
		t.Fatalf("not followed once installed: %q %q %q", cfg.GitHub.Repo, st.GitHub.LastError, st.GitHub.RepoOutside)
	}
}

// adoptedFleet is a fleet whose second machine publishes with the shared
// sign-in.
func adoptedFleet(t *testing.T) (*fleetFixture, *App) {
	t.Helper()
	fx := newFleet(t)
	fx.gh.Files["README.md"] = "# usage\n"
	b := fx.join(t, "second", "Laptop B")
	tick(t, b, TickOptions{}) // adopts
	tick(t, b, TickOptions{}) // publishes
	tick(t, fx.a, TickOptions{Force: true})
	st, _ := store.LoadState(b.Home)
	if st.GitHub.LastError != "" || len(st.GitHub.Published) == 0 || st.GitHub.RepoID != ghapitest.RepoID {
		t.Fatalf("adopted machine before the rename: %+v", st.GitHub)
	}
	return fx, b
}

func sharedRepo(t *testing.T, fx *fleetFixture) (string, int) {
	t.Helper()
	_, puts, _, share := fx.api.fleetCounts()
	s, err := fleetshare.Open(fx.k, share.Blob)
	if err != nil {
		t.Fatal(err)
	}
	return s.Repo, puts
}

// The machine that shares its sign-in follows the rename and shares the
// new name in the same tick; a machine that adopted the sign-in takes the
// new name from the share, as the same repository: what it published stays
// published, and its dashboard address is the new one.
func TestFleetFollowsARenameThroughTheShare(t *testing.T) {
	fx, b := adoptedFleet(t)
	_, puts := sharedRepo(t, fx)
	stB, _ := store.LoadState(b.Home)
	published := maps.Clone(stB.GitHub.Published)

	fx.gh.Rename("octo/tokens")
	fx.advance(time.Hour)
	republish(t, fx.a)
	if repo, p := sharedRepo(t, fx); repo != "octo/tokens" || p != puts+1 {
		t.Fatalf("shared %q (%d puts)", repo, p-puts)
	}
	if cfg, _ := store.LoadConfig(fx.a.Home); cfg.GitHub.Repo != "octo/tokens" {
		t.Fatalf("sharer %q", cfg.GitHub.Repo)
	}

	fx.advance(61 * time.Minute)
	tick(t, b, TickOptions{})
	cfg, _ := store.LoadConfig(b.Home)
	stB, _ = store.LoadState(b.Home)
	if cfg.GitHub.Repo != "octo/tokens" || !cfg.GitHub.Adopted || stB.GitHub.Fleet.LastError != "" {
		t.Fatalf("adopted machine: %q, %q", cfg.GitHub.Repo, stB.GitHub.Fleet.LastError)
	}
	if !maps.Equal(stB.GitHub.Published, published) || stB.GitHub.Rebuild || stB.GitHub.PagesURL != newDashboard || stB.GitHub.RenamedFrom != "octo/agent-usage" {
		t.Fatalf("adopted machine state after the rename: %+v", stB.GitHub)
	}
	if out := statusOf(t, b); !strings.Contains(out, "octo/tokens as") || !strings.Contains(out, newDashboard) {
		t.Fatalf("adopted status:\n%s", out)
	}
	// Its next publish goes to the new name.
	redirects := len(fx.gh.Redirects)
	fx.advance(time.Hour)
	republish(t, b)
	if stB, _ = store.LoadState(b.Home); stB.GitHub.LastError != "" || len(fx.gh.Redirects) != redirects {
		t.Fatalf("adopted publish: %q, redirects %q", stB.GitHub.LastError, fx.gh.Redirects[redirects:])
	}
}

// A machine that adopted the sign-in may notice the rename before the
// sharer does: it follows by itself, and the share that still names the old
// repository is the same repository, not a reason to go back or an error.
func TestAdoptedMachineFollowsARenameBeforeTheSharer(t *testing.T) {
	fx, b := adoptedFleet(t)
	fx.gh.Rename("octo/tokens")
	fx.advance(61 * time.Minute)
	republish(t, b)
	cfg, _ := store.LoadConfig(b.Home)
	stB, _ := store.LoadState(b.Home)
	if cfg.GitHub.Repo != "octo/tokens" || stB.GitHub.LastError != "" || stB.GitHub.PagesURL != newDashboard {
		t.Fatalf("adopted machine did not follow: %q %q %q", cfg.GitHub.Repo, stB.GitHub.LastError, stB.GitHub.PagesURL)
	}
	if repo, _ := sharedRepo(t, fx); repo != "octo/agent-usage" {
		t.Fatalf("the share changed by itself: %q", repo)
	}
	for _, sharerFirst := range []bool{false, true} {
		if sharerFirst {
			republish(t, fx.a) // the sharer follows and shares the new name
			if repo, _ := sharedRepo(t, fx); repo != "octo/tokens" {
				t.Fatalf("shared %q", repo)
			}
		}
		fx.advance(61 * time.Minute)
		tick(t, b, TickOptions{})
		cfg, _ = store.LoadConfig(b.Home)
		stB, _ = store.LoadState(b.Home)
		if cfg.GitHub.Repo != "octo/tokens" || stB.GitHub.Fleet.LastError != "" || stB.GitHub.LastError != "" {
			t.Fatalf("after the share (sharer followed: %v): %q, fleet %q, publish %q", sharerFirst, cfg.GitHub.Repo, stB.GitHub.Fleet.LastError, stB.GitHub.LastError)
		}
	}
	if slices.ContainsFunc(fx.gh.Redirects, func(r string) bool { return !strings.HasPrefix(r, "301 GET") && !strings.HasPrefix(r, "307 ") }) {
		t.Fatalf("unexpected redirects %q", fx.gh.Redirects)
	}
}

// reshare puts the share again as its machine would, naming repo and
// shared now (newer than any taken).
func reshare(t *testing.T, fx *fleetFixture, repo string) {
	t.Helper()
	_, _, _, cur := fx.api.fleetCounts()
	s, err := fleetshare.Open(fx.k, cur.Blob)
	if err != nil {
		t.Fatal(err)
	}
	s.Repo, s.SharedAt = repo, fx.now.UTC()
	blob, err := fleetshare.Seal(fx.k, s)
	if err != nil {
		t.Fatal(err)
	}
	fx.api.mu.Lock()
	fx.api.fleetGitHub = &fakeFleetGitHub{Blob: blob, UpdatedAt: time.Now(), By: cur.By}
	fx.api.mu.Unlock()
}

// adoptedState is an adopted machine's repository, the name it was renamed
// from and its fleet error after a poll of the share.
func adoptedState(t *testing.T, fx *fleetFixture, b *App) (string, string, string) {
	t.Helper()
	fx.advance(fleetPollEvery)
	tick(t, b, TickOptions{})
	cfg, _ := store.LoadConfig(b.Home)
	st, _ := store.LoadState(b.Home)
	return cfg.GitHub.Repo, st.GitHub.RenamedFrom, st.GitHub.Fleet.LastError
}

// A share may name the repository as it was called before (a sharer whose
// collector has not followed the rename): a machine that adopted the
// sign-in under the name it has now keeps that name, as the same
// repository, without recording a rename back or an error, newer share or
// not.
func TestAdoptedMachineKeepsTheNameAgainstAStaleShare(t *testing.T) {
	fx, _ := adoptedFleet(t)
	fx.gh.Rename("octo/tokens")
	fx.advance(time.Hour)
	republish(t, fx.a) // the sharer follows and shares octo/tokens
	c := fx.join(t, "third", "Laptop C")
	tick(t, c, TickOptions{}) // adopts octo/tokens
	tick(t, c, TickOptions{}) // publishes
	var logs bytes.Buffer
	c.Log = &logx.Logger{Echo: &logs}
	if repo, from, ferr := adoptedState(t, fx, c); repo != "octo/tokens" || from != "" || ferr != "" {
		t.Fatalf("adopted after the rename: %q from %q, fleet %q", repo, from, ferr)
	}
	reshare(t, fx, "octo/agent-usage")
	for i := 0; i < 2; i++ { // the newer stale share, then polled again
		repo, from, ferr := adoptedState(t, fx, c)
		st, _ := store.LoadState(c.Home)
		if repo != "octo/tokens" || from != "" || ferr != "" || st.GitHub.PagesURL != newDashboard || st.GitHub.Linked == "octo/agent-usage@main" {
			t.Fatalf("poll %d of the stale share: %q from %q, fleet %q, pages %q", i, repo, from, ferr, st.GitHub.PagesURL)
		}
	}
	if strings.Contains(logs.String(), "on GitHub now") {
		t.Fatalf("a rename back was logged:\n%s", logs.String())
	}
}

// Renamed twice while the sharer's collector follows neither: the adopted
// machine follows both by itself, and the share naming the first name, or
// the second, is this repository under its name now (no error, no rename
// back, the dashboard address stays the newest).
func TestAdoptedMachineFollowsTwoRenamesPastAStaleSharer(t *testing.T) {
	fx, b := adoptedFleet(t)
	fx.gh.Rename("octo/tokens")
	fx.advance(time.Hour)
	republish(t, b)
	fx.gh.Rename("octo/tokens2")
	fx.advance(time.Hour)
	republish(t, b)
	cfg, _ := store.LoadConfig(b.Home)
	st, _ := store.LoadState(b.Home)
	if cfg.GitHub.Repo != "octo/tokens2" || st.GitHub.RenamedFrom != "octo/tokens" || !slices.Equal(st.GitHub.FormerNames, []string{"octo/tokens", "octo/agent-usage"}) {
		t.Fatalf("two renames: %q from %q, former %q", cfg.GitHub.Repo, st.GitHub.RenamedFrom, st.GitHub.FormerNames)
	}
	if repo, from, ferr := adoptedState(t, fx, b); repo != "octo/tokens2" || from != "octo/tokens" || ferr != "" {
		t.Fatalf("the unchanged share of the first name: %q from %q, fleet %q", repo, from, ferr)
	}
	for _, stale := range []string{"octo/agent-usage", "octo/tokens"} {
		reshare(t, fx, stale)
		repo, from, ferr := adoptedState(t, fx, b)
		st, _ = store.LoadState(b.Home)
		if repo != "octo/tokens2" || from != "octo/tokens" || ferr != "" || st.GitHub.PagesURL != "https://octo.github.io/tokens2/" {
			t.Fatalf("newer share of %s: %q from %q, fleet %q, pages %q", stale, repo, from, ferr, st.GitHub.PagesURL)
		}
	}
	// A share naming the repository by its name now is the same too.
	reshare(t, fx, "octo/tokens2")
	if repo, _, ferr := adoptedState(t, fx, b); repo != "octo/tokens2" || ferr != "" {
		t.Fatalf("share of the name now: %q, fleet %q", repo, ferr)
	}
	if st, _ = store.LoadState(b.Home); st.GitHub.LastError != "" {
		t.Fatalf("publish: %q", st.GitHub.LastError)
	}
}
