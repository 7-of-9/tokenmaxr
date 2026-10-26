//go:build !windows

package webcleanup

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// Chrome reads per-user hosts from a NativeMessagingHosts folder.
func hostDirs() []string {
	h, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	if runtime.GOOS == "darwin" {
		return []string{filepath.Join(h, "Library", "Application Support", "Google", "Chrome", "NativeMessagingHosts")}
	}
	return []string{filepath.Join(h, ".config", "google-chrome", "NativeMessagingHosts"),
		filepath.Join(h, ".config", "chromium", "NativeMessagingHosts")}
}

func osRegistrations(name string) []string {
	var out []string
	for _, d := range hostDirs() {
		p := filepath.Join(d, name+".json")
		if _, err := os.Lstat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

func osUnregister(_, manifest string) error {
	if err := os.Remove(manifest); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
