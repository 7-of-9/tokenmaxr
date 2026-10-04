package app

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/accounts"
	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
	"github.com/7-of-9/tokenmaxr/collector/internal/evidence"
	"github.com/7-of-9/tokenmaxr/collector/internal/lock"
	"github.com/7-of-9/tokenmaxr/collector/internal/outbox"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/scan"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
	"github.com/7-of-9/tokenmaxr/collector/internal/termfmt"
	"github.com/7-of-9/tokenmaxr/collector/internal/upload"
	"github.com/7-of-9/tokenmaxr/collector/internal/userpath"
)

// Run is one scheduled tick: non-blocking lock (exit quietly if another tick
// holds it), background priority, log to file only.
func (a *App) Run(ctx context.Context) error {
	if err := os.MkdirAll(a.Home, 0o700); err != nil {
		return err
	}
	lk, err := lock.TryAcquire(paths.Lock(a.Home))
	if errors.Is(err, lock.ErrHeld) {
		return nil
	}
	if err != nil {
		return err
	}
	defer lk.Release()
	LowPriority()
	if err := a.OpenLog(nil); err != nil {
		return err
	}
	defer a.Log.Close()
	_, err = a.Tick(ctx, TickOptions{})
	if errors.Is(err, ErrNotEnrolled) {
		return nil
	}
	if err != nil {
		a.Log.Printf("tick: %v", err)
	}
	return err
}

// SyncNow ticks in the foreground until the backfill is done and the outbox
// is empty, printing progress.
func (a *App) SyncNow(ctx context.Context, since string) error {
	b, err := scan.ParseBound(since)
	if err != nil {
		return err
	}
	lk, err := lock.Acquire(paths.Lock(a.Home), 2*time.Minute)
	if err != nil {
		return err
	}
	defer lk.Release()
	if err := a.OpenLog(a.Out); err != nil {
		return err
	}
	defer a.Log.Close()
	ob := outbox.New(paths.Outbox(a.Home))
	for i := 1; ; i++ {
		_, before := ob.Count()
		rep, err := a.Tick(ctx, TickOptions{Since: b, Force: true})
		if err != nil {
			return err
		}
		pending := 0
		for _, s := range rep.Scan.Sources {
			pending += s.Pending
		}
		files, events := ob.Count()
		line := fmt.Sprintf("tick %d: %d events queued, %d accepted, %d to retry", i, rep.Queued, rep.Upload.Accepted, rep.Upload.Retried)
		if rep.Upload.Rejected > 0 || rep.Upload.DeadLettered > 0 {
			line += fmt.Sprintf(", %d rejected, %d dead-lettered", rep.Upload.Rejected, rep.Upload.DeadLettered)
		}
		line += fmt.Sprintf("; %d files left to scan; outbox %d files / %d events", pending, files, events)
		if dfiles, devents := ob.DeadCount(); devents > 0 {
			line += fmt.Sprintf(" (dead-letter %d files / %d events)", dfiles, devents)
		}
		a.printf("%s\n", line)
		switch {
		case rep.Upload.Unauthorized:
			return errors.New("the server rejected this machine's token (401); re-run install with a fresh join code")
		case rep.Upload.Err != nil:
			return fmt.Errorf("upload failed: %w (events stay queued; the scheduled task will retry)", rep.Upload.Err)
		case rep.Scan.Complete && events == 0:
			a.printf("in sync\n")
			return nil
		case rep.Scan.Complete && rep.Queued == 0 && rep.Upload.Accepted == 0 && rep.Upload.Dropped == 0 && rep.Upload.DeadLettered == 0 && events >= before:
			// Nothing scanned, nothing taken and the outbox did not shrink:
			// looping would only repeat the same requests.
			return fmt.Errorf("no progress: %d events stay queued (the server keeps answering \"retry\"); the scheduled task keeps trying and moves an id to outbox/%s after %d attempts",
				events, outbox.DeadDir, upload.MaxAttempts)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

// ScanDryRun parses everything from offset 0 and prints totals; it reads and
// writes no collector state and uploads nothing.
func (a *App) ScanDryRun(asJSON bool, since, until string) error {
	sb, err := scan.ParseBound(since)
	if err != nil {
		return fmt.Errorf("--since: %w", err)
	}
	ub, err := scan.ParseBound(until)
	if err != nil {
		return fmt.Errorf("--until: %w", err)
	}
	host, _ := os.Hostname()
	// The homes list follows config.json (extraHomes, discoverWsl) when it
	// is readable; a dry run must still work on a machine with no config.
	cfg, cerr := store.LoadConfig(a.Home)
	if cerr != nil {
		fmt.Fprintf(os.Stderr, "warning: %v; scanning the OS home only\n", cerr)
		cfg = store.DefaultConfig()
		cfg.DiscoverWSL = false
	}
	hr := a.homes(&cfg)
	for _, n := range hr.Notes {
		fmt.Fprintf(os.Stderr, "warning: %s\n", n)
	}
	for _, d := range hr.Skipped {
		fmt.Fprintf(os.Stderr, "wsl: %s is not running, skipped (never started by the collector)\n", d)
	}
	// Attribution as a tick would do it, from evidence harvested afresh into
	// memory (nothing is written) and the live probe as of now. Without an
	// enrolled key a throwaway key hashes ids: only qualities are reported.
	k := dryRunKey(a.Home)
	now := a.Now()
	var recs []evidence.Record
	var spans []evidence.Span
	for _, h := range hr.Homes {
		res := evidence.Harvest(evidence.Options{Home: h.Path, HomeKey: h.Key(), CodexHome: h.CodexHome(a.CodexHome), Hash: hashFn(k)},
			map[string]evidence.Mark{})
		recs = append(recs, res.Records...)
		for _, o := range accounts.Probe(h.Path, h.CodexHome(a.CodexHome)) {
			spans = append(spans, evidence.Span{Provider: o.Provider, Home: h.Key(), Acct: accounts.Hash(k, o), From: now, To: now})
		}
	}
	ix := evidence.Build(evidence.Merge(nil, recs), spans)
	var hs []scan.Home
	for _, h := range hr.Homes {
		key := h.Key()
		env := &sources.Env{
			Home:      h.Path,
			CodexHome: h.CodexHome(a.CodexHome),
			Machine:   host,
			Attribute: func(provider string, ts time.Time, sessionID string, hint sources.Hint) (string, string) {
				return accounts.Resolve(nil, ix, key, provider, ts, sessionID, hint)
			},
			HashID:      hashFn(k),
			Label:       func(string) string { return "" },
			TZOffsetMin: TZOffsetMin,
			Prompts:     true,
		}
		hs = append(hs, scan.Home{Env: env, Sources: a.Sources()})
	}
	rep, err := scan.DryRunHomes(hs, sb, ub, now, os.Stderr)
	conflicts := ix.Conflicts()
	for _, st := range rep.Sources {
		st.Conflicts = conflicts[st.Provider]
	}
	if asJSON {
		enc := json.NewEncoder(a.Out)
		enc.SetIndent("", "  ")
		if e := enc.Encode(rep); e != nil {
			return e
		}
	} else {
		rep.Text(a.Out)
	}
	return err
}

// dryRunKey is the enrolled fleet key, or a throwaway one: a dry run only
// reports qualities, so any key keeps ids hashed.
func dryRunKey(home string) []byte {
	if sec, err := store.LoadSecrets(home); err == nil && sec.HasFleet() {
		return sec.Key()
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return []byte(buildinfo.Product + " dry run fallback")
	}
	return k
}

func ago(t time.Time, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t).Round(time.Second)
	s := t.Local().Format("2006-01-02 15:04:05")
	switch {
	case d < 0:
		return s + " (in the future)"
	case d < time.Minute:
		return fmt.Sprintf("%s (%ds ago)", s, int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%s (%dm ago)", s, int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%s (%.1fh ago)", s, d.Hours())
	}
	return fmt.Sprintf("%s (%dd ago)", s, int(d.Hours()/24))
}

// Doctor is status plus live checks (report-only), account detection,
// binaries, PATH, scheduler details, endpoint reachability and the log tail.
func (a *App) Doctor(ctx context.Context) error {
	if err := a.Status(); err != nil {
		return err
	}
	p := termfmt.New(a.Out)
	fmt.Fprintln(a.Out) // a blank line after the status report
	cfg, _ := store.LoadConfig(a.Home)
	sec, _ := store.LoadSecrets(a.Home)
	st, _ := store.LoadState(a.Home)
	ok := func(s string) string {
		switch s {
		case "ok", "fixed", "yes":
			return p.Good(s)
		case "", "missing", "no":
			return p.Bad(s)
		}
		return p.Warn(s)
	}

	p.Section("Homes " + p.Dim("(live discovery; a stopped WSL distro is never started)"))
	hr := a.homes(&cfg)
	for _, h := range hr.Homes {
		p.Item(h.Label(), 20, h.Path)
	}
	for _, d := range hr.Skipped {
		p.Item("wsl:"+d, 20, p.Dim("not running, skipped"))
	}
	for _, n := range hr.Notes {
		p.Line(p.Dim("· " + n))
	}

	p.Section("Config checks " + p.Dim("(live, nothing changed)"))
	res := a.runChecksIn(false, hr.Homes)
	for _, h := range hr.Homes {
		for _, k := range []string{"claudeRetention", "grokRetention", "codexHistory"} {
			key := checkKey(k, h)
			p.Item(key, 34, ok(res.Checks[key]))
		}
	}
	for _, n := range res.Notes {
		p.Line(p.Dim("· " + n))
	}

	p.Section("Accounts")
	found := 0
	for _, h := range hr.Homes {
		for _, o := range accounts.Probe(h.Path, h.CodexHome(a.CodexHome)) {
			found++
			v := fmt.Sprintf("%-10s", o.Provider)
			if k := sec.Key(); k != nil {
				acct := accounts.Hash(k, o)
				v += "  " + p.Dim(acct)
				if cfg.AccountLabels[acct] != "" {
					v += "  label set"
				}
				if cfg.PromptExcluded(acct) {
					v += "  " + p.Warn("prompts excluded")
				}
			} else {
				v += "  " + p.Dim("detected (hash needs set-up)")
			}
			p.Item(h.Label(), 20, v)
		}
	}
	if found == 0 {
		p.Line(p.Dim("none detected (no Claude/Codex/Grok/Cursor/Gemini login found)"))
	}
	p.Item("timeline", 20, count(len(st.Accounts), "interval", "intervals"))

	p.Section("Binaries")
	variants := []bool{false}
	if runtime.GOOS == "windows" {
		variants = []bool{false, true}
	}
	for _, gui := range variants {
		bp := a.binPath(gui)
		state := "missing"
		if fileExists(bp) {
			state = "ok"
		}
		p.Item(paths.ExeName(gui), 20, ok(state)+p.Dim("  "+bp))
	}
	onPath := "no"
	if userpath.Contains(paths.Bin(a.Home)) {
		onPath = "yes"
	}
	p.Item("bin on PATH", 20, ok(onPath))

	if lines := a.autostartSys().Describe(a.autostartOptions(&cfg, false)); len(lines) > 0 {
		p.Section("Autostart entries")
		for _, l := range lines {
			p.Line(l)
		}
	}

	p.Section("Server")
	if cfg.Server() == "" {
		p.Line(p.Dim("none (GitHub or local only)"))
	} else {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(cctx, http.MethodGet, strings.TrimRight(cfg.Server(), "/")+"/api/usage?days=1", nil)
		start := time.Now()
		if resp, err := http.DefaultClient.Do(req); err != nil {
			p.Item(cfg.Server(), 20, p.Bad("unreachable: "+err.Error()))
		} else {
			resp.Body.Close()
			code := fmt.Sprintf("HTTP %d", resp.StatusCode)
			if resp.StatusCode == http.StatusOK {
				code = p.Good(code)
			} else {
				code = p.Bad(code)
			}
			p.Item(cfg.Server(), 20, "GET /api/usage "+code+p.Dim(fmt.Sprintf(" in %dms", time.Since(start).Milliseconds())))
		}
	}

	p.Section("Log " + p.Dim(paths.Log(a.Home)))
	for _, l := range tail(paths.Log(a.Home), 8) {
		p.Line(p.Dim(l))
	}
	return nil
}

func tail(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > 64*1024 {
		f.Seek(-64*1024, io.SeekEnd)
	}
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > n {
			lines = lines[1:]
		}
	}
	return lines
}
