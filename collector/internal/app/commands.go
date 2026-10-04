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
	"slices"
	"strings"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/accounts"
	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
	"github.com/7-of-9/tokenmaxr/collector/internal/configfix"
	"github.com/7-of-9/tokenmaxr/collector/internal/evidence"
	"github.com/7-of-9/tokenmaxr/collector/internal/lock"
	"github.com/7-of-9/tokenmaxr/collector/internal/outbox"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/scan"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
	"github.com/7-of-9/tokenmaxr/collector/internal/tray"
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

// Status prints collector health from local files only.
func (a *App) Status() error {
	now := a.Now()
	cfg, cerr := store.LoadConfig(a.Home)
	sec, _ := store.LoadSecrets(a.Home)
	st, serr := store.LoadState(a.Home)
	w := a.Out
	fmt.Fprintf(w, buildinfo.Product+" %s (%s/%s)\n", a.Version, runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(w, "state dir     %s\n", a.Home)
	if cerr != nil {
		fmt.Fprintf(w, "config        ERROR: %v\n", cerr)
	}
	if serr != nil {
		fmt.Fprintf(w, "state         %v\n", serr)
	}
	switch {
	case cfg.Server() == "":
		fmt.Fprintf(w, "server        none (GitHub or local only)\n")
	case sec.Enrolled():
		fmt.Fprintf(w, "server        %s, enrolled as %s\n", cfg.Server(), sec.MachineID)
	default:
		fmt.Fprintf(w, "server        %s, not enrolled: run `"+buildinfo.Product+" install`\n", cfg.Server())
	}
	if !sec.HasFleet() {
		fmt.Fprintf(w, "set up        no: run `"+buildinfo.Product+" install`\n")
	}
	a.printGitHubStatus(cfg, sec, st, now)
	fmt.Fprintf(w, "machine       %s (%s; rename: "+buildinfo.Product+" label NEW_NAME)\n", cfg.MachineLabel, publicNote)
	fmt.Fprintf(w, "time zone     %s\n", tzLine(machineTZ()))
	fmt.Fprintf(w, "prompts       %s", onOff(cfg.Prompts))
	if n := len(cfg.PromptExcludeAccts); n > 0 {
		fmt.Fprintf(w, " (%d accounts excluded)", n)
	}
	fmt.Fprintln(w)
	running := ""
	if lock.Held(paths.Lock(a.Home)) {
		running = "  [a tick is running now]"
	}
	fmt.Fprintf(w, "last tick     %s, took %.1fs%s\n", ago(st.LastTick, now), float64(st.LastTickMs)/1000, running)
	fmt.Fprintf(w, "last upload   %s\n", ago(st.LastUploadOK, now))
	if history := st.AccountHistory; !history.LastAttempt.IsZero() {
		if history.LastError != "" {
			fmt.Fprintf(w, "Codex history %s (checked %s; local collection continues)\n", history.LastError, ago(history.LastAttempt, now))
		} else {
			fmt.Fprintf(w, "Codex history %d UTC days, %s account tokens; read %s (automatic every 15m)\n", history.Days, tray.Compact(history.TotalTokens), ago(history.LastSuccess, now))
		}
	}
	if st.Unauthorized {
		fmt.Fprintf(w, "uploads       PAUSED: token rejected (401) at %s; collection continues. Re-run install with a fresh join code.\n", ago(st.UnauthorizedAt, now))
	} else if st.LastUploadErr != "" {
		fmt.Fprintf(w, "upload error  %s\n", st.LastUploadErr)
	}
	if st.Backoff.Failures > 0 {
		fmt.Fprintf(w, "backoff       %d failures, next try %s, batch limit %d\n", st.Backoff.Failures, st.Backoff.Until.Local().Format("15:04:05"), st.Backoff.Limit)
	}
	ob := outbox.New(paths.Outbox(a.Home))
	files, events := ob.Count()
	fmt.Fprintf(w, "outbox        %d files, %d events\n", files, events)
	if dfiles, devents := ob.DeadCount(); devents > 0 {
		fmt.Fprintf(w, "dead-letter   %d files, %d events (outbox/%s: rejected by the server or retried %d times; inspect or delete by hand)\n", dfiles, devents, outbox.DeadDir, upload.MaxAttempts)
	}
	fmt.Fprintf(w, "heartbeat     %s (clock skew %dms)\n", ago(st.LastHeartbeat, now), st.ClockSkewMs)
	fmt.Fprintf(w, "sources\n")
	names := make([]string, 0, len(st.Sources))
	for n := range st.Sources {
		names = append(names, n)
	}
	slices.Sort(names)
	if len(names) == 0 {
		fmt.Fprintf(w, "  (not scanned yet)\n")
	}
	for _, n := range names {
		s := st.Sources[n]
		line := fmt.Sprintf("  %-12s %6d files", n, s.Files)
		if !cfg.SourceEnabled(n) {
			line += "  disabled"
		}
		if s.LastEventTS != nil {
			line += "  last event " + ago(*s.LastEventTS, now)
		}
		if s.Pending > 0 {
			line += fmt.Sprintf("  [backfill: %d files left]", s.Pending)
		}
		if s.LastError != "" {
			line += "  error: " + s.LastError
		}
		fmt.Fprintln(w, line)
	}
	fmt.Fprintf(w, "homes         (scan roots of the last tick)\n")
	if len(st.Homes) == 0 {
		fmt.Fprintf(w, "  (not scanned yet)\n")
	}
	for _, h := range st.Homes {
		fmt.Fprintln(w, homeLine(h))
	}
	for _, d := range st.WSLSkipped {
		fmt.Fprintln(w, skippedLine(d))
	}
	fmt.Fprintf(w, "evidence      %s\n", evidenceLine(st))
	fmt.Fprintf(w, "checks        (%s)\n", ago(st.LastConfigCheck, now))
	keys := make([]string, 0, len(st.Checks))
	for k := range st.Checks {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "  %-16s %s\n", k, st.Checks[k])
	}
	fmt.Fprintf(w, "autostart     %s\n", a.autostartLine(&cfg))
	fmt.Fprintf(w, "app           %s\n", a.appLine(&cfg))
	if sac, detail, ok := configfix.SmartAppControl(); ok {
		fmt.Fprintln(w, strings.TrimSpace("smart app ctl "+sac+" "+detail))
	}
	upd := "update check  " + ago(st.LastUpdateCheck, now)
	if a.Version == "dev" {
		upd += " (dev build: never self-updates)"
	}
	if st.LastUpdateErr != "" {
		upd += "; error: " + st.LastUpdateErr
	}
	fmt.Fprintln(w, upd)
	return nil
}

// Doctor is status plus live checks (report-only), account detection,
// binaries, PATH, scheduler details, endpoint reachability and the log tail.
func (a *App) Doctor(ctx context.Context) error {
	if err := a.Status(); err != nil {
		return err
	}
	w := a.Out
	cfg, _ := store.LoadConfig(a.Home)
	sec, _ := store.LoadSecrets(a.Home)
	st, _ := store.LoadState(a.Home)

	fmt.Fprintf(w, "\nhomes (live discovery; a stopped WSL distro is never started)\n")
	hr := a.homes(&cfg)
	for _, h := range hr.Homes {
		fmt.Fprintf(w, "  %-18s %s\n", h.Label(), h.Path)
	}
	for _, d := range hr.Skipped {
		fmt.Fprintln(w, skippedLine(d))
	}
	for _, n := range hr.Notes {
		fmt.Fprintf(w, "  - %s\n", n)
	}

	fmt.Fprintf(w, "live config checks (no changes made)\n")
	res := a.runChecksIn(false, hr.Homes)
	for _, h := range hr.Homes {
		for _, k := range []string{"claudeRetention", "grokRetention", "codexHistory"} {
			key := checkKey(k, h)
			fmt.Fprintf(w, "  %-40s %s\n", key, res.Checks[key])
		}
	}
	for _, n := range res.Notes {
		fmt.Fprintf(w, "  - %s\n", n)
	}

	fmt.Fprintf(w, "accounts\n")
	found := 0
	for _, h := range hr.Homes {
		for _, o := range accounts.Probe(h.Path, h.CodexHome(a.CodexHome)) {
			found++
			line := "  " + h.Label() + "  " + o.Provider
			if k := sec.Key(); k != nil {
				acct := accounts.Hash(k, o)
				line += "  " + acct
				if cfg.AccountLabels[acct] != "" {
					line += "  (label set)"
				}
				if cfg.PromptExcluded(acct) {
					line += "  (prompts excluded)"
				}
			} else {
				line += "  detected (hash needs enrollment)"
			}
			fmt.Fprintln(w, line)
		}
	}
	if found == 0 {
		fmt.Fprintf(w, "  none detected (no Claude/Codex/Grok/Cursor/Gemini login found)\n")
	}
	fmt.Fprintf(w, "  timeline: %d intervals\n", len(st.Accounts))

	fmt.Fprintf(w, "binaries\n")
	variants := []bool{false}
	if runtime.GOOS == "windows" {
		variants = []bool{false, true}
	}
	bins := []string{}
	for _, gui := range variants {
		bins = append(bins, a.binPath(gui))
	}
	for _, p := range bins {
		state := "missing"
		if fileExists(p) {
			state = "ok"
		}
		fmt.Fprintf(w, "  %-40s %s\n", p, state)
	}
	onPath := "no"
	if userpath.Contains(paths.Bin(a.Home)) {
		onPath = "yes"
	}
	fmt.Fprintf(w, "  bin on PATH: %s\n", onPath)

	if lines := a.autostartSys().Describe(a.autostartOptions(&cfg, false)); len(lines) > 0 {
		fmt.Fprintf(w, "autostart entries\n")
		for _, l := range lines {
			fmt.Fprintf(w, "  %s\n", l)
		}
	}

	fmt.Fprintf(w, "server\n")
	if cfg.Server() == "" {
		fmt.Fprintf(w, "  none (GitHub or local only)\n")
	} else {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(cctx, http.MethodGet, strings.TrimRight(cfg.Server(), "/")+"/api/usage?days=1", nil)
		start := time.Now()
		if resp, err := http.DefaultClient.Do(req); err != nil {
			fmt.Fprintf(w, "  unreachable: %v\n", err)
		} else {
			resp.Body.Close()
			fmt.Fprintf(w, "  GET /api/usage: HTTP %d in %dms\n", resp.StatusCode, time.Since(start).Milliseconds())
		}
	}

	fmt.Fprintf(w, "log tail (%s)\n", paths.Log(a.Home))
	for _, l := range tail(paths.Log(a.Home), 8) {
		fmt.Fprintf(w, "  %s\n", l)
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
