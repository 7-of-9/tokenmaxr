// Package app implements the collector's commands and the tick
// (docs/agents/SPEC.md "Collector behaviour" and "Commands").
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/accounts"
	"github.com/7-of-9/tokenmaxr/collector/internal/accountusage"
	"github.com/7-of-9/tokenmaxr/collector/internal/autostart"
	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
	"github.com/7-of-9/tokenmaxr/collector/internal/configfix"
	"github.com/7-of-9/tokenmaxr/collector/internal/evidence"
	"github.com/7-of-9/tokenmaxr/collector/internal/homes"
	"github.com/7-of-9/tokenmaxr/collector/internal/limits"
	"github.com/7-of-9/tokenmaxr/collector/internal/logx"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/scan"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/claude"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/codex"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/cursor"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/gemini"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/grok"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

// ErrNotEnrolled means this machine has no fleet key yet: install has not
// completed (it enrols with a server, or sets up a GitHub or local machine).
var ErrNotEnrolled = errors.New("this machine is not set up: run `" + buildinfo.Product + " install`")

// App carries what every command needs.
type App struct {
	Home    string
	Version string
	// BuildTime is the release build time (RFC 3339 UTC), "" for dev builds.
	BuildTime string
	UserHome  string
	CodexHome string
	Log       *logx.Logger
	Out       io.Writer
	Now       func() time.Time
	// Sources builds the parsers; it is called once per home, because a
	// parser keeps per-home indexes (overridable in tests).
	Sources func() []sources.Source
	// ReadAccountUsage is the installed provider client's read-only account
	// history reader (replaceable in tests). Nil disables only this reader.
	ReadAccountUsage func(context.Context, string, string, []byte, time.Time) ([]model.AccountUsageSnapshot, error)
	// RefreshQuota asks one provider's installed client for a fresh quota
	// meter (quota.go). Nil disables quota refresh (tests never spawn clients).
	RefreshQuota func(ctx context.Context, provider string) error
	// WSL overrides WSL home discovery (tests); nil uses the platform default.
	WSL *homes.WSL
	// CodexLookups find CODEX_HOME values set outside this process (New sets
	// homes.DefaultCodexLookups); nil looks nowhere else (tests).
	CodexLookups []homes.CodexLookup
	// Autostart is the OS's login and scheduler entries (tests pass fakes);
	// nil uses the platform's. TaskName and RunValue override the entry
	// names (tests use TEST names).
	Autostart *autostart.System
	TaskName  string
	RunValue  string
	// UserPath edits the user PATH (tests); nil is the real one.
	UserPath interface {
		Add(dir string) (hint string, err error)
		Remove(dir string) error
	}
	// StartMenu makes the Windows Start-menu entry (tests pass fakes); nil
	// uses the shell's, for the default home only.
	StartMenu StartMenu
	// ApplicationsDirs is where the macOS Applications entry goes (tests);
	// nil is /Applications (or ~/Applications), for the default home only.
	ApplicationsDirs []string
	// InApp is set while this process is the desktop app.
	InApp bool
	// OpenURL opens the browser for a first install's link (tests); nil is
	// the platform default.
	OpenURL func(string) error
	// checked is set once this process has run the config checks, so a
	// (re)started collector fixes Claude's retention on its first tick
	// instead of waiting out the hourly interval.
	checked bool
	// updateChecked is set once the desktop app has checked for an update,
	// so a (re)started app checks on its first tick. Headless runs start a
	// process every minute and keep to updateEvery instead.
	updateChecked bool
}

// New resolves directories. The log is opened lazily by commands that write.
func New(homeFlag, version string, out io.Writer) (*App, error) {
	home, err := paths.Home(homeFlag)
	if err != nil {
		return nil, err
	}
	uh, err := paths.UserHome()
	if err != nil {
		return nil, err
	}
	a := &App{
		Home:             home,
		Version:          version,
		UserHome:         uh,
		CodexHome:        paths.CodexHome(uh),
		Log:              logx.Discard(),
		Out:              out,
		Now:              time.Now,
		Sources:          DefaultSources,
		ReadAccountUsage: accountusage.ReadCodex,
	}
	a.RefreshQuota = a.refreshQuotaWithClient
	if os.Getenv(paths.UserHomeEnv) == "" {
		// A fixture home (D0M1_USER_HOME) ignores CODEX_HOME, wherever it is set.
		a.CodexLookups = homes.DefaultCodexLookups()
	}
	return a, nil
}

// DefaultSources is every parser the collector ships.
func DefaultSources() []sources.Source {
	return []sources.Source{claude.New(), codex.New(), grok.New(), cursor.New(), gemini.New()}
}

// OpenLog starts writing collector.log; echo copies lines to w (nil: none).
func (a *App) OpenLog(echo io.Writer) error {
	if err := os.MkdirAll(a.Home, 0o700); err != nil {
		return err
	}
	l, err := logx.Open(paths.Log(a.Home))
	if err != nil {
		return err
	}
	l.Echo = echo
	a.Log = l
	return nil
}

func (a *App) printf(format string, args ...any) { fmt.Fprintf(a.Out, format, args...) }

// enabledSources filters the parsers by config.
func (a *App) enabledSources(cfg *store.Config) []sources.Source {
	var out []sources.Source
	for _, s := range a.Sources() {
		if cfg.SourceEnabled(s.Name()) {
			out = append(out, s)
		}
	}
	return out
}

// TZOffsetMin is the machine's UTC offset at t, in minutes east.
func TZOffsetMin(t time.Time) int {
	_, off := t.In(time.Local).Zone()
	return off / 60
}

// homes lists this tick's scan roots (SPEC "Scan roots"): the OS home,
// config extraHomes, Codex directories set outside this process
// (codexhomes.go) and, on Windows, the homes of running WSL distros.
func (a *App) homes(cfg *store.Config) homes.Result { return a.discoverHomes(cfg, false) }

// homesLive is homes with the CODEX_HOME lookups run now, not cached
// (doctor and scan --dry-run report what is there at this moment).
func (a *App) homesLive(cfg *store.Config) homes.Result { return a.discoverHomes(cfg, true) }

func (a *App) discoverHomes(cfg *store.Config, live bool) homes.Result {
	dirs, notes := a.codexDirs(live)
	r := homes.Discover(homes.Options{UserHome: a.UserHome, Extra: cfg.ExtraHomes, OSCodexHome: a.CodexHome, CodexDirs: dirs,
		DiscoverWSL: cfg.DiscoverWSL, WSL: a.WSL})
	r.Notes = append(notes, r.Notes...)
	return r
}

// hashFn hashes native account ids with the fleet key k (nil without one).
func hashFn(k []byte) func(provider, nativeID string) string {
	if len(k) == 0 {
		return nil
	}
	return func(provider, nativeID string) string { return model.AccountHash(k, provider, nativeID) }
}

// envFor builds the parser environment for one home. Attribution follows
// the SPEC "Accounts" order: the stream's own record, the live (provider,
// home) timeline in st, then the harvested evidence in ix.
func (a *App) envFor(k []byte, cfg *store.Config, st *store.State, h homes.Home, ix *evidence.Index) *sources.Env {
	key := h.Key()
	codexQuota := ""
	if h.Kind == homes.KindOS {
		codexQuota = paths.CodexQuota(a.Home)
	}
	return &sources.Env{
		Home:       h.Path,
		CodexHome:  h.CodexHome(a.CodexHome),
		CodexQuota: codexQuota,
		Machine:    cfg.MachineLabel,
		Attribute: func(provider string, ts time.Time, sessionID string, hint sources.Hint) (string, string) {
			return accounts.Resolve(st.Accounts, ix, key, provider, ts, sessionID, hint)
		},
		HashID: hashFn(k),
		Label:  func(acct string) string { return cfg.AccountLabels[acct] },
		OrgSince: func(provider, org string) time.Time {
			return st.NoteOrg(key+"|"+provider, org, a.Now())
		},
		TZOffsetMin: TZOffsetMin,
		Prompts:     cfg.Prompts,
	}
}

// scanHomes pairs every home with its own parser instances. The evidence
// index is built once, from state, for all of them.
func (a *App) scanHomes(k []byte, cfg *store.Config, st *store.State, hs []homes.Home) []scan.Home {
	ix := evidence.Build(st.Evidence, accounts.Spans(st.Accounts))
	out := make([]scan.Home, 0, len(hs))
	for _, h := range hs {
		out = append(out, scan.Home{Env: a.envFor(k, cfg, st, h, ix), Sources: sourcesFor(h, a.enabledSources(cfg))})
	}
	return out
}

// sourcesFor is the parsers that read home h: all of them, or the Codex
// parser alone for a Codex directory (homes.KindCodex).
func sourcesFor(h homes.Home, all []sources.Source) []sources.Source {
	if !h.CodexOnly() {
		return all
	}
	var out []sources.Source
	for _, s := range all {
		if s.Name() == model.SourceCodex {
			out = append(out, s)
		}
	}
	return out
}

// harvest reads every home's identity evidence into st, resuming from the
// watermarks, until deadline. New default labels go into cfg. It reports
// whether the harvest completed and whether cfg changed.
func (a *App) harvest(k []byte, cfg *store.Config, st *store.State, hs []homes.Home, deadline time.Time) (complete, cfgChanged bool) {
	if st.Harvest == nil {
		st.Harvest = map[string]evidence.Mark{}
	}
	complete = true
	var recs []evidence.Record
	files := 0
	for _, h := range hs {
		res := evidence.Harvest(evidence.Options{
			Home:      h.Path,
			HomeKey:   h.Key(),
			CodexHome: h.CodexHome(a.CodexHome),
			Hash:      hashFn(k),
			Deadline:  deadline,
		}, st.Harvest)
		recs = append(recs, res.Records...)
		files += res.Files
		for acct, label := range res.Labels {
			if _, ok := cfg.AccountLabels[acct]; !ok {
				cfg.AccountLabels[acct] = label
				cfgChanged = true
			}
		}
		if !res.Complete {
			complete = false
			break
		}
	}
	if len(recs) > 0 {
		before := len(st.Evidence)
		st.Evidence = evidence.Merge(st.Evidence, recs)
		if n := len(st.Evidence) - before; n > 0 {
			a.Log.Printf("accounts: %d new evidence records from %d files", n, files)
		}
	}
	return complete, cfgChanged
}

// probeAccounts extends each home's timeline and adds default labels for
// new accounts. It reports whether config.json needs saving.
func (a *App) probeAccounts(k []byte, cfg *store.Config, st *store.State, now time.Time, hs []homes.Home) (changed bool) {
	for _, h := range hs {
		for _, o := range accounts.Probe(h.Path, h.CodexHome(a.CodexHome)) {
			acct := accounts.Hash(k, o)
			st.Accounts = accounts.Observe(st.Accounts, h.Key(), o.Provider, acct, now)
			if o.Provider == model.ProviderAnthropic {
				// Before the quota refresh: the reading Claude Code is asked
				// for next is then after any organisation switch noted here.
				st.NoteOrg(h.Key()+"|"+o.Provider, limits.ClaudeOrg(h.Path, hashFn(k)), now)
			}
			if _, ok := cfg.AccountLabels[acct]; !ok && o.Label != "" {
				cfg.AccountLabels[acct] = o.Label
				changed = true
			}
		}
	}
	return changed
}

// checkKey names a config check in st.Checks: the OS home keeps the plain
// keys of the spec; another home's checks carry its label in brackets.
func checkKey(key string, h homes.Home) string {
	if h.Kind == homes.KindOS {
		return key
	}
	return key + "[" + h.Label() + "]"
}

// runChecksIn runs the config checks and fixes in every home (SPEC: fixes
// apply per home with the same backup rules). Machine-wide checks come from
// the OS home only.
func (a *App) runChecksIn(fix bool, hs []homes.Home) configfix.Result {
	res := configfix.Result{Checks: map[string]string{}}
	for _, h := range hs {
		if h.CodexOnly() {
			// A Codex directory holds no Claude or Grok settings to check or fix.
			st, msg := configfix.CodexHistory(filepath.Join(h.Path, "config.toml"))
			res.Checks[checkKey("codexHistory", h)] = st
			if msg != "" {
				res.Notes = append(res.Notes, h.Label()+": codex: "+msg)
			}
			continue
		}
		r := configfix.Run(configfix.Options{UserHome: h.Path, CodexHome: h.CodexHome(a.CodexHome), Fix: fix})
		for k, v := range r.Checks {
			if k == "smartAppControl" && h.Kind != homes.KindOS {
				continue
			}
			res.Checks[checkKey(k, h)] = v
		}
		for _, n := range r.Notes {
			if h.Kind != homes.KindOS {
				n = h.Label() + ": " + n
			}
			res.Notes = append(res.Notes, n)
		}
	}
	return res
}

// statePaths lists the homes of the last tick, for a heartbeat sent outside
// a tick.
func statePaths(st *store.State) []string {
	out := make([]string, 0, len(st.Homes))
	for _, h := range st.Homes {
		out = append(out, h.Path)
	}
	return out
}

// homeLine is one status line for a home.
func homeLine(h store.HomeState) string {
	return fmt.Sprintf("  %-18s %s (%d files)", h.Kind+labelSuffix(h.Distro), h.Path, h.Files)
}

func labelSuffix(distro string) string {
	if distro == "" {
		return ""
	}
	return ":" + distro
}

// skippedLine is the status line for a WSL distro that was not running.
func skippedLine(distro string) string {
	return fmt.Sprintf("  %-18s not running, skipped (never started by the collector)", homes.KindWSL+":"+distro)
}

// isHomeCheck reports whether a checks key belongs to a non-OS home.
func isHomeCheck(key string) bool { return strings.HasSuffix(key, "]") && strings.Contains(key, "[") }

// binPath is <home>/bin/<variant>.
func (a *App) binPath(gui bool) string { return filepath.Join(paths.Bin(a.Home), paths.ExeName(gui)) }
