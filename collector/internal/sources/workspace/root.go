package workspace

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// rootTTL bounds how long a looked-up repository root is reused, so a
// repository created while the collector runs is picked up without a
// restart. Prompt ids never depend on the workspace, so a later answer only
// affects records not yet sent.
const rootTTL = 10 * time.Minute

type rootEntry struct {
	root string
	at   time.Time
}

var (
	rootCache sync.Map // home + "\x00" + cwd -> rootEntry
	nowFn     = time.Now
	goos      = runtime.GOOS
	wslHomeRe = regexp.MustCompile(`(?i)^([\\/]{2}(?:wsl\$|wsl\.localhost)[\\/][^\\/]+)`)
)

// RepoRoot is the workspace a new prompt record carries: the nearest
// ancestor of cwd (cwd included) that contains .git (a directory, or a file
// for worktrees and submodules), else cwd unchanged. The search never
// accepts home itself, or anything above it, for a cwd below home: a
// dotfiles repository in the home folder does not swallow every project.
// cwd is kept as the tool wrote it; only its trailing elements are dropped.
// A Linux path from a WSL home (\\wsl$\<distro>\home\…) is probed through
// that distro's share and returned in Linux form.
func RepoRoot(home, cwd string) string {
	if strings.TrimSpace(cwd) == "" {
		return cwd
	}
	key := home + "\x00" + cwd
	if v, ok := rootCache.Load(key); ok {
		e := v.(rootEntry)
		if nowFn().Sub(e.at) < rootTTL {
			return e.root
		}
	}
	root := findRoot(home, cwd)
	rootCache.Store(key, rootEntry{root: root, at: nowFn()})
	return root
}

// probePath maps cwd to a path this machine can stat, and back.
func probePath(home, cwd string) (probe string, back func(string) string, ok bool) {
	if goos == "windows" && strings.HasPrefix(cwd, "/") {
		m := wslHomeRe.FindStringSubmatch(home)
		if m == nil {
			return "", nil, false
		}
		share := m[1]
		probe = share + strings.ReplaceAll(cwd, "/", `\`)
		back = func(p string) string {
			rest := strings.ReplaceAll(p[len(share):], `\`, "/")
			if rest == "" {
				rest = "/"
			}
			return rest
		}
		return filepath.Clean(probe), back, true
	}
	if !filepath.IsAbs(cwd) {
		return "", nil, false
	}
	return filepath.Clean(cwd), func(p string) string { return p }, true
}

func samePath(a, b string) bool {
	if goos == "windows" || goos == "darwin" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// under reports whether p is strictly below dir.
func under(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil || rel == "." {
		return false
	}
	if goos == "windows" || goos == "darwin" {
		if r2, err := filepath.Rel(strings.ToLower(dir), strings.ToLower(p)); err == nil {
			rel = r2
		}
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func findRoot(home, cwd string) string {
	probe, back, ok := probePath(home, cwd)
	if !ok {
		return cwd
	}
	homeProbe := ""
	if home != "" {
		homeProbe = filepath.Clean(home)
	}
	belowHome := homeProbe != "" && under(probe, homeProbe)
	for dir := probe; ; {
		if belowHome && samePath(dir, homeProbe) {
			return cwd
		}
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			if dir == probe {
				return cwd
			}
			return back(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return cwd
		}
		dir = parent
	}
}
