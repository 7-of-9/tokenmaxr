package scan_test

import (
	"os"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/logx"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/scan"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/claude"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/codex"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/cursor"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/gemini"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources/grok"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

// TestRealDataStateSize runs a full backfill scan of this machine's real
// logs the way a tick does, discarding the events (no outbox, no upload) and
// saving state.json in a temp state dir, then logs only its size and the
// cursor carry per source. Run with D0M1_REALDATA=1.
func TestRealDataStateSize(t *testing.T) {
	if os.Getenv("D0M1_REALDATA") != "1" {
		t.Skip("set D0M1_REALDATA=1 to scan this machine's real logs")
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	env := &sources.Env{
		Home:        userHome,
		Machine:     "realdata",
		Attribute:   func(string, time.Time, string, sources.Hint) (string, string) { return "", model.AcctUnknown },
		Label:       func(string) string { return "" },
		TZOffsetMin: func(ts time.Time) int { _, off := ts.In(time.Local).Zone(); return off / 60 },
		Prompts:     true,
	}
	st := store.NewState()
	tick := func() {
		t.Helper()
		_, err := scan.Run(scan.Options{
			Sources: []sources.Source{claude.New(), codex.New(), grok.New(), cursor.New(), gemini.New()},
			Env:     env,
			Log:     logx.Discard(),
			Now:     time.Now,
			Flush:   func(sources.Batch) error { return nil },
			Save:    func() error { return store.SaveState(stateDir, st) },
		}, st.Cursors)
		if err != nil {
			t.Fatal(err)
		}
	}
	report := func(label string) {
		t.Helper()
		fi, err := os.Stat(paths.State(stateDir))
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: state.json %d bytes", label, fi.Size())
		for name, cs := range st.Cursors {
			carried, bytes, idle := 0, 0, 0
			for _, c := range cs {
				if len(c.Carry) > 0 {
					carried++
					bytes += len(c.Carry)
				}
				if c.Idle {
					idle++
				}
			}
			t.Logf("%s:   %-12s cursors=%d withCarry=%d carryBytes=%d idle=%d", label, name, len(cs), carried, bytes, idle)
		}
	}
	tick()
	report("backfill")
	tick()
	report("next tick")
}
