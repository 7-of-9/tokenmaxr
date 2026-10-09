package app

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/homes"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

// The account probe at the start of a tick notes the Claude login's
// organisation, before the quota refresh asks Claude Code for a fresh
// reading: that reading is then after the switch and counts at once.
func TestProbeNotesTheClaudeOrgBeforeTheRefresh(t *testing.T) {
	home := t.TempDir()
	write := func(org string) {
		body := `{"oauthAccount": {"accountUuid": "user-1", "emailAddress": "dm@example.com", "organizationUuid": "` + org + `", "organizationType": "claude_team"}}`
		if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a := &App{}
	k := bytes.Repeat([]byte{3}, 32)
	cfg := &store.Config{AccountLabels: map[string]string{}}
	st := store.NewState()
	hs := []homes.Home{{Path: home, Kind: homes.KindOS}}
	t0 := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)

	write("org-team")
	a.probeAccounts(k, cfg, st, t0, hs)
	seen, ok := st.Orgs["|anthropic"]
	if !ok || seen.Org == "" || !seen.Since.IsZero() {
		t.Fatalf("first organisation: %+v", st.Orgs)
	}
	write("org-me")
	a.probeAccounts(k, cfg, st, t0.Add(time.Minute), hs)
	if got := st.Orgs["|anthropic"]; got.Org == seen.Org || !got.Since.Equal(t0.Add(time.Minute)) {
		t.Fatalf("switch: %+v", got)
	}
}
