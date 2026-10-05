package app

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeNet is a network the test changes; it can fail a read, and its clock
// can jump (a sleep).
type fakeNet struct {
	mu    sync.Mutex
	addr  string
	fail  int           // reads left that fail
	queue []string      // values the next reads return, one each
	skew  time.Duration // added to the clock (a sleep)
	reads int
	// clock is virtual: each reading advances it by step, so a slow test
	// machine never looks like a sleep.
	clock time.Time
	step  time.Duration
}

func (f *fakeNet) sample() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.fail > 0 {
		f.fail--
		return "", errors.New("ENOBUFS")
	}
	if len(f.queue) > 0 {
		f.addr, f.queue = f.queue[0], f.queue[1:]
	}
	return f.addr, nil
}

func (f *fakeNet) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.clock.IsZero() {
		f.clock = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC) // no monotonic reading
	}
	f.clock = f.clock.Add(f.step)
	return f.clock.Add(f.skew)
}

func (f *fakeNet) do(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

// watchNet runs the watch on f and counts its syncs and their kind.
func watchNet(t *testing.T, f *fakeNet, poll, settle, gap time.Duration) (d *Desktop, syncs func() int) {
	t.Helper()
	a, _, _ := newTestApp(t)
	d = a.newDesktop(context.Background())
	t.Cleanup(d.cancel)
	f.do(func() { f.step = poll / 4 })
	var n atomic.Int32
	go func() {
		for {
			select {
			case <-d.ctx.Done():
				return
			case <-d.kick:
				if k := d.takePending(); k != tickRetry {
					t.Errorf("network sync of kind %d, want a retry", k)
				}
				n.Add(1)
			}
		}
	}()
	go d.netWatch(netWatcher{sample: f.sample, poll: poll, settle: settle, gap: gap, now: f.now})
	return d, func() int { return int(n.Load()) }
}

// A network change retries once it settles; no change, no sync.
func TestNetWatchSyncsOnChange(t *testing.T) {
	f := &fakeNet{addr: "en0=192.168.1.5/24"}
	_, syncs := watchNet(t, f, 5*time.Millisecond, 20*time.Millisecond, 0)
	time.Sleep(60 * time.Millisecond)
	if syncs() != 0 {
		t.Fatal("synced without a change")
	}
	f.do(func() { f.addr = "en0=10.0.0.7/24" })
	deadline := time.Now().Add(2 * time.Second)
	for syncs() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if syncs() != 1 {
		t.Fatalf("%d syncs after the network changed, want 1", syncs())
	}
}

// Changes seen on consecutive polls (an IPv4 lease, then IPv6) merge into
// one sync: the settle is longer than the poll (the production ratio).
func TestNetWatchMergesABurst(t *testing.T) {
	if netSettle <= netPoll {
		t.Fatalf("settle %v must be longer than the poll %v", netSettle, netPoll)
	}
	poll := 30 * time.Millisecond
	settle := poll * netSettle / netPoll
	f := &fakeNet{addr: "a"}
	_, syncs := watchNet(t, f, poll, settle, 0)
	time.Sleep(3 * poll)
	f.do(func() { f.queue = []string{"b", "c", "d", "e", "f"} })
	time.Sleep(5*poll + 3*settle)
	if n := syncs(); n != 1 {
		t.Fatalf("%d syncs for one burst, want 1", n)
	}
}

// A failed read is not "network gone": the next good read, unchanged,
// starts no sync.
func TestNetWatchIgnoresAFailedRead(t *testing.T) {
	f := &fakeNet{addr: "en0=192.168.1.5/24"}
	_, syncs := watchNet(t, f, 5*time.Millisecond, 10*time.Millisecond, 0)
	time.Sleep(20 * time.Millisecond)
	f.do(func() { f.fail = 2 })
	time.Sleep(80 * time.Millisecond)
	if syncs() != 0 {
		t.Fatalf("%d syncs after a failed read", syncs())
	}
}

// A flapping interface retries at most once per gap.
func TestNetWatchSpacesSyncs(t *testing.T) {
	f := &fakeNet{addr: "a"}
	_, syncs := watchNet(t, f, 5*time.Millisecond, 10*time.Millisecond, time.Hour)
	time.Sleep(20 * time.Millisecond)
	for i, v := range []string{"b", "a", "b"} {
		f.do(func() { f.addr = v })
		time.Sleep(60 * time.Millisecond)
		if syncs() != 1 {
			t.Fatalf("change %d: %d syncs, want 1 within the gap", i, syncs())
		}
	}
}

// Waking from sleep on the same network retries too: the wall clock jumped
// past the poll while the addresses stayed the same.
func TestNetWatchSyncsOnWake(t *testing.T) {
	f := &fakeNet{addr: "en0=192.168.1.5/24"}
	// A slow poll: a loaded test machine must not look like a wake (a
	// tick later than twice the poll).
	_, syncs := watchNet(t, f, 100*time.Millisecond, 150*time.Millisecond, 0)
	time.Sleep(350 * time.Millisecond)
	if syncs() != 0 {
		t.Fatal("synced without a wake")
	}
	f.do(func() { f.skew = time.Hour })
	deadline := time.Now().Add(3 * time.Second)
	for syncs() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if syncs() != 1 {
		t.Fatalf("%d syncs after a wake, want 1", syncs())
	}
}

func TestWoke(t *testing.T) {
	poll := 2 * time.Second
	now := time.Now()
	for _, c := range []struct {
		prev, now time.Time
		want      bool
	}{
		{now, now.Add(poll), false},
		{now, now.Add(2 * poll), false},
		{now, now.Add(3 * poll), true},           // the monotonic clock ran through a sleep (Windows)
		{now.Round(0), now.Add(time.Hour), true}, // the wall clock did (macOS, Linux)
	} {
		if got := woke(c.prev, c.now, poll); got != c.want {
			t.Errorf("woke(%v) = %v, want %v", c.now.Sub(c.prev), got, c.want)
		}
	}
}
