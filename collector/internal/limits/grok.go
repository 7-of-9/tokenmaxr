package limits

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/jsonl"
)

// Grok Build writes the billing result fetched by /usage, prompts and its
// background polls. Read the provider's percentage, not a token-based estimate.
// The allowance can cover all Grok products; it is not a Build-only token budget.
// Source: xai-org/grok-build, xai-grok-shell/src/extensions/billing.rs and
// xai-grok-pager/src/app/effects/helpers.rs (credit_balance_from_config).
func grokLimits(env *sources.Env) []model.LimitSnapshot {
	return append(grokPlan(env), grokBilling(env)...)
}

// Decode only identity, timing and quota fields. Other unified-log entries can
// contain token prefixes or conversation data and must never be retained.
type grokBillingRow struct {
	TS  string          `json:"ts"`
	PID json.RawMessage `json:"pid"`
	Msg string          `json:"msg"`
	Ctx struct {
		UserID string `json:"user_id"`
		Method string `json:"method"`
		Tier   string `json:"subscriptionTier"`
		Config *struct {
			Used    *float64 `json:"creditUsagePercent"`
			Unified bool     `json:"isUnifiedBillingUser"`
			Period  *struct {
				Type  string `json:"type"`
				Start string `json:"start"`
				End   string `json:"end"`
			} `json:"currentPeriod"`
		} `json:"config"`
	} `json:"ctx"`
}

func grokBilling(env *sources.Env) []model.LimitSnapshot {
	if env.HashID == nil {
		return nil
	}
	r, err := jsonl.Open(filepath.Join(env.Home, ".grok", "logs", "unified.jsonl"), 0)
	if err != nil {
		return nil
	}
	defer r.Close()
	type identity struct {
		acct string
		at   time.Time
	}
	identities := map[string]identity{}
	var out []model.LimitSnapshot
	for {
		b, ok, err := r.Next()
		if err != nil || !ok {
			break
		}
		var row grokBillingRow
		// Strict decoding avoids treating a changed/invalid percent type as 0.
		if json.Unmarshal(b, &row) != nil {
			continue
		}
		ts := parseRFC(row.TS)
		pid := strings.Trim(string(row.PID), `" `)
		if ts == nil || pid == "" || pid == "null" {
			continue
		}
		switch row.Msg {
		case "agent initialized":
			// A new process may reuse an earlier process id in the same log.
			delete(identities, pid)
		case "auth started":
			// cached_token follows the initialization identity check and uses
			// that same account. Interactive sign-in can switch the account.
			if row.Ctx.Method != "" && row.Ctx.Method != "cached_token" {
				delete(identities, pid)
			}
		case "auth init user_info check":
			delete(identities, pid)
			if row.Ctx.UserID != "" {
				if acct := env.HashID(model.ProviderXAI, row.Ctx.UserID); acct != "" {
					identities[pid] = identity{acct, *ts}
				}
			}
		case "billing: fetched credits config":
			who, found := identities[pid]
			c := row.Ctx.Config
			if !found || ts.Before(who.at) || c == nil || c.Period == nil || c.Period.Type != "USAGE_PERIOD_TYPE_WEEKLY" {
				continue
			}
			start, end := parseRFC(c.Period.Start), parseRFC(c.Period.End)
			if start == nil || end == nil || !end.After(*start) || ts.Before(*start) {
				continue
			}
			used := 0.0
			if c.Used != nil {
				used = *c.Used
			} else if !c.Unified {
				continue
			}
			// Upstream's credit_balance_from_config defaults an omitted
			// creditUsagePercent to 0 (proto3 omits zero scalars). Only accept
			// that default for a recognized unified weekly config, never a
			// missing config, unknown period or legacy monthly billing row.
			if math.IsNaN(used) || math.IsInf(used, 0) || used < 0 || used > 100 {
				continue
			}
			detail := ""
			if c.Unified {
				detail = "Shared across Grok products"
			}
			out = append(out, model.LimitSnapshot{
				ID:       snapshotID(model.ProviderXAI, model.SourceGrokCLI, who.acct, "week", ""),
				Provider: model.ProviderXAI, Source: model.SourceGrokCLI,
				Acct: who.acct, AcctQ: model.AcctRecorded,
				Plan: sanitize(row.Ctx.Tier), Window: "week", Detail: detail,
				UsedPercent: pct(used), ResetsAt: end, ObservedAt: *ts,
				Status: statusFor(used),
			})
		}
	}
	return Dedupe(out)
}

// grokPlan reads the cached subscription name; billing meters are read separately.

func grokPlan(env *sources.Env) []model.LimitSnapshot {
	b, err := os.ReadFile(filepath.Join(env.Home, ".grok", "settings_cache.json"))
	if err != nil {
		return nil
	}
	var outer struct {
		Payload string `json:"payload"`
	}
	if json.Unmarshal(b, &outer) != nil || outer.Payload == "" {
		return nil
	}
	var payload struct {
		FetchedAt string `json:"fetched_at"`
		Identity  string `json:"identity"`
		Settings  struct {
			Tier string `json:"subscription_tier_display"`
		} `json:"settings"`
	}
	if json.Unmarshal([]byte(outer.Payload), &payload) != nil {
		return nil
	}
	plan := sanitize(payload.Settings.Tier)
	if plan == "" {
		return nil
	}
	observed := time.Time{}
	if t := parseRFC(payload.FetchedAt); t != nil {
		observed = *t
	}
	acct, q := "", model.AcctUnknown
	if env.HashID != nil && payload.Identity != "" {
		acct = env.HashID(model.ProviderXAI, payload.Identity)
		if acct != "" {
			q = model.AcctRecorded
		}
	}
	return []model.LimitSnapshot{{
		ID:         snapshotID(model.ProviderXAI, model.SourceGrokCLI, acct, "plan", ""),
		Provider:   model.ProviderXAI,
		Source:     model.SourceGrokCLI,
		Acct:       acct,
		AcctQ:      q,
		Plan:       plan,
		Label:      grokCacheLabel(env.Home, payload.Identity),
		Window:     "plan",
		ObservedAt: observed,
	}}
}

// The settings cache identity is a hash of account scope, NOT a user id.
// Retain the existing plan row id for compatibility with uploaded rows, but
// label it only after reproducing Grok's identity hash from non-secret auth
// metadata. This also lets the UI join its legacy plan row to the weekly meter.
// Source: xai-grok-cloud-config/src/settings_endpoint.rs settings_cache_identity
// and remote_settings/validation.rs scope_hash in xai-org/grok-build.
func grokCacheLabel(home, identity string) string {
	if identity == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(home, ".grok", "auth.json"))
	if err != nil {
		return ""
	}
	var entries map[string]struct {
		UserID         string `json:"user_id"`
		Email          string `json:"email"`
		TeamID         string `json:"team_id"`
		OrganizationID string `json:"organization_id"`
		Issuer         string `json:"oidc_issuer"`
		Mode           string `json:"auth_mode"`
	}
	if json.Unmarshal(b, &entries) != nil {
		return ""
	}
	label := ""
	for _, e := range entries {
		// An empty user id makes upstream hash a credential. Never read or
		// reproduce that fallback. Unknown/alpha-test scopes stay unlabeled.
		if e.UserID == "" {
			continue
		}
		switch e.Mode {
		case "web_login", "oidc", "external", "api_key":
		default:
			continue
		}
		h := sha256.New()
		for _, part := range []string{e.UserID, e.TeamID, e.OrganizationID, e.Issuer, e.Mode, ""} {
			h.Write([]byte(part))
			h.Write([]byte{0})
		}
		if hex.EncodeToString(h.Sum(nil)) != identity {
			continue
		}
		if email := emailOf(e.Email); email != "" {
			if label != "" && label != email {
				return ""
			}
			label = email
		}
	}
	return label
}
