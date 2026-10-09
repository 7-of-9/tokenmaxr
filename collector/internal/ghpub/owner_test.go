package ghpub

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
)

// ownerVector is pages/dashboard/testdata/owner-vector.json, the dashboard's
// test vector (owner-vector.mjs builds it with node:crypto and the server's
// validateLimit).
type ownerVector struct {
	FleetKey  string          `json:"fleetKey"`
	Context   string          `json:"context"`
	OwnerKey  string          `json:"ownerKey"`
	KeyURL    string          `json:"ownerKeyBase64Url"`
	Machine   string          `json:"machine"`
	AAD       string          `json:"aad"`
	Nonce     string          `json:"nonce"`
	Plaintext string          `json:"plaintext"`
	Items     json.RawMessage `json:"items"`
	File      json.RawMessage `json:"file"`
}

func loadOwnerVector(t *testing.T) (ownerVector, []byte) {
	t.Helper()
	b, err := os.ReadFile("../../../pages/dashboard/testdata/owner-vector.json")
	if err != nil {
		t.Skip("no pages/dashboard beside this module")
	}
	var v ownerVector
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	k, err := base64.StdEncoding.DecodeString(v.FleetKey)
	if err != nil || len(k) != 32 {
		t.Fatalf("fleet key: %v", err)
	}
	return v, k
}

// sameJSON compares two JSON texts as values.
func sameJSON(t *testing.T, what string, got, want []byte) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("%s: %v in %s", what, err, got)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("%s differs:\n got  %s\n want %s", what, got, want)
	}
}

// The collector and the dashboard agree on the format: the vector's file
// opens to its plaintext, and sealing that plaintext with its nonce gives the
// same file; it opens for no other machine and no other key.
func TestOwnerFileMatchesTheDashboardVector(t *testing.T) {
	v, k := loadOwnerVector(t)
	if v.Context != OwnerContext || v.AAD != OwnerContext+"|"+v.Machine {
		t.Fatalf("context %q, aad %q", v.Context, v.AAD)
	}
	if got := base64.StdEncoding.EncodeToString(OwnerKey(k)); got != v.OwnerKey {
		t.Fatalf("owner key %s, want %s", got, v.OwnerKey)
	}
	if got := base64.RawURLEncoding.EncodeToString(OwnerKey(k)); got != v.KeyURL {
		t.Fatalf("unlock key %s, want %s", got, v.KeyURL)
	}
	pt, err := OpenOwner(k, v.Machine, v.File)
	if err != nil || string(pt) != v.Plaintext {
		t.Fatalf("vector file opens to %q: %v", pt, err)
	}
	nonce, _ := base64.StdEncoding.DecodeString(v.Nonce)
	file, err := SealOwner(k, v.Machine, []byte(v.Plaintext), nonce)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, "sealed vector", file, v.File)

	// Another machine's id, the file moved there, or another key: no.
	if _, err := OpenOwner(k, "m_ba9876543210", v.File); err == nil {
		t.Fatal("opened as another machine's")
	}
	moved := bytes.Replace(v.File, []byte(v.Machine), []byte("m_ba9876543210"), 1)
	if _, err := OpenOwner(k, "m_ba9876543210", moved); err == nil {
		t.Fatal("a file moved to another machine opened (the AAD binds it)")
	}
	other := bytes.Clone(k)
	other[0] ^= 1
	if _, err := OpenOwner(other, v.Machine, v.File); err == nil {
		t.Fatal("opened with another fleet key")
	}
	if _, err := SealOwner(k, v.Machine, []byte(v.Plaintext), nonce[:8]); err == nil {
		t.Fatal("sealed with a short nonce")
	}

	// A published file gets a fresh nonce every time.
	a, _ := SealOwner(k, v.Machine, []byte(v.Plaintext), nil)
	b, _ := SealOwner(k, v.Machine, []byte(v.Plaintext), nil)
	if bytes.Equal(a, b) {
		t.Fatal("nonce reused")
	}
	if pt, err := OpenOwner(k, v.Machine, a); err != nil || string(pt) != v.Plaintext {
		t.Fatalf("round trip: %v", err)
	}
}

func ptrF(f float64) *float64 { return &f }

func ptrT(s string) *time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return &t
}

func at(s string) time.Time { return *ptrT(s) }

// The vector's snapshots (owner-vector.mjs SNAPSHOTS) as the collector holds
// them give exactly the vector's rows.
func TestOwnerRowsMatchTheVector(t *testing.T) {
	v, _ := loadOwnerVector(t)
	now := at("2026-10-01T12:00:00Z")
	snaps := []model.LimitSnapshot{
		{ID: strings.Repeat("1", 32), Provider: "anthropic", Source: "claude-code", Acct: "a_1539dc8e48fdf4d2", AcctQ: "recorded", Plan: "Max (20x)", Window: "session", Label: "owner@example.com", UsedPercent: ptrF(12.5), ResetsAt: ptrT("2026-10-01T15:00:00Z"), ObservedAt: at("2026-10-01T11:58:00Z"), Status: "ok"},
		{ID: strings.Repeat("2", 32), Provider: "anthropic", Source: "claude-code", Acct: "a_1539dc8e48fdf4d2", AcctQ: "recorded", Plan: "Max (20x)", Window: "week", Label: "owner@example.com", UsedPercent: ptrF(64), ResetsAt: ptrT("2026-10-06T15:00:00Z"), ObservedAt: at("2026-10-01T11:58:00Z"), Status: "ok"},
		{ID: strings.Repeat("3", 32), Provider: "anthropic", Source: "claude-code", Acct: "a_1539dc8e48fdf4d2", AcctQ: "recorded", Plan: "Max (20x)", Window: "week", Scope: "Fable", Label: "owner@example.com", UsedPercent: ptrF(81), ResetsAt: ptrT("2026-10-06T15:00:00Z"), ObservedAt: at("2026-10-01T11:58:00Z"), Status: "ok"},
		{ID: strings.Repeat("4", 32), Provider: "anthropic", Source: "claude-code", Acct: "a_0123456789abcdef", AcctQ: "recorded", Window: "week", Name: "Example Org", Label: "work@example.org", UsedPercent: ptrF(100), ResetsAt: ptrT("2026-10-02T21:00:00Z"), ObservedAt: at("2026-10-01T11:40:00Z"), Status: "full"},
		{ID: strings.Repeat("5", 32), Provider: "anthropic", Source: "claude-code", Acct: "a_0123456789abcdef", AcctQ: "recorded", Window: "extra", Name: "Example Org", Label: "work@example.org", Detail: "out of credits", ObservedAt: at("2026-10-01T11:40:00Z"), Status: "disabled"},
		{ID: strings.Repeat("6", 32), Provider: "openai", Source: "codex", Acct: "a_fedcba9876543210", AcctQ: "recorded", Plan: "Pro", Window: "week", Label: "owner@example.com", UsedPercent: ptrF(37), ResetsAt: ptrT("2026-10-05T09:30:00Z"), ObservedAt: at("2026-10-01T11:55:00Z"), Status: "ok"},
		{ID: strings.Repeat("7", 32), Provider: "xai", Source: "grok-cli", AcctQ: "unknown", Plan: "SuperGrok Heavy", Window: "plan", ObservedAt: at("2026-10-01T11:50:00Z")},
	}
	// Order does not matter: the rows come out sorted by id.
	rev := make([]model.LimitSnapshot, len(snaps))
	for i, s := range snaps {
		rev[len(snaps)-1-i] = s
	}
	pt := OwnerPlaintext(OwnerRows(rev, now))
	sameJSON(t, "plaintext", pt, []byte(v.Plaintext))
	if string(pt) != v.Plaintext {
		t.Logf("same values, other bytes (fine):\n%s\n%s", pt, v.Plaintext)
	}
}

// ownerFixtures are snapshots at the edges of the server's validation.
func ownerFixtures() []model.LimitSnapshot {
	id := func(c string) string { return strings.Repeat(c, 32/len(c)) }
	base := model.LimitSnapshot{ID: id("a"), Provider: "anthropic", Source: "claude-code", Acct: "a_1539dc8e48fdf4d2", AcctQ: "recorded",
		Plan: "Max (5x)", Window: "week", Label: "me@example.com", UsedPercent: ptrF(47.25), ResetsAt: ptrT("2026-10-06T15:00:00.123456789+01:00"),
		ObservedAt: at("2026-10-01T11:58:00.987654321-07:00"), Status: "ok"}
	with := func(c string, mod func(*model.LimitSnapshot)) model.LimitSnapshot {
		s := base
		s.ID = id(c)
		mod(&s)
		return s
	}
	return []model.LimitSnapshot{
		base,
		with("b", func(s *model.LimitSnapshot) { s.AcctQ = "" }),
		with("c", func(s *model.LimitSnapshot) { s.Acct = "" }),
		with("d", func(s *model.LimitSnapshot) { s.Acct = "a_XYZ" }),
		with("e", func(s *model.LimitSnapshot) { s.Label = "nobody" }),
		with("f", func(s *model.LimitSnapshot) { s.Label = "me @example.com" }),
		with("0", func(s *model.LimitSnapshot) { s.Plan = "Max @ home" }),
		with("1", func(s *model.LimitSnapshot) { s.Window = "Week" }),
		with("2", func(s *model.LimitSnapshot) { s.Status = "OK" }),
		with("3", func(s *model.LimitSnapshot) { s.UsedPercent = ptrF(101) }),
		with("4", func(s *model.LimitSnapshot) { s.UsedPercent = ptrF(0) }),
		with("5", func(s *model.LimitSnapshot) { s.ResetsAt = ptrT("2030-01-01T00:00:00Z") }),
		with("6", func(s *model.LimitSnapshot) { s.ObservedAt = time.Time{} }),
		with("7", func(s *model.LimitSnapshot) { s.ObservedAt = at("2026-10-04T12:00:00Z") }),
		with("8", func(s *model.LimitSnapshot) { s.Source = "codex" }),
		with("9", func(s *model.LimitSnapshot) { s.Source, s.Provider = "vim", "vim" }),
		{ID: "x", Provider: "anthropic", Source: "claude-code", Window: "week", ObservedAt: base.ObservedAt},
		with("ab", func(s *model.LimitSnapshot) { s.Name = "Café ünïcode 🚀 Org" }),
		with("ac", func(s *model.LimitSnapshot) { s.Detail = strings.Repeat("d", 81) }),
		with("ad", func(s *model.LimitSnapshot) { s.Label = strings.Repeat("u", 65) + "@example.com" }),
		with("ae", func(s *model.LimitSnapshot) { s.AcctQ = "weird" }),
		with("af", func(s *model.LimitSnapshot) { s.Scope = strings.Repeat("🚀", 41) }), // 82 UTF-16 units
		with("ba", func(s *model.LimitSnapshot) { s.Scope = strings.Repeat("🚀", 40) }), // 80
		with("c1", func(s *model.LimitSnapshot) {
			s.ResetsAt, s.UsedPercent, s.Status, s.Label, s.Plan = nil, nil, "", "", ""
		}),
		with("c2", func(s *model.LimitSnapshot) { s.Label = "me@exa\u00a0mple.com" }),
		with("c3", func(s *model.LimitSnapshot) { s.Window = "plan"; s.AcctQ = "session" }),
		with("c4", func(s *model.LimitSnapshot) { s.Org, s.OrgKind = "a_00112233445566ff", "team" }),
		with("c5", func(s *model.LimitSnapshot) { s.Org, s.OrgKind = "a_00112233445566ff", "personal" }),
		with("c6", func(s *model.LimitSnapshot) { s.Org = "org-uuid" }),
		with("c7", func(s *model.LimitSnapshot) { s.Org, s.OrgKind = "a_00112233445566ff", "claude_team" }),
		with("c8", func(s *model.LimitSnapshot) { s.OrgKind = "enterprise" }),
	}
}

// Every row is what the server's validateLimit makes of the same snapshot
// (as JSON values), and a snapshot it rejects is left out: run against
// api/src/lib/validate.js with node.
func TestOwnerRowsMatchTheServersValidation(t *testing.T) {
	validate, err := filepath.Abs("../../../api/src/lib/validate.js")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(validate); err != nil {
		t.Skip("no api/ beside this module")
	}
	// validate.js imports the server's packages (npm ci --prefix api).
	if _, err := os.Stat(filepath.Join(filepath.Dir(validate), "../../node_modules")); err != nil {
		t.Skip("the server's packages are not installed")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	now := at("2026-10-01T12:00:00Z")
	snaps := ownerFixtures()
	in, _ := json.Marshal(snaps) // as the collector uploads them
	script := `import { validateLimit } from ` + jsString("file:///"+filepath.ToSlash(validate)) + `
let s = ''
process.stdin.on('data', (d) => { s += d })
process.stdin.on('end', () => {
  const now = new Date('2026-10-01T12:00:00Z')
  process.stdout.write(JSON.stringify(JSON.parse(s).map((x) => { const r = validateLimit(x, now); return r.ok ? r.value : null })))
})`
	cmd := exec.Command(node, "--input-type=module", "-e", script)
	cmd.Stdin = bytes.NewReader(in)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	var want []json.RawMessage
	if err := json.Unmarshal(out, &want); err != nil || len(want) != len(snaps) {
		t.Fatalf("node output %s: %v", out, err)
	}
	kept := 0
	for i, s := range snaps {
		r, ok := ownerRow(s, now)
		if string(want[i]) == "null" {
			if ok {
				t.Errorf("snapshot %s: the server rejects it, the collector keeps %+v", s.ID, r)
			}
			continue
		}
		if !ok {
			t.Errorf("snapshot %s: the server keeps %s, the collector drops it", s.ID, want[i])
			continue
		}
		kept++
		got, _ := json.Marshal(r)
		sameJSON(t, "row "+s.ID, got, want[i])
	}
	if kept < 6 || kept == len(snaps) {
		t.Fatalf("fixtures kept %d of %d: both kinds must be covered", kept, len(snaps))
	}
	if rows := OwnerRows(snaps, now); len(rows) != kept {
		t.Fatalf("OwnerRows kept %d, want %d", len(rows), kept)
	}
}

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// The newest reading of an id wins; the digests tell a changed meter from
// one only read again, and neither reveals the rows.
func TestOwnerRowsAndDigests(t *testing.T) {
	now := at("2026-10-01T12:00:00Z")
	k := bytes.Repeat([]byte{7}, 32)
	s := ownerFixtures()[0]
	newer := s
	newer.ObservedAt = s.ObservedAt.Add(time.Minute)
	newer.UsedPercent = ptrF(50)
	rows := OwnerRows([]model.LimitSnapshot{s, newer, s}, now)
	if len(rows) != 1 || *rows[0].UsedPercent != 50 {
		t.Fatalf("rows %+v", rows)
	}
	data, read := OwnerDigests(k, "m_0123456789ab", rows)
	if strings.Contains(data+read, "me@example.com") || len(data) != 32 || data == read {
		t.Fatalf("digests %s %s", data, read)
	}
	reread := s
	reread.ObservedAt = newer.ObservedAt.Add(time.Hour)
	reread.UsedPercent = ptrF(50)
	d2, r2 := OwnerDigests(k, "m_0123456789ab", OwnerRows([]model.LimitSnapshot{reread}, now.Add(time.Hour)))
	if d2 == data || r2 != read {
		t.Fatal("a meter read again must change data only")
	}
	changed := reread
	changed.UsedPercent = ptrF(51)
	if _, r3 := OwnerDigests(k, "m_0123456789ab", OwnerRows([]model.LimitSnapshot{changed}, now.Add(time.Hour))); r3 == read {
		t.Fatal("a changed meter keeps its digest")
	}
	if d4, _ := OwnerDigests(k, "m_ba9876543210", rows); d4 == data {
		t.Fatal("the digest must depend on the machine")
	}
	if got := string(OwnerPlaintext(nil)); got != `{"v":1,"items":[]}` {
		t.Fatalf("empty plaintext %s", got)
	}
}
