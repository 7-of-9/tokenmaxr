// Package limits reads the usage meters the tools already write: Claude
// Code's cachedUsageUtilization, the newest Codex rollout rate_limits, and
// Grok's billing snapshots. It does not call a provider API.
package limits

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/accounts"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/jsonl"
)

// tailBytes is how much of a Codex rollout is read from the end. The current
// meter is on a recent token_count line. Tests shrink it.
var tailBytes int64 = 256 << 10

// Collect returns the newest meter per window for one home.
func Collect(env *sources.Env) []model.LimitSnapshot {
	if env == nil || env.Home == "" {
		return nil
	}
	var out []model.LimitSnapshot
	out = append(out, claudeLimits(env)...)
	out = append(out, codexLimits(env)...)
	out = append(out, grokLimits(env)...)
	return applyAccounts(env, out)
}

// applyAccounts labels a meter only when its account matches the current login,
// and adds a plan row for a signed-in account that wrote no meter, so every
// account is visible. The email stays off public usage; only the owner
// limits read returns it.
func applyAccounts(env *sources.Env, rows []model.LimitSnapshot) []model.LimitSnapshot {
	obs := accounts.Probe(env.Home, codexDir(env))
	email := map[string]string{}
	currentAcct := map[string]string{}
	for _, o := range obs {
		if env.HashID != nil {
			currentAcct[o.Provider] = env.HashID(o.Provider, o.NativeID)
		}
		if e := emailOf(o.Label); e != "" {
			email[o.Provider] = e
		}
	}
	for i := range rows {
		// A historical meter may belong to an account that is no longer
		// signed in. Never label it with the current account's email.
		if rows[i].Label == "" && (rows[i].Acct == "" || rows[i].Acct == currentAcct[rows[i].Provider]) {
			rows[i].Label = email[rows[i].Provider]
		}
	}
	rows = Dedupe(rows)
	have := map[string]bool{}
	for _, r := range rows {
		have[r.Provider+"|"+r.Acct] = true
	}
	now := time.Now().UTC().Truncate(time.Second)
	for _, o := range obs {
		if have[o.Provider+"|"+currentAcct[o.Provider]] {
			continue
		}
		src, ok := sourceFor(o.Provider)
		e := emailOf(o.Label)
		if !ok || e == "" {
			continue
		}
		acct, q := "", model.AcctUnknown
		if env.HashID != nil {
			if acct = env.HashID(o.Provider, o.NativeID); acct != "" {
				q = model.AcctRecorded
			}
		}
		rows = append(rows, model.LimitSnapshot{
			ID:         snapshotID(o.Provider, src, acct, "plan", ""),
			Provider:   o.Provider,
			Source:     src,
			Acct:       acct,
			AcctQ:      q,
			Window:     "plan",
			Label:      e,
			ObservedAt: now,
		})
	}
	return rows
}

// EmailOf is the email in an account label ("email · org"), or "".
func EmailOf(label string) string { return emailOf(label) }

func emailOf(label string) string {
	for _, p := range strings.Split(label, " · ") {
		p = strings.TrimSpace(p)
		if strings.Count(p, "@") == 1 && !strings.ContainsAny(p, " \r\n") && len(p) <= 80 {
			return p
		}
	}
	return ""
}

func sourceFor(provider string) (string, bool) {
	switch provider {
	case model.ProviderAnthropic:
		return model.SourceClaudeCode, true
	case model.ProviderOpenAI:
		return model.SourceCodex, true
	case model.ProviderXAI:
		return model.SourceGrokCLI, true
	case "cursor":
		return "cursor", true
	case "google":
		return "gemini-cli", true
	default:
		return "", false
	}
}

// Dedupe keeps the newest ObservedAt for each snapshot id.
func Dedupe(in []model.LimitSnapshot) []model.LimitSnapshot {
	by := map[string]model.LimitSnapshot{}
	var order []string
	for _, s := range in {
		if s.ID == "" {
			continue
		}
		if prev, ok := by[s.ID]; ok {
			if !s.ObservedAt.After(prev.ObservedAt) {
				continue
			}
		} else {
			order = append(order, s.ID)
		}
		by[s.ID] = s
	}
	out := make([]model.LimitSnapshot, 0, len(order))
	for _, id := range order {
		out = append(out, by[id])
	}
	return out
}

// Fingerprint is what a snapshot says, apart from when it was read: two
// snapshots with the same fingerprint need not both be uploaded.
func Fingerprint(s model.LimitSnapshot) string {
	s.ObservedAt = time.Time{}
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// ResendAfter is how far a provider's re-report (observedAt) must advance
// before an otherwise unchanged meter is uploaded again, so the site's
// "Read … ago" tracks the provider without uploading on every tick.
const ResendAfter = 5 * time.Minute

// SentMark records what was queued for a snapshot: its fingerprint and, for
// provider-reported meters, when the provider reported it. Synthetic "plan"
// rows carry local time, not a provider time, so only their content counts.
func SentMark(s model.LimitSnapshot) string {
	fp := Fingerprint(s)
	if s.Window == "plan" || s.ObservedAt.IsZero() {
		return fp
	}
	return fmt.Sprintf("%s@%d", fp, s.ObservedAt.Unix())
}

// ShouldResend reports whether a snapshot whose mark is next must be queued
// given the mark last queued (prev, possibly a pre-0.2.6 bare fingerprint).
func ShouldResend(prev, next string) bool {
	if prev == next {
		return false
	}
	prevFP, prevAt, prevHasAt := splitMark(prev)
	nextFP, nextAt, nextHasAt := splitMark(next)
	if prevFP != nextFP {
		return true
	}
	if !nextHasAt {
		return false
	}
	if !prevHasAt {
		return true // first mark with a time since upgrading: refresh once
	}
	return nextAt-prevAt >= int64(ResendAfter/time.Second)
}

func splitMark(mark string) (fp string, at int64, ok bool) {
	i := strings.LastIndexByte(mark, '@')
	if i < 0 {
		return mark, 0, false
	}
	var n int64
	if _, err := fmt.Sscan(mark[i+1:], &n); err != nil {
		return mark, 0, false
	}
	return mark[:i], n, true
}

func snapshotID(provider, source, acct, window, scope string) string {
	return model.EventID("limit", provider, source, acct+"|"+window+"|"+scope)
}

// sanitize keeps a short label. An email, or anything with one, is dropped.
func sanitize(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, "@\r\n") || len(s) > 80 {
		return ""
	}
	return s
}

func pct(v float64) *float64 { return &v }

// statusFor is "full" once a window is used up. Claude's is_active only marks
// the limit its UI leads with (a 3% session is inactive beside a 60% week),
// so it never means the window is paused.
func statusFor(used float64) string {
	if used >= 100 {
		return "full"
	}
	return "ok"
}

func unixAuto(n int64) time.Time {
	if n <= 0 {
		return time.Time{}
	}
	if n < 1_000_000_000_000 {
		return time.Unix(n, 0).UTC()
	}
	return time.UnixMilli(n).UTC()
}

func parseRFC(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil
	}
	t = t.UTC()
	return &t
}

// --- Claude Code: ~/.claude.json cachedUsageUtilization ---

type claudeFile struct {
	OAuth *struct {
		AccountUUID string `json:"accountUuid"`
		OrgName     string `json:"organizationName"`
		Tier        string `json:"organizationRateLimitTier"`
	} `json:"oauthAccount"`
	Cache *struct {
		FetchedAtMs int64  `json:"fetchedAtMs"`
		AccountUUID string `json:"accountUuid"`
		Utilization struct {
			FiveHour *claudeWin `json:"five_hour"`
			SevenDay *claudeWin `json:"seven_day"`
			Extra    *struct {
				IsEnabled      bool    `json:"is_enabled"`
				Utilization    float64 `json:"utilization"`
				DisabledReason string  `json:"disabled_reason"`
			} `json:"extra_usage"`
			Limits []struct {
				Kind     string  `json:"kind"`
				Percent  float64 `json:"percent"`
				ResetsAt string  `json:"resets_at"`
				Scope    *struct {
					Model *struct {
						DisplayName string `json:"display_name"`
					} `json:"model"`
				} `json:"scope"`
			} `json:"limits"`
		} `json:"utilization"`
	} `json:"cachedUsageUtilization"`
}

type claudeWin struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at"`
}

func claudePlan(tier string) string {
	switch {
	case strings.Contains(tier, "max_20x"):
		return "Max (20x)"
	case strings.Contains(tier, "max_5x"):
		return "Max (5x)"
	case strings.Contains(tier, "_max"):
		return "Max"
	case strings.Contains(tier, "pro"):
		return "Pro"
	case strings.HasPrefix(tier, "default_"):
		// An internal tier code such as default_raven names no plan.
		return ""
	default:
		return sanitize(tier)
	}
}

func claudeWindow(kind string) string {
	switch kind {
	case "session", "five_hour":
		return "session"
	case "weekly_all", "weekly", "seven_day":
		return "week"
	case "weekly_scoped":
		return "week"
	default:
		if kind == "" || strings.ContainsAny(kind, " @") {
			return ""
		}
		return kind
	}
}

func claudeLimits(env *sources.Env) []model.LimitSnapshot {
	b, err := os.ReadFile(filepath.Join(env.Home, ".claude.json"))
	if err != nil {
		return nil
	}
	var f claudeFile
	if json.Unmarshal(b, &f) != nil || f.Cache == nil {
		return nil
	}
	uuid := f.Cache.AccountUUID
	name, plan := "", ""
	if f.OAuth != nil {
		if uuid == "" {
			uuid = f.OAuth.AccountUUID
		}
		name = sanitize(f.OAuth.OrgName)
		plan = claudePlan(f.OAuth.Tier)
	}
	acct, q := "", model.AcctUnknown
	if env.HashID != nil && uuid != "" {
		acct = env.HashID(model.ProviderAnthropic, uuid)
		if acct != "" {
			q = model.AcctRecorded
		}
	}
	observed := unixAuto(f.Cache.FetchedAtMs)
	if observed.IsZero() {
		if st, err := os.Stat(filepath.Join(env.Home, ".claude.json")); err == nil {
			observed = st.ModTime().UTC()
		}
	}
	u := f.Cache.Utilization
	var out []model.LimitSnapshot
	add := func(window, scope, detail, status string, used float64, hasUsed bool, resets *time.Time) {
		if window == "" {
			return
		}
		s := model.LimitSnapshot{
			ID:         snapshotID(model.ProviderAnthropic, model.SourceClaudeCode, acct, window, scope),
			Provider:   model.ProviderAnthropic,
			Source:     model.SourceClaudeCode,
			Acct:       acct,
			AcctQ:      q,
			Plan:       plan,
			Window:     window,
			Scope:      scope,
			Name:       name,
			Detail:     detail,
			ResetsAt:   resets,
			ObservedAt: observed,
			Status:     status,
		}
		if hasUsed {
			s.UsedPercent = pct(used)
		}
		out = append(out, s)
	}
	if len(u.Limits) > 0 {
		for _, row := range u.Limits {
			scope := ""
			if row.Scope != nil && row.Scope.Model != nil {
				scope = sanitize(row.Scope.Model.DisplayName)
			}
			add(claudeWindow(row.Kind), scope, "", statusFor(row.Percent), row.Percent, true, parseRFC(row.ResetsAt))
		}
	} else {
		if u.FiveHour != nil {
			add("session", "", "", statusFor(u.FiveHour.Utilization), u.FiveHour.Utilization, true, parseRFC(u.FiveHour.ResetsAt))
		}
		if u.SevenDay != nil {
			add("week", "", "", statusFor(u.SevenDay.Utilization), u.SevenDay.Utilization, true, parseRFC(u.SevenDay.ResetsAt))
		}
	}
	if u.Extra != nil {
		detail := ""
		status := "ok"
		if !u.Extra.IsEnabled {
			status = "disabled"
			detail = sanitize(strings.ReplaceAll(u.Extra.DisabledReason, "_", " "))
		}
		add("extra", "", detail, status, u.Extra.Utilization, true, nil)
	}
	return out
}

// --- Codex: newest rate_limits in each rollout ---

func codexDir(env *sources.Env) string {
	if env.CodexHome != "" {
		return env.CodexHome
	}
	return filepath.Join(env.Home, ".codex")
}

type codexRates struct {
	LimitName *string   `json:"limit_name"`
	Primary   *codexWin `json:"primary"`
	Secondary *codexWin `json:"secondary"`
	PlanType  *string   `json:"plan_type"`
}

type codexWin struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int     `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"`
}

type codexLine struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type             string      `json:"type"`
		ID               string      `json:"id"`
		CreatorAccountID string      `json:"creator_account_id"`
		RateLimits       *codexRates `json:"rate_limits"`
	} `json:"payload"`
}

func minutesWindow(m int) string {
	switch {
	case m <= 0:
		return ""
	case m <= 12*60:
		return "session"
	case m >= 24*60:
		return "week"
	default:
		return fmt.Sprintf("%dm", m)
	}
}

func codexPlan(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "":
		return ""
	case "pro":
		return "Pro"
	case "plus":
		return "Plus"
	case "team":
		return "Team"
	case "business":
		return "Business"
	case "enterprise":
		return "Enterprise"
	default:
		return sanitize(p)
	}
}

func codexLimits(env *sources.Env) []model.LimitSnapshot {
	var out []model.LimitSnapshot
	for _, sub := range []string{"sessions", "archived_sessions"} {
		root := filepath.Join(codexDir(env), sub)
		filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				if p == root {
					return fs.SkipAll
				}
				return nil
			}
			if d.IsDir() || !strings.HasPrefix(d.Name(), "rollout-") || !strings.HasSuffix(d.Name(), ".jsonl") {
				return nil
			}
			out = append(out, codexFile(env, p)...)
			return nil
		})
	}
	// A fresh reading fetched through Codex's app server, written in the
	// rollout format; Dedupe keeps whichever reading is newest.
	if env.CodexQuota != "" {
		out = append(out, codexFile(env, env.CodexQuota)...)
	}
	return out
}

func codexFile(env *sources.Env, path string) []model.LimitSnapshot {
	head, tail, err := readEdges(path)
	if err != nil {
		return nil
	}
	var sessionID, creator string
	for _, line := range bytes.Split(head, []byte("\n")) {
		var l codexLine
		if jsonl.Decode(line, &l) && l.Type == "session_meta" {
			sessionID = l.Payload.ID
			creator = l.Payload.CreatorAccountID
			break
		}
	}
	lines := bytes.Split(tail, []byte("\n"))
	var found *codexLine
	var foundTS time.Time
	for i := len(lines) - 1; i >= 0; i-- {
		var l codexLine
		if !jsonl.Decode(lines[i], &l) || l.Payload.RateLimits == nil {
			continue
		}
		ts, ok := jsonl.ParseTime(l.Timestamp)
		if !ok {
			continue
		}
		found = &l
		foundTS = ts.UTC()
		break
	}
	if found == nil {
		return nil
	}
	rl := found.Payload.RateLimits
	plan := ""
	if rl.PlanType != nil {
		plan = codexPlan(*rl.PlanType)
	}
	scope := ""
	if rl.LimitName != nil {
		scope = sanitize(*rl.LimitName)
	}
	acct, q := "", model.AcctUnknown
	if env.Attribute != nil {
		hint := jsonl.HintFor(env, model.ProviderOpenAI, sources.HintAccount, creator, foundTS)
		acct, q = env.Attribute(model.ProviderOpenAI, foundTS, sessionID, hint)
		if q == "" {
			q = model.AcctUnknown
		}
	}
	primaryName := ""
	if rl.Primary != nil {
		primaryName = minutesWindow(rl.Primary.WindowMinutes)
	}
	var out []model.LimitSnapshot
	add := func(which string, w *codexWin) {
		if w == nil {
			return
		}
		window := minutesWindow(w.WindowMinutes)
		if window == "" {
			return
		}
		sc := scope
		if which == "secondary" && window == primaryName {
			if sc != "" {
				sc += " "
			}
			sc += "secondary"
		}
		resets := unixAuto(w.ResetsAt)
		var resetsAt *time.Time
		if !resets.IsZero() {
			resetsAt = &resets
		}
		used := w.UsedPercent
		out = append(out, model.LimitSnapshot{
			ID:          snapshotID(model.ProviderOpenAI, model.SourceCodex, acct, window, sc),
			Provider:    model.ProviderOpenAI,
			Source:      model.SourceCodex,
			Acct:        acct,
			AcctQ:       q,
			Plan:        plan,
			Window:      window,
			Scope:       sc,
			UsedPercent: pct(used),
			ResetsAt:    resetsAt,
			ObservedAt:  foundTS,
			Status:      statusFor(used),
		})
	}
	add("primary", rl.Primary)
	add("secondary", rl.Secondary)
	return out
}

// readEdges returns the start of the file and its tail. A tail that is not
// the whole file begins on a line boundary.
func readEdges(path string) (head, tail []byte, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	headN := int64(32 << 10)
	if st.Size() < headN {
		headN = st.Size()
	}
	head = make([]byte, headN)
	if _, err = io.ReadFull(f, head); err != nil && err != io.ErrUnexpectedEOF {
		return nil, nil, err
	}
	if st.Size() <= tailBytes {
		all, err := os.ReadFile(path)
		return all, all, err
	}
	if _, err = f.Seek(-tailBytes, io.SeekEnd); err != nil {
		return nil, nil, err
	}
	tail, err = io.ReadAll(f)
	if err != nil {
		return nil, nil, err
	}
	if i := bytes.IndexByte(tail, '\n'); i >= 0 {
		tail = tail[i+1:]
	}
	return head, tail, nil
}
