package instance

import (
	"errors"
	"slices"
	"testing"
	"time"
)

func TestHandOff(t *testing.T) {
	home := t.TempDir()
	// A request left for a process that died is not replayed to the next one.
	if err := Send(home, Show); err != nil {
		t.Fatal(err)
	}
	lk, err := Claim(home)
	if err != nil {
		t.Fatal(err)
	}
	if got := Take(home); len(got) != 0 {
		t.Fatalf("stale requests replayed: %v", got)
	}
	if !Running(home) {
		t.Fatal("Running = false while claimed")
	}
	// The second launch cannot claim; it leaves Show for the first.
	if _, err := Claim(home); !errors.Is(err, ErrRunning) {
		t.Fatalf("second Claim: %v", err)
	}
	Send(home, Show)
	Send(home, Restart)
	if got := Take(home); !slices.Equal(got, []Verb{Restart, Show}) {
		t.Fatalf("Take = %v", got)
	}
	if got := Take(home); len(got) != 0 {
		t.Fatalf("Take again = %v", got)
	}

	// Stop: the app sees Quit, lets go of the lock, Stop returns true.
	go func() {
		for {
			if v := Take(home); slices.Contains(v, Quit) {
				lk.Release()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	if !Stop(home, 5*time.Second) {
		t.Fatal("Stop: app still running")
	}
	if Running(home) {
		t.Fatal("Running after Stop")
	}
	if !Stop(home, time.Second) {
		t.Fatal("Stop with no app")
	}
}

func TestStopped(t *testing.T) {
	home := t.TempDir()
	if Stopped(home) {
		t.Fatal("stopped before any quit")
	}
	if err := MarkStopped(home); err != nil || !Stopped(home) {
		t.Fatalf("MarkStopped: %v", err)
	}
	ClearStopped(home)
	if Stopped(home) {
		t.Fatal("still stopped")
	}
}
