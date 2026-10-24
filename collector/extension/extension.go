// Package extension embeds tokenmaxr's Chrome extension for Claude Web
// capture (docs/agents/SPEC.md "Claude chat"). `tokenmaxr web-capture
// install` writes these files to the state directory for the owner to load
// unpacked; the manifest's public key pins the extension id, which the
// native-messaging host manifest allows and the host checks.
package extension

import (
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"strings"
)

//go:embed manifest.json config.json estimate.js main-world.js relay.js sw.js queue.js
var files embed.FS

// Files is the extension as installed (config.json is rewritten by install).
func Files() fs.FS { return files }

// Manifest is the parts of manifest.json the collector reads.
type Manifest struct {
	Version string `json:"version"`
	Key     string `json:"key"`
}

func manifest() (Manifest, error) {
	var m Manifest
	b, err := files.ReadFile("manifest.json")
	if err == nil {
		err = json.Unmarshal(b, &m)
	}
	return m, err
}

// Version is the extension's version.
func Version() string {
	m, _ := manifest()
	return m.Version
}

// ID is the extension id Chrome derives from the manifest key: the first
// 128 bits of SHA-256 of the public key, as letters a-p.
func ID() string {
	m, err := manifest()
	if err != nil {
		return ""
	}
	der, err := base64.StdEncoding.DecodeString(m.Key)
	if err != nil || len(der) == 0 {
		return ""
	}
	sum := sha256.Sum256(der)
	h := hex.EncodeToString(sum[:16])
	var b strings.Builder
	for _, c := range h {
		if c <= '9' {
			b.WriteByte(byte('a' + (c - '0')))
		} else {
			b.WriteByte(byte('a' + 10 + (c - 'a')))
		}
	}
	return b.String()
}
