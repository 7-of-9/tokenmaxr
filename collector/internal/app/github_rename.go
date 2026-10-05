package app

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
	"github.com/7-of-9/tokenmaxr/collector/internal/ghapi"
	"github.com/7-of-9/tokenmaxr/collector/internal/ghpub"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

// Following a renamed or transferred usage repository (owner direction
// 2026-10-05: "Rename the repo to tokens ... 7-of-9.github.io/tokens/"):
// GitHub keeps redirecting the old name's API requests (301 for GET, 307
// for the rest, which ghapi follows), but GitHub Pages does not redirect the
// old dashboard address. Each publishing machine notices by itself (a
// redirected or not-found publish at once, otherwise a daily check), takes
// the new name into its config and publishes there; the dashboard address,
// the repository links to it and the dashboard check are worked out again
// for the new name. A sharer shares the new name with the fleet; a machine
// that adopted the fleet's sign-in follows either way.

const (
	// repoCheckEvery is how often a publishing machine asks GitHub what its
	// usage repository is called now (one GET).
	repoCheckEvery = 24 * time.Hour
	// repoLearnEvery spaces the checks while the repository's id is not
	// known (not answered yet, or the repository is unreachable).
	repoLearnEvery = time.Hour
)

// repoKnown reports whether st holds the id of cfg's repository.
func repoKnown(cfg *store.Config, st *store.State) bool {
	return st.GitHub.RepoID != 0 && strings.EqualFold(st.GitHub.RepoName, cfg.GitHub.Repo)
}

// repoCheckDue reports whether the repository's name is to be checked
// before this publish.
func repoCheckDue(cfg *store.Config, st *store.State, now time.Time) bool {
	every := repoCheckEvery
	if !repoKnown(cfg, st) {
		every = repoLearnEvery
	}
	return now.Sub(st.GitHub.RepoChecked) >= every
}

// repoMoved reports whether a publish's failure may mean the repository has
// another name now: not found, or a redirect a write is not sent through.
func repoMoved(err error) bool {
	s := ghapi.StatusOf(err)
	return s == http.StatusNotFound || s == http.StatusMovedPermanently
}

// checkRepo asks GitHub what cfg's repository is called now, by its name
// (GitHub follows a rename or transfer from it) or, when the name no longer
// leads to it (not found, or another repository has the name now), by its
// id, and follows a new name (renamedRepo). A repository transferred to an
// account where the App's installation does not cover it is not followed
// (installationCovers): its publishes fail as before. It reports whether
// cfg.GitHub.Repo changed.
func (a *App) checkRepo(ctx context.Context, c *ghapi.Client, cfg *store.Config, st *store.State, now time.Time) bool {
	st.GitHub.RepoChecked = now
	known := repoKnown(cfg, st)
	info, err := c.Repository(ctx, cfg.GitHub.Repo)
	if known && (err == nil && info.ID != st.GitHub.RepoID || ghapi.StatusOf(err) == http.StatusNotFound) {
		info, err = c.RepositoryByID(ctx, st.GitHub.RepoID)
	}
	c.Moved() // this check's own redirect is not news
	if err != nil || info.ID == 0 || !strings.Contains(info.FullName, "/") {
		return false
	}
	st.GitHub.RepoID = info.ID
	if strings.EqualFold(info.FullName, cfg.GitHub.Repo) {
		st.GitHub.RepoName = cfg.GitHub.Repo // GitHub's names ignore case
		st.GitHub.RepoOutside = ""
		return false
	}
	if !sameOwner(info.FullName, cfg.GitHub.Repo) {
		// A transfer. The usage repository is public, so any sign-in reads
		// it wherever it went: only the installation's own list says the
		// App can still publish to it.
		covered, err := installationCovers(ctx, c, info.ID)
		c.Moved()
		if err != nil {
			return false
		}
		if !covered {
			if st.GitHub.RepoOutside != info.FullName {
				a.Log.Printf("github: the usage repository %s is %s on GitHub now, which the %s App's installation does not cover: install the App with access to it to publish there", cfg.GitHub.Repo, info.FullName, buildinfo.GitHubAppSlug)
			}
			st.GitHub.RepoOutside = info.FullName
			return false
		}
	}
	st.GitHub.RepoOutside = ""
	from := cfg.GitHub.Repo
	cfg.GitHub.Repo = info.FullName
	if err := store.SaveConfig(a.Home, *cfg); err != nil {
		cfg.GitHub.Repo = from
		a.Log.Printf("github: %s is called %s on GitHub now, but the config could not be saved: %v", from, info.FullName, err)
		return false
	}
	a.renamedRepo(ctx, c, cfg, st, from, info.ID, now)
	return true
}

// renamedRepo records that cfg's repository (saved under its new name)
// was called from until now: the dashboard address is read again for the
// new name, the repository's links to the dashboard are moved there and its
// dashboard is checked again; what was published stays published.
func (a *App) renamedRepo(ctx context.Context, c *ghapi.Client, cfg *store.Config, st *store.State, from string, id int64, now time.Time) {
	g := &st.GitHub
	to, branch := cfg.GitHub.Repo, cfg.GitHub.BranchOrDefault()
	g.RepoID, g.RepoName, g.RepoChecked = id, to, now
	g.RenamedFrom, g.RenamedAt, g.FormerPagesURL = from, now, g.PagesURL
	formerName(g, from, to)
	if g.QuotaSwept == from+"@"+branch {
		g.QuotaSwept = to + "@" + branch
	}
	g.Site, g.SiteChecked = "", time.Time{}
	g.Linked, g.LinkRetryAt = "", time.Time{}
	g.PagesURL = ""
	if u, err := c.PagesURL(ctx, to); err == nil {
		g.PagesURL = u
	}
	dashboard := g.PagesURL
	if dashboard == "" {
		dashboard = "not on yet"
	}
	a.Log.Printf("github: the usage repository %s is %s on GitHub now (renamed or transferred); publishing there from now on (dashboard: %s)", from, to, dashboard)
}

// renamedShowFor is how long status and Settings mention a followed rename.
const renamedShowFor = 7 * 24 * time.Hour

// renamedFrom is the repository's name before a rename followed within
// renamedShowFor ("" when none).
func renamedFrom(st *store.State, now time.Time) string {
	if st == nil || st.GitHub.RenamedFrom == "" || now.Sub(st.GitHub.RenamedAt) >= renamedShowFor {
		return ""
	}
	return st.GitHub.RenamedFrom
}

// formerDashboards are the addresses the dashboard had before the rename
// this machine last followed, whose links move to the new one.
func formerDashboards(st *store.State) []string {
	if st.GitHub.RenamedFrom == "" {
		return nil
	}
	return []string{st.GitHub.FormerPagesURL, ghpub.PagesURLFor(st.GitHub.RenamedFrom)}
}

// maxFormerNames bounds GitHubState.FormerNames.
const maxFormerNames = 8

// formerName records name as one the repository called cur now had before
// (GitHubState.FormerNames, newest first); cur itself is not one any more.
func formerName(g *store.GitHubState, name, cur string) {
	g.FormerNames = slices.DeleteFunc(g.FormerNames, func(n string) bool {
		return strings.EqualFold(n, name) || strings.EqualFold(n, cur)
	})
	if name != "" && !strings.EqualFold(name, cur) {
		g.FormerNames = append([]string{name}, g.FormerNames...)
	}
	g.FormerNames = g.FormerNames[:min(len(g.FormerNames), maxFormerNames)]
}

// shareNamesRenamed reports whether a fleet share naming repo means cfg's
// repository under a name it had before (shared before the sharer followed
// a rename, or by a collector that does not follow).
func shareNamesRenamed(cfg *store.Config, st *store.State, repo string) bool {
	return strings.EqualFold(cfg.GitHub.Repo, st.GitHub.RepoName) &&
		slices.ContainsFunc(st.GitHub.FormerNames, func(n string) bool { return strings.EqualFold(n, repo) })
}

// sameOwner reports whether two "owner/name" repositories have the same
// owner (GitHub's logins ignore case).
func sameOwner(a, b string) bool {
	oa, _, _ := strings.Cut(a, "/")
	ob, _, _ := strings.Cut(b, "/")
	return strings.EqualFold(oa, ob)
}

// installationCovers reports whether an installation of the App the user's
// token can use grants the repository with id: a public repository answers
// any sign-in, but only one the installation covers can be published to.
func installationCovers(ctx context.Context, c *ghapi.Client, id int64) (bool, error) {
	insts, err := c.Installations(ctx)
	if err != nil {
		return false, err
	}
	for _, in := range insts {
		if in.AppSlug != buildinfo.GitHubAppSlug && (githubAppID() == 0 || in.AppID != githubAppID()) {
			continue
		}
		repos, err := c.InstallationRepos(ctx, in.ID)
		if err != nil {
			return false, err
		}
		if slices.ContainsFunc(repos, func(r ghapi.Repo) bool { return r.ID == id }) {
			return true, nil
		}
	}
	return false, nil
}

// sameRepository reports whether the repository named to is the one named
// from (by GitHub's id: a rename or transfer), its id and its name on
// GitHub now, which may be neither (to may be a name it had before).
func sameRepository(ctx context.Context, c *ghapi.Client, cfg *store.Config, st *store.State, from, to string) (int64, string, bool) {
	next, err := c.Repository(ctx, to)
	if err != nil || next.ID == 0 || !strings.Contains(next.FullName, "/") {
		return 0, "", false
	}
	id := st.GitHub.RepoID
	if !repoKnown(cfg, st) || !strings.EqualFold(cfg.GitHub.Repo, from) {
		prev, err := c.Repository(ctx, from)
		if err != nil {
			return 0, "", false
		}
		id = prev.ID
	}
	c.Moved()
	return next.ID, next.FullName, next.ID == id
}
