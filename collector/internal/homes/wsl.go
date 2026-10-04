package homes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
)

// WSLRoot is the UNC root that exposes each distro's file system.
const WSLRoot = `\\wsl$`

// toolDirs are the directories that make a WSL home worth scanning.
var toolDirs = []string{".claude", ".codex", ".grok", ".gemini"}

// listTimeout bounds one wsl.exe listing; a hung WSL service must not eat
// the tick.
const listTimeout = 15 * time.Second

// WSL finds the homes of running distros. It only ever lists distros with
// wsl.exe and reads inside \\wsl$\<distro> for distros that are running;
// listing does not start anything, reading a stopped distro would.
type WSL struct {
	// List returns the raw output of `wsl.exe -l -q` (all distros) or
	// `wsl.exe -l --running -q` (runningOnly). The output is UTF-16LE.
	List func(ctx context.Context, runningOnly bool) ([]byte, error)
	// Root is the directory holding one entry per distro (WSLRoot; tests
	// point it at a temp dir).
	Root string
}

// Discover returns the homes of running distros, the distros that are not
// running, and notes for the log.
func (w *WSL) Discover() (found []Home, skipped []string, notes []string) {
	ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
	defer cancel()
	all, err := w.list(ctx, false)
	if err != nil {
		// No WSL at all (or no distros): nothing to scan, nothing to say
		// beyond one note.
		return nil, nil, []string{"wsl: " + err.Error()}
	}
	if len(all) == 0 {
		return nil, nil, nil
	}
	running, err := w.list(ctx, true)
	if err != nil {
		return nil, nil, []string{"wsl: " + err.Error()}
	}
	for _, d := range all {
		if !slices.Contains(running, d) {
			skipped = append(skipped, d)
		}
	}
	for _, d := range running {
		hs, err := w.homesOf(d)
		if err != nil {
			notes = append(notes, "wsl: "+d+": "+err.Error())
			continue
		}
		found = append(found, hs...)
	}
	return found, skipped, notes
}

func (w *WSL) list(ctx context.Context, runningOnly bool) ([]string, error) {
	if w.List == nil {
		return nil, errors.New("no wsl runner")
	}
	out, err := w.List(ctx, runningOnly)
	if err != nil {
		return nil, err
	}
	return Distros(out), nil
}

// Distros decodes the output of `wsl.exe -l -q`: UTF-16LE (an optional BOM,
// CRLF line ends), one distro name per line. Lines that are not a bare name
// (a "no installed distributions" message, for instance) are dropped, so a
// stray message can never become a path under \\wsl$.
func Distros(out []byte) []string {
	text := decodeUTF16(out)
	var names []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.Trim(line, "\r\t \x00\ufeff")
		if line == "" || strings.ContainsAny(line, " \t\\/:*?\"<>|") {
			continue
		}
		if !slices.Contains(names, line) {
			names = append(names, line)
		}
	}
	return names
}

// decodeUTF16 decodes little-endian UTF-16. Output with no NUL bytes at all
// is taken as UTF-8 (WSL_UTF8=1 makes wsl.exe write that).
func decodeUTF16(b []byte) string {
	if !slices.Contains(b, 0) {
		return string(b)
	}
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
	}
	return string(utf16.Decode(u))
}

// root is the directory holding one entry per distro.
func (w *WSL) root() string {
	if w.Root == "" {
		return WSLRoot
	}
	return w.Root
}

// Roots are the file-system roots of the given distros (<root>\<distro>).
func (w *WSL) Roots(distros []string) []string {
	out := make([]string, 0, len(distros))
	for _, d := range distros {
		out = append(out, filepath.Join(w.root(), d))
	}
	return out
}

// DefaultWSLRoots is Roots under the real \\wsl$.
func DefaultWSLRoots(distros []string) []string { return (&WSL{}).Roots(distros) }

// homesOf enumerates <root>\<distro>\home\* and <root>\<distro>\root and
// keeps the directories that hold at least one tool directory.
func (w *WSL) homesOf(distro string) ([]Home, error) {
	base := filepath.Join(w.root(), distro)
	var cands []string
	if ents, err := os.ReadDir(filepath.Join(base, "home")); err == nil {
		for _, e := range ents {
			if e.IsDir() {
				cands = append(cands, filepath.Join(base, "home", e.Name()))
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	cands = append(cands, filepath.Join(base, "root"))
	sort.Strings(cands)
	var out []Home
	for _, c := range cands {
		if hasToolDir(c) {
			out = append(out, Home{Path: c, Kind: KindWSL, Distro: distro})
		}
	}
	return out, nil
}

func hasToolDir(home string) bool {
	for _, d := range toolDirs {
		if st, err := os.Stat(filepath.Join(home, d)); err == nil && st.IsDir() {
			return true
		}
	}
	return false
}
