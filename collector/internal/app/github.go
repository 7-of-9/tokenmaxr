package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/accounts"
	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
	"github.com/7-of-9/tokenmaxr/collector/internal/evidence"
	"github.com/7-of-9/tokenmaxr/collector/internal/ghapi"
	"github.com/7-of-9/tokenmaxr/collector/internal/ghpub"
	"github.com/7-of-9/tokenmaxr/collector/internal/homes"
	"github.com/7-of-9/tokenmaxr/collector/internal/limits"
	"github.com/7-of-9/tokenmaxr/collector/internal/lock"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/rollup"
	"github.com/7-of-9/tokenmaxr/collector/internal/scan"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
	"github.com/7-of-9/tokenmaxr/collector/internal/tray"
)

// GitHubLoginUI shows the GitHub sign-in's steps: the CLI prints them, the
// settings page renders them.
type GitHubLoginUI interface {
	// Code shows the one-time code to enter at url (the browser is opened too).
	Code(userCode, url string)
	// Step names what the user must do next on GitHub, with its link.
	Step(text, url string)
	// Progress reports a status line.
	Progress(text string)
}

// GitHubLoginResult is where this machine publishes.
type GitHubLoginResult struct {
	Login, Repo, PagesURL, Label string
	// NewFleet: this machine created the fleet key (first machine).
	NewFleet bool
	// Rehash: the machine adopted the fleet's key; its history is re-read.
	Rehash bool
}

// githubWait bounds each wait for the user on GitHub.
var githubWait = 20 * time.Minute

// githubLockWait bounds the wait for a running tick before saving.
var githubLockWait = TickBudget + appTickWait

// githubPoll is how often a waiting step re-checks GitHub.
var githubPoll = 5 * time.Second

// githubDeviceSleep waits between device-flow polls (nil: real time; tests
// replace it).
var githubDeviceSleep func(context.Context, time.Duration) error

// newGitHubClient is replaced in tests (fake GitHub).
var newGitHubClient = func(token, version string) *ghapi.Client {
	return ghapi.New(token, "tokenmaxr-collector/"+version)
}

// repoExists checks a public repository anonymously (replaced in tests).
var repoExists = func(ctx context.Context, fullName string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+fullName, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode == http.StatusOK
}

func sleepOrDone(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (a *App) open(u string) {
	open := a.OpenURL
	if open == nil {
		open = tray.Open
	}
	_ = open(u)
}

// GitHubLogin signs this machine in to GitHub with the App's device flow,
// guides the user until their publishing repository exists and the App can
// write to it, joins the fleet (the key in the repository variable) and saves
// everything. label is the public machine name ("" keeps or picks one).
func (a *App) GitHubLogin(ctx context.Context, ui GitHubLoginUI, label string) (GitHubLoginResult, error) {
	var res GitHubLoginResult
	c := newGitHubClient("", a.Version)
	dc, err := c.StartDevice(ctx, buildinfo.GitHubClientID)
	if err != nil {
		return res, err
	}
	ui.Code(dc.UserCode, dc.VerificationURI)
	a.open(dc.VerificationURI)
	waitCtx, cancel := context.WithTimeout(ctx, time.Duration(dc.ExpiresIn)*time.Second)
	token, err := c.PollDevice(waitCtx, buildinfo.GitHubClientID, dc, githubDeviceSleep)
	cancel()
	if err != nil {
		return res, err
	}
	c.Token = token
	ui.Progress("Signed in to GitHub")

	// Repository and App access: wait for each step the user completes.
	deadline := time.Now().Add(githubWait)
	var g ghpub.Guide
	opened := map[string]bool{}
	for {
		if g, err = ghpub.Discover(ctx, c, buildinfo.GitHubAppSlug, buildinfo.GitHubTemplateRepo); err != nil {
			return res, err
		}
		if g.Ready() {
			break
		}
		repo := g.User.Login + "/" + ghpub.DefaultRepoName
		switch {
		case g.Installation == nil && !repoExists(ctx, repo):
			if !opened["create"] {
				ui.Step("Create your public tokenmaxr repository from the template (keep the name "+ghpub.DefaultRepoName+")", g.CreateRepoURL)
				a.open(g.CreateRepoURL)
				opened["create"] = true
			}
		default:
			if !opened["install"] {
				ui.Step("Install the "+buildinfo.GitHubAppSlug+" GitHub App and give it access to "+repo+" only", g.InstallURL)
				a.open(g.InstallURL)
				opened["install"] = true
			}
		}
		if time.Now().After(deadline) {
			return res, errors.New("timed out waiting for the repository and App access on GitHub; run the sign-in again to continue")
		}
		if err := sleepOrDone(ctx, githubPoll); err != nil {
			return res, err
		}
	}
	res.Login, res.Repo = g.User.Login, g.Repo.FullName
	ui.Progress("Publishing to " + res.Repo)

	// The rest writes secrets, config and state: not while a tick runs.
	lk, err := lock.Acquire(paths.Lock(a.Home), githubLockWait)
	if err != nil {
		return res, fmt.Errorf("waiting for a running tick: %w", err)
	}
	defer lk.Release()
	sec, err := store.LoadSecrets(a.Home)
	if err != nil {
		return res, err
	}
	local := sec.Key()
	key, created, err := ghpub.Join(ctx, c, res.Repo, local, sec.Enrolled())
	if err != nil {
		return res, err
	}
	res.NewFleet = created
	res.Rehash = local != nil && !bytes.Equal(local, key)
	sec.K = base64.StdEncoding.EncodeToString(key)
	sec.GitHub = &store.GitHubSecrets{Token: token, Login: g.User.Login, UserID: g.User.ID}
	if err := store.SaveSecrets(a.Home, sec); err != nil {
		return res, err
	}

	cfg, err := store.LoadConfig(a.Home)
	if err != nil {
		return res, err
	}
	gh := &store.GitHubConfig{Repo: res.Repo, Branch: g.Repo.DefaultBranch}
	if cfg.GitHub != nil {
		gh.Label, gh.PublishEveryMinutes, gh.NoQuota = cfg.GitHub.Label, cfg.GitHub.PublishEveryMinutes, cfg.GitHub.NoQuota
		gh.ShowCountry, gh.ShowAccountHistory = cfg.GitHub.ShowCountry, cfg.GitHub.ShowAccountHistory
	}
	if label = strings.TrimSpace(label); label != "" {
		gh.Label = label
	}
	st, err := store.LoadState(a.Home)
	if err != nil {
		return res, err
	}
	if st.GitHub.MachineID == "" {
		st.GitHub.MachineID = ghpub.NewMachineID()
	}
	if gh.Label == "" {
		gh.Label = "machine-" + strings.TrimPrefix(st.GitHub.MachineID, "m_")[:4]
	}
	res.Label = gh.Label
	cfg.GitHub = gh
	if err := store.SaveConfig(a.Home, cfg); err != nil {
		return res, err
	}
	// A new or changed fleet key re-hashes accounts: re-read all history so
	// every published total uses it, and rebuild the rollup from scratch.
	if res.Rehash || local == nil {
		st.Cursors = map[string]map[string]store.FileCursor{}
	}
	if res.Rehash {
		os.Remove(paths.Rollup(a.Home))
		st.AccountHistory.LastAttempt = time.Time{} // account totals under the new key, before the next publish
	}
	if _, err := os.Stat(paths.Rollup(a.Home)); errors.Is(err, os.ErrNotExist) && !res.Rehash && local != nil {
		// First publish needs the whole history in the rollup: re-read it
		// for the rollup alone, so a server is not sent it all again.
		st.GitHub.StartRebuild()
	}
	st.GitHub.Published, st.GitHub.LastAttempt, st.GitHub.LastError = nil, time.Time{}, ""
	st.GitHub.Site, st.GitHub.SiteChecked = "", time.Time{} // maybe another repository: check its dashboard
	if err := c.EnablePages(ctx, res.Repo); err != nil {
		ui.Progress("Could not switch GitHub Pages on (" + err.Error() + "): enable it in the repository's Settings → Pages, source \"GitHub Actions\"")
	}
	if u, err := c.PagesURL(ctx, res.Repo); err == nil {
		res.PagesURL, st.GitHub.PagesURL = u, u
	}
	if err := store.SaveState(a.Home, st); err != nil {
		return res, err
	}
	a.Log.Printf("github: signed in as %s, publishing to %s as %q", res.Login, res.Repo, res.Label)
	return res, nil
}

// GitHubLogout stops publishing to GitHub on this machine: the token and the
// GitHub settings are removed (the repository and its data stay on GitHub).
func (a *App) GitHubLogout() error {
	lk, err := lock.Acquire(paths.Lock(a.Home), githubLockWait)
	if err != nil {
		return fmt.Errorf("waiting for a running tick: %w", err)
	}
	defer lk.Release()
	sec, err := store.LoadSecrets(a.Home)
	if err != nil {
		return err
	}
	sec.GitHub = nil
	if err := store.SaveSecrets(a.Home, sec); err != nil {
		return err
	}
	cfg, err := store.LoadConfig(a.Home)
	if err != nil {
		return err
	}
	cfg.GitHub = nil
	return store.SaveConfig(a.Home, cfg)
}

// githubEnabled reports whether this machine publishes to GitHub.
func githubEnabled(cfg *store.Config, sec store.Secrets) bool {
	return cfg.GitHub != nil && cfg.GitHub.Repo != "" && sec.GitHub != nil && sec.GitHub.Token != ""
}

// publishGitHub publishes the rollup and quota meters when due (or forced).
// Failures are recorded and logged; they never stop collection.
func (a *App) publishGitHub(ctx context.Context, cfg *store.Config, sec store.Secrets, st *store.State, ru *rollup.Rollup, hs []homes.Home, force bool) {
	if !githubEnabled(cfg, sec) || ru == nil || len(ru.Rows()) == 0 {
		return // nothing read yet (e.g. the first tick only harvests evidence)
	}
	if st.GitHub.Rebuild {
		// Half a rebuild would publish part of the history over the whole
		// of it: the dashboard keeps the last complete totals until it ends.
		return
	}
	now := a.Now()
	if !force && !st.GitHub.LastAttempt.IsZero() && now.Sub(st.GitHub.LastAttempt) < cfg.GitHub.PublishEvery() {
		return
	}
	st.GitHub.LastAttempt = now
	if st.GitHub.MachineID == "" {
		st.GitHub.MachineID = ghpub.NewMachineID()
	}
	var meters []model.LimitSnapshot
	if !cfg.GitHub.NoQuota {
		meters = []model.LimitSnapshot{}
		k := sec.Key()
		ix := evidence.Build(st.Evidence, accounts.Spans(st.Accounts))
		for _, h := range hs {
			meters = append(meters, limits.Collect(a.envFor(k, cfg, st, h, ix))...)
		}
		meters = limits.Dedupe(meters)
	}
	m := ghpub.Machine{ID: st.GitHub.MachineID, Label: cfg.GitHub.Label, OS: runtime.GOOS, Collector: a.Version, LastEventAt: ru.LastEvent()}
	if cfg.GitHub.ShowCountry {
		// The country alone, never the zone, its Windows id or the offset.
		m.CC = machineCountry()
	}
	var history ghpub.AccountHistory
	if cfg.GitHub.ShowAccountHistory {
		history = accountHistory(ru, st)
	}
	files := ghpub.Files(m, ru.Rows(), meters, history, now)
	if p := ghpub.AccountUsagePath(m.ID); !cfg.GitHub.ShowAccountHistory && st.GitHub.Published[p] != "" {
		// Opted out: the account history this machine published goes too.
		files = append(files, ghapi.File{Path: p, Delete: true})
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	c := newGitHubClient(sec.GitHub.Token, a.Version)
	refreshMeta := st.GitHub.LastPublish.IsZero() || now.Sub(st.GitHub.LastPublish) >= 24*time.Hour
	changed, err := ghpub.Publish(ctx, c, cfg.GitHub.Repo, cfg.GitHub.BranchOrDefault(), cfg.GitHub.Label, files, st.GitHub.Published, refreshMeta)
	if err != nil {
		st.GitHub.LastError = githubErrorText(err)
		a.Log.Printf("github: publish: %s", st.GitHub.LastError)
		return
	}
	st.GitHub.LastError = ""
	if changed != nil {
		if st.GitHub.Published == nil {
			st.GitHub.Published = map[string]string{}
		}
		for p, h := range changed {
			if h == "" {
				delete(st.GitHub.Published, p) // deleted
			} else {
				st.GitHub.Published[p] = h
			}
		}
		st.GitHub.LastPublish = now
		a.Log.Printf("github: published %d file(s) to %s", len(changed), cfg.GitHub.Repo)
	}
	if st.GitHub.PagesURL == "" {
		if u, err := c.PagesURL(ctx, cfg.GitHub.Repo); err == nil {
			st.GitHub.PagesURL = u
		}
	}
	a.publishSite(parent, c, cfg, st, now, force)
}

// siteBuild is the dashboard this collector carries (replaced in tests).
var siteBuild = ghpub.Site

// siteCheckEvery is how often the repository's dashboard version is read
// again while it is this collector's build (or newer).
const siteCheckEvery = 24 * time.Hour

// publishSite updates the repository's dashboard (site/) to the build this
// collector carries when the repository's is older, so a usage repository
// follows tokenmaxr releases instead of keeping the dashboard its template
// had. Data and every other path are left alone; a failure is recorded and
// retried with the next publish.
func (a *App) publishSite(ctx context.Context, c *ghapi.Client, cfg *store.Config, st *store.State, now time.Time, force bool) {
	v, files, err := siteBuild()
	if err != nil {
		a.Log.Printf("github: dashboard: %v", err)
		return
	}
	if !force && st.GitHub.Site == v.Hash && now.Sub(st.GitHub.SiteChecked) < siteCheckEvery {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	updated, err := ghpub.PublishSite(ctx, c, cfg.GitHub.Repo, cfg.GitHub.BranchOrDefault(), v, files)
	if err != nil {
		st.GitHub.LastError = "dashboard update: " + githubErrorText(err)
		a.Log.Printf("github: %s", st.GitHub.LastError)
		return
	}
	st.GitHub.Site, st.GitHub.SiteChecked = v.Hash, now
	if updated {
		a.Log.Printf("github: dashboard updated to the build of %s in %s", v.BuiltAt.UTC().Format(time.RFC3339), cfg.GitHub.Repo)
	}
}

// accountHistorySources are the sources whose provider account totals the
// collector reads (internal/accountusage) and the server accepts.
var accountHistorySources = []string{model.SourceCodex}

// accountHistory is what account-usage.json publishes: the newest account
// totals and the account ledger of the account-history sources. The ledger
// goes out when this machine has totals or a signed-in account (whose reader
// may be failing here while another machine publishes its totals), so the
// dashboard can tell that this machine's local tokens are covered; a machine
// with neither publishes no ledger. Dates whose totals an older rollup filled
// in (rollup.Unledgered) are listed, as the ledger does not cover them. While
// a rebuild's account totals are not read again, nothing is published, so the
// file already in the repository stays as it is.
func accountHistory(ru *rollup.Rollup, st *store.State) ghpub.AccountHistory {
	if ru.AccountsStale() {
		return ghpub.AccountHistory{}
	}
	h := ghpub.AccountHistory{Snapshots: ru.AccountUsage()}
	if len(h.Snapshots) == 0 && st.AccountHistory.Account == "" {
		return h
	}
	sources := slices.Clone(accountHistorySources)
	for _, s := range h.Snapshots {
		if !slices.Contains(sources, s.Source) {
			sources = append(sources, s.Source)
		}
	}
	for _, l := range ru.LedgerRows() {
		if slices.Contains(sources, l.Source) {
			h.Ledger = append(h.Ledger, l)
		}
	}
	for _, u := range ru.Unledgered() {
		if slices.Contains(sources, u.Source) && !slices.Contains(h.Unledgered, u.Date) {
			h.Unledgered = append(h.Unledgered, u.Date)
		}
	}
	return h
}

// githubErrorText turns GitHub failures into actionable, credential-free text.
func githubErrorText(err error) string {
	switch ghapi.StatusOf(err) {
	case http.StatusUnauthorized:
		return "GitHub sign-in is no longer valid: sign in again"
	case http.StatusForbidden, http.StatusNotFound:
		return "the tokenmaxor App cannot write to the repository: check it is installed with access to it"
	}
	return fmt.Sprint(err)
}

// serverOn reports whether this machine uploads to a server: enrolled, and
// the server not switched off in config.json.
func serverOn(cfg *store.Config, sec store.Secrets) bool {
	return sec.Enrolled() && cfg.Server() != ""
}

// rebuildRollup continues a rollup rebuild (GitHubState.Rebuild): the whole
// history again, with its own cursors, into the rollup only (no outbox, no
// local numbers, no quota marks), until a complete pass.
func (a *App) rebuildRollup(cfg *store.Config, sec store.Secrets, st *store.State, hr homes.Result, ru *rollup.Rollup, deadline time.Time) error {
	if !st.GitHub.Rebuild || !a.Now().Before(deadline) {
		return nil
	}
	if st.GitHub.RebuildCursors == nil {
		st.GitHub.RebuildCursors = map[string]map[string]store.FileCursor{}
	}
	keep := hr.Unreachable
	stats, err := scan.Run(scan.Options{
		Homes: a.scanHomes(sec.Key(), cfg, st, hr.Homes),
		KeepCursor: func(p string) bool {
			for _, pre := range keep {
				if homes.Under(p, pre) {
					return true
				}
			}
			return false
		},
		Deadline: deadline,
		Log:      a.Log,
		Now:      a.Now,
		Flush: func(b sources.Batch) error {
			_, err := a.persist(nil, cfg, nil, b, nil, ru)
			return err
		},
		Save:        func() error { return store.SaveState(a.Home, st) },
		FlushEvents: outboxFileEvents,
		FlushEvery:  flushEvery,
		LimitsSent:  map[string]string{},
	}, st.GitHub.RebuildCursors)
	if err != nil {
		return fmt.Errorf("rollup rebuild: %w", err)
	}
	if stats.Complete {
		st.GitHub.Rebuild, st.GitHub.RebuildCursors = false, nil
		ru.ApplyFloor() // what the logs no longer hold, from the older file
		ru.Settle(a.Now(), true)
		a.Log.Printf("github: history re-read for the dashboard")
	}
	if ru.Dirty() {
		return ru.Save(paths.Rollup(a.Home))
	}
	return nil
}
