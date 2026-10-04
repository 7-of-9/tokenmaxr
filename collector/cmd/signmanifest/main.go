// Command signmanifest signs the collector release manifest (latest.json) with
// ed25519 so installed collectors can verify self-updates. See the "Release and
// publishing" section of docs/agents/SPEC.md.
//
//	COLLECTOR_SIGNING_KEY=<base64 seed> signmanifest [-expect-pubkey B64] FILE
//	signmanifest -verify -pubkey B64 FILE
//
// Signing reads the exact bytes of FILE and writes the base64 signature to
// FILE.sig (or -sig PATH). Verify exits 0 only when the signature matches.
package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	verify := flag.Bool("verify", false, "verify FILE against its signature instead of signing")
	pubkey := flag.String("pubkey", "", "base64 ed25519 public key (required with -verify)")
	expect := flag.String("expect-pubkey", "", "when signing, fail unless the key's public half equals this base64 key")
	sigPath := flag.String("sig", "", "signature path (default FILE.sig)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: COLLECTOR_SIGNING_KEY=<base64 seed> signmanifest [-expect-pubkey B64] [-sig PATH] FILE")
		fmt.Fprintln(os.Stderr, "       signmanifest -verify -pubkey B64 [-sig PATH] FILE")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	file := flag.Arg(0)
	if *sigPath == "" {
		*sigPath = file + ".sig"
	}

	var err error
	if *verify {
		err = verifyFile(file, *sigPath, *pubkey)
		if err == nil {
			fmt.Fprintf(os.Stderr, "signmanifest: %s: signature ok\n", file)
		}
	} else {
		err = signFile(file, *sigPath, os.Getenv("COLLECTOR_SIGNING_KEY"), *expect)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "signmanifest:", err)
		os.Exit(1)
	}
}

func signFile(file, sigPath, seedB64, expectPub string) error {
	key, err := privateKey(seedB64)
	if err != nil {
		return err
	}
	pub := base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
	if expectPub != "" && pub != strings.TrimSpace(expectPub) {
		return fmt.Errorf("COLLECTOR_SIGNING_KEY public key %s does not match the embedded release key %s", pub, expectPub)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(key, data))
	// No trailing newline: the .sig file is exactly the base64 signature.
	if err := os.WriteFile(sigPath, []byte(sig), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "signmanifest: signed %s (%d bytes) with public key %s -> %s\n", file, len(data), pub, sigPath)
	return nil
}

// privateKey accepts a base64 32-byte seed (the documented secret format) or a
// full 64-byte ed25519 private key, and ignores surrounding whitespace.
func privateKey(b64 string) (ed25519.PrivateKey, error) {
	b64 = strings.TrimSpace(b64)
	if b64 == "" {
		return nil, errors.New("COLLECTOR_SIGNING_KEY is not set")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, errors.New("COLLECTOR_SIGNING_KEY is not valid base64")
	}
	switch len(raw) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(raw), nil
	case ed25519.PrivateKeySize:
		key := ed25519.NewKeyFromSeed(raw[:ed25519.SeedSize])
		if !bytes.Equal(key, raw) {
			return nil, errors.New("COLLECTOR_SIGNING_KEY is a 64-byte key whose public half does not match its seed")
		}
		return key, nil
	default:
		return nil, fmt.Errorf("COLLECTOR_SIGNING_KEY decodes to %d bytes, want a %d-byte seed", len(raw), ed25519.SeedSize)
	}
}

func verifyFile(file, sigPath, pubB64 string) error {
	if strings.TrimSpace(pubB64) == "" {
		return errors.New("-verify needs -pubkey")
	}
	pub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(pubB64))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("-pubkey must be a base64 32-byte ed25519 public key")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	sigText, err := os.ReadFile(sigPath)
	if err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigText)))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("%s is not a base64 ed25519 signature", sigPath)
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), data, sig) {
		return fmt.Errorf("%s: signature does not verify", file)
	}
	return nil
}
