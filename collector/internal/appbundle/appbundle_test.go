package appbundle

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEnsureWritesRepairsAndRemovesTheBundle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("macOS bundles: Windows has no exec bit to write or check")
	}
	apps := t.TempDir()
	exe := "/Users/someone/Library/Application Support/tokenmaxr/bin/tokenmaxr"
	path, changed, err := Ensure([]string{apps}, exe, "0.4.2")
	if err != nil || !changed || path != filepath.Join(apps, "tokenmaxr.app") {
		t.Fatalf("ensure: %q changed %v err %v", path, changed, err)
	}
	launcher := filepath.Join(path, "Contents", "MacOS", "tokenmaxr")
	b, _ := os.ReadFile(launcher)
	if !strings.HasPrefix(string(b), "#!/bin/sh\n") || !strings.HasSuffix(string(b), "exec '"+exe+"' app\n") {
		t.Fatalf("launcher:\n%s", b)
	}
	if st, _ := os.Stat(launcher); st.Mode().Perm() != 0o755 {
		t.Fatalf("launcher mode %v", st.Mode())
	}
	plist, _ := os.ReadFile(filepath.Join(path, "Contents", "Info.plist"))
	for _, want := range []string{
		"<string>com.tokenmaxr.collector</string>", "<key>CFBundleExecutable</key>\n  <string>tokenmaxr</string>",
		"<key>LSUIElement</key>\n  <true/>", "<string>0.4.2</string>",
	} {
		if !bytes.Contains(plist, []byte(want)) {
			t.Fatalf("Info.plist lacks %q:\n%s", want, plist)
		}
	}
	if Find([]string{t.TempDir(), apps}) != path {
		t.Fatal("Find missed the bundle")
	}

	// Unchanged, nothing is written; a new version or a damaged launcher is repaired.
	if _, changed, err := Ensure([]string{apps}, exe, "0.4.2"); err != nil || changed {
		t.Fatalf("second ensure changed %v err %v", changed, err)
	}
	os.Chmod(launcher, 0o644)
	if _, changed, _ := Ensure([]string{apps}, exe, "0.4.3"); !changed {
		t.Fatal("version and mode not repaired")
	}
	if st, _ := os.Stat(launcher); st.Mode().Perm() != 0o755 {
		t.Fatalf("launcher mode %v", st.Mode())
	}

	// A bundle stays where it is, even when another folder comes first.
	first := t.TempDir()
	if p, _, _ := Ensure([]string{first, apps}, exe, "0.4.3"); p != path {
		t.Fatalf("moved to %q", p)
	}
	removed, err := Remove([]string{first, apps})
	if err != nil || len(removed) != 1 || removed[0] != path {
		t.Fatalf("removed %v err %v", removed, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("bundle left behind")
	}
}

func TestSomeoneElsesBundleIsLeftAlone(t *testing.T) {
	apps := t.TempDir()
	other := filepath.Join(apps, Name, "Contents")
	os.MkdirAll(other, 0o755)
	os.WriteFile(filepath.Join(other, "Info.plist"), []byte("<plist><dict><key>CFBundleIdentifier</key><string>com.example.other</string></dict></plist>"), 0o644)
	if _, _, err := Ensure([]string{apps}, "/x/tokenmaxr", "1"); err == nil {
		t.Fatal("overwrote another app's bundle")
	}
	if removed, err := Remove([]string{apps}); err != nil || len(removed) != 0 {
		t.Fatalf("removed %v err %v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(other, "Info.plist")); err != nil {
		t.Fatal("another app's bundle was deleted")
	}
}

func TestLauncherQuotesThePath(t *testing.T) {
	got := Launcher(`/Users/o'neil/Library/Application Support/tokenmaxr/bin/tokenmaxr`)
	if want := `exec '/Users/o'\''neil/Library/Application Support/tokenmaxr/bin/tokenmaxr' app`; !strings.Contains(got, want) {
		t.Fatalf("launcher:\n%s", got)
	}
}

func TestICNSHoldsAPNGForEverySize(t *testing.T) {
	b := ICNS()
	if string(b[:4]) != "icns" || int(binary.BigEndian.Uint32(b[4:8])) != len(b) {
		t.Fatalf("header %q length %d of %d", b[:4], binary.BigEndian.Uint32(b[4:8]), len(b))
	}
	var kinds []string
	for i := 8; i < len(b); {
		kind, n := string(b[i:i+4]), int(binary.BigEndian.Uint32(b[i+4:i+8]))
		if n < 8 || i+n > len(b) || !bytes.HasPrefix(b[i+8:i+n], []byte("\x89PNG\r\n\x1a\n")) {
			t.Fatalf("entry %q at %d: bad length %d or not a PNG", kind, i, n)
		}
		kinds = append(kinds, kind)
		i += n
	}
	if strings.Join(kinds, " ") != "icp4 icp5 ic11 ic12 ic07 ic13 ic08 ic14 ic09 ic10" {
		t.Fatalf("entries %v", kinds)
	}
}
