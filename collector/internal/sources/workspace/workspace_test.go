package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type vectors struct {
	Normalize    [][2]string `json:"normalize"`
	DecodeSlug   [][2]string `json:"decodeSlug"`
	Generic      [][2]any    `json:"generic"`
	Canonicalize []struct {
		Name   string            `json:"name"`
		Inputs map[string]int    `json:"inputs"`
		Expect map[string]string `json:"expect"`
	} `json:"canonicalize"`
}

// The vectors are shared with api/test/unit/workspaces.test.js.
func load(t *testing.T) vectors {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSharedVectors(t *testing.T) {
	v := load(t)
	for _, c := range v.Normalize {
		if got := Normalize(c[0]); got != c[1] {
			t.Errorf("Normalize(%q) = %q, want %q", c[0], got, c[1])
		}
	}
	for _, c := range v.DecodeSlug {
		if got := DecodeSlug(c[0]); got != c[1] {
			t.Errorf("DecodeSlug(%q) = %q, want %q", c[0], got, c[1])
		}
	}
	for _, c := range v.Generic {
		if got := IsGeneric(c[0].(string)); got != c[1].(bool) {
			t.Errorf("IsGeneric(%q) = %v", c[0], got)
		}
	}
	for _, c := range v.Canonicalize {
		got := Canonicalize(c.Inputs)
		for raw, want := range c.Expect {
			if got[raw] != want {
				t.Errorf("%s: %q -> %q, want %q", c.Name, raw, got[raw], want)
			}
		}
		if len(got) != len(c.Expect) {
			t.Errorf("%s: %d results, want %d", c.Name, len(got), len(c.Expect))
		}
	}
}

func mkdir(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRepoRoot(t *testing.T) {
	base := t.TempDir()
	home := mkdir(t, base, "home")
	repo := mkdir(t, home, "src", "app")
	mkdir(t, repo, ".git")
	sub := mkdir(t, repo, "collector", "ui")
	wt := mkdir(t, home, "src", "worktree")
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wtSub := mkdir(t, wt, "pkg")
	loose := mkdir(t, home, "src", "tmp", "scratch")
	outside := mkdir(t, base, "elsewhere", "proj", "deep")
	mkdir(t, base, "elsewhere", "proj", ".git")

	cases := []struct{ name, cwd, want string }{
		{"repo root itself stays as written", repo, repo},
		{"sub-folder folds to the repo root", sub, repo},
		{"a .git file (worktree) counts", wtSub, wt},
		{"no repository: cwd unchanged", loose, loose},
		{"outside home: searched to the root", outside, filepath.Join(base, "elsewhere", "proj")},
		{"missing path: unchanged", filepath.Join(home, "gone", "x"), filepath.Join(home, "gone", "x")},
		{"relative: unchanged", "some/where", "some/where"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		if got := RepoRoot(home, c.cwd); got != c.want {
			t.Errorf("%s: RepoRoot(%q) = %q, want %q", c.name, c.cwd, got, c.want)
		}
	}

	// A dotfiles repository in home never swallows the projects below it.
	mkdir(t, home, ".git")
	rootCache.Clear()
	if got := RepoRoot(home, loose); got != loose {
		t.Errorf("home .git: got %q, want %q", got, loose)
	}
	if got := RepoRoot(home, home); got != home {
		t.Errorf("cwd = home: got %q", got)
	}
}

func TestRepoRootCacheExpires(t *testing.T) {
	base := t.TempDir()
	repo := mkdir(t, base, "r")
	sub := mkdir(t, repo, "sub")
	clock := time.Unix(1_800_000_000, 0)
	nowFn = func() time.Time { return clock }
	defer func() { nowFn = time.Now }()
	if got := RepoRoot(base, sub); got != sub {
		t.Fatalf("before git init: %q", got)
	}
	mkdir(t, repo, ".git")
	if got := RepoRoot(base, sub); got != sub {
		t.Fatalf("cached answer expected, got %q", got)
	}
	clock = clock.Add(rootTTL + time.Second)
	if got := RepoRoot(base, sub); got != repo {
		t.Fatalf("after TTL: %q, want %q", got, repo)
	}
}

func TestWSLProbe(t *testing.T) {
	old := goos
	goos = "windows"
	defer func() { goos = old }()
	probe, back, ok := probePath(`\\wsl$\Ubuntu\home\dom`, "/home/dom/src/app/sub")
	if !ok || probe != `\\wsl$\Ubuntu\home\dom\src\app\sub` {
		t.Fatalf("probe %q ok=%v", probe, ok)
	}
	if got := back(`\\wsl$\Ubuntu\home\dom\src\app`); got != "/home/dom/src/app" {
		t.Fatalf("back %q", got)
	}
	if _, _, ok := probePath(`C:\Users\me`, "/home/dom/x"); ok {
		t.Fatal("a Linux path from a Windows home cannot be probed")
	}
}
