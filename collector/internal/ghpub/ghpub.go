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
// label, never the hostname), daily token and prompt totals per provider,
// source, model and account hash, and quota meters without emails or org
// names. No event, prompt, path or project ever leaves the machine this way.
package ghpub

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
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
	// Schema is the published file format version.
	Schema = 1
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
}

// UsageCols is the column order of usage-YYYY-MM.json rows.
var UsageCols = []string{"date", "provider", "source", "model", "acct", "in", "cacheW", "cacheR", "out", "events", "prompts"}

type usageFile struct {
	Schema  int      `json:"schema"`
	Machine string   `json:"machine"`
	Month   string   `json:"month"`
	Cols    []string `json:"cols"`
	Rows    [][]any  `json:"rows"`
}

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
}

// MachineDir is the repository folder for machine id.
func MachineDir(id string) string { return "data/machines/" + id }

func marshal(v any) []byte {
	b, _ := json.MarshalIndent(v, "", " ")
	return append(b, '\n')
}

// Files builds the machine's files: meta.json, one usage-YYYY-MM.json per
// month with data and quota.json (meters nil: none published). Deterministic
// for the same input, so unchanged data hashes the same.
func Files(m Machine, rows []rollup.Row, meters []model.LimitSnapshot, now time.Time) []ghapi.File {
	dir := MachineDir(m.ID)
	byMonth := map[string][][]any{}
	for _, r := range rows {
		if len(r.Date) < 7 {
			continue
		}
		month := r.Date[:7]
		byMonth[month] = append(byMonth[month], []any{r.Date, r.Provider, r.Source, r.Model, r.Acct, r.In, r.CacheW, r.CacheR, r.Out, r.Events, r.Prompts})
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
	files = append(files, ghapi.File{Path: dir + "/meta.json", Content: marshal(metaFile{Schema: Schema, ID: m.ID, Label: m.Label, OS: m.OS, Collector: m.Collector, UpdatedAt: now.UTC().Truncate(time.Second)})})
	return files
}

// Hash is the content hash recorded per published path.
func Hash(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Publish commits the files whose content differs from published (path ->
// Hash) in one commit, with meta.json, and returns the new hashes of every
// committed path. When no data file changed, nothing is committed unless
// refreshMeta (e.g. a daily "last seen"). A branch moved by another machine
// is retried: each machine writes only its own paths.
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
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if _, err = c.Commit(ctx, repo, branch, msg, commit); !errors.Is(err, ghapi.ErrConflict) {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	return changed, nil
}
