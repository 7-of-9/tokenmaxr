// Package selfupdate fetches the signed release manifest, downloads and
// verifies new binaries and swaps them in (docs/agents/SPEC.md "Self-update").
package selfupdate

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
)

// PublicKey is the release signing key (raw 32 bytes, base64), from
// internal/buildinfo (collector/release.json).
var PublicKey = buildinfo.ReleasePublicKey

// ReleaseKey decodes PublicKey.
func ReleaseKey() ed25519.PublicKey {
	k, err := base64.StdEncoding.DecodeString(PublicKey)
	if err != nil || len(k) != ed25519.PublicKeySize {
		panic("selfupdate: bad embedded public key")
	}
	return k
}

type File struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Manifest struct {
	Version    string          `json:"version"`
	MinVersion string          `json:"minVersion"`
	Files      map[string]File `json:"files"`
}

// Verify checks the base64 ed25519 signature over the exact manifest bytes.
func Verify(pub ed25519.PublicKey, body, sig []byte) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return errors.New("manifest signature is malformed")
	}
	if !ed25519.Verify(pub, body, raw) {
		return errors.New("manifest signature does not verify")
	}
	return nil
}

func get(ctx context.Context, c *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("GET %s: response too large", url)
	}
	return data, nil
}

// Fetch downloads url and url+".sig" and returns the verified manifest.
func Fetch(ctx context.Context, c *http.Client, url string, pub ed25519.PublicKey) (Manifest, error) {
	var m Manifest
	body, err := get(ctx, c, url, 1<<20)
	if err != nil {
		return m, err
	}
	sig, err := get(ctx, c, url+".sig", 4096)
	if err != nil {
		return m, err
	}
	if err := Verify(pub, body, sig); err != nil {
		return m, err
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return m, fmt.Errorf("manifest: %w", err)
	}
	if m.Version == "" {
		return m, errors.New("manifest has no version")
	}
	return m, nil
}

// version is a parsed semver 2.0.0 version: MAJOR.MINOR.PATCH plus the
// dot-separated prerelease identifiers, if any. Build metadata is ignored.
type version struct {
	nums [3]int
	pre  []string
}

func isNumeric(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func isIdent(s string) bool {
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-') {
			return false
		}
	}
	return s != ""
}

func parseVersion(v string) (out version, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	if i := strings.IndexByte(v, '-'); i >= 0 {
		out.pre = strings.Split(v[i+1:], ".")
		v = v[:i]
		for _, id := range out.pre {
			if !isIdent(id) {
				return out, false
			}
		}
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || !isNumeric(p) {
			return out, false
		}
		out.nums[i] = n
	}
	return out, true
}

// compareIdent orders two prerelease identifiers per semver 2.0.0 item 11:
// numeric ones compare numerically and rank below alphanumeric ones, which
// compare in ASCII order.
func compareIdent(a, b string) int {
	na, nb := isNumeric(a), isNumeric(b)
	switch {
	case na && nb:
		ia, _ := strconv.Atoi(a)
		ib, _ := strconv.Atoi(b)
		return ia - ib
	case na:
		return -1
	case nb:
		return 1
	}
	return strings.Compare(a, b)
}

// compare returns -1, 0 or 1 by semver precedence. A release outranks its
// own prereleases; between prereleases, identifiers compare left to right
// and a longer set wins when the shared ones are equal.
func (a version) compare(b version) int {
	for i := range a.nums {
		if a.nums[i] != b.nums[i] {
			if a.nums[i] > b.nums[i] {
				return 1
			}
			return -1
		}
	}
	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return 0
	case len(a.pre) == 0:
		return 1
	case len(b.pre) == 0:
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		if c := compareIdent(a.pre[i], b.pre[i]); c != 0 {
			if c > 0 {
				return 1
			}
			return -1
		}
	}
	switch {
	case len(a.pre) > len(b.pre):
		return 1
	case len(a.pre) < len(b.pre):
		return -1
	}
	return 0
}

// Newer reports whether version a is strictly newer than b by semver
// precedence, so 0.10.0 > 0.9.0 and 0.2.0 > 0.2.0-rc.1. Unparseable versions
// (including "dev") are never newer and never upgraded.
func Newer(a, b string) bool {
	va, ok1 := parseVersion(a)
	vb, ok2 := parseVersion(b)
	if !ok1 || !ok2 {
		return false
	}
	return va.compare(vb) > 0
}

// Variant is one binary this platform ships.
type Variant struct {
	Key  string // manifest files key, e.g. windows-amd64-w
	Name string // file name in bin/
}

// Variants lists the binaries for goos/goarch: Windows ships a console and a
// windowsgui build (the app); macOS one binary, which is also the app.
func Variants(goos, goarch string) []Variant {
	base := goos + "-" + goarch
	if goos == "windows" {
		return []Variant{{Key: base, Name: buildinfo.Product + ".exe"}, {Key: base + "-w", Name: buildinfo.Product + "w.exe"}}
	}
	return []Variant{{Key: base, Name: buildinfo.Product}}
}

// Staged is a verified download waiting to replace Target.
type Staged struct {
	Target string
	New    string
}

// DownloadBase is the only origin binaries are fetched from. A release's
// files must live under DownloadBase + <version> + "/" (docs/agents/SPEC.md
// "Release and publishing"); this is defence in depth on top of the manifest
// signature.
var DownloadBase = buildinfo.DownloadBase

// downloadBase is DownloadBase, overridden only by tests that serve a
// release from a local httptest server.
var downloadBase = DownloadBase

// downloadPrefix returns the prefix every file URL of m must start with.
func downloadPrefix(m Manifest) (string, error) {
	if _, ok := parseVersion(m.Version); !ok || strings.ContainsAny(m.Version, "/?#\\ \t") {
		return "", fmt.Errorf("manifest version %q is not a release version", m.Version)
	}
	return downloadBase + m.Version + "/", nil
}

// checkURL rejects a file URL outside the release prefix, or one whose path
// could escape it (empty, "." or ".." segments, a query or a fragment).
func checkURL(prefix, u string) error {
	if !strings.HasPrefix(u, prefix) {
		return fmt.Errorf("url %q is outside %s", u, prefix)
	}
	rest := u[len(prefix):]
	if rest == "" || strings.ContainsAny(rest, "?#\\") {
		return fmt.Errorf("url %q: bad path", u)
	}
	for _, seg := range strings.Split(rest, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("url %q: bad path", u)
		}
	}
	return nil
}

// Stage downloads every variant into binDir as <name>.new, checking size and
// sha256. Nothing is replaced yet. Every file URL is checked against the
// release prefix before anything is fetched.
func Stage(ctx context.Context, c *http.Client, m Manifest, binDir string) ([]Staged, error) {
	prefix, err := downloadPrefix(m)
	if err != nil {
		return nil, err
	}
	variants := Variants(runtime.GOOS, runtime.GOARCH)
	for _, v := range variants {
		f, ok := m.Files[v.Key]
		if !ok {
			return nil, fmt.Errorf("manifest has no %s build", v.Key)
		}
		if f.Size <= 0 || f.Size > 256<<20 {
			return nil, fmt.Errorf("manifest %s: bad size", v.Key)
		}
		if err := checkURL(prefix, f.URL); err != nil {
			return nil, fmt.Errorf("manifest %s: %w", v.Key, err)
		}
	}
	var out []Staged
	for _, v := range variants {
		f := m.Files[v.Key]
		data, err := get(ctx, c, f.URL, f.Size)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		want, err := hex.DecodeString(f.SHA256)
		if err != nil || !bytes.Equal(sum[:], want) || int64(len(data)) != f.Size {
			return nil, fmt.Errorf("%s: sha256/size mismatch", v.Key)
		}
		target := filepath.Join(binDir, v.Name)
		tmp := target + ".new"
		if err := os.WriteFile(tmp, data, 0o755); err != nil {
			return nil, err
		}
		out = append(out, Staged{Target: target, New: tmp})
	}
	return out, nil
}

// Swap moves staged binaries into place. On Windows a running exe cannot be
// overwritten but can be renamed, so the old one goes to .old first; CleanOld
// removes it on a later tick.
func Swap(staged []Staged) error {
	for _, s := range staged {
		if runtime.GOOS == "windows" {
			if _, err := os.Stat(s.Target); err == nil {
				old := s.Target + ".old"
				if os.Remove(old) != nil {
					if _, err := os.Stat(old); err == nil {
						old = fmt.Sprintf("%s.%d.old", s.Target, time.Now().UnixNano())
					}
				}
				if err := os.Rename(s.Target, old); err != nil {
					return err
				}
				if err := os.Rename(s.New, s.Target); err != nil {
					os.Rename(old, s.Target)
					return err
				}
				continue
			}
		}
		if err := os.Rename(s.New, s.Target); err != nil {
			return err
		}
	}
	return nil
}

// CleanOld deletes leftovers of earlier swaps and failed stages. A .old file
// still running (a tick in flight) stays until a later call.
func CleanOld(binDir string) {
	for _, pat := range []string{"*.old", "*.new"} {
		matches, _ := filepath.Glob(filepath.Join(binDir, pat))
		for _, m := range matches {
			os.Remove(m)
		}
	}
}
