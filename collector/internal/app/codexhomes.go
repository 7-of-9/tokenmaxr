package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/fsx"
	"github.com/7-of-9/tokenmaxr/collector/internal/homes"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/codex"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

// codexEnvEvery is how often the CODEX_HOME lookups outside this process
// (homes.DefaultCodexLookups: a login shell, launchd, the registry) run
// again. Between runs a tick reuses their answer from codex-env.json, so a
// headless tick every minute does not start a login shell every minute.
const codexEnvEvery = 6 * time.Hour

// codexEnvFile caches the lookups' answer in the state directory.
type codexEnvFile struct {
	CheckedAt time.Time `json:"checkedAt"`
	Dirs      []string  `json:"dirs"`
}

func codexEnvPath(home string) string { return filepath.Join(home, "codex-env.json") }

// codexDirs are the Codex directories to read beside the OS home's: every
// CODEX_HOME set outside this process, and ~/.codex itself (history from
// before a CODEX_HOME moved Codex elsewhere). homes.Discover drops the ones
// that do not exist or that a home reads already. live runs the lookups
// whatever the cache says, and leaves the cache alone (doctor, scan
// --dry-run).
func (a *App) codexDirs(live bool) (dirs, notes []string) {
	defaultDir := filepath.Join(a.UserHome, ".codex")
	if a.CodexLookups == nil {
		return []string{defaultDir}, nil
	}
	now := a.Now()
	var cached codexEnvFile
	if !live {
		if b, err := fsx.ReadFile(codexEnvPath(a.Home)); err == nil && json.Unmarshal(b, &cached) == nil &&
			now.Sub(cached.CheckedAt) >= 0 && now.Sub(cached.CheckedAt) < codexEnvEvery {
			return append(cached.Dirs, defaultDir), nil
		}
	}
	found, notes := homes.CodexEnvDirs(context.Background(), a.UserHome, a.CodexLookups)
	if b, err := json.Marshal(codexEnvFile{CheckedAt: now, Dirs: found}); err == nil && !live {
		// Best effort: without the cache the next tick only looks again. A
		// live look (doctor, a dry run) writes nothing.
		_ = fsx.WriteFileAtomic(codexEnvPath(a.Home), b, 0o600)
	}
	return append(found, defaultDir), notes
}

// codexMissing counts the sessions of the homes' history.jsonl files that
// have no rollout file in any of the homes' Codex directories
// (codex.MissingLogs): this machine's CODEX_HOME and ~/.codex can each hold
// part of one history.
func (a *App) codexMissing(hs []homes.Home) codex.Missing {
	dirs := make([]string, 0, len(hs))
	for _, h := range hs {
		dirs = append(dirs, h.CodexHome(a.CodexHome))
	}
	// An unreadable history leaves its own prompts out; the others still count.
	m, _ := codex.MissingLogs(a.Now(), dirs...)
	return m
}

// lastHomes are the scan roots of the last tick (st.Homes), or the OS home
// alone before the first one: status reads them without discovering again.
// WSL homes are left out: the distro may have stopped since that tick, and
// opening \\wsl$\<distro> would start it (SPEC "Scan roots"); doctor counts
// them through live discovery.
func (a *App) lastHomes(st *store.State) []homes.Home {
	if st == nil || len(st.Homes) == 0 {
		return []homes.Home{{Path: a.UserHome, Kind: homes.KindOS}}
	}
	out := make([]homes.Home, 0, len(st.Homes))
	for _, h := range st.Homes {
		if h.Kind == homes.KindWSL {
			continue
		}
		out = append(out, homes.Home{Path: h.Path, Kind: h.Kind, Distro: h.Distro})
	}
	return out
}
