package app

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/autostart"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

// A d0m1-collector install (old home, old autostart entries, old PATH entry)
// moves to tokenmaxr once: state and enrolment intact, the server kept, the
// update channel switched, the entries swapped; the old home is removed on a
// later start.
func TestMigrateLegacyInstall(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("drives the Windows entries (the macOS agent is covered in internal/autostart)")
	}
	a, _, _ := newTestApp(t)
	legacy := filepath.Join(t.TempDir(), "d0m1-collector")
	k := make([]byte, 32)
	k[0] = 9
	cfg := store.Config{Endpoint: "https://d0m1.com", UpdateURL: legacyUpdateURL, MachineLabel: "STUDIO", Autostart: true, App: true}
	if err := store.SaveConfig(legacy, cfg); err != nil {
		t.Fatal(err)
	}
	sec := store.Secrets{Token: "tok", MachineID: "m_old", K: base64.StdEncoding.EncodeToString(k), GitHub: &store.GitHubSecrets{Token: "ghu_x", Login: "octo"}}
	if err := store.SaveSecrets(legacy, sec); err != nil {
		t.Fatal(err)
	}
	st := &store.State{LastTick: time.Now(), Cursors: map[string]map[string]store.FileCursor{"claude-code": {"f": {Offset: 42}}}}
	if err := store.SaveState(legacy, st); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"outbox/0001.json", "rollup.json", "bin/d0m1-collector.exe", "bin/d0m1-collectorw.exe", "app.lock"} {
		p := filepath.Join(legacy, filepath.FromSlash(f))
		os.MkdirAll(filepath.Dir(p), 0o700)
		os.WriteFile(p, []byte("x "+f), 0o600)
	}
	run, tasks := fakeRun{}, &fakeTasks{tasks: map[string]autostart.Options{}}
	run[autostart.LegacyRunValueName] = `"` + legacy + `\bin\d0m1-collectorw.exe" app`
	tasks.tasks[autostart.LegacyTaskName] = autostart.Options{Name: autostart.LegacyTaskName, App: true}
	a.Autostart = &autostart.System{GOOS: "windows", Run: run, Tasks: tasks, Spawn: func(string, []string) error { return nil }}
	path := a.UserPath.(*fakePath)
	path.dirs = []string{paths.Bin(legacy)}

	res, err := a.Migrate(legacy)
	if err != nil || !res.Moved || res.Handover {
		t.Fatalf("migrate: %+v %v", res, err)
	}
	// State and enrolment moved, the server kept, updates from tokenmaxr.
	s2, _ := store.LoadSecrets(a.Home)
	if s2.Token != "tok" || s2.MachineID != "m_old" || s2.K != sec.K || s2.GitHub == nil || s2.GitHub.Login != "octo" {
		t.Fatalf("secrets %+v", s2)
	}
	c2, _ := store.LoadConfig(a.Home)
	if c2.Server() != "https://d0m1.com" || c2.UpdateURL != store.DefaultUpdateURL || c2.MachineLabel != "STUDIO" {
		t.Fatalf("config %+v", c2)
	}
	if st2, _ := store.LoadState(a.Home); st2.Cursors["claude-code"]["f"].Offset != 42 {
		t.Fatal("cursors not moved")
	}
	for _, f := range []string{"outbox/0001.json", "rollup.json"} {
		if !fileExists(filepath.Join(a.Home, filepath.FromSlash(f))) {
			t.Fatalf("%s not moved", f)
		}
	}
	for _, f := range []string{"app.lock", "bin/d0m1-collector.exe"} {
		if fileExists(filepath.Join(a.Home, filepath.FromSlash(f))) {
			t.Fatalf("%s must not move", f)
		}
	}
	if !fileExists(a.binPath(false)) {
		t.Fatal("tokenmaxr binary not installed")
	}
	// Entries swapped.
	if _, ok := run[autostart.LegacyRunValueName]; ok {
		t.Fatal("old Run value left")
	}
	if _, ok := tasks.tasks[autostart.LegacyTaskName]; ok {
		t.Fatal("old task left")
	}
	if v := run[autostart.RunValueName]; !strings.Contains(v, paths.ExeName(true)) {
		t.Fatalf("new Run value %q", v)
	}
	if len(path.dirs) != 1 || path.dirs[0] != paths.Bin(a.Home) {
		t.Fatalf("PATH %v", path.dirs)
	}
	if !fileExists(filepath.Join(legacy, migratedMark)) {
		t.Fatal("old home not marked")
	}

	// Again: nothing to do; a later start removes the old home.
	res, err = a.Migrate(legacy)
	if err != nil || res.Moved || !res.Cleaned || fileExists(legacy) {
		t.Fatalf("cleanup: %+v %v", res, err)
	}
}

// tokenmaxr set up on its own is never overwritten by an old install.
func TestMigrateKeepsAnExistingTokenmaxr(t *testing.T) {
	a, _, _ := newTestApp(t)
	localKey(t, a)
	legacy := filepath.Join(t.TempDir(), "d0m1-collector")
	store.SaveSecrets(legacy, store.Secrets{Token: "tok", MachineID: "m_old", K: base64.StdEncoding.EncodeToString(make([]byte, 32))})
	before, _ := store.LoadSecrets(a.Home)
	if res, err := a.Migrate(legacy); err != nil || res.Moved {
		t.Fatalf("%+v %v", res, err)
	}
	if after, _ := store.LoadSecrets(a.Home); after.K != before.K || after.Token != "" {
		t.Fatal("an existing tokenmaxr home was overwritten")
	}
	if res, _ := a.Migrate(""); res.Moved || res.Cleaned {
		t.Fatal("an explicit --home never migrates")
	}
}

// The new home's log may already be open (on Windows an open file cannot be
// replaced): the move still completes.
func TestMigrateWithTheNewLogOpen(t *testing.T) {
	a, _, _ := newTestApp(t)
	legacy := filepath.Join(t.TempDir(), "d0m1-collector")
	k := make([]byte, 32)
	store.SaveConfig(legacy, store.Config{Endpoint: "https://d0m1.com"})
	store.SaveSecrets(legacy, store.Secrets{Token: "tok", MachineID: "m_old", K: base64.StdEncoding.EncodeToString(k)})
	os.WriteFile(filepath.Join(legacy, "collector.log"), []byte("old log\n"), 0o600)
	os.MkdirAll(a.Home, 0o700)
	f, err := os.OpenFile(filepath.Join(a.Home, "collector.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	res, err := a.Migrate(legacy)
	if err != nil || !res.Moved {
		t.Fatalf("migrate with an open log: %+v %v", res, err)
	}
	if s, _ := store.LoadSecrets(a.Home); s.MachineID != "m_old" {
		t.Fatal("secrets not moved")
	}
}

// The old channel is d0m1.com's manifest: a config naming it updates from
// tokenmaxr's releases (a literal on purpose: renames must not touch it).
func TestLegacyUpdateURL(t *testing.T) {
	if legacyUpdateURL != "https://d0m1.com/collector/latest.json" {
		t.Fatalf("legacyUpdateURL %q", legacyUpdateURL)
	}
}

// Leftovers of a move (no state, no mark) are removed once tokenmaxr runs.
func TestMigrateRemovesLeftovers(t *testing.T) {
	a, _, _ := newTestApp(t)
	localKey(t, a)
	legacy := filepath.Join(t.TempDir(), "d0m1-collector")
	os.MkdirAll(filepath.Join(legacy, "bin"), 0o700)
	os.WriteFile(filepath.Join(legacy, "bin", "d0m1-collector.exe"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(legacy, "collector.lock"), nil, 0o600)
	if res, err := a.Migrate(legacy); err != nil || !res.Cleaned || fileExists(legacy) {
		t.Fatalf("leftovers: %+v %v", res, err)
	}
}
