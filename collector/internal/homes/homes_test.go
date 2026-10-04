package homes

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"
)

// utf16le encodes s the way wsl.exe writes its output: little-endian, CRLF
// line ends, optionally with a BOM.
func utf16le(s string, bom bool) []byte {
	var out []byte
	put := func(u uint16) { out = binary.LittleEndian.AppendUint16(out, u) }
	if bom {
		put(0xfeff)
	}
	for _, u := range utf16.Encode([]rune(strings.ReplaceAll(s, "\n", "\r\n"))) {
		put(u)
	}
	return out
}

func TestDistros(t *testing.T) {
	// Captured on this machine: `wsl.exe -l -q` with two distros, no BOM.
	captured := []byte{
		0x55, 0x00, 0x62, 0x00, 0x75, 0x00, 0x6e, 0x00, 0x74, 0x00, 0x75, 0x00, 0x2d, 0x00, 0x32, 0x00, 0x32, 0x00, 0x2e, 0x00, 0x30, 0x00, 0x34, 0x00, 0x0d, 0x00, 0x0a, 0x00,
		0x55, 0x00, 0x62, 0x00, 0x75, 0x00, 0x6e, 0x00, 0x74, 0x00, 0x75, 0x00, 0x2d, 0x00, 0x32, 0x00, 0x34, 0x00, 0x2e, 0x00, 0x30, 0x00, 0x34, 0x00, 0x0d, 0x00, 0x0a, 0x00,
	}
	cases := []struct {
		name string
		in   []byte
		want []string
	}{
		{"captured", captured, []string{"Ubuntu-22.04", "Ubuntu-24.04"}},
		{"bom", utf16le("Ubuntu-22.04\ndocker-desktop\n", true), []string{"Ubuntu-22.04", "docker-desktop"}},
		{"empty (nothing running)", nil, nil},
		{"blank lines and duplicates", utf16le("\nUbuntu\n\nUbuntu\n", false), []string{"Ubuntu"}},
		{"message is not a distro", utf16le("Windows Subsystem for Linux has no installed distributions.\n", false), nil},
		{"path characters rejected", utf16le("..\\evil\nUbuntu\n", false), []string{"Ubuntu"}},
		{"utf-8 (WSL_UTF8=1)", []byte("Ubuntu-22.04\r\nDebian\r\n"), []string{"Ubuntu-22.04", "Debian"}},
		{"unicode name", utf16le("Ubuntü\n", false), []string{"Ubuntü"}},
	}
	for _, c := range cases {
		got := Distros(c.in)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// fakeWSL is a \\wsl$ stand-in: a temp root with one directory per distro,
// and a runner that answers the two listings and records the calls.
type fakeWSL struct {
	root    string
	all     []string
	running []string
	calls   []bool
	err     error
}

func (f *fakeWSL) wsl() *WSL {
	return &WSL{
		Root: f.root,
		List: func(_ context.Context, runningOnly bool) ([]byte, error) {
			f.calls = append(f.calls, runningOnly)
			if f.err != nil {
				return nil, f.err
			}
			names := f.all
			if runningOnly {
				names = f.running
			}
			return utf16le(strings.Join(names, "\n")+"\n", runningOnly), nil
		},
	}
}

func mkdirs(t *testing.T, root string, rel ...string) {
	t.Helper()
	for _, r := range rel {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(r)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWSLDiscovery(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root,
		"Ubuntu-22.04/home/dom/.claude/projects",
		"Ubuntu-22.04/home/dom/.gemini",
		"Ubuntu-22.04/home/nobody/Documents", // no tool dirs: not a scan root
		"Ubuntu-22.04/root/.codex",
		"Ubuntu-24.04/home/dom/.claude", // installed, not running: never read
		"docker-desktop",                // running, no /home at all
	)
	// A file named like a tool dir does not count.
	os.WriteFile(filepath.Join(root, "Ubuntu-22.04", "home", "nobody", ".grok"), []byte("x"), 0o644)
	f := &fakeWSL{root: root, all: []string{"Ubuntu-22.04", "Ubuntu-24.04", "docker-desktop"}, running: []string{"Ubuntu-22.04", "docker-desktop"}}

	res := Discover(Options{UserHome: filepath.Join(root, "os-home"), DiscoverWSL: true, WSL: f.wsl()})
	var got []string
	for _, h := range res.Homes {
		got = append(got, h.Label()+"="+strings.TrimPrefix(h.Path, root))
	}
	sep := string(filepath.Separator)
	want := []string{
		"os=" + sep + "os-home",
		"wsl:Ubuntu-22.04=" + filepath.Join(sep, "Ubuntu-22.04", "home", "dom"),
		"wsl:Ubuntu-22.04=" + filepath.Join(sep, "Ubuntu-22.04", "root"),
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("homes:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if strings.Join(res.Skipped, ",") != "Ubuntu-24.04" || len(res.Notes) != 0 {
		t.Fatalf("skipped %v notes %v", res.Skipped, res.Notes)
	}
	// Both listings ran (all first, then running); the stopped distro's tree
	// was never opened.
	if len(f.calls) != 2 || f.calls[0] || !f.calls[1] {
		t.Fatalf("listing calls %v", f.calls)
	}
	for _, h := range res.Homes {
		if strings.Contains(h.Path, "Ubuntu-24.04") {
			t.Fatal("stopped distro scanned")
		}
	}
	if k := res.Homes[1].Key(); k != res.Homes[1].Path || res.Homes[0].Key() != "" {
		t.Fatalf("keys: os %q wsl %q", res.Homes[0].Key(), k)
	}
	if got := res.Homes[1].CodexHome("C:/codex"); got != filepath.Join(res.Homes[1].Path, ".codex") {
		t.Fatalf("wsl codex home %q", got)
	}
	if got := res.Homes[0].CodexHome("C:/codex"); got != "C:/codex" {
		t.Fatalf("os codex home %q", got)
	}
	if p := res.Unreachable; len(p) != 1 || p[0] != filepath.Join(root, "Ubuntu-24.04") {
		t.Fatalf("unreachable roots %v", p)
	}
	if p := DefaultWSLRoots([]string{"X"}); p[0] != filepath.Join(WSLRoot, "X") {
		t.Fatalf("default roots %v", p)
	}

	// Nothing running: no homes, everything skipped, no notes.
	f.running = nil
	f.calls = nil
	res = Discover(Options{UserHome: root, DiscoverWSL: true, WSL: f.wsl()})
	if len(res.Homes) != 1 || strings.Join(res.Skipped, ",") != "Ubuntu-22.04,Ubuntu-24.04,docker-desktop" {
		t.Fatalf("nothing running: %+v", res)
	}

	// wsl.exe unavailable: one note, no homes; discovery off: not even called.
	f.err = errors.New("wsl.exe not found")
	res = Discover(Options{UserHome: root, DiscoverWSL: true, WSL: f.wsl()})
	if len(res.Homes) != 1 || len(res.Notes) != 1 || len(res.Skipped) != 0 {
		t.Fatalf("wsl error: %+v", res)
	}
	f.calls = nil
	Discover(Options{UserHome: root, DiscoverWSL: false, WSL: f.wsl()})
	if len(f.calls) != 0 {
		t.Fatal("wsl listed although discovery is off")
	}
}

func TestExtraHomesAndUnder(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "os", "extra1/.codex", "extra2")
	os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0o644)
	os1 := filepath.Join(root, "os")
	res := Discover(Options{
		UserHome: os1,
		Extra:    []string{filepath.Join(root, "extra1"), " ", os1, filepath.Join(root, "extra1"), filepath.Join(root, "missing"), filepath.Join(root, "file"), filepath.Join(root, "extra2")},
	})
	if len(res.Homes) != 3 || res.Homes[0].Kind != KindOS || res.Homes[1].Path != filepath.Join(root, "extra1") || res.Homes[2].Path != filepath.Join(root, "extra2") {
		t.Fatalf("homes %+v", res.Homes)
	}
	if len(res.Notes) != 2 {
		t.Fatalf("notes %v", res.Notes)
	}
	if !Contains(res.Homes, os1) || Contains(res.Homes, filepath.Join(root, "missing")) {
		t.Fatal("Contains")
	}
	if !Under(filepath.Join(os1, ".claude", "x.jsonl"), os1) || Under(filepath.Join(root, "osx", "y"), os1) || !Under(os1, os1) {
		t.Fatal("Under")
	}
	if runtime.GOOS == "windows" && !Under(strings.ToUpper(filepath.Join(os1, "a")), os1) {
		t.Fatal("Under must ignore case on Windows")
	}
	if p := Paths(res.Homes); len(p) != 3 || p[0] != os1 {
		t.Fatalf("paths %v", p)
	}
}
