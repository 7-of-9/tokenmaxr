package homes

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// KindCodex is a Codex directory outside the OS home's: a CODEX_HOME set
// where the collector's own process does not see it (the user's login shell,
// launchd, the Windows registry), or ~/.codex beside a CODEX_HOME that points
// elsewhere. Only the Codex parser reads it; its Path is the Codex directory.
const KindCodex = "codex"

// CodexLookup reads one place a CODEX_HOME can be set. It returns "" when
// the variable is not set there.
type CodexLookup struct {
	// Origin names the place for notes ("login shell", "launchd", ...).
	Origin string
	Lookup func(ctx context.Context) (string, error)
}

// codexLookupTimeout bounds every lookup: a slow or hung login shell must
// not hold up a tick.
const codexLookupTimeout = 4 * time.Second

// CodexEnvDirs runs the lookups and returns the absolute Codex directories
// they name, in order and without repeats, plus notes for the log. A value
// starting with ~ is taken relative to userHome; a relative value is
// dropped, as there is nothing it could be relative to.
func CodexEnvDirs(ctx context.Context, userHome string, lookups []CodexLookup) (dirs, notes []string) {
	seen := map[string]bool{}
	for _, l := range lookups {
		lctx, cancel := context.WithTimeout(ctx, codexLookupTimeout)
		v, err := l.Lookup(lctx)
		cancel()
		if err != nil {
			notes = append(notes, "CODEX_HOME from "+l.Origin+": "+err.Error())
			continue
		}
		d := cleanCodexDir(v, userHome)
		if d == "" {
			continue
		}
		if k := canon(d); !seen[k] {
			seen[k] = true
			dirs = append(dirs, d)
		}
	}
	return dirs, notes
}

func cleanCodexDir(v, userHome string) string {
	v = strings.Trim(strings.TrimSpace(v), `"'`)
	switch {
	case v == "":
		return ""
	case v == "~":
		v = userHome
	case strings.HasPrefix(v, "~/") || strings.HasPrefix(v, `~\`):
		v = filepath.Join(userHome, v[2:])
	}
	if !filepath.IsAbs(v) {
		return ""
	}
	return filepath.Clean(v)
}

// addCodexHomes appends a KindCodex home for each Codex directory in dirs
// that exists and that no home already reads (osCodexHome for the OS home,
// <path>/.codex for the others). Directories are compared by identity
// (os.SameFile), so one reached through a link or junction is the same one.
func addCodexHomes(r *Result, dirs []string, osCodexHome string, add func(Home)) {
	var read []os.FileInfo
	same := func(st os.FileInfo) bool {
		for _, r := range read {
			if os.SameFile(r, st) {
				return true
			}
		}
		return false
	}
	for _, h := range r.Homes {
		if st, err := os.Stat(h.CodexHome(osCodexHome)); err == nil {
			read = append(read, st)
		}
	}
	for _, d := range dirs {
		st, err := os.Stat(d)
		if err != nil || !st.IsDir() || same(st) {
			continue // set but never used, since removed, or read already
		}
		read = append(read, st)
		add(Home{Path: d, Kind: KindCodex})
	}
}
