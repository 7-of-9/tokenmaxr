// Package fsx opens tool log files without blocking the tools that write them.
package fsx

import (
	"io"
	"os"
)

// Open opens path read-only. On Windows it also shares delete/rename access,
// so Claude Code, Codex and Grok can rotate or delete files we are reading.
func Open(path string) (*os.File, error) { return open(path) }

// ReadFile is os.ReadFile through Open: on Windows a reader (the app's
// status, `status` in a terminal) never blocks the tick's atomic rename of
// the file it is reading.
func ReadFile(path string) ([]byte, error) {
	f, err := Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// Rename is os.Rename; on Windows it retries briefly while another process
// (a scanner, an older reader) holds the target without delete sharing.
func Rename(from, to string) error { return rename(from, to) }
