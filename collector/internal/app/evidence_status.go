package app

import (
	"fmt"
	"slices"
	"strings"

	"github.com/7-of-9/tokenmaxr/collector/internal/evidence"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

// evidenceLine summarises the harvested account evidence for status:
// counts per provider and kind only (SPEC "Accounts").
func evidenceLine(st *store.State) string {
	if len(st.Evidence) == 0 {
		return "none harvested yet"
	}
	counts := evidence.Counts(st.Evidence)
	provs := make([]string, 0, len(counts))
	for p := range counts {
		provs = append(provs, p)
	}
	slices.Sort(provs)
	parts := []string{fmt.Sprintf("%d records, %d files watched, attribution v%d", len(st.Evidence), len(st.Harvest), st.AttribVersion)}
	for _, p := range provs {
		kinds := make([]string, 0, len(counts[p]))
		for k := range counts[p] {
			kinds = append(kinds, k)
		}
		slices.Sort(kinds)
		ks := make([]string, 0, len(kinds))
		for _, k := range kinds {
			ks = append(ks, fmt.Sprintf("%s %d", k, counts[p][k]))
		}
		parts = append(parts, p+": "+strings.Join(ks, ", "))
	}
	return strings.Join(parts, "; ")
}
