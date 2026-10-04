// Package store reads and writes the collector's config.json, secrets.json
// and state.json (docs/agents/SPEC.md "Collector behaviour").
package store

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
	"github.com/7-of-9/tokenmaxr/collector/internal/evidence"
	"github.com/7-of-9/tokenmaxr/collector/internal/fsx"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/recent"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
)

// The release identity comes from internal/buildinfo (collector/release.json).
var (
	DefaultEndpoint  = buildinfo.DefaultEndpoint
	DefaultUpdateURL = buildinfo.UpdateURL
)

// NoServer is the endpoint value for a machine without a server: it
// publishes to GitHub, or keeps its numbers local.
const NoServer = "off"

// Config is the user-editable config.json.
type Config struct {
	Endpoint string `json:"endpoint"`
	// ServerOff pauses uploads to Endpoint; the enrolment is kept.
	ServerOff    bool   `json:"serverOff,omitempty"`
	UpdateURL    string `json:"updateUrl"`
	MachineLabel string `json:"machineLabel"`
	// AccountLabels maps acct hash -> private label. Local only; labels leave
	// the machine only inside (server-encrypted) prompt records.
	AccountLabels map[string]string `json:"accountLabels"`
	// Sources enables or disables a source by name; a missing entry is enabled.
	Sources            map[string]bool `json:"sources"`
	Prompts            bool            `json:"prompts"`
	PromptExcludeAccts []string        `json:"promptExcludeAccts"`
	FixConfig          bool            `json:"fixConfig"`
	Autostart          bool            `json:"autostart"`
	// App runs the collector as the desktop app (tray icon / menu-bar
	// item) at login; install --no-app turns it off for the headless
	// every-minute tick.
	App        bool `json:"app"`
	AutoUpdate bool `json:"autoUpdate"`
	// ExtraHomes are additional home directories to scan with every parser
	// and account probe (SPEC "Scan roots"); DiscoverWSL adds the homes of
	// running WSL distros on Windows.
	ExtraHomes  []string `json:"extraHomes"`
	DiscoverWSL bool     `json:"discoverWsl"`
	// Panel is the desktop app's pinned live panel (absent until first
	// pinned).
	Panel *Panel `json:"panel,omitempty"`
	// GitHub publishes daily aggregates to the user's own GitHub repository
	// and Pages dashboard (absent: off). The token lives in secrets.json.
	GitHub *GitHubConfig `json:"github,omitempty"`
}

// GitHubConfig is the GitHub publisher's settings. Everything published is
// public: daily token and prompt totals, quota meters, and Label.
type GitHubConfig struct {
	// Repo is "owner/name" of the publishing repository; Branch its branch.
	Repo   string `json:"repo"`
	Branch string `json:"branch,omitempty"`
	// Label is this machine's public name there (never the hostname by default).
	Label string `json:"label"`
	// PublishEveryMinutes is the publishing interval (default 30, minimum 10).
	PublishEveryMinutes int `json:"publishEveryMinutes,omitempty"`
	// NoQuota leaves quota meters (plan, % used, reset time) unpublished.
	NoQuota bool `json:"noQuota,omitempty"`
}

// PublishEvery returns the effective publishing interval.
func (g *GitHubConfig) PublishEvery() time.Duration {
	m := g.PublishEveryMinutes
	switch {
	case m <= 0:
		m = 30
	case m < 10:
		m = 10
	}
	return time.Duration(m) * time.Minute
}

// BranchOrDefault returns Branch, or "main".
func (g *GitHubConfig) BranchOrDefault() string {
	if g.Branch == "" {
		return "main"
	}
	return g.Branch
}

// Panel is whether the live panel is pinned and where it was left: X, Y
// in the platform's screen coordinates (Windows: the top-left corner in
// physical pixels; macOS: the top-left corner in points, y up). Placed is
// false until it has been moved, for the default spot.
type Panel struct {
	Pinned bool `json:"pinned"`
	X      int  `json:"x"`
	Y      int  `json:"y"`
	Placed bool `json:"placed"`
}

func DefaultConfig() Config {
	host, _ := os.Hostname()
	return Config{
		Endpoint:      DefaultEndpoint,
		UpdateURL:     DefaultUpdateURL,
		MachineLabel:  host,
		AccountLabels: map[string]string{},
		Sources: map[string]bool{
			model.SourceClaudeCode: true,
			model.SourceCodex:      true,
			model.SourceGrokCLI:    true,
		},
		Prompts:            true,
		PromptExcludeAccts: []string{},
		FixConfig:          true,
		Autostart:          true,
		App:                true,
		AutoUpdate:         true,
		ExtraHomes:         []string{},
		DiscoverWSL:        true,
	}
}

func (c *Config) SourceEnabled(name string) bool {
	on, ok := c.Sources[name]
	return !ok || on
}

func (c *Config) PromptExcluded(acct string) bool {
	return slices.Contains(c.PromptExcludeAccts, acct)
}

// LoadConfig returns defaults overlaid with config.json; a missing file is not
// an error, a malformed one is (it is user-edited, so never overwrite it).
func LoadConfig(home string) (Config, error) {
	c := DefaultConfig()
	b, err := fsx.ReadFile(paths.Config(home))
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("config.json: %w", err)
	}
	if c.AccountLabels == nil {
		c.AccountLabels = map[string]string{}
	}
	if c.PromptExcludeAccts == nil {
		c.PromptExcludeAccts = []string{}
	}
	if c.ExtraHomes == nil {
		c.ExtraHomes = []string{}
	}
	if c.Endpoint == "" {
		c.Endpoint = DefaultEndpoint
	}
	if c.UpdateURL == "" {
		c.UpdateURL = DefaultUpdateURL
	}
	return c, nil
}

// Server is the API base URL this machine enrols with and uploads to, or ""
// when it has none (NoServer, or a build without a default endpoint).
func (c Config) Server() string {
	if c.Endpoint == NoServer || c.ServerOff {
		return ""
	}
	return c.Endpoint
}

// ParseServer normalises an --endpoint or settings value: "off" or "none"
// is NoServer, else an http(s) URL without its trailing slash.
func ParseServer(s string) (string, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	switch strings.ToLower(s) {
	case "off", "none":
		return NoServer, nil
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("server URL %q: use https://host[/path], or off", s)
	}
	return s, nil
}

func SaveConfig(home string, c Config) error {
	return writeJSON(paths.Config(home), c, 0o600)
}

// Secrets is secrets.json: user-only file permissions. macOS gets 0600; on
// Windows the file gets a protected DACL for the current user only
// (acl_windows.go), because the ACL inherited from %LOCALAPPDATA% can
// include other principals (sandbox groups, Authenticated Users under a
// custom --home) and os.Chmod there only toggles the read-only bit.
type Secrets struct {
	Token     string `json:"token"`
	K         string `json:"k"` // base64 fleet key, 32 bytes
	MachineID string `json:"machineId"`
	// GitHub is the GitHub publisher's sign-in (absent: not signed in).
	GitHub *GitHubSecrets `json:"github,omitempty"`
}

// GitHubSecrets is the GitHub App user token and the user it belongs to. The
// token is a user-to-server token limited to the repositories the user
// granted the App; it does not expire (the App opts out of expiry).
type GitHubSecrets struct {
	Token  string `json:"token"`
	Login  string `json:"login"`
	UserID int64  `json:"userId"`
}

// Enrolled: enrolled with an HTTP server (token, machine id and fleet key), so
// this machine uploads to cfg.Endpoint.
func (s Secrets) Enrolled() bool { return s.Token != "" && s.MachineID != "" && len(s.Key()) == 32 }

// HasFleet: this machine holds its fleet key K, from a server enrolment or a
// GitHub sign-in, so it can hash account ids and collect. Delivery to each
// destination is gated separately.
func (s Secrets) HasFleet() bool { return len(s.Key()) == 32 }

// Key decodes K; nil when absent or malformed.
func (s Secrets) Key() []byte {
	k, err := base64.StdEncoding.DecodeString(s.K)
	if err != nil || len(k) != 32 {
		return nil
	}
	return k
}

func LoadSecrets(home string) (Secrets, error) {
	var s Secrets
	b, err := fsx.ReadFile(paths.Secrets(home))
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	// Every start re-checks the ACL: a file from an older build or a
	// hand-copied one may still carry the inherited directory ACL.
	if err := ensureSecretsACL(paths.Secrets(home)); err != nil {
		return s, fmt.Errorf("secrets.json: %w", err)
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("secrets.json is malformed")
	}
	return s, nil
}

func SaveSecrets(home string, s Secrets) error {
	b, err := marshalJSON(s)
	if err != nil {
		return err
	}
	return writeSecrets(paths.Secrets(home), b)
}

// FileCursor is a parser cursor plus collector bookkeeping.
type FileCursor struct {
	sources.Cursor
	// Idle marks a file whose parser state has settled (no pending carry
	// output); unchanged idle files are not re-opened.
	Idle bool `json:"idle,omitempty"`
}

// Interval is one span during which an account was observed active in one
// home (SPEC: timelines are keyed by provider and home). Home is "" for the
// OS user home, so intervals written before v1.3 keep their meaning.
type Interval struct {
	Provider string    `json:"provider"`
	Home     string    `json:"home,omitempty"`
	Acct     string    `json:"acct"`
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
}

// HomeState is one scan root as of the last tick (status shows them).
type HomeState struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Distro string `json:"distro,omitempty"`
	Files  int    `json:"files"`
}

// SourceState is per-source health.
type SourceState struct {
	Files       int        `json:"files"`
	Pending     int        `json:"pending"` // files left for a later tick (backfill)
	LastEventTS *time.Time `json:"lastEventTs,omitempty"`
	LastError   string     `json:"lastError,omitempty"`
}

// Backoff tracks upload failures.
type Backoff struct {
	Failures int       `json:"failures"`
	Until    time.Time `json:"until"`
	// Limit is the item cap for the next request (halved on 413 and
	// on repeated 5xx, grown back on success).
	Limit int `json:"limit"`
}

// State is state.json, written atomically.
// LimitsSent returns the queued-limits map, creating it.
func (s *State) LimitsSent() map[string]string {
	if s.Limits == nil {
		s.Limits = map[string]string{}
	}
	return s.Limits
}

type State struct {
	// AccountHistory tracks account-wide provider reads separately from file
	// cursors. Successful snapshots are durably queued before this advances.
	AccountHistory AccountHistoryState `json:"accountHistory,omitempty"`
	// GitHub is the GitHub publisher's progress.
	GitHub GitHubState `json:"github,omitzero"`
	// Cursors is source -> absolute path -> cursor.
	Cursors  map[string]map[string]FileCursor `json:"cursors"`
	Accounts []Interval                       `json:"accounts"`
	Sources  map[string]SourceState           `json:"sources"`
	Checks   map[string]string                `json:"checks"`
	// Limits is snapshot id -> fingerprint of the last limit snapshot
	// queued, so an unchanged meter (or plan row) is not re-sent every tick.
	Limits  map[string]string `json:"limits,omitempty"`
	Backoff Backoff           `json:"backoff"`
	// Homes are the scan roots of the last tick; WSLSkipped the WSL distros
	// that were installed but not running (never started by the collector).
	Homes      []HomeState `json:"homes,omitempty"`
	WSLSkipped []string    `json:"wslSkipped,omitempty"`

	LastTick        time.Time `json:"lastTick"`
	LastTickMs      int64     `json:"lastTickMs"`
	LastScan        time.Time `json:"lastScan"`
	LastFullReparse time.Time `json:"lastFullReparse"`
	LastConfigCheck time.Time `json:"lastConfigCheck"`
	LastHeartbeat   time.Time `json:"lastHeartbeat"`
	LastUploadOK    time.Time `json:"lastUploadOk"`
	LastUploadErr   string    `json:"lastUploadErr,omitempty"`
	LastUpdateCheck time.Time `json:"lastUpdateCheck"`
	LastUpdateErr   string    `json:"lastUpdateErr,omitempty"`
	// QuotaRefreshed is when each provider's client was last asked for a
	// fresh quota meter (attempts, successful or not).
	QuotaRefreshed map[string]time.Time `json:"quotaRefreshed,omitempty"`
	ClockSkewMs    int64                `json:"clockSkewMs"`

	// Unauthorized is set on a 401: uploads stop (collection continues) until
	// a re-install or the periodic retry succeeds.
	Unauthorized   bool      `json:"unauthorized,omitempty"`
	UnauthorizedAt time.Time `json:"unauthorizedAt,omitzero"`

	// Recent is the desktop app's local numbers per provider, and RecentIDs
	// the bounded id set that keeps them idempotent (SPEC "Local numbers").
	Recent    map[string]*recent.Provider `json:"recent,omitempty"`
	RecentIDs recent.IDs                  `json:"recentIds"`

	// Evidence is the harvested account evidence (hashes only; SPEC
	// "Accounts"), Harvest the harvest watermarks by file, and
	// AttribVersion the attribution version the cursors were last reset
	// for (evidence.AttribVersion).
	Evidence      []evidence.Record        `json:"evidence,omitempty"`
	Harvest       map[string]evidence.Mark `json:"harvest,omitempty"`
	AttribVersion int                      `json:"attribVersion,omitempty"`
}

type AccountHistoryState struct {
	Account     string    `json:"account,omitempty"`
	Days        int       `json:"days,omitempty"`
	TotalTokens int64     `json:"totalTokens,omitempty"`
	LastAttempt time.Time `json:"lastAttempt,omitzero"`
	LastSuccess time.Time `json:"lastSuccess,omitzero"`
	LastRefresh time.Time `json:"lastRefresh,omitzero"`
	LastError   string    `json:"lastError,omitempty"`
	// Queued is snapshot id -> last queued total (not observedAt).
	Queued map[string]int64 `json:"queued,omitempty"`
}

// RecentWindow is the local numbers, updated in place.
func (st *State) RecentWindow() *recent.Window {
	if st.Recent == nil {
		st.Recent = map[string]*recent.Provider{}
	}
	return &recent.Window{Providers: st.Recent, IDs: &st.RecentIDs}
}

func NewState() *State {
	return &State{
		Cursors: map[string]map[string]FileCursor{},
		Sources: map[string]SourceState{},
		Checks:  map[string]string{},
		Harvest: map[string]evidence.Mark{},
	}
}

// LoadState reads state.json. A corrupt file is set aside and replaced by a
// fresh state: that only costs a full reparse, which is idempotent.
func LoadState(home string) (*State, error) {
	st := NewState()
	p := paths.State(home)
	b, err := fsx.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, st); err != nil {
		os.Rename(p, p+".corrupt")
		return NewState(), fmt.Errorf("state.json was corrupt (moved aside, starting fresh): %w", err)
	}
	if st.Cursors == nil {
		st.Cursors = map[string]map[string]FileCursor{}
	}
	if st.Sources == nil {
		st.Sources = map[string]SourceState{}
	}
	if st.Checks == nil {
		st.Checks = map[string]string{}
	}
	if st.Harvest == nil {
		st.Harvest = map[string]evidence.Mark{}
	}
	return st, nil
}

// SaveState writes compact JSON: state.json holds a cursor per log file.
func SaveState(home string, st *State) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(paths.State(home), b, 0o600)
}

func writeJSON(path string, v any, perm os.FileMode) error {
	b, err := marshalJSON(v)
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(path, b, perm)
}

func marshalJSON(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// GitHubState is what the GitHub publisher remembers between ticks.
type GitHubState struct {
	// MachineID is this machine's random public id in the repository ("m_" +
	// 12 hex); it is not derived from any secret or local identifier.
	MachineID   string    `json:"machineId,omitempty"`
	LastAttempt time.Time `json:"lastAttempt,omitzero"`
	LastPublish time.Time `json:"lastPublish,omitzero"`
	LastError   string    `json:"lastError,omitempty"`
	// Published is path -> sha256 of what was last committed, so unchanged
	// files are never committed again.
	Published map[string]string `json:"published,omitempty"`
	PagesURL  string            `json:"pagesUrl,omitempty"`
	// Rebuild: the rollup is being rebuilt from all history with its own
	// cursors, so nothing is queued for the server again (the main cursors
	// stay where they are).
	Rebuild        bool                             `json:"rebuild,omitempty"`
	RebuildCursors map[string]map[string]FileCursor `json:"rebuildCursors,omitempty"`
}

// StartRebuild re-reads all history into the GitHub rollup only.
func (g *GitHubState) StartRebuild() {
	g.Rebuild, g.RebuildCursors = true, map[string]map[string]FileCursor{}
}
