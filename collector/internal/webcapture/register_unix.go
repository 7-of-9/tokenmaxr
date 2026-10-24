//go:build !windows

package webcapture

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Chrome reads per-user hosts from a NativeMessagingHosts directory.
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

func osRegister(name, manifest string) error {
	b, err := os.ReadFile(manifest)
	if err != nil {
		return err
	}
	var first error
	ok := false
	for _, d := range hostDirs() {
		if err := os.MkdirAll(d, 0o755); err != nil {
			first = errors.Join(first, err)
			continue
		}
		if err := os.WriteFile(filepath.Join(d, name+".json"), b, 0o644); err != nil {
			first = errors.Join(first, err)
			continue
		}
		ok = true
	}
	if !ok {
		return first
	}
	return nil
}

func osUnregister(name string) error {
	var errs error
	for _, d := range hostDirs() {
		if err := os.Remove(filepath.Join(d, name+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = errors.Join(errs, err)
		}
	}
	return errs
}

func osRegistered(name string) string {
	for _, d := range hostDirs() {
		p := filepath.Join(d, name+".json")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

const launcherName = "native-host.sh"

func launcher(exe, home string) []byte {
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	return []byte("#!/bin/sh\nexec " + q(exe) + " --home " + q(home) + " native-host \"$@\"\n")
}
