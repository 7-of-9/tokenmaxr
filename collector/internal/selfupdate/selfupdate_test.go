package selfupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
)

func TestEmbeddedKeyDecodes(t *testing.T) {
	if len(ReleaseKey()) != ed25519.PublicKeySize {
		t.Fatal("bad embedded key")
	}
}

func TestVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	body := []byte(`{"version":"0.2.0"}`)
	sig := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, body)) + "\n")
	if err := Verify(pub, body, sig); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if err := Verify(pub, []byte(`{"version":"0.2.1"}`), sig); err == nil {
		t.Fatal("tampered body accepted")
	}
	if err := Verify(ReleaseKey(), body, sig); err == nil {
		t.Fatal("signature by another key accepted")
	}
	if err := Verify(pub, body, []byte("not base64!")); err == nil {
		t.Fatal("garbage signature accepted")
	}
}

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.2.0", "0.1.0", true},
		{"0.1.10", "0.1.9", true},
		{"1.0.0", "0.9.9", true},
		{"0.1.0", "0.1.0", false},
		{"0.1.0", "0.2.0", false},
		{"0.2.0", "dev", false},
		{"dev", "0.2.0", false},
		{"v0.2.0", "0.1.0-rc1", true},
		// Numeric, not lexical, component comparison.
		{"0.10.0", "0.9.0", true},
		{"0.9.0", "0.10.0", false},
		{"0.2.10", "0.2.9", true},
		// A release outranks its own prereleases (semver 2.0.0 item 11).
		{"0.2.0", "0.2.0-rc.1", true},
		{"0.2.0-rc.1", "0.2.0", false},
		{"0.2.0-rc.1", "0.1.9", true},
		{"0.2.0-rc.1", "0.2.0-rc.1", false},
		{"0.2.0-rc.2", "0.2.0-rc.1", true},
		{"0.2.0-rc.10", "0.2.0-rc.9", true},
		{"0.2.0-rc.9", "0.2.0-rc.10", false},
		{"0.2.0-beta", "0.2.0-alpha", true},
		{"0.2.0-alpha.1", "0.2.0-alpha", true},
		{"0.2.0-alpha", "0.2.0-alpha.1", false},
		// Numeric identifiers rank below alphanumeric ones.
		{"0.2.0-alpha", "0.2.0-1", true},
		{"0.2.0-1", "0.2.0-alpha", false},
		{"0.2.0-alpha.beta", "0.2.0-alpha.1", true},
		// Build metadata never affects precedence.
		{"0.2.0+build.7", "0.2.0", false},
		{"0.2.0", "0.2.0+build.7", false},
		{"0.2.1+build.7", "0.2.0", true},
		// Malformed versions are never newer.
		{"0.2.0-", "0.1.0", false},
		{"0.2.0-rc..1", "0.1.0", false},
		{"0.2.0-rc_1", "0.1.0", false},
		{"0.2", "0.1.0", false},
		{"0.+2.0", "0.1.0", false},
		{"0.2.0.0", "0.1.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q,%q) = %v", c.a, c.b, got)
		}
	}
}

// useLocalReleases points Stage at a local server for the test's lifetime.
func useLocalReleases(t *testing.T, base string) {
	t.Helper()
	old := downloadBase
	downloadBase = base
	t.Cleanup(func() { downloadBase = old })
}

func TestDownloadPrefix(t *testing.T) {
	p, err := downloadPrefix(Manifest{Version: "0.2.0"})
	if err != nil || p != DownloadBase+"0.2.0/" {
		t.Fatalf("downloadPrefix(0.2.0) = %q, %v", p, err)
	}
	if p != "https://github.com/7-of-9/tokenmaxr/releases/download/v0.2.0/" {
		t.Fatalf("release prefix drifted: %q", p)
	}
	for _, v := range []string{"", "dev", "0.2", "0.2.0/../0.1.0", "0.2.0?x", "0.2.0#x", "0.2.0 ", "0.2.0-"} {
		if _, err := downloadPrefix(Manifest{Version: v}); err == nil {
			t.Errorf("version %q accepted", v)
		}
	}
	prefix := DownloadBase + "0.2.0/"
	good := []string{prefix + "windows-amd64/d0m1-collector.exe", prefix + "darwin-arm64/d0m1-collector"}
	for _, u := range good {
		if err := checkURL(prefix, u); err != nil {
			t.Errorf("checkURL(%q): %v", u, err)
		}
	}
	bad := []string{
		"",
		prefix,
		prefix + "/x",
		prefix + "x/",
		prefix + "../v0.1.0/windows-amd64/d0m1-collector.exe",
		prefix + "windows-amd64/../../v0.1.0/x",
		prefix + "./x",
		prefix + "x?y=1",
		prefix + "x#y",
		prefix + "x\\y",
		"http://cdn.d0m1.com/d0m1-media/collector/v0.2.0/x",
		"https://cdn.d0m1.com.evil.example/d0m1-media/collector/v0.2.0/x",
		"https://cdn.d0m1.com/d0m1-media/collector/v0.2.0-x/x",
		"https://cdn.d0m1.com/d0m1-media/collector/v0.1.0/x",
		"https://cdn.d0m1.com/d0m1-media/other/x",
		"https://github.com/7-of-9/tokenmaxr/collector/x",
		"HTTPS://CDN.D0M1.COM/d0m1-media/collector/v0.2.0/x",
	}
	for _, u := range bad {
		if err := checkURL(prefix, u); err == nil {
			t.Errorf("checkURL(%q) accepted", u)
		}
	}
}

// TestStageRejectsForeignURL: a signed manifest whose files point outside the
// release prefix is refused before a single byte is requested.
func TestStageRejectsForeignURL(t *testing.T) {
	var hits int32
	data := []byte("binary")
	sum := sha256.Sum256(data)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write(data)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	other := httptest.NewServer(mux)
	defer other.Close()
	useLocalReleases(t, srv.URL+"/collector/v")

	mk := func(url func(v Variant) string) Manifest {
		files := map[string]File{}
		for _, v := range Variants(runtime.GOOS, runtime.GOARCH) {
			files[v.Key] = File{URL: url(v), SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
		}
		return Manifest{Version: "0.2.0", Files: files}
	}
	good := srv.URL + "/collector/v0.2.0/"
	cases := map[string]Manifest{
		"other host":    mk(func(v Variant) string { return other.URL + "/collector/v0.2.0/" + v.Key + "/" + v.Name }),
		"other version": mk(func(v Variant) string { return srv.URL + "/collector/v0.1.0/" + v.Key + "/" + v.Name }),
		"other path":    mk(func(v Variant) string { return srv.URL + "/bin/" + v.Key }),
		"traversal":     mk(func(v Variant) string { return good + "../v0.1.0/" + v.Key + "/" + v.Name }),
		"query":         mk(func(v Variant) string { return good + v.Key + "/" + v.Name + "?x=1" }),
	}
	// Only the last variant strays; the first must not be fetched either.
	last := Variants(runtime.GOOS, runtime.GOARCH)
	cases["last variant only"] = mk(func(v Variant) string {
		if v.Key == last[len(last)-1].Key {
			return other.URL + "/collector/v0.2.0/" + v.Key + "/" + v.Name
		}
		return good + v.Key + "/" + v.Name
	})
	dir := t.TempDir()
	for name, m := range cases {
		staged, err := Stage(context.Background(), srv.Client(), m, dir)
		if err == nil {
			t.Errorf("%s: staged %v", name, staged)
		}
		if n := atomic.LoadInt32(&hits); n != 0 {
			t.Fatalf("%s: %d request(s) made for a refused manifest", name, n)
		}
		if left, _ := filepath.Glob(filepath.Join(dir, "*")); len(left) != 0 {
			t.Fatalf("%s: files written: %v", name, left)
		}
	}
	// A non-release version is refused whatever the URLs say.
	m := mk(func(v Variant) string { return good + v.Key + "/" + v.Name })
	m.Version = "dev"
	if _, err := Stage(context.Background(), srv.Client(), m, dir); err == nil {
		t.Fatal("manifest with version dev staged")
	}
	// The same manifest with the right prefix goes through.
	m.Version = "0.2.0"
	if _, err := Stage(context.Background(), srv.Client(), m, dir); err != nil {
		t.Fatalf("in-prefix manifest refused: %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != int32(len(last)) {
		t.Fatalf("expected %d fetches, got %d", len(last), n)
	}
}

// TestFetchStageSwap runs the whole update against a local server with a
// throwaway signing key.
func TestFetchStageSwap(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	bins := map[string][]byte{}
	files := map[string]File{}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	// Files are laid out as the publisher does: <base>v<ver>/<key>/<file>.
	useLocalReleases(t, srv.URL+"/collector/v")
	for i, v := range Variants(runtime.GOOS, runtime.GOARCH) {
		data := []byte("new binary " + v.Key + string(rune('a'+i)))
		sum := sha256.Sum256(data)
		bins[v.Key] = data
		path := "/collector/v9.9.9/" + v.Key + "/" + v.Name
		files[v.Key] = File{URL: srv.URL + path, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) { w.Write(data) })
	}
	manifest, _ := json.Marshal(Manifest{Version: "9.9.9", MinVersion: "0.1.0", Files: files})
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, manifest))
	mux.HandleFunc("/latest.json", func(w http.ResponseWriter, r *http.Request) { w.Write(manifest) })
	mux.HandleFunc("/latest.json.sig", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(sig)) })

	ctx := context.Background()
	m, err := Fetch(ctx, srv.Client(), srv.URL+"/latest.json", pub)
	if err != nil || m.Version != "9.9.9" {
		t.Fatalf("Fetch: %+v %v", m, err)
	}
	if _, err := Fetch(ctx, srv.Client(), srv.URL+"/latest.json", ReleaseKey()); err == nil {
		t.Fatal("manifest signed by another key accepted")
	}

	dir := t.TempDir()
	for _, v := range Variants(runtime.GOOS, runtime.GOARCH) {
		os.WriteFile(filepath.Join(dir, v.Name), []byte("old"), 0o755)
	}
	staged, err := Stage(ctx, srv.Client(), m, dir)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if err := Swap(staged); err != nil {
		t.Fatalf("Swap: %v", err)
	}
	for _, v := range Variants(runtime.GOOS, runtime.GOARCH) {
		got, _ := os.ReadFile(filepath.Join(dir, v.Name))
		if string(got) != string(bins[v.Key]) {
			t.Fatalf("%s not swapped: %q", v.Name, got)
		}
	}
	CleanOld(dir)
	if left, _ := filepath.Glob(filepath.Join(dir, "*.old")); len(left) != 0 {
		t.Fatalf("leftovers: %v", left)
	}

	// A corrupted download must not be staged.
	bad := m
	bad.Files = map[string]File{}
	for k, f := range m.Files {
		f.SHA256 = hex.EncodeToString(make([]byte, 32))
		bad.Files[k] = f
	}
	if _, err := Stage(ctx, srv.Client(), bad, dir); err == nil {
		t.Fatal("sha256 mismatch staged")
	}
}

// TestVariants: Windows updates both builds; macOS has one binary, which
// is also the app (no extra manifest keys).
func TestVariants(t *testing.T) {
	if vs := Variants("windows", "arm64"); len(vs) != 2 || vs[0].Key != "windows-arm64" || vs[1].Key != "windows-arm64-w" || vs[1].Name != "tokenmaxrw.exe" {
		t.Fatalf("windows variants %+v", vs)
	}
	if vs := Variants("darwin", "amd64"); len(vs) != 1 || vs[0].Key != "darwin-amd64" || vs[0].Name != "tokenmaxr" {
		t.Fatalf("darwin variants %+v", vs)
	}
}
