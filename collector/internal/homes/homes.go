// Package homes builds the list of scan roots (docs/agents/SPEC.md "Scan
// roots: multiple homes"): the OS user home, any extraHomes from config.json,
// Codex directories set outside the collector's environment (codex.go) and,
// on Windows, the homes inside running WSL distros. Every parser and account
// probe runs once per home (a Codex directory: the Codex parser and its
// account only). A stopped distro is never touched, because
// opening \\wsl$\<distro> would boot it.
package homes

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// Home kinds.
const (
	KindOS    = "os"
	KindExtra = "extra"
	KindWSL   = "wsl"
)

// Home is one scan root.
type Home struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	// Distro is the WSL distribution name for KindWSL.
	Distro string `json:"distro,omitempty"`
}

// Label is the short name status prints: "os", "extra" or "wsl:<distro>".
func (h Home) Label() string {
	if h.Kind == KindWSL {
		return KindWSL + ":" + h.Distro
	}
	return h.Kind
}

// Key identifies the home in the account timeline: "" for the OS home (so
// state.json from before v1.3 keeps meaning what it meant), else the path.
func (h Home) Key() string {
	if h.Kind == KindOS {
		return ""
	}
	return h.Path
}

// CodexHome is the Codex directory for this home. $CODEX_HOME applies to the
// OS home only (osCodexHome); a KindCodex home is a Codex directory itself;
// any other home uses its own .codex.
func (h Home) CodexHome(osCodexHome string) string {
	switch {
	case h.Kind == KindOS && osCodexHome != "":
		return osCodexHome
	case h.Kind == KindCodex:
		return h.Path
	}
	return filepath.Join(h.Path, ".codex")
}

// CodexOnly reports whether only the Codex parser (and Codex's own checks
// and account) reads this home.
func (h Home) CodexOnly() bool { return h.Kind == KindCodex }

type Options struct {
	UserHome string
	Extra    []string
	// OSCodexHome is the OS home's Codex directory ($CODEX_HOME of this
	// process, else ~/.codex); CodexDirs are more Codex directories, read
	// when no other home reads them already (codex.go).
	OSCodexHome string
	CodexDirs   []string
	// DiscoverWSL enables the WSL lookup (Windows only; a no-op elsewhere).
	DiscoverWSL bool
	// WSL overrides the WSL runner (tests). nil selects the platform default.
	WSL *WSL
}

// Result is the homes to scan plus what was skipped and why.
type Result struct {
	Homes []Home
	// Skipped lists the WSL distros that are installed but not running; their
	// files cannot change, so they are only delayed, never missed.
	Skipped []string
	// Unreachable are the roots (<wsl root>\<distro>) of the skipped
	// distros: cursors under them are kept, not pruned, until they return.
	Unreachable []string
	// Notes are non-fatal discovery messages for the log (no paths of other
	// users, no secrets).
	Notes []string
}

// Discover builds the homes list: the OS home first, then extras (cleaned,
// deduplicated, missing ones dropped), then Codex directories no home reads
// yet, then WSL homes.
func Discover(o Options) Result {
	var r Result
	seen := map[string]bool{}
	add := func(h Home) {
		k := canon(h.Path)
		if k == "" || seen[k] {
			return
		}
		seen[k] = true
		r.Homes = append(r.Homes, h)
	}
	add(Home{Path: filepath.Clean(o.UserHome), Kind: KindOS})
	for _, p := range o.Extra {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			r.Notes = append(r.Notes, "extraHomes: "+p+": "+err.Error())
			continue
		}
		if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			r.Notes = append(r.Notes, "extraHomes: "+abs+": not a directory, skipped")
			continue
		}
		add(Home{Path: abs, Kind: KindExtra})
	}
	addCodexHomes(&r, o.CodexDirs, o.OSCodexHome, add)
	if o.DiscoverWSL {
		w := o.WSL
		if w == nil {
			w = DefaultWSL()
		}
		if w != nil {
			found, skipped, notes := w.Discover()
			for _, h := range found {
				add(h)
			}
			r.Skipped = skipped
			r.Unreachable = w.Roots(skipped)
			r.Notes = append(r.Notes, notes...)
		}
	}
	return r
}

// canon is the comparison form of a path: cleaned, and case-folded on
// Windows, where the file system ignores case.
func canon(p string) string {
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return p
}

// Under reports whether path lies inside root (or is root).
func Under(path, root string) bool {
	p, r := canon(path), canon(root)
	if p == r {
		return true
	}
	if !strings.HasSuffix(r, string(filepath.Separator)) {
		r += string(filepath.Separator)
	}
	return strings.HasPrefix(p, r)
}

// Paths lists the homes' paths in order.
func Paths(hs []Home) []string {
	out := make([]string, 0, len(hs))
	for _, h := range hs {
		out = append(out, h.Path)
	}
	return out
}

// Contains reports whether hs holds a home at path.
func Contains(hs []Home, path string) bool {
	return slices.ContainsFunc(hs, func(h Home) bool { return canon(h.Path) == canon(path) })
}
