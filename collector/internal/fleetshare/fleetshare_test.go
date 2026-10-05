package fleetshare

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func newKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func sample() Share {
	return Share{Token: "ghu_secret_token_value", Login: "octo", UserID: 42, Repo: "octo/agent-usage", Branch: "main", SharedAt: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
}

func TestRoundTrip(t *testing.T) {
	k := newKey(t)
	blob, err := Seal(k, sample())
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(k, blob)
	if err != nil {
		t.Fatal(err)
	}
	want := sample()
	want.V = Version
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	// A fresh nonce each time: the same share never seals the same.
	again, _ := Seal(k, sample())
	if again == blob {
		t.Fatal("nonce reused")
	}
}

// The wire format is exactly the contract: base64("tmx1" || nonce(12) ||
// AES-256-GCM(HMAC-SHA256(K, info), nonce, json, info)), so the server's
// format check and any other implementation agree.
func TestWireFormat(t *testing.T) {
	k := newKey(t)
	blob, err := Seal(k, sample())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil || !bytes.HasPrefix(raw, []byte("tmx1")) || len(raw) > MaxBlob {
		t.Fatalf("blob %q: %v", blob, err)
	}
	m := hmac.New(sha256.New, k)
	m.Write([]byte("tokenmaxr fleet github v1"))
	b, _ := aes.NewCipher(m.Sum(nil))
	g, _ := cipher.NewGCM(b)
	pt, err := g.Open(nil, raw[4:16], raw[16:], []byte("tokenmaxr fleet github v1"))
	if err != nil {
		t.Fatalf("not decryptable per the contract: %v", err)
	}
	for _, field := range []string{`"v":1`, `"token":"ghu_secret_token_value"`, `"login":"octo"`, `"userId":42`, `"repo":"octo/agent-usage"`, `"branch":"main"`, `"sharedAt":"2026-10-05T12:00:00Z"`} {
		if !strings.Contains(string(pt), field) {
			t.Errorf("plaintext %s lacks %s", pt, field)
		}
	}
}

func TestNoPlaintextInTheBlob(t *testing.T) {
	k := newKey(t)
	blob, _ := Seal(k, sample())
	raw, _ := base64.StdEncoding.DecodeString(blob)
	for _, s := range []string{"ghu_secret_token_value", "octo", "agent-usage"} {
		if strings.Contains(blob, s) || bytes.Contains(raw, []byte(s)) {
			t.Fatalf("the blob reveals %q", s)
		}
	}
}

func TestWrongKeyFails(t *testing.T) {
	blob, _ := Seal(newKey(t), sample())
	if _, err := Open(newKey(t), blob); !errors.Is(err, ErrOpen) {
		t.Fatalf("another fleet's key: %v", err)
	}
}

func TestTamperingFails(t *testing.T) {
	k := newKey(t)
	blob, _ := Seal(k, sample())
	raw, _ := base64.StdEncoding.DecodeString(blob)
	for i := len(Prefix); i < len(raw); i++ {
		bad := bytes.Clone(raw)
		bad[i] ^= 0x01
		if _, err := Open(k, base64.StdEncoding.EncodeToString(bad)); !errors.Is(err, ErrOpen) {
			t.Fatalf("byte %d flipped: %v", i, err)
		}
	}
	// Cut short, or with a byte added.
	if _, err := Open(k, base64.StdEncoding.EncodeToString(raw[:len(raw)-1])); !errors.Is(err, ErrOpen) {
		t.Fatalf("truncated: %v", err)
	}
	if _, err := Open(k, base64.StdEncoding.EncodeToString(append(bytes.Clone(raw), 0))); !errors.Is(err, ErrOpen) {
		t.Fatalf("extended: %v", err)
	}
}

func TestFormatErrors(t *testing.T) {
	k := newKey(t)
	blob, _ := Seal(k, sample())
	raw, _ := base64.StdEncoding.DecodeString(blob)
	other := bytes.Clone(raw)
	copy(other, "tmx2")
	for name, in := range map[string]string{
		"empty":       "",
		"not base64":  "tmx1!!!",
		"raw prefix":  "tmx1" + blob,
		"wrong tag":   base64.StdEncoding.EncodeToString(other),
		"prefix only": base64.StdEncoding.EncodeToString([]byte("tmx1")),
		"no tag":      base64.StdEncoding.EncodeToString(append([]byte("tmx1"), make([]byte, 20)...)),
		"too long":    base64.StdEncoding.EncodeToString(append([]byte("tmx1"), make([]byte, MaxBlob)...)),
	} {
		if _, err := Open(k, in); !errors.Is(err, ErrFormat) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := Open(k[:16], blob); err == nil {
		t.Error("a short key opened the blob")
	}
}

func TestSealRefusesIncompleteShares(t *testing.T) {
	k := newKey(t)
	for name, mod := range map[string]func(*Share){
		"no token":  func(s *Share) { s.Token = "" },
		"no login":  func(s *Share) { s.Login = "" },
		"no repo":   func(s *Share) { s.Repo = "" },
		"bad repo":  func(s *Share) { s.Repo = "octo" },
		"deep repo": func(s *Share) { s.Repo = "octo/a/b" },
	} {
		s := sample()
		mod(&s)
		if _, err := Seal(k, s); err == nil {
			t.Errorf("%s: sealed", name)
		}
	}
	if _, err := Seal(k[:31], sample()); err == nil {
		t.Error("sealed with a 31-byte key")
	}
}

func TestFingerprint(t *testing.T) {
	k := newKey(t)
	s := sample()
	fp := Fingerprint(k, s)
	if strings.Contains(fp, s.Token) || len(fp) != 32 {
		t.Fatalf("fingerprint %q", fp)
	}
	later := s
	later.SharedAt = later.SharedAt.Add(time.Hour)
	if Fingerprint(k, later) != fp {
		t.Fatal("the share time must not change the fingerprint")
	}
	for name, mod := range map[string]func(*Share){
		"token":  func(s *Share) { s.Token = "ghu_other" },
		"login":  func(s *Share) { s.Login = "other" },
		"repo":   func(s *Share) { s.Repo = "octo/other" },
		"branch": func(s *Share) { s.Branch = "gh-data" },
	} {
		c := s
		mod(&c)
		if Fingerprint(k, c) == fp {
			t.Errorf("a changed %s keeps the fingerprint", name)
		}
	}
	if Fingerprint(newKey(t), s) == fp {
		t.Fatal("the fingerprint must depend on the fleet key")
	}
}

// The choices a sharer passes on travel sealed with the rest; a share without
// them (a collector before 0.4.2) opens with none.
func TestPrefsRoundTrip(t *testing.T) {
	k := newKey(t)
	s := sample()
	s.Prefs = &Prefs{From: "rabbit", ShowCountry: true}
	blob, err := Seal(k, s)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(k, blob)
	if err != nil || got.Prefs == nil || *got.Prefs != *s.Prefs {
		t.Fatalf("prefs %+v: %v", got.Prefs, err)
	}
	old, _ := Seal(k, sample())
	if got, err := Open(k, old); err != nil || got.Prefs != nil {
		t.Fatalf("a share without prefs: %+v, %v", got.Prefs, err)
	}
	if fp := Fingerprint(k, s); fp != Fingerprint(k, sample()) {
		t.Fatal("the prefs must not change the sign-in's fingerprint")
	}
	pf := PrefsFingerprint(k, s)
	for name, mod := range map[string]func(*Prefs){
		"from":    func(p *Prefs) { p.From = "fox" },
		"country": func(p *Prefs) { p.ShowCountry = false },
		"history": func(p *Prefs) { p.ShowAccountHistory = true },
	} {
		c := s
		p := *s.Prefs
		mod(&p)
		c.Prefs = &p
		if PrefsFingerprint(k, c) == pf {
			t.Errorf("a changed %s keeps the prefs fingerprint", name)
		}
	}
	if PrefsFingerprint(k, sample()) == pf {
		t.Fatal("no prefs and prefs fingerprint the same")
	}
}

// share041 and open041 are collector 0.4.1's decoder, verbatim apart from
// the names: a share with prefs must still open there, as a plain sign-in.
type share041 struct {
	V        int       `json:"v"`
	Token    string    `json:"token"`
	Login    string    `json:"login"`
	UserID   int64     `json:"userId"`
	Repo     string    `json:"repo"`
	Branch   string    `json:"branch"`
	SharedAt time.Time `json:"sharedAt"`
}

func (s share041) valid() error {
	owner, name, ok := strings.Cut(s.Repo, "/")
	switch {
	case s.V != 1:
		return errors.New("version")
	case s.Token == "" || s.Login == "":
		return errors.New("no sign-in")
	case !ok || owner == "" || name == "" || strings.Contains(name, "/"):
		return errors.New("no repository")
	}
	return nil
}

func open041(k []byte, blob string) (share041, error) {
	var s share041
	b, err := base64.StdEncoding.DecodeString(blob)
	if err != nil || len(b) > 4096 || len(b) < 4+12 || string(b[:4]) != "tmx1" {
		return s, ErrFormat
	}
	m := hmac.New(sha256.New, k)
	m.Write([]byte("tokenmaxr fleet github v1"))
	c, _ := aes.NewCipher(m.Sum(nil))
	g, _ := cipher.NewGCM(c)
	rest := b[4:]
	pt, err := g.Open(nil, rest[:12], rest[12:], []byte("tokenmaxr fleet github v1"))
	if err != nil {
		return s, ErrOpen
	}
	if err := json.Unmarshal(pt, &s); err != nil {
		return share041{}, err
	}
	if err := s.valid(); err != nil {
		return share041{}, err
	}
	return s, nil
}

func TestCollector041OpensAShareWithPrefs(t *testing.T) {
	k := newKey(t)
	s := sample()
	s.Prefs = &Prefs{From: "rabbit", ShowCountry: true, ShowAccountHistory: true}
	blob, err := Seal(k, s)
	if err != nil {
		t.Fatal(err)
	}
	got, err := open041(k, blob)
	want := share041{V: 1, Token: s.Token, Login: s.Login, UserID: s.UserID, Repo: s.Repo, Branch: s.Branch, SharedAt: s.SharedAt}
	if err != nil || got != want {
		t.Fatalf("0.4.1 read %+v: %v", got, err)
	}
}
