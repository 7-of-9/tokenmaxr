// Package model holds the wire types shared by the collector and the d0m1.com
// agents API. docs/agents/SPEC.md is the source of truth; keep them in sync.
package model

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

const WireVersion = 1

// Providers.
const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
	ProviderXAI       = "xai"
)

// Sources.
const (
	SourceClaudeCode = "claude-code"
	SourceCodex      = "codex"
	SourceGrokCLI    = "grok-cli"
)

// Event quality and account-attribution quality (SPEC "Accounts").
const (
	QualityExact     = "exact"
	QualityEstimated = "estimated"

	// AcctRecorded: the tool's own log names the account for this event's
	// stream, before the event, with no login boundary in between.
	AcctRecorded = "recorded"
	// AcctSession: a live session hook.
	AcctSession = "session"
	// AcctTimeline: a live probe interval (within one tick).
	AcctTimeline = "timeline"
	// AcctBounded: between two login boundaries whose identity samples agree.
	AcctBounded = "bounded"
	// AcctLineage: a continuous plan or token chain into a known account.
	AcctLineage = "lineage"
	// AcctInferred: the nearest or sole candidate, with no supporting chain.
	AcctInferred = "inferred"
	AcctUnknown  = "unknown"
)

// AcctRank orders attribution qualities; the server keeps the higher one.
func AcctRank(q string) int {
	switch q {
	case AcctRecorded:
		return 6
	case AcctSession:
		return 5
	case AcctTimeline:
		return 4
	case AcctBounded:
		return 3
	case AcctLineage:
		return 2
	case AcctInferred:
		return 1
	}
	return 0
}

// LabelledQ reports whether an attribution is strong enough for a prompt
// record to carry the account's private label (rank 3 and above).
func LabelledQ(q string) bool { return AcctRank(q) >= 3 }

// Tokens are disjoint buckets: summing In+CacheW+CacheR+Out never double counts.
// Reasoning is a subset of Out and CacheW1h a subset of CacheW; both are
// informational only (CacheW1h prices 1-hour cache writes at their own rate).
type Tokens struct {
	In        int64 `json:"in"`
	CacheW    int64 `json:"cacheW"`
	CacheW1h  int64 `json:"cacheW1h"`
	CacheR    int64 `json:"cacheR"`
	Out       int64 `json:"out"`
	Reasoning int64 `json:"reasoning"`
	Calls     int64 `json:"calls"`
}

// Max merges b into t fieldwise (the server applies the same rule).
func (t *Tokens) Max(b Tokens) {
	t.In = max(t.In, b.In)
	t.CacheW = max(t.CacheW, b.CacheW)
	t.CacheW1h = max(t.CacheW1h, b.CacheW1h)
	t.CacheR = max(t.CacheR, b.CacheR)
	t.Out = max(t.Out, b.Out)
	t.Reasoning = max(t.Reasoning, b.Reasoning)
	t.Calls = max(t.Calls, b.Calls)
}

func (t Tokens) Effective() float64 {
	return float64(t.In+t.CacheW+t.Out) + 0.1*float64(t.CacheR)
}

type UsageEvent struct {
	ID          string    `json:"id"`
	Provider    string    `json:"provider"`
	Source      string    `json:"source"`
	TS          time.Time `json:"ts"`
	TZOffsetMin int       `json:"tzOffsetMin"`
	Model       string    `json:"model"`
	Acct        string    `json:"acct"`
	AcctQ       string    `json:"acctQ"`
	Q           string    `json:"q"`
	PV          int       `json:"pv"`
	Session     string    `json:"session"`
	// Workspace is the same git-root folder a prompt for this cwd stores.
	// It stays off the wire: public usage rows carry no project.
	Workspace string `json:"-"`
	// WS is Workspace hashed with the fleet key (w_ + 16 hex), so the server
	// can rate tokens per prompt per workspace without the path.
	WS string `json:"ws,omitempty"`
	Tokens
}

type ActivityEvent struct {
	ID          string    `json:"id"`
	Provider    string    `json:"provider"`
	Source      string    `json:"source"`
	TS          time.Time `json:"ts"`
	TZOffsetMin int       `json:"tzOffsetMin"`
	Acct        string    `json:"acct"`
	AcctQ       string    `json:"acctQ"`
	Session     string    `json:"session"`
	HasUsage    bool      `json:"hasUsage"`
	// Workspace matches the prompt recorded for the same folder. Not uploaded.
	Workspace string `json:"-"`
	WS        string `json:"ws,omitempty"`
}

type PromptRecord struct {
	ID          string    `json:"id"`
	Provider    string    `json:"provider"`
	Source      string    `json:"source"`
	TS          time.Time `json:"ts"`
	TZOffsetMin int       `json:"tzOffsetMin"`
	Model       string    `json:"model"`
	Acct        string    `json:"acct"`
	AcctQ       string    `json:"acctQ"`
	AcctLabel   string    `json:"acctLabel"`
	Workspace   string    `json:"workspace"`
	Machine     string    `json:"machine"`
	Session     string    `json:"session"`
	Text        string    `json:"text"`
}

// MaxPromptBytes caps prompt text; longer text is truncated with a marker.
const MaxPromptBytes = 256 * 1024

type SourceHealth struct {
	Files       int        `json:"files"`
	LastEventTS *time.Time `json:"lastEventTs,omitempty"`
}

type Heartbeat struct {
	MachineLabel string                  `json:"machineLabel"`
	OS           string                  `json:"os"`
	Arch         string                  `json:"arch"`
	Version      string                  `json:"version"`
	ScanAt       time.Time               `json:"scanAt"`
	ClockSkewMs  int64                   `json:"clockSkewMs"`
	OutboxEvents int                     `json:"outboxEvents"`
	Sources      map[string]SourceHealth `json:"sources"`
	Checks       map[string]string       `json:"checks"`
	TZ           *TZInfo                 `json:"tz,omitempty"`
}

// TZInfo is the machine time zone and derived country (see internal/tzinfo).
type TZInfo struct {
	IANA      string `json:"iana"`
	WindowsID string `json:"windowsId,omitempty"`
	Country   string `json:"country"`
	Source    string `json:"source"`
}

// LimitSnapshot is one account's plan window as the tool last wrote it
// (Claude Code's usage cache, a Codex rollout rate_limits object, Grok's
// plan cache or billing snapshot). The server keeps the newest ObservedAt per id. Percent is not
// merged by max: a window resets and the percent falls. Name is an
// organization or plan label, never an email or a raw account id.
type LimitSnapshot struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Source   string `json:"source"`
	Acct     string `json:"acct"`
	AcctQ    string `json:"acctQ"`
	Plan     string `json:"plan,omitempty"`
	Window   string `json:"window"`
	Scope    string `json:"scope,omitempty"`
	Name     string `json:"name,omitempty"`
	// Label is the account email. Owner-only: GET /api/limits returns it, public usage does not.
	Label       string     `json:"label,omitempty"`
	Detail      string     `json:"detail,omitempty"`
	UsedPercent *float64   `json:"usedPercent,omitempty"`
	ResetsAt    *time.Time `json:"resetsAt,omitempty"`
	ObservedAt  time.Time  `json:"observedAt"`
	Status      string     `json:"status,omitempty"`
}

// AccountUsageSnapshot is the provider's recorded total for one UTC day.
// It is account-wide, not attributable to the reporting machine, a project,
// model, or input/output bucket. The server reconciles it with local events.
type AccountUsageSnapshot struct {
	ID          string    `json:"id"`
	Provider    string    `json:"provider"`
	Source      string    `json:"source"`
	Acct        string    `json:"acct"`
	AcctQ       string    `json:"acctQ"`
	Date        string    `json:"date"`
	Timezone    string    `json:"timezone"`
	TotalTokens int64     `json:"totalTokens"`
	ObservedAt  time.Time `json:"observedAt"`
}

type IngestRequest struct {
	V                int                    `json:"v"`
	CollectorVersion string                 `json:"collectorVersion"`
	SentAt           time.Time              `json:"sentAt"`
	Usage            []UsageEvent           `json:"usage,omitempty"`
	Activity         []ActivityEvent        `json:"activity,omitempty"`
	Prompts          []PromptRecord         `json:"prompts,omitempty"`
	Limits           []LimitSnapshot        `json:"limits,omitempty"`
	AccountUsage     []AccountUsageSnapshot `json:"accountUsage,omitempty"`
	Heartbeat        *Heartbeat             `json:"heartbeat,omitempty"`
}

type IngestResponse struct {
	OK         bool      `json:"ok"`
	Accepted   []string  `json:"accepted"`
	Retry      []string  `json:"retry"`
	ServerTime time.Time `json:"serverTime"`
}

type EnrollRequest struct {
	Invite       string `json:"invite"`
	MachineLabel string `json:"machineLabel"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	Version      string `json:"version"`
	KFingerprint string `json:"kFingerprint"`
}

type EnrollResponse struct {
	MachineID string `json:"machineId"`
	Token     string `json:"token"`
}

type InviteResponse struct {
	Invite    string    `json:"invite"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Event kinds used in ID derivation.
const (
	KindUsage    = "usage"
	KindActivity = "activity"
	KindPrompt   = "prompt"
)

// EventID derives the content-addressed id: no machine, path, offset or secret.
func EventID(kind, provider, source, nativeKey string) string {
	sum := sha256.Sum256([]byte("v1|" + kind + "|" + provider + "|" + source + "|" + nativeKey))
	return hex.EncodeToString(sum[:])[:32]
}

// SessionKey hashes a provider session id so raw ids never leave the machine.
func SessionKey(provider, sessionID string) string {
	if sessionID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(provider + "|" + sessionID))
	return "s_" + hex.EncodeToString(sum[:])[:16]
}

// AccountHash is HMAC(K, provider|nativeAccountID) with the fleet key K.
func AccountHash(k []byte, provider, nativeAccountID string) string {
	m := hmac.New(sha256.New, k)
	m.Write([]byte(provider + "|" + nativeAccountID))
	return "a_" + hex.EncodeToString(m.Sum(nil))[:16]
}

// KFingerprint lets the server detect a machine joined with the wrong fleet key.
func KFingerprint(k []byte) string {
	sum := sha256.Sum256(k)
	return hex.EncodeToString(sum[:])[:16]
}
