package homes

import (
	"os"
	"testing"
	"time"
)

// TestRealDiscovery runs the platform WSL discovery once and logs how long
// the two wsl.exe listings take (they run every tick), how many distros are
// running or skipped, and how many homes were found. It reads inside a
// distro only if that distro is already running; it never starts one. Run
// with D0M1_REALDATA=1.
func TestRealDiscovery(t *testing.T) {
	if os.Getenv("D0M1_REALDATA") != "1" {
		t.Skip("set D0M1_REALDATA=1 to run WSL discovery on this machine")
	}
	w := DefaultWSL()
	if w == nil {
		t.Skip("no wsl.exe on this machine")
	}
	for i := range 3 {
		start := time.Now()
		found, skipped, notes := w.Discover()
		t.Logf("run %d: %s, homes=%d running-with-homes, skipped=%d (%v), notes=%d", i+1, time.Since(start).Round(time.Millisecond), len(found), len(skipped), skipped, len(notes))
		for _, h := range found {
			t.Logf("  home %s (%s)", h.Path, h.Label())
		}
	}
}
