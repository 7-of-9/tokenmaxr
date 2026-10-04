// Package lock is the cross-process collector.lock (LockFileEx on Windows,
// flock elsewhere), so only one tick runs at a time.
package lock

import (
	"errors"
	"os"
	"path/filepath"
	"time"
)

// ErrHeld means another process holds the lock.
var ErrHeld = errors.New("collector lock is held by another process")

type Lock struct{ f *os.File }

// TryAcquire takes the lock without waiting; it returns ErrHeld if busy.
func TryAcquire(path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	return &Lock{f: f}, nil
}

// Acquire polls until the lock is free or wait elapses.
func Acquire(path string, wait time.Duration) (*Lock, error) {
	deadline := time.Now().Add(wait)
	for {
		l, err := TryAcquire(path)
		if !errors.Is(err, ErrHeld) || time.Now().After(deadline) {
			return l, err
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// Held reports whether some process currently holds the lock.
func Held(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	l, err := TryAcquire(path)
	if err != nil {
		return errors.Is(err, ErrHeld)
	}
	l.Release()
	return false
}

func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	unlockFile(l.f)
	l.f.Close()
	l.f = nil
}
