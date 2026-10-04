package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testKey(t *testing.T) (seed, pub string) {
	t.Helper()
	p, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(k.Seed()), base64.StdEncoding.EncodeToString(p)
}

func writeManifest(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "latest.json")
	body := "{\n  \"version\": \"1.4.0\",\n  \"files\": {\n    \"darwin-arm64\": {\n      \"url\": \"x\"\n    }\n  }\n}\n"
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestSignVerifyRoundTrip(t *testing.T) {
	seed, pub := testKey(t)
	file := writeManifest(t)
	if err := signFile(file, file+".sig", seed, pub); err != nil {
		t.Fatal(err)
	}
	sig, _ := os.ReadFile(file + ".sig")
	if strings.ContainsAny(string(sig), "\r\n") {
		t.Fatal(".sig must be the bare base64 signature")
	}
	if err := verifyFile(file, file+".sig", pub); err != nil {
		t.Fatal(err)
	}
	// The 64-byte private key form signs identically.
	raw, _ := base64.StdEncoding.DecodeString(seed)
	full := base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(raw))
	if err := signFile(file, file+".sig2", full, ""); err != nil {
		t.Fatal(err)
	}
	if err := verifyFile(file, file+".sig2", pub); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRejects(t *testing.T) {
	seed, pub := testKey(t)
	_, otherPub := testKey(t)
	file := writeManifest(t)
	if err := signFile(file, file+".sig", seed, ""); err != nil {
		t.Fatal(err)
	}
	if err := verifyFile(file, file+".sig", otherPub); err == nil {
		t.Fatal("verified against another key")
	}
	if err := verifyFile(file, file+".sig", ""); err == nil {
		t.Fatal("verified without -pubkey")
	}
	data, _ := os.ReadFile(file)
	tampered := strings.Replace(string(data), "1.4.0", "1.4.1", 1)
	if err := os.WriteFile(file, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyFile(file, file+".sig", pub); err == nil {
		t.Fatal("verified a changed manifest")
	}
}

func TestSignRefusesWrongKey(t *testing.T) {
	seed, _ := testKey(t)
	_, otherPub := testKey(t)
	file := writeManifest(t)
	if err := signFile(file, file+".sig", seed, otherPub); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("signed with a key that is not the release key: %v", err)
	}
	if _, err := os.Stat(file + ".sig"); !os.IsNotExist(err) {
		t.Fatal("a .sig was written for the wrong key")
	}
	for _, bad := range []string{"", "not base64!", base64.StdEncoding.EncodeToString([]byte("short"))} {
		if err := signFile(file, file+".sig", bad, ""); err == nil {
			t.Fatalf("accepted signing key %q", bad)
		}
	}
}
