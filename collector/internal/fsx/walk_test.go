package fsx

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// dirLink makes link a link to the directory target: a symbolic link, or on
// Windows without the symlink privilege a junction (what mklink /J makes,
// and what people use there to move a folder to another disk).
func dirLink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err == nil {
		return
	}
	if runtime.GOOS == "windows" {
		if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err == nil {
			return
		} else {
			t.Fatalf("junction: %v %s", err, out)
		}
	}
	t.Fatalf("cannot link %s", link)
}

func write(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// files lists the regular files WalkFollow reports, relative to base, with
// forward slashes.
func files(t *testing.T, base string, roots ...string) []string {
	t.Helper()
	var out []string
	err := WalkFollow(roots, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
			return nil
		}
		rel, _ := filepath.Rel(base, p)
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

func TestWalkFollowLinkedRootAndSubfolders(t *testing.T) {
	base := t.TempDir()
	// sessions/ moved to another disk and linked back, holding a linked year.
	write(t, filepath.Join(base, "elsewhere", "sessions", "2026", "a.jsonl"))
	write(t, filepath.Join(base, "older", "b.jsonl"))
	codex := filepath.Join(base, "codex")
	os.MkdirAll(codex, 0o755)
	dirLink(t, filepath.Join(base, "elsewhere", "sessions"), filepath.Join(codex, "sessions"))
	dirLink(t, filepath.Join(base, "older"), filepath.Join(base, "elsewhere", "sessions", "2025"))
	got := files(t, base, filepath.Join(codex, "sessions"), filepath.Join(codex, "archived_sessions"))
	want := []string{"codex/sessions/2025/b.jsonl", "codex/sessions/2026/a.jsonl"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestWalkFollowEndsLoopsAndReadsAFolderOnce(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "sessions")
	write(t, filepath.Join(root, "2026", "a.jsonl"))
	write(t, filepath.Join(base, "shared", "s.jsonl"))
	// A loop back to the root, and one to its own folder.
	dirLink(t, root, filepath.Join(root, "2026", "up"))
	dirLink(t, filepath.Join(root, "2026"), filepath.Join(root, "2026", "self"))
	// One folder linked from both roots, and the archive linked to the sessions.
	archived := filepath.Join(base, "archived_sessions")
	os.MkdirAll(archived, 0o755)
	dirLink(t, filepath.Join(base, "shared"), filepath.Join(root, "shared"))
	dirLink(t, filepath.Join(base, "shared"), filepath.Join(archived, "again"))
	dirLink(t, root, filepath.Join(archived, "all"))
	got := files(t, base, root, archived)
	want := []string{"sessions/2026/a.jsonl", "sessions/shared/s.jsonl"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	// The archive linked as a root of its own is the same folder: read once.
	if got := files(t, base, root, root); !slices.Equal(got, want) {
		t.Fatalf("the same root twice: %v", got)
	}
}

// A root that is a subfolder of a later root is read once, under the first
// root, in either order.
func TestWalkFollowRootInsideALaterRoot(t *testing.T) {
	base := t.TempDir()
	write(t, filepath.Join(base, "real", "x", "sub", "f.jsonl"))
	codex := filepath.Join(base, "codex")
	os.MkdirAll(codex, 0o755)
	dirLink(t, filepath.Join(base, "real", "x", "sub"), filepath.Join(codex, "sessions"))
	dirLink(t, filepath.Join(base, "real", "x"), filepath.Join(codex, "archived_sessions"))
	sessions, archived := filepath.Join(codex, "sessions"), filepath.Join(codex, "archived_sessions")
	if got := files(t, base, sessions, archived); !slices.Equal(got, []string{"codex/sessions/f.jsonl"}) {
		t.Fatalf("sessions first: %v", got)
	}
	if got := files(t, base, archived, sessions); !slices.Equal(got, []string{"codex/archived_sessions/sub/f.jsonl"}) {
		t.Fatalf("archive first: %v", got)
	}
}

func TestWalkFollowMissingAndBroken(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "sessions")
	write(t, filepath.Join(root, "a.jsonl"))
	gone := filepath.Join(base, "gone")
	os.MkdirAll(gone, 0o755)
	dirLink(t, gone, filepath.Join(root, "broken"))
	os.Remove(gone)
	var rootErr error
	var seen []string
	WalkFollow([]string{filepath.Join(base, "missing"), root}, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			rootErr = err
			return nil
		}
		seen = append(seen, filepath.Base(p))
		return nil
	})
	if rootErr == nil || !os.IsNotExist(rootErr) {
		t.Fatalf("a missing root reports its error: %v", rootErr)
	}
	if !slices.Contains(seen, "a.jsonl") || !slices.Contains(seen, "broken") {
		t.Fatalf("seen %v: the broken link is reported, not followed", seen)
	}
	// SkipAll stops at once.
	n := 0
	WalkFollow([]string{root}, func(string, fs.DirEntry, error) error { n++; return fs.SkipAll })
	if n != 1 {
		t.Fatalf("SkipAll: %d calls", n)
	}
}
