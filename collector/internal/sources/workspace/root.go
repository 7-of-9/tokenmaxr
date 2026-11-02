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

// worktreeSeg is Claude Code's own worktree folder inside a repository
// (`isolation: worktree`, EnterWorktree): <repo>/.claude/worktrees/<agent>.
var worktreeSeg = regexp.MustCompile(`(?i)[\\/]\.claude[\\/]worktrees[\\/]`)

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
// for worktrees and submodules), else cwd unchanged. A git worktree is its
// main repository's (owner direction 2026-11-02: agents working in
// <repo>/.claude/worktrees/<agent> spend the repository's tokens, not a
// project's each): a .git file whose gitdir is under <main>/.git/worktrees/
// resolves to <main>, and a folder below .claude/worktrees/ resolves to the
// repository above it even once git has forgotten the worktree. A submodule
// stays its own workspace. The search never accepts home itself, or anything
// above it, for a cwd below home: a dotfiles repository in the home folder
// does not swallow every project. cwd is kept as the tool wrote it; only its
// trailing elements are dropped. A Linux path from a WSL home
// (\\wsl$\<distro>\home\…) is probed through that distro's share and
// returned in Linux form.
func RepoRoot(home, cwd string) string {
	if strings.TrimSpace(cwd) == "" {
		return cwd
	}
	// Claude's worktree folder: the repository is the part before it, as
	// the tool wrote it, so it hashes like the repository's own prompts.
	if m := worktreeSeg.FindStringIndex(cwd); m != nil && m[0] > 0 {
		cwd = cwd[:m[0]]
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
		if fi, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			if !fi.IsDir() {
				if main := worktreeMain(dir); main != "" {
					return back(main)
				}
			}
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

// worktreeMain is the main repository of a worktree at dir, read from its
// .git file ("gitdir: <main>/.git/worktrees/<name>"), or "" when dir is not
// a worktree of another repository (a submodule, an unreadable file).
func worktreeMain(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, ".git"))
	if err != nil || len(b) > 4096 {
		return ""
	}
	line := strings.TrimSpace(string(b))
	gitdir, ok := strings.CutPrefix(line, "gitdir:")
	if !ok {
		return ""
	}
	gitdir = strings.TrimSpace(strings.SplitN(gitdir, "\n", 2)[0])
	if gitdir == "" {
		return ""
	}
	if !filepath.IsAbs(gitdir) && !strings.HasPrefix(gitdir, "/") {
		gitdir = filepath.Join(dir, gitdir)
	}
	slash := filepath.ToSlash(gitdir)
	i := strings.LastIndex(strings.ToLower(slash), "/.git/worktrees/")
	if i <= 0 {
		return ""
	}
	main := filepath.FromSlash(slash[:i])
	if fi, err := os.Stat(filepath.Join(main, ".git")); err != nil || !fi.IsDir() {
		return ""
	}
	return filepath.Clean(main)
}
