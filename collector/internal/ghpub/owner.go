package ghpub

// owner.json: the owner's plan limits on the public dashboard, encrypted.
// GitHub Pages has no sign-in, so what only the owner may read on a server's
// Agents page (account emails, organisation names, plans, reset times) is
// published encrypted, one file per machine, and the dashboard decrypts it in
// the owner's browser with the owner key (pages/dashboard/owner.ts has the
// contract; pages/dashboard/testdata/owner-vector.json is its test vector):
//
//   - data/machines/<id>/owner.json = {"schema":1,"machine":"<id>",
//     "alg":"A256GCM","nonce":"<base64, 12 bytes>","data":"<base64 of
//     ciphertext || 16-byte tag>"}
//   - owner key = HMAC-SHA256(K, OwnerContext); the dashboard never sees K
//   - AAD = OwnerContext + "|" + machine id, so a file opens only as its own
//     machine's
//   - plaintext = {"v":1,"items":[...]}: the rows GET /api/limits returns on
//     a server for this machine's snapshots (OwnerRows)

import (
	"bytes"
	"cmp"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
)

const (
	// OwnerContext derives the owner key from K and starts the AAD.
	OwnerContext = "tokenmaxr dashboard owner v1"
	ownerNonce   = 12
)

// OwnerPath is machine id's owner.json.
func OwnerPath(id string) string { return MachineDir(id) + "/owner.json" }

// OwnerKey is the key that opens the owner files of fleet key k (the
// dashboard's unlock key).
func OwnerKey(k []byte) []byte {
	m := hmac.New(sha256.New, k)
	m.Write([]byte(OwnerContext))
	return m.Sum(nil)
}

// OwnerRow is one plan window exactly as the server's GET /api/limits
// returns it (api/src/lib/validate.js validateLimit), in its field order.
type OwnerRow struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Source   string `json:"source"`
	Acct     string `json:"acct"`
	AcctQ    string `json:"acctQ"`
	Plan     string `json:"plan"`
	Window   string `json:"window"`
	Scope    string `json:"scope"`
	Name     string `json:"name"`
	// Label is the account email (absent when unknown).
	Label       string   `json:"label,omitempty"`
	Detail      string   `json:"detail"`
	ResetsAt    *string  `json:"resetsAt"`
	ObservedAt  string   `json:"observedAt"`
	Status      string   `json:"status"`
	UsedPercent *float64 `json:"usedPercent,omitempty"`
	// Org is the hashed organisation and OrgKind its kind ("team",
	// "enterprise", "personal"); absent when unknown.
	Org     string `json:"org,omitempty"`
	OrgKind string `json:"orgKind,omitempty"`
}

// sourceProvider, acctQualities and the patterns below are validate.js's.
var (
	sourceProvider = map[string]string{
		"claude-code": "anthropic", "codex": "openai", "grok-cli": "xai", "cursor": "cursor",
		"gemini-cli": "google", "web-claude": "anthropic", "web-chatgpt": "openai", "web-grok": "xai",
	}
	acctQualities = []string{"recorded", "session", "timeline", "bounded", "lineage", "inferred", "unknown"}
	orgKinds      = []string{"team", "enterprise", "personal"}
	idRE          = regexp.MustCompile(`^[0-9a-f]{32}$`)
	acctRE        = regexp.MustCompile(`^a_[0-9a-f]{16}$`)
	windowRE      = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	minRowTime    = time.Date(2015, 1, 1, 0, 0, 0, 0, time.UTC)
)

const (
	maxObservedAhead = 2 * 24 * time.Hour
	maxResetsAhead   = 400 * 24 * time.Hour
)

// jsLen is a string's length in JavaScript (UTF-16 code units).
func jsLen(s string) int { return len(utf16.Encode([]rune(s))) }

// jsSpace is JavaScript's \s.
func jsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// isEmail is validate.js's /^[^\s@]{1,64}@[^\s@]{1,80}$/.
func isEmail(s string) bool {
	user, host, ok := strings.Cut(s, "@")
	bad := func(p string, max int) bool {
		return p == "" || jsLen(p) > max || strings.ContainsRune(p, '@') || strings.ContainsFunc(p, jsSpace)
	}
	return ok && !bad(user, 64) && !bad(host, 80)
}

// isoMillis is JavaScript's Date.prototype.toISOString.
func isoMillis(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// ownerRow is validateLimit for one snapshot as the collector uploads it:
// false where the server rejects it.
func ownerRow(s model.LimitSnapshot, now time.Time) (OwnerRow, bool) {
	r := OwnerRow{ID: s.ID, Provider: s.Provider, Source: s.Source, Acct: s.Acct, AcctQ: s.AcctQ, Window: s.Window, Status: s.Status}
	field := func(v string, max int) bool { return jsLen(v) <= max }
	label := func(v string) bool { return field(v, 80) && !strings.ContainsAny(v, "@\r\n") }
	switch {
	case !idRE.MatchString(s.ID),
		s.Source == "" || !field(s.Source, 32), s.Provider == "" || !field(s.Provider, 32),
		sourceProvider[s.Source] != s.Provider,
		!field(s.Window, 32) || !windowRE.MatchString(s.Window),
		!field(s.Acct, 32) || s.Acct != "" && !acctRE.MatchString(s.Acct),
		!field(s.AcctQ, 16):
		return r, false
	}
	if r.AcctQ == "" {
		r.AcctQ = model.AcctUnknown
	}
	if !slices.Contains(acctQualities, r.AcctQ) {
		return r, false
	}
	if r.Acct == "" {
		r.AcctQ = model.AcctUnknown
	}
	obs := s.ObservedAt.Truncate(time.Millisecond)
	if obs.Before(minRowTime) || obs.After(now.Add(maxObservedAhead)) {
		return r, false
	}
	r.ObservedAt = isoMillis(obs)
	if u := s.UsedPercent; u != nil {
		if math.IsNaN(*u) || *u < 0 || *u > 100 {
			return r, false
		}
		v := *u
		r.UsedPercent = &v
	}
	if s.ResetsAt != nil {
		t := s.ResetsAt.Truncate(time.Millisecond)
		if t.Before(minRowTime) || t.After(now.Add(maxResetsAhead)) {
			return r, false
		}
		iso := isoMillis(t)
		r.ResetsAt = &iso
	}
	for _, v := range []string{s.Plan, s.Scope, s.Name, s.Detail} {
		if !label(v) {
			return r, false
		}
	}
	r.Plan, r.Scope, r.Name, r.Detail = s.Plan, s.Scope, s.Name, s.Detail
	if !field(s.Label, 80) || s.Label != "" && !isEmail(s.Label) {
		return r, false
	}
	r.Label = s.Label
	if !field(s.Org, 32) || s.Org != "" && !acctRE.MatchString(s.Org) ||
		!field(s.OrgKind, 16) || s.OrgKind != "" && !slices.Contains(orgKinds, s.OrgKind) {
		return r, false
	}
	r.Org, r.OrgKind = s.Org, s.OrgKind
	if !field(s.Status, 32) || s.Status != "" && !windowRE.MatchString(s.Status) {
		return r, false
	}
	return r, true
}

// OwnerRows are the rows a server's GET /api/limits returns for snapshots
// uploaded at now: each validated as the server does (a snapshot it would
// reject is left out), the newest reading per id, sorted by id so the same
// snapshots always give the same plaintext.
func OwnerRows(snaps []model.LimitSnapshot, now time.Time) []OwnerRow {
	byID := map[string]OwnerRow{}
	for _, s := range snaps {
		r, ok := ownerRow(s, now)
		if !ok {
			continue
		}
		if held, ok := byID[r.ID]; ok && held.ObservedAt >= r.ObservedAt {
			continue
		}
		byID[r.ID] = r
	}
	rows := make([]OwnerRow, 0, len(byID))
	for _, r := range byID {
		rows = append(rows, r)
	}
	slices.SortFunc(rows, func(a, b OwnerRow) int { return cmp.Compare(a.ID, b.ID) })
	return rows
}

// OwnerPlaintext is the plaintext owner.json carries: {"v":1,"items":rows}.
func OwnerPlaintext(rows []OwnerRow) []byte {
	if rows == nil {
		rows = []OwnerRow{}
	}
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false) // as JSON.stringify writes it
	_ = e.Encode(struct {
		V     int        `json:"v"`
		Items []OwnerRow `json:"items"`
	}{1, rows})
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

// OwnerDigests identify what an owner.json says without keeping it: data
// covers the plaintext, meters the same rows without their read times (a
// meter only read again). Keyed with the owner key, so state.json reveals
// nothing about the rows.
func OwnerDigests(k []byte, machine string, rows []OwnerRow) (data, meters string) {
	sum := func(pt []byte) string {
		m := hmac.New(sha256.New, OwnerKey(k))
		m.Write([]byte(machine + "\n"))
		m.Write(pt)
		return hex.EncodeToString(m.Sum(nil)[:16])
	}
	unread := slices.Clone(rows)
	for i := range unread {
		unread[i].ObservedAt = ""
	}
	return sum(OwnerPlaintext(rows)), sum(OwnerPlaintext(unread))
}

type ownerFile struct {
	Schema  int    `json:"schema"`
	Machine string `json:"machine"`
	Alg     string `json:"alg"`
	Nonce   string `json:"nonce"`
	Data    string `json:"data"`
}

func ownerAEAD(k []byte) (cipher.AEAD, error) {
	if len(k) != 32 {
		return nil, errors.New("owner file: the fleet key must be 32 bytes")
	}
	b, err := aes.NewCipher(OwnerKey(k))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

func ownerAAD(machine string) []byte { return []byte(OwnerContext + "|" + machine) }

// SealOwner is machine's owner.json holding plaintext, encrypted for fleet
// key k with nonce (nil: a fresh random one, as every published file uses).
func SealOwner(k []byte, machine string, plaintext, nonce []byte) ([]byte, error) {
	g, err := ownerAEAD(k)
	if err != nil {
		return nil, err
	}
	if nonce == nil {
		nonce = make([]byte, ownerNonce)
		if _, err := rand.Read(nonce); err != nil {
			return nil, err
		}
	}
	if len(nonce) != ownerNonce {
		return nil, errors.New("owner file: the nonce must be 12 bytes")
	}
	data := g.Seal(nil, nonce, plaintext, ownerAAD(machine))
	return marshal(ownerFile{Schema: 1, Machine: machine, Alg: "A256GCM",
		Nonce: base64.StdEncoding.EncodeToString(nonce), Data: base64.StdEncoding.EncodeToString(data)}), nil
}

// OpenOwner decrypts machine's owner.json with fleet key k, as the dashboard
// does.
func OpenOwner(k []byte, machine string, file []byte) ([]byte, error) {
	var f ownerFile
	if err := json.Unmarshal(file, &f); err != nil || f.Schema != 1 || f.Alg != "A256GCM" || f.Machine != machine {
		return nil, errors.New("owner file: not this machine's owner.json")
	}
	nonce, err1 := base64.StdEncoding.DecodeString(f.Nonce)
	data, err2 := base64.StdEncoding.DecodeString(f.Data)
	if err1 != nil || err2 != nil || len(nonce) != ownerNonce {
		return nil, errors.New("owner file: malformed")
	}
	g, err := ownerAEAD(k)
	if err != nil {
		return nil, err
	}
	pt, err := g.Open(nil, nonce, data, ownerAAD(machine))
	if err != nil {
		return nil, errors.New("owner file: does not open with this key for this machine")
	}
	return pt, nil
}
