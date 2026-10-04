//go:build !windows

package fsx

import "os"

func open(path string) (*os.File, error) { return os.Open(path) }

// syncDir flushes a directory entry after a rename (best effort).
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}

func rename(from, to string) error { return os.Rename(from, to) }
