package app

import (
	"net"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The network watch (owner direction 2026-10-05: "make it detect network
// changes and retry sync immediately upon network changes"): the app
// samples the machine's network addresses, and when they change (Wi-Fi
// joined or lost, a cable, a VPN) or the machine wakes from sleep, it
// retries at once what failed (Desktop.Retry: the upload despite a backoff,
// a failed GitHub publish), once the network has settled so DNS and routes
// are up. No OS notification API is needed: one sample per poll is cheap
// (netAddrs; on Windows a single GetAdaptersAddresses call).

const (
	// netPoll is how often the addresses are sampled.
	netPoll = 2 * time.Second
	// netSettle is how long the addresses must stay the same before the
	// sync. Longer than the poll, so changes seen on consecutive polls (an
	// IPv4 lease, then the IPv6 addresses) merge into one sync.
	netSettle = 3 * time.Second
	// netGap is the least time between two syncs the watch starts, so a
	// flapping interface cannot override the backoff every few seconds.
	netGap = 30 * time.Second
)

// netWatcher is the network watch's settings (tests shorten them).
type netWatcher struct {
	// sample is the machine's network identity (netAddrs); an error skips
	// the poll, so a failed read never looks like the network going away.
	sample            func() (string, error)
	poll, settle, gap time.Duration
	now               func() time.Time
}

func defaultNetWatcher() netWatcher {
	return netWatcher{sample: netAddrs, poll: netPoll, settle: netSettle, gap: netGap, now: time.Now}
}

// netKey is one address in the sample: the interface and its address.
func netKey(ifc string, ip net.IP, prefix int) string {
	return ifc + "=" + ip.String() + "/" + strconv.Itoa(prefix)
}

// netJoin is the sample: every address, sorted.
func netJoin(out []string) string {
	slices.Sort(out)
	return strings.Join(out, ",")
}

// woke reports whether the gap between two polls means the machine slept:
// the wall clock runs through a sleep (Go's monotonic clock stops on macOS
// and Linux, and runs on Windows), so either clock moving more than twice
// the poll is a wake. A process starved that long is rare, and a sync then
// is harmless.
func woke(prev, now time.Time, poll time.Duration) bool {
	wall := now.Round(0).Sub(prev.Round(0))
	mono := now.Sub(prev)
	return max(wall, mono) > 2*poll
}

// netWatch retries a sync when the network changes or the machine wakes,
// until the app stops.
func (d *Desktop) netWatch(w netWatcher) {
	last, err := w.sample()
	known := err == nil // a first sample that failed sets no baseline
	prev := w.now()
	var lastSync time.Time
	t := time.NewTicker(w.poll)
	defer t.Stop()
	due := time.NewTimer(time.Hour)
	due.Stop()
	arm := func(after time.Duration) { due.Reset(after) }
	defer due.Stop()
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-t.C:
			now := w.now()
			wake := woke(prev, now, w.poll)
			prev = now
			cur, err := w.sample()
			changed := err == nil && known && cur != last
			if err == nil {
				last, known = cur, true
			}
			if wake {
				d.a.Log.Printf("app: woke from sleep")
			}
			if changed || wake {
				arm(w.settle) // a later change restarts the settle
			}
		case <-due.C:
			if known && last == "" {
				d.a.Log.Printf("app: network gone")
				d.Refresh()
				continue
			}
			if now := w.now(); !lastSync.IsZero() && now.Sub(lastSync) < w.gap {
				arm(w.gap - now.Sub(lastSync))
				continue
			}
			lastSync = w.now()
			d.a.Log.Printf("app: network changed: retrying now")
			d.Retry()
		}
	}
}
