// Package fleetshare seals the GitHub sign-in one machine shares with the
// rest of its fleet through the server (docs/agents/SPEC.md "Fleet GitHub
// sign-in"). The server stores the blob without opening it: it is encrypted
// with a key derived from the fleet key K. A fleet whose K a machine made is
// unreadable to the server (it knows only K's fingerprint); on a fleet
// linked from the browser the server keeps K (escrowed under its
// PROMPT_ENC_KEY), so whoever runs it could open the blob.
//
// Blob = base64( "tmx1" || nonce (12 random bytes) || AES-256-GCM(key, nonce,
// plaintext, aad) ), key = HMAC-SHA256(K, Info), aad = Info.
package fleetshare

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// Prefix starts every decoded blob (the server checks it).
	Prefix = "tmx1"
	// Info derives the encryption key from K and is the AEAD's associated data.
	Info = "tokenmaxr fleet github v1"
	// MaxBlob is the server's cap on the decoded blob.
	MaxBlob = 4096
	// Version is the plaintext's "v".
	Version = 1

	nonceSize = 12
)

var (
	// ErrFormat: not a blob of this format (not base64, too long, wrong
	// prefix, truncated).
	ErrFormat = errors.New("fleet share: not a tmx1 blob")
	// ErrOpen: the blob does not decrypt with this fleet key (another fleet,
	// or altered).
	ErrOpen = errors.New("fleet share: does not decrypt with this machine's fleet key")
)

// Share is the shared sign-in: the GitHub App user token, the user it belongs
// to, and where the fleet publishes.
type Share struct {
	V        int       `json:"v"`
	Token    string    `json:"token"`
	Login    string    `json:"login"`
	UserID   int64     `json:"userId"`
	Repo     string    `json:"repo"`
	Branch   string    `json:"branch"`
	SharedAt time.Time `json:"sharedAt"`
}

// key derives the share's AES-256 key from the fleet key.
func key(k []byte) []byte {
	m := hmac.New(sha256.New, k)
	m.Write([]byte(Info))
	return m.Sum(nil)
}

func aead(k []byte) (cipher.AEAD, error) {
	if len(k) != 32 {
		return nil, errors.New("fleet share: the fleet key must be 32 bytes")
	}
	b, err := aes.NewCipher(key(k))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

// valid checks the fields every share must carry.
func (s Share) valid() error {
	owner, name, ok := strings.Cut(s.Repo, "/")
	switch {
	case s.V != Version:
		return fmt.Errorf("fleet share: version %d", s.V)
	case s.Token == "" || s.Login == "":
		return errors.New("fleet share: no sign-in")
	case !ok || owner == "" || name == "" || strings.Contains(name, "/"):
		return errors.New("fleet share: no repository")
	}
	return nil
}

// Seal encrypts s (V is set) under the fleet key k.
func Seal(k []byte, s Share) (string, error) {
	s.V = Version
	if err := s.valid(); err != nil {
		return "", err
	}
	g, err := aead(k)
	if err != nil {
		return "", err
	}
	pt, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := append([]byte(Prefix), nonce...)
	out = g.Seal(out, nonce, pt, []byte(Info))
	if len(out) > MaxBlob {
		return "", fmt.Errorf("fleet share: %d bytes, over the server's %d", len(out), MaxBlob)
	}
	return base64.StdEncoding.EncodeToString(out), nil
}

// Open decrypts a blob with the fleet key k.
func Open(k []byte, blob string) (Share, error) {
	var s Share
	if len(blob) > base64.StdEncoding.EncodedLen(MaxBlob) {
		return s, ErrFormat
	}
	b, err := base64.StdEncoding.DecodeString(blob)
	if err != nil || len(b) > MaxBlob || len(b) < len(Prefix)+nonceSize || string(b[:len(Prefix)]) != Prefix {
		return s, ErrFormat
	}
	g, err := aead(k)
	if err != nil {
		return s, err
	}
	rest := b[len(Prefix):]
	if len(rest) < nonceSize+g.Overhead() {
		return s, ErrFormat
	}
	pt, err := g.Open(nil, rest[:nonceSize], rest[nonceSize:], []byte(Info))
	if err != nil {
		return s, ErrOpen
	}
	if err := json.Unmarshal(pt, &s); err != nil {
		return Share{}, fmt.Errorf("fleet share: %w", err)
	}
	if err := s.valid(); err != nil {
		return Share{}, err
	}
	return s, nil
}

// Fingerprint identifies what a share carries (token, login, repository and
// branch, not when it was shared) without revealing the token: an HMAC under
// the share key, so state.json can tell a changed sign-in from an unchanged
// one.
func Fingerprint(k []byte, s Share) string {
	m := hmac.New(sha256.New, key(k))
	b, _ := json.Marshal([]any{s.Token, s.Login, s.UserID, s.Repo, s.Branch})
	m.Write(b)
	return hex.EncodeToString(m.Sum(nil)[:16])
}
