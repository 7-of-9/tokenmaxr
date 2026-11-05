package limits

// Provider adapters write one credential-free snapshot per account/workspace.
// Account count and sign-in method are deliberately absent from this contract.
// Identities are hashed here with the same fleet key as native-client readings.
import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
)

type accountQuota struct {
	Schema     int       `json:"schema"`
	Provider   string    `json:"provider"`
	AccountID  string    `json:"accountId"`
	Email      string    `json:"email"`
	OrgID      string    `json:"orgId"`
	OrgKind    string    `json:"orgKind"`
	Name       string    `json:"name"`
	Plan       string    `json:"plan"`
	ObservedAt time.Time `json:"observedAt"`
	Windows    []struct {
		Window      string     `json:"window"`
		Scope       string     `json:"scope"`
		UsedPercent *float64   `json:"usedPercent"`
		ResetsAt    *time.Time `json:"resetsAt"`
		Status      string     `json:"status"`
	} `json:"windows"`
}

func accountQuotas(env *sources.Env) []model.LimitSnapshot {
	if env.AccountQuotas == "" || env.HashID == nil {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(env.AccountQuotas, "*.json"))
	var out []model.LimitSnapshot
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil || info.Size() > 1024*1024 {
			continue
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		var q accountQuota
		if json.Unmarshal(raw, &q) != nil || q.Schema != 1 || q.AccountID == "" || len(q.AccountID) > 512 ||
			q.ObservedAt.IsZero() || q.ObservedAt.After(time.Now().Add(2*time.Minute)) {
			continue
		}
		source, ok := sourceFor(q.Provider)
		if !ok {
			continue
		}
		acct := env.HashID(q.Provider, q.AccountID)
		if acct == "" {
			continue
		}
		org := ""
		if q.OrgID != "" {
			org = env.HashID(q.Provider, "org:"+q.OrgID)
		}
		if q.OrgKind != "" && q.OrgKind != "personal" && q.OrgKind != "team" && q.OrgKind != "enterprise" {
			continue
		}
		for _, w := range q.Windows {
			if w.Window != "week" && w.Window != "session" && w.Window != "plan" && w.Window != "extra" {
				continue
			}
			if w.UsedPercent != nil && (math.IsNaN(*w.UsedPercent) || math.IsInf(*w.UsedPercent, 0) || *w.UsedPercent < 0 || *w.UsedPercent > 100) {
				continue
			}
			if w.Status != "" && w.Status != "ok" && w.Status != "full" && w.Status != "disabled" {
				continue
			}
			out = append(out, model.LimitSnapshot{
				ID: orgSnapshotID(q.Provider, source, acct, org, w.Window, sanitize(w.Scope)), Provider: q.Provider, Source: source,
				Acct: acct, AcctQ: model.AcctRecorded, QuotaSource: "account-api", Label: emailOf(q.Email),
				Org: org, OrgKind: q.OrgKind, Name: sanitize(q.Name), Plan: sanitize(q.Plan), Window: w.Window, Scope: sanitize(w.Scope),
				UsedPercent: w.UsedPercent, ResetsAt: w.ResetsAt, ObservedAt: q.ObservedAt, Status: w.Status,
			})
		}
	}
	return out
}
