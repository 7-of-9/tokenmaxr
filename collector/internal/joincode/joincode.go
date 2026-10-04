// Package joincode formats and parses D0M1-<invite>[-<base32(K)>] join codes.
// K never reaches the server: it travels only inside the code.
package joincode

import (
	"encoding/base32"
	"errors"
	"strings"
)

const prefix = "D0M1-"

var enc = base32.StdEncoding.WithPadding(base32.NoPadding)

// keyLen is len(base32(32 bytes)) without padding.
const keyLen = 52

// Format renders the join code for invite and fleet key k.
func Format(invite string, k []byte) string {
	if len(k) == 0 {
		return prefix + invite
	}
	return prefix + invite + "-" + enc.EncodeToString(k)
}

// Parse splits a join code into the invite and K. K is nil for a
// first-machine bootstrap code ("D0M1-<invite>").
func Parse(code string) (invite string, k []byte, err error) {
	code = strings.TrimSpace(code)
	if len(code) < len(prefix) || !strings.EqualFold(code[:len(prefix)], prefix) {
		return "", nil, errors.New("join code must start with D0M1-")
	}
	rest := code[len(prefix):]
	// The invite may itself contain '-', so K is only the final segment when
	// it is exactly a 52-char base32 key that decodes to 32 bytes.
	if i := strings.LastIndexByte(rest, '-'); i >= 0 && len(rest)-i-1 == keyLen {
		if key, derr := enc.DecodeString(rest[i+1:]); derr == nil && len(key) == 32 {
			invite, k = rest[:i], key
		}
	}
	if k == nil {
		invite = rest
	}
	if invite == "" || strings.ContainsAny(invite, " \t\r\n") {
		return "", nil, errors.New("join code has no invite")
	}
	return invite, k, nil
}
