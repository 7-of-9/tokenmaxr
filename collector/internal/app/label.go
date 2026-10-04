package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
	"github.com/7-of-9/tokenmaxr/collector/internal/lock"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/outbox"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
	"github.com/7-of-9/tokenmaxr/collector/internal/tzinfo"
	"github.com/7-of-9/tokenmaxr/collector/internal/upload"
)

// maxLabelRunes matches the API's cleanLabel cap (MAX_LABEL_RUNES), so the
// label status shows is the label the public page shows.
const maxLabelRunes = 48

// labelTimeout bounds the heartbeat the label command sends (managed
// Functions can take 10-30 s to cold-start).
const labelTimeout = 45 * time.Second

// publicNote says where the machine label is shown.
const publicNote = "public: shown on your server's dashboard"

// CleanLabel normalises a machine label like the API's cleanLabel: control and
// bidi characters dropped, runs of whitespace collapsed, trimmed and capped at
// maxLabelRunes.
func CleanLabel(s string) string {
	var b strings.Builder
	space := false
	n := 0
	for _, r := range strings.TrimSpace(s) {
		switch {
		case unicode.IsSpace(r):
			space = true
			continue
		case unicode.IsControl(r) || r == unicode.ReplacementChar || isBidiControl(r):
			continue
		}
		if n >= maxLabelRunes {
			break
		}
		if space && n > 0 {
			if n+1 >= maxLabelRunes {
				break
			}
			b.WriteByte(' ')
			n++
		}
		space = false
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// isBidiControl reports the bidi embedding, override and isolate controls,
// which could make a public label render as something else.
func isBidiControl(r rune) bool {
	return (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069)
}

// defaultLabel is the hostname, the label suggested at install.
func defaultLabel() string {
	host, _ := os.Hostname()
	return CleanLabel(host)
}

// terminal is where install asks its questions.
type terminal struct {
	in      *bufio.Reader
	out     io.Writer
	closers []func() error
}

func (t *terminal) Close() {
	for _, c := range t.closers {
		c()
	}
}

// openTerminal returns the interactive terminal, if there is one; tests
// replace it.
var openTerminal = openConsole

// askLabel asks for the public machine label, defaulting to def on an empty
// answer or end of input.
func askLabel(t *terminal, def string) string {
	for range 3 {
		fmt.Fprintf(t.out, "Public name for this machine (%s) [%s]: ", publicNote, def)
		line, err := t.in.ReadString('\n')
		raw := strings.TrimSpace(line)
		if raw == "" {
			if err != nil {
				fmt.Fprintln(t.out)
			}
			return def
		}
		if l := CleanLabel(raw); l != "" {
			if l != raw {
				fmt.Fprintf(t.out, "  using %q\n", l)
			}
			return l
		}
		if err != nil {
			break
		}
	}
	return def
}

// machineTZ is this machine's time zone for the heartbeat.
func machineTZ() *model.TZInfo {
	info := tzinfo.Detect()
	return &model.TZInfo{IANA: info.IANA, WindowsID: info.WindowsID, Country: info.Country, Source: info.Source}
}

// machineCountry is the country of this machine's time zone ("" unknown),
// published to GitHub only on opt-in (replaced in tests).
var machineCountry = func() string { return tzinfo.Detect().Country }

// tzLine renders the time zone for status and doctor.
func tzLine(tz *model.TZInfo) string {
	s := tz.IANA
	if s == "" {
		s = "unknown"
	}
	cc := tz.Country
	if cc == "" {
		cc = "no country"
	}
	s += " · " + cc + " (" + tz.Source
	if tz.WindowsID != "" {
		s += `, Windows "` + tz.WindowsID + `"`
	}
	return s + ")"
}

// SetLabel renames this machine: it saves config.json, then sends a
// heartbeat carrying the new label right away. The heartbeat is best
// effort; if it fails the next tick sends it.
func (a *App) SetLabel(ctx context.Context, name string) error {
	label := CleanLabel(name)
	if label == "" {
		return errors.New("the label needs at least one visible character")
	}
	if err := os.MkdirAll(a.Home, 0o700); err != nil {
		return err
	}
	// A tick saves config.json too (new account labels): wait for it.
	lk, err := lock.Acquire(paths.Lock(a.Home), 2*time.Minute)
	if err != nil {
		return fmt.Errorf("waiting for a running tick: %w", err)
	}
	defer lk.Release()
	cfg, err := store.LoadConfig(a.Home)
	if err != nil {
		return err
	}
	old := cfg.MachineLabel
	cfg.MachineLabel = label
	if err := store.SaveConfig(a.Home, cfg); err != nil {
		return err
	}
	a.Log.Printf("label: machine label changed")
	if old != label {
		a.printf("machine label is now %q (was %q), %s\n", label, old, publicNote)
	} else {
		a.printf("machine label is %q, %s\n", label, publicNote)
	}

	sec, err := store.LoadSecrets(a.Home)
	if err != nil {
		return err
	}
	if !serverOn(&cfg, sec) {
		if githubEnabled(&cfg, sec) {
			a.printf("no server; on GitHub this machine is %q (change: "+buildinfo.Product+" github login --label NAME)\n", cfg.GitHub.Label)
		}
		return nil
	}
	st, err := store.LoadState(a.Home)
	if err != nil {
		a.Log.Printf("state: %v", err)
	}
	hb := a.heartbeat(&cfg, st, outbox.New(paths.Outbox(a.Home)))
	hctx, cancel := context.WithTimeout(ctx, labelTimeout)
	defer cancel()
	c := upload.NewClient(cfg.Server(), sec.Token, a.Version)
	if err := c.SendHeartbeat(hctx, a.Version, a.Now().UTC(), hb, statePaths(st)); err != nil {
		// Due now: the next tick carries the new label.
		st.LastHeartbeat = time.Time{}
		a.Log.Printf("label: heartbeat: %v", err)
		a.printf("could not reach %s (%v); the next tick sends it\n", cfg.Server(), err)
	} else {
		st.LastHeartbeat = a.Now()
		a.printf("sent to %s\n", cfg.Server())
	}
	return store.SaveState(a.Home, st)
}
