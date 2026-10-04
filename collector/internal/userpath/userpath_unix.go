//go:build !windows

// Package userpath puts <home>/bin on the user's PATH: HKCU\Environment\Path
// on Windows, a ~/.local/bin symlink on macOS.
package userpath

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
)

var exe = buildinfo.Product

func linkPath() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".local", "bin", exe), nil
}

// Contains reports whether the ~/.local/bin symlink points into dir.
func Contains(dir string) bool {
	l, err := linkPath()
	if err != nil {
		return false
	}
	t, err := os.Readlink(l)
	return err == nil && filepath.Clean(t) == filepath.Join(dir, exe)
}

// Add links ~/.local/bin/<product> to dir's binary. hint tells the user
// to add ~/.local/bin to PATH when it is not already there.
func Add(dir string) (hint string, err error) {
	l, err := linkPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(l), 0o755); err != nil {
		return "", err
	}
	target := filepath.Join(dir, exe)
	if t, err := os.Readlink(l); err != nil || t != target {
		if err := os.Remove(l); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		if err := os.Symlink(target, l); err != nil {
			return "", err
		}
	}
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.Clean(p) == filepath.Dir(l) {
			return "", nil
		}
	}
	shell := "~/.zshrc"
	if strings.HasSuffix(os.Getenv("SHELL"), "bash") {
		shell = "~/.bash_profile"
	}
	return "~/.local/bin is not on your PATH; add it with:\n  echo 'export PATH=\"$HOME/.local/bin:$PATH\"' >> " + shell, nil
}

// Remove deletes the symlink if it points into dir.
func Remove(dir string) error {
	l, err := linkPath()
	if err != nil {
		return err
	}
	if t, err := os.Readlink(l); err == nil && filepath.Clean(t) == filepath.Join(dir, exe) {
		return os.Remove(l)
	}
	return nil
}
