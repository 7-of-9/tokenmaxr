//go:build windows

package homes

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// The registry lookup reads CODEX_HOME as the user or machine environment
// holds it, expanding a REG_EXPAND_SZ value. A throwaway key under
// HKCU\Software stands in for HKCU\Environment, so the real one is never
// touched.
func TestRegistryEnv(t *testing.T) {
	b := make([]byte, 6)
	rand.Read(b)
	path := `Software\tokenmaxr-test-` + hex.EncodeToString(b)
	k, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { registry.DeleteKey(registry.CURRENT_USER, path) })

	if v, err := registryEnv(registry.CURRENT_USER, path); v != "" || err != nil {
		t.Fatalf("unset: %q %v", v, err)
	}
	if v, err := registryEnv(registry.CURRENT_USER, path+`\missing`); v != "" || err != nil {
		t.Fatalf("missing key: %q %v", v, err)
	}
	if err := k.SetExpandStringValue("CODEX_HOME", `%USERPROFILE%\codex-work`); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(os.Getenv("USERPROFILE"), "codex-work")
	if v, err := registryEnv(registry.CURRENT_USER, path); v != want || err != nil {
		t.Fatalf("expand: %q %v, want %q", v, err, want)
	}
	if err := k.SetStringValue("CODEX_HOME", `D:\codex`); err != nil {
		t.Fatal(err)
	}
	if v, _ := registryEnv(registry.CURRENT_USER, path); v != `D:\codex` {
		t.Fatalf("plain: %q", v)
	}
	k.Close()
	if n := len(DefaultCodexLookups()); n != 2 {
		t.Fatalf("%d lookups, want the user and the machine environment", n)
	}
}
