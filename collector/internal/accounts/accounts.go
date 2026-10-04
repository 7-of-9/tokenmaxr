// Package accounts probes which account each tool is logged in as, keeps the
// per-provider timeline in state.json and attributes events by timestamp
// (docs/agents/SPEC.md "Accounts"). Raw account ids and labels never leave
// this machine except labels inside server-encrypted prompt records.
package accounts

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/evidence"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/cursor"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/gemini"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

// Observation is one provider's currently active account.
type Observation struct {
	Provider string
	NativeID string
	Label    string // default label candidate ("email · org"); private
}

// Probe reads the tools' auth files in one home. Missing or unreadable files
// just mean no observation for that provider. It runs once per scan root.
func Probe(userHome, codexHome string) []Observation {
	var out []Observation
	if o, ok := probeClaude(filepath.Join(userHome, ".claude.json")); ok {
		out = append(out, o)
	}
	if o, ok := probeCodex(filepath.Join(codexHome, "auth.json")); ok {
		out = append(out, o)
	}
	if o, ok := probeGrok(filepath.Join(userHome, ".grok", "auth.json")); ok {
		out = append(out, o)
	}
	if id, label, ok := cursor.Account(userHome); ok {
		out = append(out, Observation{Provider: cursor.ProviderName, NativeID: id, Label: joinLabel(label)})
	}
	if id, label, ok := gemini.Account(userHome); ok {
		out = append(out, Observation{Provider: gemini.ProviderName, NativeID: id, Label: joinLabel(label)})
	}
	return out
}

func readJSON(path string, v any) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return json.Unmarshal(b, v) == nil
}

func joinLabel(parts ...string) string {
	var keep []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, " · ")
}

func probeClaude(path string) (Observation, bool) {
	var f struct {
		OAuthAccount *struct {
			AccountUUID      string `json:"accountUuid"`
			EmailAddress     string `json:"emailAddress"`
			OrganizationName string `json:"organizationName"`
		} `json:"oauthAccount"`
	}
	if !readJSON(path, &f) || f.OAuthAccount == nil || f.OAuthAccount.AccountUUID == "" {
		return Observation{}, false
	}
	a := f.OAuthAccount
	return Observation{
		Provider: model.ProviderAnthropic,
		NativeID: a.AccountUUID,
		Label:    joinLabel(a.EmailAddress, a.OrganizationName),
	}, true
}

// CodexAccount reads only the current Codex identity. Account history readers
// use it without probing unrelated providers or opening their local stores.
func CodexAccount(codexHome string) (Observation, bool) {
	return probeCodex(filepath.Join(codexHome, "auth.json"))
}

func probeCodex(path string) (Observation, bool) {
	var f struct {
		Tokens *struct {
			IDToken   string `json:"id_token"`
			AccountID string `json:"account_id"`
		} `json:"tokens"`
	}
	if !readJSON(path, &f) || f.Tokens == nil {
		return Observation{}, false
	}
	claims := jwtClaims(f.Tokens.IDToken)
	var id, email string
	if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		id, _ = auth["chatgpt_account_id"].(string)
	}
	if id == "" {
		id = f.Tokens.AccountID
	}
	email, _ = claims["email"].(string)
	if id == "" {
		return Observation{}, false
	}
	return Observation{Provider: model.ProviderOpenAI, NativeID: id, Label: joinLabel(email)}, true
}

// jwtClaims decodes a JWT payload without verifying it (we only need to know
// which account the local tool is logged in as).
func jwtClaims(tok string) map[string]any {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

func probeGrok(path string) (Observation, bool) {
	var f map[string]json.RawMessage
	if !readJSON(path, &f) {
		return Observation{}, false
	}
	var best Observation
	var bestExp time.Time
	found := false
	// Iterate in key order so ties resolve the same way every tick.
	keys := make([]string, 0, len(f))
	for k := range f {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		var e struct {
			UserID    string `json:"user_id"`
			Email     string `json:"email"`
			ExpiresAt string `json:"expires_at"`
		}
		if json.Unmarshal(f[k], &e) != nil || e.UserID == "" {
			continue
		}
		exp, _ := time.Parse(time.RFC3339Nano, e.ExpiresAt)
		if !found || exp.After(bestExp) {
			best = Observation{Provider: model.ProviderXAI, NativeID: e.UserID, Label: joinLabel(e.Email)}
			bestExp, found = exp, true
		}
	}
	return best, found
}

// Hash is the fleet-wide account id for an observation.
func Hash(k []byte, o Observation) string { return model.AccountHash(k, o.Provider, o.NativeID) }

// Observe extends the (provider, home) timeline's latest interval while the
// account is unchanged, or starts a new one. home is "" for the OS home.
func Observe(ivs []store.Interval, home, provider, acct string, now time.Time) []store.Interval {
	last := -1
	for i, iv := range ivs {
		if iv.Provider == provider && iv.Home == home && (last < 0 || iv.To.After(ivs[last].To)) {
			last = i
		}
	}
	if last >= 0 && ivs[last].Acct == acct {
		if now.After(ivs[last].To) {
			ivs[last].To = now
		}
		return ivs
	}
	return append(ivs, store.Interval{Provider: provider, Home: home, Acct: acct, From: now, To: now})
}

// tickSlack is how far outside an observed interval an event still counts as
// "timeline": one scheduler period plus a probe/scan margin.
const tickSlack = 2 * time.Minute

// Attribute picks the account for an event at ts from the (provider, home)
// timeline. Events inside an observed interval are "timeline"; events before
// the first observation get the earliest account as "inferred"; events in a
// gap or after the last observation get the nearest interval's account
// ("timeline" within a tick of it, otherwise "inferred"); with no
// observation at all for that home, "unknown".
func Attribute(ivs []store.Interval, home, provider string, ts time.Time) (acct, acctQ string) {
	var mine []store.Interval
	for _, iv := range ivs {
		if iv.Provider == provider && iv.Home == home {
			mine = append(mine, iv)
		}
	}
	if len(mine) == 0 {
		return "", model.AcctUnknown
	}
	slices.SortFunc(mine, func(a, b store.Interval) int { return a.From.Compare(b.From) })
	if ts.Before(mine[0].From.Add(-tickSlack)) {
		return mine[0].Acct, model.AcctInferred
	}
	best, bestDist := 0, time.Duration(-1)
	for i, iv := range mine {
		if !ts.Before(iv.From) && !ts.After(iv.To) {
			return iv.Acct, model.AcctTimeline
		}
		d := iv.From.Sub(ts)
		if ts.After(iv.To) {
			d = ts.Sub(iv.To)
		}
		if bestDist < 0 || d < bestDist {
			best, bestDist = i, d
		}
	}
	if bestDist <= tickSlack {
		return mine[best].Acct, model.AcctTimeline
	}
	return mine[best].Acct, model.AcctInferred
}

// Spans hands the live timeline to evidence.Build as identity samples.
func Spans(ivs []store.Interval) []evidence.Span {
	out := make([]evidence.Span, 0, len(ivs))
	for _, iv := range ivs {
		out = append(out, evidence.Span{Provider: iv.Provider, Home: iv.Home, Acct: iv.Acct, From: iv.From, To: iv.To})
	}
	return out
}

// Resolve is the attribution order (SPEC "Accounts"): the stream's own
// identity record (recorded), the live timeline (timeline), then the
// evidence fallbacks (bounded, lineage, inferred). Attribute's own inferred
// answer stands only when the evidence knows nothing better.
func Resolve(ivs []store.Interval, ix *evidence.Index, home, provider string, ts time.Time, sessionID string, h sources.Hint) (acct, acctQ string) {
	if ix != nil {
		if a, q, ok := ix.Recorded(provider, home, ts, sessionID, h); ok {
			return a, q
		}
	}
	acct, acctQ = Attribute(ivs, home, provider, ts)
	if acctQ == model.AcctTimeline || ix == nil {
		return acct, acctQ
	}
	if a, q, ok := ix.Fallback(provider, home, ts); ok && (q != model.AcctUnknown || acct == "") {
		return a, q
	}
	return acct, acctQ
}
