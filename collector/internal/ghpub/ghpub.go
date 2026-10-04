// Package ghpub publishes this machine's daily aggregates to the user's own
// GitHub repository, where a Pages workflow turns them into a dashboard.
//
// Setup is two calls. Discover finds the user's installation of the App and
// their tokenmaxr repository (one holding tokenmaxr.json), or returns the two
// links that create them. Join returns the fleet key K, kept in the
// repository Actions variable TOKENMAXR_FLEET_KEY (readable only by
// collaborators): the first machine seeds it (with its server fleet key when
// it already has one, so account hashes match a private server), later
// machines read it.
//
// Everything Files produces is public by design: per-machine meta (a public
// label, never the hostname; the country only when the owner opts in), daily
// token and prompt totals per provider, source, model, account hash and event
// quality, quota meters without emails or org names, and (only when the owner
// opts in, as UTC days next to local dates reveal the time zone's offset)
// account history: provider account totals per UTC day with the local tokens
// per UTC day they reconcile against. No event, prompt text, path or project
// ever leaves the machine this way.
//
// PublishSite (site.go) keeps the repository's dashboard, site/, at the
// newest build any of its collectors carries.
package ghpub

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/ghapi"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/rollup"
)

const (
	// FleetVariable holds the fleet key (base64, 32 bytes).
	FleetVariable = "TOKENMAXR_FLEET_KEY"
	// MarkerFile marks a repository as a tokenmaxr publishing repository.
	MarkerFile = "tokenmaxr.json"
	// DefaultRepoName is suggested when creating the repository.
	DefaultRepoName = "tokenmaxr-usage"
	// Schema is the published file format version. Schema 2 added columns
	// (readers find them by name in "cols", so schema 1 files still read)
	// and the meta fields cc, firstSeenAt and lastEventAt, and the file
	// account-usage.json.
	Schema = 2
)

// Guide is what Discover found and, when something is missing, the links
// that fix it.
type Guide struct {
	User         ghapi.User
	Installation *ghapi.Installation
	Repo         *ghapi.Repo
	// CreateRepoURL creates the publishing repository from the template.
	CreateRepoURL string
	// InstallURL installs the App, or (when installed) lets the user add the
	// repository to it.
	InstallURL string
}

// Ready reports whether publishing can start.
func (g Guide) Ready() bool { return g.Repo != nil }

// Discover finds the user, the App's installation on their account and the
// first repository it can access that holds MarkerFile.
func Discover(ctx context.Context, c *ghapi.Client, appSlug, template string) (Guide, error) {
	var g Guide
	u, err := c.User(ctx)
	if err != nil {
		return g, err
	}
	g.User = u
	owner, name, _ := strings.Cut(template, "/")
	g.CreateRepoURL = "https://github.com/new?" + url.Values{
		"template_owner": {owner}, "template_name": {name}, "owner": {u.Login},
		"name": {DefaultRepoName}, "visibility": {"public"},
		"description": {"My AI token usage, published by tokenmaxr"},
	}.Encode()
	// Straight to the user's own account (no account picker).
	g.InstallURL = fmt.Sprintf("https://github.com/apps/%s/installations/new/permissions?target_id=%d", appSlug, u.ID)
	insts, err := c.Installations(ctx)
	if err != nil {
		return g, err
	}
	for i := range insts {
		if insts[i].AppSlug == appSlug && insts[i].Account.ID == u.ID {
			g.Installation = &insts[i]
			g.InstallURL = fmt.Sprintf("https://github.com/settings/installations/%d", insts[i].ID)
			break
		}
	}
	if g.Installation == nil {
		return g, nil
	}
	repos, err := c.InstallationRepos(ctx, g.Installation.ID)
	if err != nil {
		return g, err
	}
	// Prefer the default name, then any other marked repository.
	slices.SortStableFunc(repos, func(a, b ghapi.Repo) int {
		return boolInt(b.Name == DefaultRepoName) - boolInt(a.Name == DefaultRepoName)
	})
	for i := range repos {
		if repos[i].Owner.ID != u.ID {
			continue
		}
		b, err := c.GetFile(ctx, repos[i].FullName, MarkerFile)
		if err != nil {
			return g, err
		}
		var m struct {
			Tokenmaxr int `json:"tokenmaxr"`
		}
		if b != nil && json.Unmarshal(b, &m) == nil && m.Tokenmaxr >= 1 {
			g.Repo = &repos[i]
			return g, nil
		}
	}
	return g, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ErrFleetMismatch: the repository's fleet key differs from this machine's,
// and this machine cannot change its key because a server pins it.
var ErrFleetMismatch = errors.New("this machine belongs to a different fleet than the GitHub repository (its server pins its fleet key)")

// Join returns the fleet key: the repository's when it has one, otherwise it
// seeds the repository with local (this machine's existing key) or, when nil,
// a new random key; created reports that this call seeded it. A returned key
// that differs from a non-nil local means the caller must re-hash its history.
func Join(ctx context.Context, c *ghapi.Client, repo string, local []byte, serverEnrolled bool) (key []byte, created bool, err error) {
	for attempt := 0; attempt < 2; attempt++ {
		v, ok, err := c.GetVariable(ctx, repo, FleetVariable)
		if err != nil {
			return nil, false, err
		}
		if ok {
			k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v))
			if err != nil || len(k) != 32 {
				return nil, false, fmt.Errorf("repository variable %s is not a 32-byte base64 key", FleetVariable)
			}
			if local != nil && !bytes.Equal(k, local) && serverEnrolled {
				return nil, false, ErrFleetMismatch
			}
			return k, false, nil
		}
		k := local
		if k == nil {
			k = make([]byte, 32)
			if _, err := rand.Read(k); err != nil {
				return nil, false, err
			}
		}
		err = c.CreateVariable(ctx, repo, FleetVariable, base64.StdEncoding.EncodeToString(k))
		if errors.Is(err, ghapi.ErrConflict) {
			continue // another machine seeded it first: read theirs
		}
		if err != nil {
			return nil, false, err
		}
		return k, true, nil
	}
	return nil, false, errors.New("could not read or create the fleet key")
}

// NewMachineID returns a random public machine id: "m_" + 12 hex.
func NewMachineID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return "m_" + hex.EncodeToString(b)
}

// MeterMaxAge drops quota meters last read longer ago than this.
const MeterMaxAge = 7 * 24 * time.Hour

// Machine is how this machine appears in the repository.
type Machine struct {
	ID, Label, OS, Collector string
	// CC is the ISO 3166-1 alpha-2 country of the machine's time zone, set
	// only when the owner opted in (GitHubConfig.ShowCountry); anything that
	// is not two letters is never published.
	CC string
	// LastEventAt is the newest usage or activity event (zero: none).
	LastEventAt time.Time
}

// UsageCols is the column order of usage-YYYY-MM.json rows. date is the
// machine-local day; q is "" for exact usage and "e" for estimated (prompt
// columns sit on q "" rows). Token columns and events sum usage events.
// prompts and promptsNoUsage (prompts whose tokens were never recorded) sit
// on rows with model "", modelPrompts on the row of each prompt's model
// (model "": not known).
var UsageCols = []string{"date", "provider", "source", "model", "acct", "q", "in", "cacheW", "cacheW1h", "cacheR", "out", "reasoning", "events", "prompts", "promptsNoUsage", "modelPrompts"}

type usageFile struct {
	Schema  int      `json:"schema"`
	Machine string   `json:"machine"`
	Month   string   `json:"month"`
	Cols    []string `json:"cols"`
	Rows    [][]any  `json:"rows"`
}

// LedgerCols is the column order of account-usage.json ledger rows: local
// tokens (in + cacheW + cacheR + out) per UTC day, provider, source and
// account ("" when the attribution is not labelled quality), the server's
// account ledger (api/src/lib/account-usage.js).
var LedgerCols = []string{"date", "provider", "source", "acct", "tokens"}

// AccountSnapshot is one published provider account total (UTC day).
type AccountSnapshot struct {
	Provider    string `json:"provider"`
	Source      string `json:"source"`
	Acct        string `json:"acct"`
	Date        string `json:"date"`
	TotalTokens int64  `json:"totalTokens"`
	ObservedAt  string `json:"observedAt"`
}

type accountUsageFile struct {
	Schema     int               `json:"schema"`
	Machine    string            `json:"machine"`
	Snapshots  []AccountSnapshot `json:"snapshots"`
	LedgerCols []string          `json:"ledgerCols"`
	Ledger     [][]any           `json:"ledger"`
	// Unledgered are local dates whose rows the ledger does not cover.
	Unledgered []string `json:"unledgered,omitempty"`
}

// AccountHistory is what account-usage.json carries: the provider account
// totals this machine read, and its account ledger for those sources. The
// caller passes only the ledger rows of account-history sources.
type AccountHistory struct {
	Snapshots []rollup.AccountTotal
	Ledger    []rollup.LedgerRow
	// Unledgered are the local dates whose usage rows of those sources hold
	// tokens the ledger does not (an older rollup's totals): the dashboard
	// reconciles nothing against those days.
	Unledgered []string
}

// AccountUsagePath is machine id's account-usage.json.
func AccountUsagePath(id string) string { return MachineDir(id) + "/account-usage.json" }

// Meter is one published quota meter.
type Meter struct {
	Provider    string     `json:"provider"`
	Source      string     `json:"source"`
	Acct        string     `json:"acct,omitempty"`
	Window      string     `json:"window"`
	Scope       string     `json:"scope,omitempty"`
	Plan        string     `json:"plan,omitempty"`
	UsedPercent *float64   `json:"usedPercent,omitempty"`
	ResetsAt    *time.Time `json:"resetsAt,omitempty"`
	ObservedAt  time.Time  `json:"observedAt"`
	Status      string     `json:"status,omitempty"`
}

type quotaFile struct {
	Schema  int     `json:"schema"`
	Machine string  `json:"machine"`
	Meters  []Meter `json:"meters"`
}

type metaFile struct {
	Schema    int       `json:"schema"`
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	OS        string    `json:"os"`
	Collector string    `json:"collector"`
	UpdatedAt time.Time `json:"updatedAt"`
	CC        string    `json:"cc,omitempty"`
	// FirstSeenAt is the machine-local date of the first data.
	FirstSeenAt string `json:"firstSeenAt,omitempty"`
	// LastEventAt is RFC 3339 UTC, to the minute.
	LastEventAt string `json:"lastEventAt,omitempty"`
}

// country returns cc when it is an upper-case two-letter code, else "".
func country(cc string) string {
	if len(cc) != 2 || cc[0] < 'A' || cc[0] > 'Z' || cc[1] < 'A' || cc[1] > 'Z' {
		return ""
	}
	return cc
}

// MachineDir is the repository folder for machine id.
func MachineDir(id string) string { return "data/machines/" + id }

func marshal(v any) []byte {
	b, _ := json.MarshalIndent(v, "", " ")
	return append(b, '\n')
}

// Files builds the machine's files: meta.json, one usage-YYYY-MM.json per
// month with data, quota.json (meters nil: none published) and
// account-usage.json (none when history has neither totals nor ledger).
// Deterministic for the same input, so unchanged data hashes the same.
func Files(m Machine, rows []rollup.Row, meters []model.LimitSnapshot, history AccountHistory, now time.Time) []ghapi.File {
	dir := MachineDir(m.ID)
	byMonth := map[string][][]any{}
	for _, r := range rows {
		if len(r.Date) < 7 {
			continue
		}
		month := r.Date[:7]
		byMonth[month] = append(byMonth[month], []any{r.Date, r.Provider, r.Source, r.Model, r.Acct, r.Q,
			r.In, r.CacheW, r.CacheW1h, r.CacheR, r.Out, r.Reasoning, r.Events, r.Prompts, r.PromptsNoUsage, r.ModelPrompts})
	}
	var files []ghapi.File
	for _, month := range slices.Sorted(maps.Keys(byMonth)) {
		files = append(files, ghapi.File{Path: dir + "/usage-" + month + ".json", Content: marshal(usageFile{Schema: Schema, Machine: m.ID, Month: month, Cols: UsageCols, Rows: byMonth[month]})})
	}
	if meters != nil {
		q := quotaFile{Schema: Schema, Machine: m.ID, Meters: []Meter{}}
		for _, s := range meters {
			if now.Sub(s.ObservedAt) > MeterMaxAge {
				continue // a meter nobody read for a week says nothing now
			}
			q.Meters = append(q.Meters, Meter{Provider: s.Provider, Source: s.Source, Acct: s.Acct, Window: s.Window, Scope: s.Scope,
				Plan: s.Plan, UsedPercent: s.UsedPercent, ResetsAt: s.ResetsAt, ObservedAt: s.ObservedAt.UTC(), Status: s.Status})
		}
		slices.SortFunc(q.Meters, func(a, b Meter) int {
			return strings.Compare(a.Provider+a.Source+a.Acct+a.Window+a.Scope, b.Provider+b.Source+b.Acct+b.Window+b.Scope)
		})
		files = append(files, ghapi.File{Path: dir + "/quota.json", Content: marshal(q)})
	}
	if f, ok := accountUsage(m.ID, history); ok {
		files = append(files, ghapi.File{Path: AccountUsagePath(m.ID), Content: marshal(f)})
	}
	meta := metaFile{Schema: Schema, ID: m.ID, Label: m.Label, OS: m.OS, Collector: m.Collector, UpdatedAt: now.UTC().Truncate(time.Second), CC: country(m.CC)}
	for _, r := range rows {
		if len(r.Date) == len("2006-01-02") && (meta.FirstSeenAt == "" || r.Date < meta.FirstSeenAt) {
			meta.FirstSeenAt = r.Date
		}
	}
	if !m.LastEventAt.IsZero() {
		// An event stamped ahead of the clock is never "seen" in the future.
		last := m.LastEventAt
		if last.After(now) {
			last = now
		}
		meta.LastEventAt = last.UTC().Truncate(time.Minute).Format(time.RFC3339)
	}
	files = append(files, ghapi.File{Path: dir + "/meta.json", Content: marshal(meta)})
	return files
}

// accountUsage builds account-usage.json, sorted so that it is deterministic;
// ledger rows without tokens are left out (a missing entry is zero).
func accountUsage(machine string, h AccountHistory) (accountUsageFile, bool) {
	f := accountUsageFile{Schema: Schema, Machine: machine, Snapshots: []AccountSnapshot{}, LedgerCols: LedgerCols, Ledger: [][]any{}}
	for _, s := range h.Snapshots {
		f.Snapshots = append(f.Snapshots, AccountSnapshot{Provider: s.Provider, Source: s.Source, Acct: s.Acct, Date: s.Date,
			TotalTokens: s.TotalTokens, ObservedAt: s.ObservedAt.UTC().Truncate(time.Second).Format(time.RFC3339)})
	}
	slices.SortFunc(f.Snapshots, func(a, b AccountSnapshot) int {
		return cmp.Or(strings.Compare(a.Date, b.Date), strings.Compare(a.Source, b.Source), strings.Compare(a.Acct, b.Acct), strings.Compare(a.Provider, b.Provider))
	})
	ledger := slices.Clone(h.Ledger)
	slices.SortFunc(ledger, func(a, b rollup.LedgerRow) int {
		return cmp.Or(strings.Compare(a.Date, b.Date), strings.Compare(a.Provider, b.Provider), strings.Compare(a.Source, b.Source), strings.Compare(a.Acct, b.Acct))
	})
	for _, l := range ledger {
		if l.Tokens > 0 {
			f.Ledger = append(f.Ledger, []any{l.Date, l.Provider, l.Source, l.Acct, l.Tokens})
		}
	}
	f.Unledgered = slices.Compact(slices.Sorted(slices.Values(h.Unledgered)))
	return f, len(f.Snapshots) > 0 || len(f.Ledger) > 0
}

// Hash is the content hash recorded per published path.
func Hash(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Publish commits the files whose content differs from published (path ->
// Hash) in one commit, with meta.json, and returns the new hashes of every
// committed path ("" for a path deleted: a File.Delete is committed only for
// a path in published). When no data file changed, nothing is committed
// unless refreshMeta (e.g. a daily "last seen"). A branch moved by another
// machine is retried: each machine writes only its own paths.
func Publish(ctx context.Context, c *ghapi.Client, repo, branch, label string, files []ghapi.File, published map[string]string, refreshMeta bool) (map[string]string, error) {
	var commit []ghapi.File
	changed := map[string]string{}
	var meta *ghapi.File
	for i := range files {
		f := files[i]
		if strings.HasSuffix(f.Path, "/meta.json") {
			meta = &files[i]
			continue
		}
		if f.Delete {
			if _, ok := published[f.Path]; ok {
				commit = append(commit, f)
				changed[f.Path] = ""
			}
			continue
		}
		if h := Hash(f.Content); published[f.Path] != h {
			commit = append(commit, f)
			changed[f.Path] = h
		}
	}
	if len(commit) == 0 && !refreshMeta {
		return nil, nil
	}
	if meta != nil {
		commit = append(commit, *meta)
		changed[meta.Path] = Hash(meta.Content)
	}
	msg := fmt.Sprintf("tokenmaxr: %s (%d file", label, len(commit))
	if len(commit) != 1 {
		msg += "s"
	}
	msg += ")"
	err := commitRetrying(ctx, c, repo, branch, msg, commit)
	if ghapi.StatusOf(err) == http.StatusUnprocessableEntity && slices.ContainsFunc(commit, func(f ghapi.File) bool { return f.Delete }) {
		// GitHub cannot delete a path that is gone already (removed by
		// hand): commit the rest; the deleted paths count as deleted.
		err = commitRetrying(ctx, c, repo, branch, msg, slices.DeleteFunc(commit, func(f ghapi.File) bool { return f.Delete }))
	}
	if err != nil {
		return nil, err
	}
	return changed, nil
}

func commitRetrying(ctx context.Context, c *ghapi.Client, repo, branch, msg string, files []ghapi.File) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if _, err = c.Commit(ctx, repo, branch, msg, files); !errors.Is(err, ghapi.ErrConflict) {
			break
		}
	}
	return err
}
