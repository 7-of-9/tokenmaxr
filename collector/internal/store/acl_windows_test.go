//go:build windows

package store

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
)

// assertUserOnly reads the DACL back through GetNamedSecurityInfo and fails
// unless it is protected and every entry is an allow ACE for the current user.
func assertUserOnly(t *testing.T, path string) {
	t.Helper()
	me, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	protected, entries, err := readDACL(path)
	if err != nil {
		t.Fatal(err)
	}
	if !protected {
		t.Fatalf("%s: DACL still inherits from the directory", filepath.Base(path))
	}
	if len(entries) == 0 {
		t.Fatalf("%s: empty or NULL DACL", filepath.Base(path))
	}
	for _, e := range entries {
		if e.Inherited {
			t.Fatalf("%s: inherited ACE for %s", filepath.Base(path), e.SID)
		}
		if !e.Allow {
			t.Fatalf("%s: deny ACE for %s", filepath.Base(path), e.SID)
		}
		if !e.SID.Equals(me) {
			t.Fatalf("%s: ACE for another principal %s", filepath.Base(path), e.SID)
		}
	}
	if ok, err := userOnlyACL(path); err != nil || !ok {
		t.Fatalf("userOnlyACL = %v, %v", ok, err)
	}
}

func TestSecretsDACLIsUserOnly(t *testing.T) {
	// A home with spaces, like a profile of a user whose name has spaces:
	// the DACL work is SID-based and must not care about the path or name.
	home := filepath.Join(t.TempDir(), "d0m1 collector home")
	s := Secrets{Token: "tok", K: base64.StdEncoding.EncodeToString(make([]byte, 32)), MachineID: "m_1"}
	if err := SaveSecrets(home, s); err != nil {
		t.Fatal(err)
	}
	p := paths.Secrets(home)
	assertUserOnly(t, p)

	// The staged file was renamed into place, nothing was left behind, and
	// the content survived the extra hop.
	ents, _ := os.ReadDir(home)
	if len(ents) != 1 || ents[0].Name() != filepath.Base(p) {
		names := make([]string, 0, len(ents))
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Fatalf("expected only secrets.json, got %v", names)
	}
	got, err := LoadSecrets(home)
	if err != nil || !got.Enrolled() {
		t.Fatalf("round trip: %+v %v", got, err)
	}

	// Overwriting keeps the file user-only (the new staged file starts out
	// with the inherited ACL again).
	s.Token = "tok2"
	if err := SaveSecrets(home, s); err != nil {
		t.Fatal(err)
	}
	assertUserOnly(t, p)
}

func TestLoadSecretsRepairsDACL(t *testing.T) {
	home := t.TempDir()
	s := Secrets{Token: "tok", K: base64.StdEncoding.EncodeToString(make([]byte, 32)), MachineID: "m_1"}
	if err := SaveSecrets(home, s); err != nil {
		t.Fatal(err)
	}
	p := paths.Secrets(home)

	// Simulate a file from an older build: inheritance back on plus an
	// explicit read grant to Everyone.
	sd, err := windows.SecurityDescriptorFromString("D:(A;;FR;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	err = windows.SetNamedSecurityInfo(p, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := userOnlyACL(p); err != nil || ok {
		t.Fatalf("loosened DACL still reported user-only: %v %v", ok, err)
	}

	got, err := LoadSecrets(home)
	if err != nil || got.Token != "tok" {
		t.Fatalf("LoadSecrets: %+v %v", got, err)
	}
	assertUserOnly(t, p)
}
