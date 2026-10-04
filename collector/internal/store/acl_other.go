//go:build !windows

package store

import "github.com/7-of-9/tokenmaxr/collector/internal/fsx"

// writeSecrets relies on the 0600 mode: on POSIX that already excludes other
// users, so no ACL work is needed.
func writeSecrets(path string, data []byte) error {
	return fsx.WriteFileAtomic(path, data, 0o600)
}

func ensureSecretsACL(string) error { return nil }
