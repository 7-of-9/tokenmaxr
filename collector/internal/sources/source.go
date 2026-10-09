// Package sources defines how the collector reads each tool's local logs.
// Parsers live in the claude, codex and grok subpackages; see docs/agents/SPEC.md.
package sources

import (
	"encoding/json"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
)

// Cursor is the persisted read position for one file. It is an optimisation
// only: re-parsing from offset 0 must always be safe because event ids are
// content-derived and the server merges by fieldwise max.
type Cursor struct {
	Size     int64           `json:"size"`
	MtimeNs  int64           `json:"mtimeNs"`
	Offset   int64           `json:"offset"`   // bytes consumed; always at a line boundary
	HeadHash string          `json:"headHash"` // sha256 of the first 4 KB, detects replaced files
	PV       int             `json:"pv"`       // parser version that produced this cursor
	Carry    json.RawMessage `json:"carry,omitempty"`
}

// CarryTTL bounds carry that only matters while a file is live (Claude's
// in-flight usage keys): once the file's last event is this old, no more
// lines arrive for those messages, so the parser drops it. Resume context
// (Codex's rollout id and running total) is kept. The scan re-offers an
// unchanged file with carry for longer than this, so the parser always gets
// the call that drops it before the file goes idle.
const CarryTTL = 30 * time.Minute

// Batch is everything one Parse call produced.
type Batch struct {
	Usage    []model.UsageEvent
	Activity []model.ActivityEvent
	Prompts  []model.PromptRecord
	// Limits are the current plan windows, not log events. A scan reads
	// them once per home; they are not tied to a file cursor.
	Limits []model.LimitSnapshot
	// LastEventTS is the newest event timestamp seen, for health reporting.
	LastEventTS time.Time
}

func (b *Batch) Add(o Batch) {
	b.Usage = append(b.Usage, o.Usage...)
	b.Activity = append(b.Activity, o.Activity...)
	b.Prompts = append(b.Prompts, o.Prompts...)
	b.Limits = append(b.Limits, o.Limits...)
	if o.LastEventTS.After(b.LastEventTS) {
		b.LastEventTS = o.LastEventTS
	}
}

// Hint kinds.
const (
	// HintAccount names an account (Codex session_meta.creator_account_id).
	HintAccount = "acct"
	// HintOrg names an organization only (Claude credential_org); the core
	// maps it to an account through the local org-to-account map.
	HintOrg = "org"
)

// Hint is the last identity record a parser saw in the stream (file or
// rollout) it is reading, as of the event it attributes. ID is already
// hashed (model.AccountHash; an org is hashed as "org:<uuid>"), so a hint
// kept in a cursor carry never holds a raw id.
type Hint struct {
	Kind string    `json:"k,omitempty"`
	ID   string    `json:"id,omitempty"`
	At   time.Time `json:"at,omitzero"`
}

// IsZero reports whether the hint names nothing.
func (h Hint) IsZero() bool { return h.ID == "" }

// Env is what a parser needs from the rest of the collector.
type Env struct {
	// Home is the home directory this Env scans (the OS home, an extra home
	// or a WSL home; SPEC "Scan roots"). One Env per home.
	Home string
	// CodexHome is the Codex directory for Home ("" means <Home>/.codex).
	// The core sets it from $CODEX_HOME for the OS home only.
	CodexHome string
	// CodexQuota is a collector-owned file holding a fresh Codex rate-limit
	// reading in Codex's rollout format ("" if none). OS home only.
	CodexQuota string
	// Machine is the machine label written into prompt records.
	Machine string
	// Attribute returns (acct, acctQ) for an event of provider at ts.
	// sessionID is the provider's raw session id ("" if unknown) and h the
	// stream's identity hint (zero if none).
	Attribute func(provider string, ts time.Time, sessionID string, h Hint) (acct, acctQ string)
	// HashID hashes a native account (or "org:<uuid>") id with the fleet
	// key; nil means hints are not available (no key).
	HashID func(provider, nativeID string) string
	// Label returns the private label for an account hash ("" if none).
	Label func(acct string) string
	// OrgSince records that provider's login at this home is in organisation
	// org (a hash) and returns when this home switched to it: zero when it
	// has been the only one seen. Nil means nothing is remembered (dry runs).
	OrgSince func(provider, org string) time.Time
	// TZOffsetMin returns the machine's UTC offset in minutes east at t.
	TZOffsetMin func(t time.Time) int
	// Prompts disables prompt capture when false (usage/activity still flow).
	Prompts bool
}

// Source is one tool's log reader.
type Source interface {
	// Name is the source id, e.g. model.SourceClaudeCode.
	Name() string
	Provider() string
	// PV is the parser version; bumping it forces a full reparse of every file.
	PV() int
	// Prepare is called once per scan before Files/Parse, for any global
	// indexes a parser needs (e.g. which session ids have transcripts).
	Prepare(env *Env) error
	// Files lists every file this source reads, as absolute paths.
	Files(env *Env) ([]string, error)
	// Parse reads path starting at cur.Offset (cur is the zero Cursor for a
	// new or reset file) and returns the events found plus the new cursor.
	// It must never consume a trailing line that has no terminating '\n'.
	// Whole-file sources (e.g. JSON) re-read from 0 and set Offset=Size.
	Parse(env *Env, path string, cur Cursor) (Batch, Cursor, error)
}
