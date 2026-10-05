package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/fleetshare"
	"github.com/7-of-9/tokenmaxr/collector/internal/ghapi"
	"github.com/7-of-9/tokenmaxr/collector/internal/ghpub"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
	"github.com/7-of-9/tokenmaxr/collector/internal/upload"
)

// The fleet's GitHub sign-in (SPEC "Fleet GitHub sign-in"): a machine that
// publishes to GitHub with its own sign-in and uploads to a server shares
// that sign-in with the rest of its fleet through the server, sealed with
// the fleet key (internal/fleetshare). The server code never opens it, but
// a server that keeps the fleet key (a fleet linked from the browser, see
// FleetShareState.Escrowed) could. Every other machine of the fleet that
// uploads to the server and does not publish to GitHub adopts it, and
// publishes its own machine folder with it.
const (
	// fleetPollEvery is how often a machine without its own sign-in looks
	// for the fleet's (to adopt it, take a renewed one, or stop when it is
	// withdrawn).
	fleetPollEvery = time.Hour
	// fleetReshareEvery re-shares an unchanged sign-in, so a share lost on
	// the server comes back.
	fleetReshareEvery = 24 * time.Hour
	// fleetRetryEvery spaces retries after a failed share or withdrawal.
	fleetRetryEvery = 15 * time.Minute
	// fleetTimeout bounds each step's requests.
	fleetTimeout = 30 * time.Second
)

// fleetReach reports whether the server this machine is enrolled with can
// be reached for the fleet sign-in, even while uploads to it are switched
// off (ServerOff keeps the enrolment): a withdrawal still gets there.
func fleetReach(cfg *store.Config, sec store.Secrets) bool {
	return sec.Enrolled() && cfg.Endpoint != "" && cfg.Endpoint != store.NoServer
}

func (a *App) fleetClient(cfg *store.Config, sec store.Secrets) *upload.Client {
	return upload.NewClient(cfg.Endpoint, sec.Token, a.Version)
}

// fleetGitHub runs this tick's part of the fleet sign-in: sharing this
// machine's own sign-in when it changed or a day has passed, withdrawing it
// once sharing is off, or looking for the fleet's to adopt. cfg and sec are
// updated (and saved) when a sign-in is adopted, renewed or withdrawn. The
// caller holds the lock.
func (a *App) fleetGitHub(ctx context.Context, cfg *store.Config, sec *store.Secrets, st *store.State) {
	if !fleetReach(cfg, *sec) || st.Unauthorized {
		return
	}
	on := serverOn(cfg, *sec)
	now := a.Now()
	f := &st.GitHub.Fleet
	if cfg.GitHub == nil && !cfg.GitHubFleetOptOut && st.GitHub.MachineID != "" && f.Seen.IsZero() {
		// It published with its own sign-in and its owner stopped that
		// before sign-ins were shared (0.4.0): that stays a stop.
		cfg.GitHubFleetOptOut = true
		if err := store.SaveConfig(a.Home, *cfg); err != nil {
			a.Log.Printf("github: %v", err)
			return
		}
		a.Log.Printf("github: this machine stopped publishing to GitHub before; it does not take the fleet's sign-in (signing in here undoes that)")
	}
	retry := f.LastError == "" || now.Sub(f.Polled) >= fleetRetryEvery
	own := githubEnabled(cfg, *sec) && !cfg.GitHub.Adopted
	sharing := own && cfg.GitHub.SharesWithFleet()
	adopted := cfg.GitHub != nil && cfg.GitHub.Adopted
	switch {
	case sharing && on:
		s := a.fleetShare(cfg, *sec)
		changed := f.Shared != fleetshare.Fingerprint(sec.Key(), s) || f.By != sec.MachineID
		// Changed choices (or label) to pass on go out now, over this
		// machine's own share only (the periodic check).
		prefs := f.Prefs != fleetshare.PrefsFingerprint(sec.Key(), s) && !f.Superseded
		if retry && (changed || prefs || now.Sub(f.SharedAt) >= fleetReshareEvery) {
			a.shareGitHub(ctx, cfg, *sec, st, !changed)
		}
	case f.Shared != "" && !sharing:
		// Sharing switched off, or signed out: withdraw what this machine
		// shared (with uploads switched off too).
		if retry {
			a.unshareGitHub(ctx, cfg, *sec, st)
		}
	case !own && !cfg.GitHubFleetOptOut && (on || adopted) && now.Sub(f.Polled) >= fleetPollEvery:
		// An adopted machine whose uploads are switched off still follows
		// the share, so a withdrawn sign-in stops it.
		a.adoptGitHub(ctx, cfg, sec, st)
	}
}

// fleetShare is this machine's own sign-in as it is shared, with the
// publishing choices the machines that adopt it follow.
func (a *App) fleetShare(cfg *store.Config, sec store.Secrets) fleetshare.Share {
	return fleetshare.Share{Token: sec.GitHub.Token, Login: sec.GitHub.Login, UserID: sec.GitHub.UserID,
		Repo: cfg.GitHub.Repo, Branch: cfg.GitHub.Branch, SharedAt: a.Now().UTC(),
		Prefs: &fleetshare.Prefs{From: cfg.GitHub.Label, ShowCountry: cfg.GitHub.ShowCountry, ShowAccountHistory: true}}
}

// fleetFailed records a failed step; it is retried after fleetRetryEvery.
func (a *App) fleetFailed(st *store.State, what string, err error) {
	st.GitHub.Fleet.LastError = what + ": " + err.Error()
	a.Log.Printf("github: %s", st.GitHub.Fleet.LastError)
}

// shareGitHub seals this machine's own sign-in with the fleet key and puts
// it on the server, replacing any other. A periodic re-share of an unchanged
// sign-in (periodic) leaves a share another machine made since in place: the
// newest sign-in wins, and a dead one here must not replace a live one; it
// looks again a day later. Failures are recorded, never fatal.
func (a *App) shareGitHub(ctx context.Context, cfg *store.Config, sec store.Secrets, st *store.State, periodic bool) {
	f := &st.GitHub.Fleet
	f.Polled = a.Now()
	ctx, cancel := context.WithTimeout(ctx, fleetTimeout)
	defer cancel()
	c := a.fleetClient(cfg, sec)
	if periodic {
		cur, err := c.GetFleetGitHub(ctx)
		if err != nil {
			a.fleetFailed(st, "sharing the GitHub sign-in with the fleet", err)
			return
		}
		if cur != nil && cur.By != f.By {
			// It replaced this machine's share since (or a share lost on
			// the server is put back below).
			if !f.Superseded {
				a.Log.Printf("github: another machine shared its GitHub sign-in with the fleet since; this one's is not shared over it")
			}
			f.SharedAt, f.Superseded, f.LastError = f.Polled, true, ""
			return
		}
	}
	s := a.fleetShare(cfg, sec)
	blob, err := fleetshare.Seal(sec.Key(), s)
	var put upload.FleetGitHubPut
	if err == nil {
		put, err = c.PutFleetGitHub(ctx, blob)
	}
	if err != nil {
		a.fleetFailed(st, "sharing the GitHub sign-in with the fleet", err)
		return
	}
	first := f.Shared == "" || f.Superseded
	f.Shared, f.SharedAt, f.By, f.LastError = fleetshare.Fingerprint(sec.Key(), s), f.Polled, sec.MachineID, ""
	f.Prefs, f.Escrowed, f.Superseded = fleetshare.PrefsFingerprint(sec.Key(), s), put.Escrowed, false
	if first {
		a.Log.Printf("github: sign-in shared with the fleet through %s (sealed with the fleet key%s)", cfg.Endpoint, escrowNote(put.Escrowed))
	}
}

// escrowNote qualifies "sealed with the fleet key": a server that keeps the
// fleet key could open the share.
func escrowNote(escrowed bool) string {
	if escrowed {
		return ", which your server also keeps for linking machines from the browser, so whoever runs it could read it"
	}
	return "; your server cannot read it"
}

// unshareGitHub withdraws the share from the server when it is still the
// one this machine put there (another machine may have shared since).
func (a *App) unshareGitHub(ctx context.Context, cfg *store.Config, sec store.Secrets, st *store.State) {
	f := &st.GitHub.Fleet
	f.Polled = a.Now()
	ctx, cancel := context.WithTimeout(ctx, fleetTimeout)
	defer cancel()
	c := a.fleetClient(cfg, sec)
	by := f.By // the machine id it was shared under, before any re-enrolment
	if by == "" {
		by = sec.MachineID
	}
	cur, err := c.GetFleetGitHub(ctx)
	mine := err == nil && cur != nil && cur.By == by
	if mine {
		err = c.DeleteFleetGitHub(ctx)
	}
	if err != nil {
		a.fleetFailed(st, "withdrawing the GitHub sign-in shared with the fleet", err)
		return
	}
	f.Shared, f.SharedAt, f.By, f.LastError = "", time.Time{}, "", ""
	f.Prefs, f.Escrowed, f.Superseded = "", false, false
	if mine {
		a.Log.Printf("github: sign-in no longer shared with the fleet")
	}
}

// adoptGitHub looks for the fleet's shared sign-in. A machine that does not
// publish adopts it once the repository's fleet key proves to be its own,
// and rebuilds its rollup from all history for its first publish (nothing is
// queued for the server again). An adopted machine takes a renewed sign-in
// and the sharer's changed choices (followPrefs), and stops publishing when
// the share is withdrawn (its data stays).
func (a *App) adoptGitHub(ctx context.Context, cfg *store.Config, sec *store.Secrets, st *store.State) {
	f := &st.GitHub.Fleet
	f.Polled = a.Now()
	ctx, cancel := context.WithTimeout(ctx, fleetTimeout)
	defer cancel()
	cur, err := a.fleetClient(cfg, *sec).GetFleetGitHub(ctx)
	if err != nil {
		a.fleetFailed(st, "looking for the fleet's GitHub sign-in", err)
		return
	}
	adopted := cfg.GitHub != nil && cfg.GitHub.Adopted
	if cur == nil {
		f.LastError = ""
		if adopted {
			if gh := cfg.GitHub; len(gh.LocalPrefs) > 0 {
				// The choices made here outlast the share (adoptGitHub).
				cfg.GitHubKeptPrefs = &store.KeptPrefs{LocalPrefs: gh.LocalPrefs, ShowCountry: gh.ShowCountry}
			}
			cfg.GitHub, sec.GitHub = nil, nil
			if err := a.saveGitHub(cfg, sec); err != nil {
				a.fleetFailed(st, "stopping the fleet's GitHub sign-in", err)
				return
			}
			a.Log.Printf("github: the fleet's shared sign-in was withdrawn; this machine stops publishing to GitHub (its data stays)")
		}
		return
	}
	k := sec.Key()
	s, err := fleetshare.Open(k, cur.Blob)
	if err != nil {
		// Another fleet's (or a damaged) share: nothing here changes.
		a.fleetFailed(st, "the fleet's GitHub sign-in", err)
		return
	}
	same := adopted && sec.GitHub != nil && sec.GitHub.Token == s.Token && sec.GitHub.Login == s.Login &&
		cfg.GitHub.Repo == s.Repo && cfg.GitHub.Branch == s.Branch
	if same {
		// Shared again: the sign-in unchanged, maybe the choices. The share
		// already taken also passes them on while none were taken, as when
		// a collector before 0.4.2 took it and ignored them.
		if s.SharedAt.After(f.Seen) || (s.SharedAt.Equal(f.Seen) && cfg.GitHub.PrefsFrom == "") {
			if a.followPrefs(cfg.GitHub, st, s.Prefs) {
				if err := store.SaveConfig(a.Home, *cfg); err != nil {
					a.fleetFailed(st, "following the fleet's GitHub choices", err)
					return
				}
			}
			f.Seen = s.SharedAt
		}
		f.LastError = ""
		return
	}
	if !s.SharedAt.After(f.Seen) {
		// An older share served again (or the one withdrawn): only a newer
		// one is taken. sharedAt is sealed, so the server cannot change it.
		a.fleetFailed(st, "the fleet's GitHub sign-in", fmt.Errorf("the share from %s is not newer than the one this machine took (%s)",
			s.SharedAt.UTC().Format(time.RFC3339), f.Seen.UTC().Format(time.RFC3339)))
		return
	}
	// The repository must hold this machine's fleet key: a server pins it,
	// so a repository of another fleet is refused.
	key, _, err := ghpub.Join(ctx, newGitHubClient(s.Token, a.Version), s.Repo, k, true)
	if err == nil && !bytes.Equal(key, k) {
		err = ghpub.ErrFleetMismatch
	}
	if err != nil {
		a.fleetFailed(st, "adopting the fleet's GitHub sign-in for "+s.Repo, errors.New(githubErrorText(err)))
		return
	}
	moved := !adopted || cfg.GitHub.Repo != s.Repo || cfg.GitHub.BranchOrDefault() != (&store.GitHubConfig{Branch: s.Branch}).BranchOrDefault()
	gh := cfg.GitHub
	if !adopted {
		if st.GitHub.MachineID == "" {
			st.GitHub.MachineID = ghpub.NewMachineID()
		}
		gh = &store.GitHubConfig{Label: CleanLabel(cfg.MachineLabel), Adopted: true}
		if gh.Label == "" {
			gh.Label = "machine-" + strings.TrimPrefix(st.GitHub.MachineID, "m_")[:4]
		}
		if kp := cfg.GitHubKeptPrefs; kp != nil {
			// Chosen here under a share since withdrawn: still this machine's.
			gh.LocalPrefs = slices.Clone(kp.LocalPrefs)
			gh.ShowCountry = kp.ShowCountry && gh.LocalPref(store.PrefShowCountry)
		}
	}
	gh.Repo, gh.Branch = s.Repo, s.Branch
	a.followPrefs(gh, st, s.Prefs)
	cfg.GitHub, sec.GitHub = gh, &store.GitHubSecrets{Token: s.Token, Login: s.Login, UserID: s.UserID}
	cfg.GitHubKeptPrefs = nil
	if err := a.saveGitHub(cfg, sec); err != nil {
		a.fleetFailed(st, "adopting the fleet's GitHub sign-in", err)
		return
	}
	f.LastError, f.Seen = "", s.SharedAt
	st.GitHub.LastAttempt, st.GitHub.LastError = time.Time{}, "" // publish with it now
	if moved {
		st.GitHub.Published, st.GitHub.LastPublish = nil, time.Time{}
		st.GitHub.Site, st.GitHub.SiteChecked, st.GitHub.PagesURL = "", time.Time{}, ""
	}
	if !adopted {
		// The first publish needs the whole history in the rollup: re-read
		// it for the rollup alone, so the server is not sent it again.
		st.GitHub.StartRebuild()
		a.Log.Printf("github: publishing to %s as %q with the sign-in %s shared through the fleet", s.Repo, gh.Label, s.Login)
		return
	}
	a.Log.Printf("github: took the fleet's renewed GitHub sign-in for %s", s.Repo)
}

// followPrefs takes the sharer's publishing choices p (nil from a collector
// before 0.4.2: nothing changes) into gh for every option not chosen on this
// machine, records whose they are, and reports whether gh changed. An
// adopted machine taking choices for the first time keeps an option it has
// switched on (before 0.4.2 only its owner could have) as its own.
func (a *App) followPrefs(gh *store.GitHubConfig, st *store.State, p *fleetshare.Prefs) bool {
	if p == nil {
		return false
	}
	from := CleanLabel(p.From)
	if from == "" {
		from = "another machine"
	}
	changed := false
	for _, o := range []struct {
		name string
		cur  *bool
		v    bool
	}{
		{store.PrefShowCountry, &gh.ShowCountry, p.ShowCountry},
	} {
		switch {
		case gh.LocalPref(o.name):
		case gh.PrefsFrom == "" && *o.cur:
			gh.SetLocalPref(o.name)
			changed = true
		case *o.cur != o.v:
			*o.cur, changed = o.v, true
			st.GitHub.LastPublish = time.Time{} // meta.json with (or without) the country now
			st.GitHub.LastAttempt = time.Time{}
			a.Log.Printf("github: %s %s, following %s", o.name, onOff(o.v), from)
		}
	}
	if gh.PrefsFrom != from {
		gh.PrefsFrom, changed = from, true
	}
	return changed
}

// saveGitHub saves the GitHub sign-in and settings.
func (a *App) saveGitHub(cfg *store.Config, sec *store.Secrets) error {
	if err := store.SaveSecrets(a.Home, *sec); err != nil {
		return err
	}
	return store.SaveConfig(a.Home, *cfg)
}

// githubAuthFailed: GitHub refused the sign-in itself (revoked, or the App's
// access to the repository withdrawn).
func githubAuthFailed(err error) bool {
	c := ghapi.StatusOf(err)
	return c == http.StatusUnauthorized || c == http.StatusForbidden
}

// fleetLine describes the fleet sign-in for status and the settings page
// ("" when there is nothing to say).
func fleetLine(cfg *store.Config, sec store.Secrets, st *store.State) string {
	if cfg.GitHub == nil || !githubEnabled(cfg, sec) {
		return ""
	}
	if cfg.GitHub.Adopted {
		return fmt.Sprintf("signed in through your fleet (shared by %s)", sec.GitHub.Login)
	}
	switch {
	case !serverOn(cfg, sec):
		return ""
	case !cfg.GitHub.SharesWithFleet():
		return "sign-in not shared with your other machines"
	case st != nil && st.GitHub.Fleet.Superseded:
		return "sign-in not shared: your other machines use the one another of your machines shared since"
	case st != nil && st.GitHub.Fleet.Shared != "":
		return "sign-in shared with your other machines through your server (sealed with the fleet key" + escrowNote(st.GitHub.Fleet.Escrowed) + ")"
	}
	return "sign-in to be shared with your other machines through your server"
}
