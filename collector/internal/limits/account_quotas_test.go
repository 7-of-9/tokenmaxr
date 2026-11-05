package limits

import (
	"encoding/json"
	"fmt"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIndependentProviderAccounts(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	env := &sources.Env{Home: t.TempDir(), AccountQuotas: dir, HashID: func(p, id string) string { return p + "-" + id }}
	for _, provider := range []string{"openai", "anthropic", "xai", "cursor", "google"} {
		for i := 0; i < 50; i++ {
			// Same display email does not collapse distinct provider identities.
			body := map[string]any{"schema": 1, "provider": provider, "accountId": fmt.Sprint(i), "email": "same@example.com", "observedAt": at, "windows": []any{map[string]any{"window": "week", "usedPercent": 100, "status": "full", "resetsAt": at.Add(24 * time.Hour)}}}
			raw, _ := json.Marshal(body)
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s-%d.json", provider, i)), raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	rows := Collect(env)
	if len(rows) != 250 {
		t.Fatalf("got %d independent account meters", len(rows))
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if seen[row.ID] || row.AcctQ != "recorded" || row.QuotaSource != "account-api" || !row.ObservedAt.Equal(at) || *row.UsedPercent != 100 || row.Status != "full" {
			t.Fatalf("invalid account reading: %+v", row)
		}
		seen[row.ID] = true
	}
}
