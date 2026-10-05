package homes

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func lookup(origin, value string, err error) CodexLookup {
	return CodexLookup{Origin: origin, Lookup: func(context.Context) (string, error) { return value, err }}
}

// CODEX_HOME values found elsewhere become absolute directories: ~ is the
// user home, quotes and blanks go, relative values and repeats are dropped,
// and a failing lookup is a note, not an error.
func TestCodexEnvDirs(t *testing.T) {
	user := t.TempDir()
	abs := filepath.Join(t.TempDir(), "codex-work")
	dirs, notes := CodexEnvDirs(context.Background(), user, []CodexLookup{
		lookup("login shell", "  ~/.codex-work\n", nil),
		lookup("launchd", `"`+abs+`"`, nil),
		lookup("user environment", "", nil),
		lookup("machine environment", "relative/codex", nil),
		lookup("again", abs, nil),
		lookup("broken", "", errors.New("exit status 1")),
	})
	want := []string{filepath.Join(user, ".codex-work"), abs}
	if strings.Join(dirs, "|") != strings.Join(want, "|") {
		t.Fatalf("dirs %q, want %q", dirs, want)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "CODEX_HOME from broken") {
		t.Fatalf("notes %q", notes)
	}
}

// A lookup that hangs is cut off by its timeout and the others still run.
func TestCodexEnvDirsTimeout(t *testing.T) {
	hung := CodexLookup{Origin: "login shell", Lookup: func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // stands in for the timeout: the lookup sees a done context
	dir := t.TempDir()
	dirs, notes := CodexEnvDirs(ctx, t.TempDir(), []CodexLookup{hung, lookup("launchd", dir, nil)})
	if len(dirs) != 1 || dirs[0] != dir || len(notes) != 1 {
		t.Fatalf("dirs %q notes %q", dirs, notes)
	}
}

// Codex directories become KindCodex homes only when they exist and no home
// reads them already: the OS home's (its $CODEX_HOME or ~/.codex), an extra
// home's .codex, or the same directory through a link.
func TestDiscoverCodexHomes(t *testing.T) {
	user := t.TempDir()
	extra := t.TempDir()
	for _, d := range []string{filepath.Join(user, ".codex"), filepath.Join(extra, ".codex")} {
		os.MkdirAll(d, 0o755)
	}
	work := filepath.Join(t.TempDir(), "codex-work")
	os.MkdirAll(work, 0o755)
	other := filepath.Join(t.TempDir(), "codex-other")
	os.MkdirAll(other, 0o755)
	linked := filepath.Join(t.TempDir(), "codex-link")
	linkDir(t, other, linked)

	// The process saw CODEX_HOME=work: ~/.codex is read as its own Codex home.
	r := Discover(Options{UserHome: user, Extra: []string{extra}, OSCodexHome: work,
		CodexDirs: []string{work, filepath.Join(user, ".codex"), filepath.Join(extra, ".codex"), filepath.Join(user, "missing"), other, linked}})
	var got []string
	for _, h := range r.Homes {
		got = append(got, h.Label()+"="+h.Path)
	}
	want := []string{"os=" + user, "extra=" + extra, "codex=" + filepath.Join(user, ".codex"), "codex=" + other}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("homes\n%q\nwant\n%q", got, want)
	}
	h := r.Homes[2]
	if h.CodexHome(work) != filepath.Join(user, ".codex") || !h.CodexOnly() || r.Homes[0].CodexOnly() || h.Key() != h.Path {
		t.Fatalf("codex home %+v reads %s", h, h.CodexHome(work))
	}
	// Without a CODEX_HOME anywhere, ~/.codex is the OS home's: nothing more.
	r = Discover(Options{UserHome: user, OSCodexHome: filepath.Join(user, ".codex"), CodexDirs: []string{filepath.Join(user, ".codex")}})
	if len(r.Homes) != 1 {
		t.Fatalf("homes %+v", r.Homes)
	}
}

// linkDir links link to the directory target: a symbolic link, or on
// Windows without the symlink privilege a junction.
func linkDir(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err == nil {
		return
	}
	if runtime.GOOS == "windows" {
		if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
			t.Fatalf("junction: %v %s", err, out)
		}
		return
	}
	t.Fatalf("cannot link %s", link)
}
