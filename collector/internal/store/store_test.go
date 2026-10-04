package store

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/fsx"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
)

func TestStateRoundTripAndAtomicWrite(t *testing.T) {
	home := t.TempDir()
	st := NewState()
	st.Cursors["codex"] = map[string]FileCursor{
		"/x/rollout-1.jsonl": {Cursor: sources.Cursor{Size: 10, Offset: 8, HeadHash: "ab", PV: 2, Carry: json.RawMessage(`{"epoch":1}`)}, Idle: true},
	}
	st.Accounts = []Interval{{Provider: "openai", Acct: "a_1", From: time.Unix(100, 0).UTC(), To: time.Unix(200, 0).UTC()}}
	st.Backoff = Backoff{Failures: 2, Limit: 125}
	if err := SaveState(home, st); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(home)
	if err != nil {
		t.Fatal(err)
	}
	c := got.Cursors["codex"]["/x/rollout-1.jsonl"]
	if c.Offset != 8 || c.PV != 2 || !c.Idle || string(c.Carry) != `{"epoch":1}` {
		t.Fatalf("cursor round trip: %+v", c)
	}
	if got.Backoff.Limit != 125 || len(got.Accounts) != 1 {
		t.Fatalf("state round trip: %+v", got)
	}
	ents, _ := os.ReadDir(home)
	if len(ents) != 1 {
		t.Fatalf("expected only state.json, got %d entries", len(ents))
	}
}

// The app and `status` read state.json while a tick saves it: on Windows the
// rename over an open file must still succeed.
func TestSaveStateWhileReaderHoldsIt(t *testing.T) {
	home := t.TempDir()
	if err := SaveState(home, NewState()); err != nil {
		t.Fatal(err)
	}
	f, err := fsx.Open(paths.State(home))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st := NewState()
	st.LastTick = time.Unix(1_800_000_000, 0).UTC()
	if err := SaveState(home, st); err != nil {
		t.Fatalf("save while a reader has state.json open: %v", err)
	}
	got, err := LoadState(home)
	if err != nil || !got.LastTick.Equal(st.LastTick) {
		t.Fatalf("reload: %v %v", got.LastTick, err)
	}
}

func TestCorruptStateIsSetAside(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(paths.State(home), []byte(`{"cursors":`), 0o600)
	st, err := LoadState(home)
	if err == nil || st == nil || st.Cursors == nil {
		t.Fatalf("LoadState = %v, %v", st, err)
	}
	if _, err := os.Stat(paths.State(home) + ".corrupt"); err != nil {
		t.Fatal("corrupt state not moved aside")
	}
}

func TestConfigDefaultsAndOverlay(t *testing.T) {
	home := t.TempDir()
	c, err := LoadConfig(home)
	if err != nil || !c.Prompts || !c.FixConfig || c.Endpoint != DefaultEndpoint || !c.SourceEnabled("codex") || !c.DiscoverWSL || c.ExtraHomes == nil || len(c.ExtraHomes) != 0 {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	os.WriteFile(paths.Config(home), []byte(`{"prompts":false,"sources":{"codex":false},"promptExcludeAccts":["a_x"],"extraHomes":["D:\\other"],"discoverWsl":false}`), 0o600)
	c, err = LoadConfig(home)
	if err != nil || c.Prompts || c.SourceEnabled("codex") || !c.SourceEnabled("grok-cli") || !c.PromptExcluded("a_x") || c.Endpoint != DefaultEndpoint {
		t.Fatalf("overlay: %+v %v", c, err)
	}
	if c.DiscoverWSL || len(c.ExtraHomes) != 1 || c.ExtraHomes[0] != `D:\other` {
		t.Fatalf("homes overlay: %+v", c)
	}
	// A config written before v1.3 keeps WSL discovery on.
	os.WriteFile(paths.Config(home), []byte(`{"prompts":true}`), 0o600)
	if c, _ = LoadConfig(home); !c.DiscoverWSL || c.ExtraHomes == nil {
		t.Fatalf("pre-1.3 config: %+v", c)
	}
	os.WriteFile(paths.Config(home), []byte(`{bad`), 0o600)
	if _, err := LoadConfig(home); err == nil {
		t.Fatal("malformed config accepted")
	}
}

func TestSecrets(t *testing.T) {
	home := t.TempDir()
	s, _ := LoadSecrets(home)
	if s.Enrolled() {
		t.Fatal("empty secrets enrolled")
	}
	k := make([]byte, 32)
	s = Secrets{Token: "tok", K: base64.StdEncoding.EncodeToString(k), MachineID: "m_1"}
	if err := SaveSecrets(home, s); err != nil {
		t.Fatal(err)
	}
	got, _ := LoadSecrets(home)
	if !got.Enrolled() || len(got.Key()) != 32 {
		t.Fatalf("secrets round trip: %+v", got)
	}
	if st, _ := os.Stat(filepath.Join(home, "secrets.json")); st.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("secrets.json mode %v", st.Mode())
	}
}

func TestParseServer(t *testing.T) {
	for in, want := range map[string]string{
		"off": NoServer, " None ": NoServer, "https://d0m1.com/": "https://d0m1.com",
		"http://127.0.0.1:7094": "http://127.0.0.1:7094", "https://x.example/api": "https://x.example/api",
	} {
		if got, err := ParseServer(in); err != nil || got != want {
			t.Errorf("ParseServer(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "d0m1.com", "ftp://x", "https://u:p@x", "https://x/?a=1", "https://"} {
		if got, err := ParseServer(bad); err == nil {
			t.Errorf("ParseServer(%q) = %q, want error", bad, got)
		}
	}
	if (Config{Endpoint: NoServer}).Server() != "" || (Config{Endpoint: "https://a"}).Server() != "https://a" {
		t.Error("Server()")
	}
}
