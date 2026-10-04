package lock

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestExclusive(t *testing.T) {
	p := filepath.Join(t.TempDir(), "collector.lock")
	a, err := TryAcquire(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := TryAcquire(p); !errors.Is(err, ErrHeld) {
		t.Fatalf("second TryAcquire = %v, want ErrHeld", err)
	}
	if !Held(p) {
		t.Fatal("Held = false while locked")
	}
	start := time.Now()
	if _, err := Acquire(p, 300*time.Millisecond); !errors.Is(err, ErrHeld) || time.Since(start) < 250*time.Millisecond {
		t.Fatalf("Acquire did not wait and fail: %v", err)
	}
	a.Release()
	b, err := TryAcquire(p)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	b.Release()
	if Held(p) {
		t.Fatal("Held = true after release")
	}
}
