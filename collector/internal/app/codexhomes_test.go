package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/homes"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/rollup"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/codex"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

// codexFixture copies the Codex parser's fixture directory (three rollouts
// and a history.jsonl naming two sessions without one) to dir.
func codexFixture(t *testing.T, dir string) {
	t.Helper()
	src := filepath.Join("..", "sources", "codex", "testdata", "home", ".codex")
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dir, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A CODEX_HOME set only where the collector does not inherit it (a login
// shell profile, launchd, the registry) is found, read by the Codex parser
// alone, cached between ticks, and its missing logs are reported by status
// and scan --dry-run.
func TestCodexHomeOutsideTheProcessIsRead(t *testing.T) {
	f, _ := useFakeGitHub(t)
	a, logPath, out := newTestApp(t)
	githubOnly(t, a, f)
	work := filepath.Join(t.TempDir(), "codex-work")
	codexFixture(t, work)
	lookups := 0
	a.CodexLookups = []homes.CodexLookup{
		{Origin: "login shell", Lookup: func(context.Context) (string, error) { lookups++; return work, nil }},
		{Origin: "launchd", Lookup: func(context.Context) (string, error) { return "", nil }},
	}
	a.Sources = func() []sources.Source { return []sources.Source{&synthSource{path: logPath}, codex.New()} }

	tick(t, a, TickOptions{})
	st, _ := store.LoadState(a.Home)
	var kinds []string
	for _, h := range st.Homes {
		kinds = append(kinds, h.Kind+"="+h.Path)
	}
	if strings.Join(kinds, "|") != "os="+a.UserHome+"|codex="+work {
		t.Fatalf("homes %q", kinds)
	}
	ru, err := rollup.Load(paths.Rollup(a.Home))
	if err != nil {
		t.Fatal(err)
	}
	var codexTokens, claudeRows int64
	for _, r := range ru.Rows() {
		switch r.Source {
		case "codex":
			codexTokens += r.Tokens()
		case "claude-code":
			claudeRows++
		}
	}
	if codexTokens == 0 || claudeRows == 0 {
		t.Fatalf("codex tokens %d, claude rows %d", codexTokens, claudeRows)
	}
	if _, err := os.Stat(filepath.Join(work, ".claude")); !os.IsNotExist(err) {
		t.Fatal("a Codex directory got Claude settings: only Codex reads and checks it")
	}
	if st.Checks["codexHistory[codex]"] != "ok" || st.Checks["claudeRetention[codex]"] != "" {
		t.Fatalf("checks %v", st.Checks)
	}

	// Cached: the next tick does not start the login shell again.
	tick(t, a, TickOptions{})
	if lookups != 1 {
		t.Fatalf("%d lookups, want 1 (cached in codex-env.json)", lookups)
	}
	a.Now = func() time.Time { return time.Now().Add(codexEnvEvery) }
	tick(t, a, TickOptions{})
	if lookups != 2 {
		t.Fatalf("%d lookups, want 2 once the cache is old", lookups)
	}

	// Status and the dry run report the sessions history.jsonl lists with no log.
	out.Reset()
	if err := a.Status(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "2 Codex sessions have no log on this machine (deleted or made elsewhere)") {
		t.Fatalf("status:\n%s", out)
	}
	out.Reset()
	if err := a.ScanDryRun(true, "", ""); err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Homes        []string       `json:"homes"`
		CodexMissing *codex.Missing `json:"codexMissing"`
	}
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if rep.CodexMissing == nil || rep.CodexMissing.Sessions != 2 || len(rep.Homes) != 2 {
		t.Fatalf("dry run %+v\n%s", rep, out)
	}
	if lookups != 3 {
		t.Fatalf("a dry run looks live: %d lookups", lookups)
	}
	out.Reset()
	if err := a.ScanDryRun(false, "", ""); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("codex logs: 2 Codex sessions have no log")) {
		t.Fatalf("dry run text:\n%s", out)
	}
}

// Status reads the last tick's homes without discovering again, but never a
// WSL home: that distro may have stopped since, and opening its \wsl$ path
// would start it. A history copied to a CODEX_HOME whose rollouts stayed in
// ~/.codex is not reported as missing.
func TestStatusCodexLogsSkipWSLAndJoinCodexDirs(t *testing.T) {
	a, _, _ := newTestApp(t)
	work := filepath.Join(t.TempDir(), "codex-work")
	os.MkdirAll(work, 0o755)
	codexFixture(t, filepath.Join(a.UserHome, ".codex"))
	b, err := os.ReadFile(filepath.Join(a.UserHome, ".codex", "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "history.jsonl"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	wsl := filepath.Join(t.TempDir(), "wsl-home")
	codexFixture(t, filepath.Join(wsl, ".codex"))
	st := &store.State{Homes: []store.HomeState{
		{Path: a.UserHome, Kind: homes.KindOS},
		{Path: work, Kind: homes.KindCodex},
		{Path: wsl, Kind: homes.KindWSL, Distro: "Ubuntu"},
	}}
	hs := a.lastHomes(st)
	for _, h := range hs {
		if h.Kind == homes.KindWSL {
			t.Fatalf("status would open the WSL home %s", h.Path)
		}
	}
	got := a.codexMissing(hs)
	want := a.codexMissing([]homes.Home{{Path: a.UserHome, Kind: homes.KindOS}})
	if want.Sessions != 2 || got != want {
		t.Fatalf("missing %+v, want %+v (the copied history's rollouts are in ~/.codex)", got, want)
	}
}
