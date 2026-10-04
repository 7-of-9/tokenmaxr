package app

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"time"
	"unicode/utf8"

	"github.com/7-of-9/tokenmaxr/collector/internal/configfix"
	"github.com/7-of-9/tokenmaxr/collector/internal/evidence"
	"github.com/7-of-9/tokenmaxr/collector/internal/homes"
	"github.com/7-of-9/tokenmaxr/collector/internal/instance"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/outbox"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/recent"
	"github.com/7-of-9/tokenmaxr/collector/internal/rollup"
	"github.com/7-of-9/tokenmaxr/collector/internal/scan"
	"github.com/7-of-9/tokenmaxr/collector/internal/selfupdate"
	"github.com/7-of-9/tokenmaxr/collector/internal/sources"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
	"github.com/7-of-9/tokenmaxr/collector/internal/upload"
)

// Tick timing (SPEC: hard limit 50 s; the backfill continues across ticks).
const (
	TickBudget        = 50 * time.Second
	uploadReserve     = 15 * time.Second
	configEvery       = time.Hour
	heartbeatEvery    = 5 * time.Minute
	updateEvery       = time.Hour
	fullReparseEvery  = 7 * 24 * time.Hour
	unauthorizedRetry = 6 * time.Hour
	// harvestReserve keeps time for the scan after the evidence harvest.
	harvestReserve   = 15 * time.Second
	outboxFileEvents = 2000
	flushEvery       = 5 * time.Second
	// maxPromptJSON keeps one prompt record well under the 512 KB prompt cap
	// once JSON-escaped.
	maxPromptJSON = 480 * 1024
)

type TickProgress struct {
	Phase  string
	Upload upload.Progress
	// Recent is an owned snapshot of local totals, published during scanning
	// and before any upload. Upload success never gates the provider rows.
	Recent *recent.Window
}

type TickOptions struct {
	Progress func(TickProgress)
	Budget   time.Duration
	// Since drops events before it (sync-now --since; tests only, because
	// cursors still advance past the dropped events).
	Since *scan.Bound
	// Force uploads despite backoff or an earlier 401 (sync-now).
	Force bool
}

type TickReport struct {
	Scan          scan.Stats
	Queued        int
	Upload        upload.Result
	UploadSkipped string
	Updated       string
	// State is state.json as the tick saved it (the desktop app reads its
	// status and local numbers from it).
	State *store.State
}

// Tick runs one collection cycle. The caller holds the lock.
func (a *App) Tick(ctx context.Context, o TickOptions) (TickReport, error) {
	var rep TickReport
	progress := func(phase string, up upload.Progress) {
		if o.Progress != nil {
			o.Progress(TickProgress{Phase: phase, Upload: up})
		}
	}
	progress("scanning", upload.Progress{})
	start := a.Now()
	if o.Budget <= 0 {
		o.Budget = TickBudget
	}
	deadline := start.Add(o.Budget)
	bin := paths.Bin(a.Home)
	selfupdate.CleanOld(bin)

	cfg, err := store.LoadConfig(a.Home)
	if err != nil {
		return rep, err
	}
	sec, err := store.LoadSecrets(a.Home)
	if err != nil {
		return rep, err
	}
	if !sec.HasFleet() {
		return rep, ErrNotEnrolled
	}
	st, err := store.LoadState(a.Home)
	if err != nil {
		a.Log.Printf("state: %v", err)
	}
	// The desktop app's local numbers live in state.json and are saved
	// with the cursors of the events they count.
	win := st.RecentWindow()
	win.Prune(start)
	// GitHub publishing reads the full-history rollup, fed by the same
	// durable flush as the outbox and the local numbers.
	var ru *rollup.Rollup
	if githubEnabled(&cfg, sec) {
		if ru, err = rollup.Load(paths.Rollup(a.Home)); err != nil {
			a.Log.Printf("rollup: %v; rebuilding it from all history", err)
			ru = rollup.New()
			st.Cursors = map[string]map[string]store.FileCursor{}
		}
	}
	// Only a server destination drains the outbox; without one nothing is
	// queued for it (adding a server later re-reads history).
	var ob *outbox.Outbox
	if serverOn(&cfg, sec) {
		ob = outbox.New(paths.Outbox(a.Home))
	}

	// Scan roots: the OS home, extra homes and running WSL distros. A
	// stopped distro is listed as skipped and never touched.
	hr := a.homes(&cfg)
	for _, n := range hr.Notes {
		a.Log.Printf("homes: %s", n)
	}
	if len(hr.Homes) > 1 || len(hr.Skipped) > 0 {
		a.Log.Printf("homes: %d scanned, %d wsl distros not running", len(hr.Homes), len(hr.Skipped))
	}

	// Config checks and fixes: on a process's first tick, then hourly.
	if !a.checked || start.Sub(st.LastConfigCheck) >= configEvery {
		a.runChecks(&cfg, st, cfg.FixConfig, hr.Homes)
		st.LastConfigCheck = start
		a.checked = true
	}

	// Accounts, per home: the live probe, then the harvested evidence,
	// bounded by watermarks and a budget (SPEC "Accounts").
	cfgChanged := a.probeAccounts(sec.Key(), &cfg, st, start, hr.Homes)
	harvested, labelled := a.harvest(sec.Key(), &cfg, st, hr.Homes, deadline.Add(-uploadReserve-harvestReserve))
	if cfgChanged || labelled {
		if err := store.SaveConfig(a.Home, cfg); err != nil {
			a.Log.Printf("config: save: %v", err)
		}
	}
	if !harvested {
		a.Log.Printf("accounts: evidence harvest continues next tick; scan deferred")
	} else if st.AttribVersion < evidence.AttribVersion {
		// Stronger attribution: re-read history once so the server upgrades
		// every event in place (its merge keeps the higher acctQ).
		a.Log.Printf("accounts: attribution v%d, re-reading history once", evidence.AttribVersion)
		st.Cursors = map[string]map[string]store.FileCursor{}
		st.AttribVersion = evidence.AttribVersion
		st.LastFullReparse = start
	}

	// Weekly full reparse (the first backfill counts as the first one).
	if st.LastFullReparse.IsZero() {
		st.LastFullReparse = start
	} else if start.Sub(st.LastFullReparse) >= fullReparseEvery {
		a.Log.Printf("scan: weekly full reparse")
		st.Cursors = map[string]map[string]store.FileCursor{}
		st.LastFullReparse = start
	}

	// Fresh quota meters from the installed clients, just before the scan
	// reads them, so a provider's new reading uploads this tick.
	a.refreshQuotas(ctx, &cfg, st, deadline.Add(-uploadReserve))

	// Scan, writing each batch to the outbox before its cursors are saved.
	// The scan waits until the evidence is complete, so history is never
	// attributed from half of it.
	if harvested {
		// Files inside a distro that is not running keep their cursors: they
		// cannot have changed, and reading them would boot the distro.
		keep := hr.Unreachable
		stats, err := scan.Run(scan.Options{
			Homes: a.scanHomes(sec.Key(), &cfg, st, hr.Homes),
			KeepCursor: func(p string) bool {
				for _, pre := range keep {
					if homes.Under(p, pre) {
						return true
					}
				}
				return false
			},
			Deadline: deadline.Add(-uploadReserve),
			Log:      a.Log,
			Now:      a.Now,
			Flush: func(b sources.Batch) error {
				n, err := a.persist(ob, &cfg, o.Since, b, win, ru)
				rep.Queued += n
				return err
			},
			Save: func() error {
				if err := store.SaveState(a.Home, st); err != nil {
					return err
				}
				if o.Progress != nil {
					o.Progress(TickProgress{Phase: "scanning", Recent: win.Clone()})
				}
				return nil
			},
			FlushEvents: outboxFileEvents,
			FlushEvery:  flushEvery,
			LimitsSent:  st.LimitsSent(),
		}, st.Cursors)
		rep.Scan = stats
		if err != nil {
			// The outbox or state could not be written: stop before uploading
			// so nothing runs ahead of what is durably recorded.
			a.Log.Printf("scan: %v", err)
			return rep, err
		}
		if ru != nil {
			ru.Settle(a.Now(), stats.Complete)
			if ru.Dirty() {
				if err := ru.Save(paths.Rollup(a.Home)); err != nil {
					return rep, err
				}
			}
		}
		st.LastScan = a.Now()
		st.Homes = st.Homes[:0]
		for _, h := range hr.Homes {
			st.Homes = append(st.Homes, store.HomeState{Path: h.Path, Kind: h.Kind, Distro: h.Distro, Files: stats.HomeFiles[h.Path]})
		}
		st.WSLSkipped = hr.Skipped
		for name, ss := range stats.Sources {
			prev := st.Sources[name]
			cur := store.SourceState{Files: ss.Files, Pending: ss.Pending, LastEventTS: prev.LastEventTS, LastError: ss.Err}
			if !ss.LastEventTS.IsZero() && (prev.LastEventTS == nil || ss.LastEventTS.After(*prev.LastEventTS)) {
				t := ss.LastEventTS.UTC()
				cur.LastEventTS = &t
			}
			st.Sources[name] = cur
		}
		if rep.Queued > 0 || !stats.Complete {
			a.Log.Printf("scan: %d events queued, complete=%v", rep.Queued, stats.Complete)
		}
		if o.Progress != nil {
			o.Progress(TickProgress{Phase: "scanning", Recent: win.Clone()})
		}
	}

	// Account totals supplement file history. Read after local scanning so the
	// desktop's provider summary is available before any provider network wait.
	// Its own bounded deadline always reserves the normal upload budget.
	if ob != nil {
		n, err := a.collectAccountUsage(ctx, &cfg, sec.Key(), st, ob, deadline.Add(-uploadReserve), func() { progress("account-history", upload.Progress{}) })
		rep.Queued += n
		if err != nil {
			return rep, err // durable outbox failure, not a provider read failure
		}
	}

	// Destinations. The private server (HTTP: cfg.Endpoint) takes the full
	// outbox, with the heartbeat every 5 minutes (and on the first tick).
	// A machine with a fleet key but no server enrolment (GitHub-only or
	// local-only) skips it.
	if !serverOn(&cfg, sec) {
		rep.UploadSkipped = "no server destination"
	} else {
		var hb *model.Heartbeat
		if start.Sub(st.LastHeartbeat) >= heartbeatEvery {
			hb = a.heartbeat(&cfg, st, ob)
		}
		switch {
		case st.Unauthorized && !o.Force && start.Sub(st.UnauthorizedAt) < unauthorizedRetry:
			rep.UploadSkipped = "token rejected (401); re-run install with a new join code"
		case start.Before(st.Backoff.Until) && !o.Force:
			rep.UploadSkipped = "backing off until " + st.Backoff.Until.Format(time.RFC3339)
		default:
			up := &upload.Uploader{
				Client:  upload.NewClient(cfg.Server(), sec.Token, a.Version),
				Outbox:  ob,
				Log:     a.Log,
				Version: a.Version,
				Now:     a.Now,
				Sleep:   time.Sleep,
				Homes:   homes.Paths(hr.Homes),
			}
			if o.Progress != nil {
				up.Progress = func(p upload.Progress) { progress("uploading", p) }
			}
			res := up.Run(ctx, deadline, &st.Backoff, hb)
			rep.Upload = res
			a.recordUpload(st, res)
		}

	}

	// The GitHub publisher: daily aggregates and quota meters, when due.
	if ru != nil {
		progress("publishing", upload.Progress{})
		a.publishGitHub(ctx, &cfg, sec, st, ru, hr.Homes, o.Force)
	}

	progress("finishing", upload.Progress{})

	// Self-update, hourly and once when the desktop app starts, only for
	// release builds and with time to spare. A check fetches only the small
	// signed manifest; binaries download only when a newer version is out.
	var staged []selfupdate.Staged
	due := start.Sub(st.LastUpdateCheck) >= updateEvery || (a.InApp && !a.updateChecked)
	if a.Version != "dev" && cfg.AutoUpdate && due && deadline.Sub(a.Now()) >= 15*time.Second {
		a.updateChecked = true
		st.LastUpdateCheck = start
		staged, rep.Updated = a.checkUpdate(ctx, &cfg, st)
	}

	st.LastTick = start
	st.LastTickMs = a.Now().Sub(start).Milliseconds()
	win.Prune(a.Now())
	if err := store.SaveState(a.Home, st); err != nil {
		return rep, err
	}
	rep.State = st
	if len(staged) > 0 {
		if err := selfupdate.Swap(staged); err != nil {
			a.Log.Printf("update: swap failed: %v", err)
			rep.Updated = ""
		} else {
			a.Log.Printf("update: installed %s (was %s)", rep.Updated, a.Version)
			// The app re-execs itself after its own tick; any other
			// process that updated tells a running app to.
			if !a.InApp && instance.Running(a.Home) {
				if err := instance.Send(a.Home, instance.Restart); err != nil {
					a.Log.Printf("update: app restart: %v", err)
				}
			}
		}
	}
	return rep, nil
}

// runChecks runs the config checks in every home, records them in st and
// logs the notes (notes never hold file contents, emails or secrets).
func (a *App) runChecks(cfg *store.Config, st *store.State, fix bool, hs []homes.Home) configfix.Result {
	res := a.runChecksIn(fix, hs)
	// Checks of homes that are gone (a distro that stopped) do not linger.
	for k := range st.Checks {
		if isHomeCheck(k) {
			delete(st.Checks, k)
		}
	}
	for k, v := range res.Checks {
		st.Checks[k] = v
	}
	st.Checks["autostart"] = "missing"
	if cfg.Autostart && a.autostartMode(cfg) != "missing" {
		st.Checks["autostart"] = "ok"
	}
	for _, n := range res.Notes {
		a.Log.Printf("check: %s", n)
	}
	return res
}

func (a *App) recordUpload(st *store.State, res upload.Result) {
	now := a.Now()
	if res.ServerSkewMs != nil {
		st.ClockSkewMs = *res.ServerSkewMs
	}
	if res.HeartbeatSent {
		st.LastHeartbeat = now
	}
	switch {
	case res.Unauthorized:
		st.Unauthorized, st.UnauthorizedAt = true, now
		st.LastUploadErr = "token rejected (HTTP 401)"
		a.Log.Printf("upload: token rejected (401); uploads paused, collection continues")
	case res.Err != nil:
		st.LastUploadErr = res.Err.Error()
		a.Log.Printf("upload: %v (failures=%d, limit=%d)", res.Err, st.Backoff.Failures, st.Backoff.Limit)
	case res.Requests > 0:
		st.Unauthorized = false
		st.LastUploadOK = now
		st.LastUploadErr = ""
	}
	if res.Accepted > 0 || res.Retried > 0 || res.Dropped > 0 {
		a.Log.Printf("upload: %d accepted, %d to retry, %d dropped in %d requests", res.Accepted, res.Retried, res.Dropped, res.Requests)
	}
}

func (a *App) heartbeat(cfg *store.Config, st *store.State, ob *outbox.Outbox) *model.Heartbeat {
	_, events := ob.Count()
	hb := &model.Heartbeat{
		// Public (d0m1.com/agents), so a hand-edited config.json is
		// normalised to what the API accepts.
		MachineLabel: CleanLabel(cfg.MachineLabel),
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		Version:      a.Version,
		ScanAt:       st.LastScan.UTC(),
		ClockSkewMs:  st.ClockSkewMs,
		OutboxEvents: events,
		Sources:      map[string]model.SourceHealth{},
		Checks:       map[string]string{},
		TZ:           machineTZ(),
	}
	for name, s := range st.Sources {
		hb.Sources[name] = model.SourceHealth{Files: s.Files, LastEventTS: s.LastEventTS}
	}
	for k, v := range st.Checks {
		hb.Checks[k] = v
	}
	return hb
}

func (a *App) checkUpdate(ctx context.Context, cfg *store.Config, st *store.State) ([]selfupdate.Staged, string) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Minute}
	url := cfg.UpdateURL
	if url == legacyUpdateURL {
		// A config from before the move to tokenmaxr: its channel is signed
		// with another key; tokenmaxr's releases are the update channel.
		url = store.DefaultUpdateURL
	}
	m, err := selfupdate.Fetch(ctx, client, url, selfupdate.ReleaseKey())
	if err != nil {
		st.LastUpdateErr = err.Error()
		a.Log.Printf("update: %v", err)
		return nil, ""
	}
	st.LastUpdateErr = ""
	if !selfupdate.Newer(m.Version, a.Version) {
		return nil, ""
	}
	staged, err := selfupdate.Stage(ctx, client, m, paths.Bin(a.Home))
	if err != nil {
		st.LastUpdateErr = err.Error()
		a.Log.Printf("update: %s: %v", m.Version, err)
		return nil, ""
	}
	return staged, m.Version
}

// persist filters a parsed batch (prompt controls, --since), merges repeated
// usage ids, and writes it to the outbox, then adds its usage to the local
// numbers (merged by id, so a re-read counts nothing twice). It returns the
// events queued.
func (a *App) persist(ob *outbox.Outbox, cfg *store.Config, since *scan.Bound, b sources.Batch, win *recent.Window, ru *rollup.Rollup) (int, error) {
	var out outbox.Batch
	idx := map[string]int{}
	for _, e := range b.Usage {
		if !scan.InRange(since, nil, e.TS, e.TZOffsetMin) {
			continue
		}
		if i, ok := idx[e.ID]; ok {
			scan.MergeUsage(&out.Usage[i], e)
			continue
		}
		idx[e.ID] = len(out.Usage)
		out.Usage = append(out.Usage, e)
	}
	seen := map[string]int{}
	for _, e := range b.Activity {
		if !scan.InRange(since, nil, e.TS, e.TZOffsetMin) {
			continue
		}
		if i, ok := seen[e.ID]; ok {
			have := &out.Activity[i]
			have.HasUsage = have.HasUsage || e.HasUsage
			if e.TS.Before(have.TS) {
				have.TS, have.TZOffsetMin = e.TS, e.TZOffsetMin
			}
			continue
		}
		seen[e.ID] = len(out.Activity)
		out.Activity = append(out.Activity, e)
	}
	seen = map[string]int{}
	for _, e := range b.Limits {
		if i, ok := seen[e.ID]; ok {
			if e.ObservedAt.After(out.Limits[i].ObservedAt) {
				out.Limits[i] = e
			}
			continue
		}
		seen[e.ID] = len(out.Limits)
		out.Limits = append(out.Limits, e)
	}
	if cfg.Prompts {
		seen = map[string]int{}
		for _, p := range b.Prompts {
			if cfg.PromptExcluded(p.Acct) || !scan.InRange(since, nil, p.TS, p.TZOffsetMin) {
				continue
			}
			if i, ok := seen[p.ID]; ok {
				// Keep one record per id, preferring one that knows its model.
				if out.Prompts[i].Model == "" && p.Model != "" {
					out.Prompts[i] = capPrompt(p)
				}
				continue
			}
			seen[p.ID] = len(out.Prompts)
			out.Prompts = append(out.Prompts, capPrompt(p))
		}
	}
	if ob != nil {
		for _, chunk := range out.Split(outboxFileEvents) {
			if _, err := ob.Write(chunk); err != nil {
				return 0, err
			}
		}
	}
	// The rollup is saved here, before the scan commits the cursors of the
	// files these events came from, so a crash never loses them.
	if ru != nil {
		now := a.Now()
		for _, e := range out.Usage {
			ru.AddUsage(e, now)
		}
		for _, e := range out.Activity {
			ru.AddActivity(e)
		}
		if ru.Dirty() {
			if err := ru.Save(paths.Rollup(a.Home)); err != nil {
				return 0, err
			}
		}
	}
	if win != nil {
		now := a.Now()
		for _, e := range out.Usage {
			win.Add(e, now)
		}
	}
	return out.Len(), nil
}

const truncMarker = "\n[... truncated by the collector]"

// capPrompt shortens a prompt whose JSON encoding would not fit a request.
func capPrompt(p model.PromptRecord) model.PromptRecord {
	b, _ := json.Marshal(p)
	if len(b) <= maxPromptJSON {
		return p
	}
	text := p.Text
	keep := len(text)
	for range 8 {
		keep = keep * maxPromptJSON / len(b) * 9 / 10
		for keep > 0 && !utf8.RuneStart(text[keep]) {
			keep--
		}
		p.Text = text[:keep] + truncMarker
		if b, _ = json.Marshal(p); len(b) <= maxPromptJSON {
			return p
		}
	}
	p.Text = truncMarker
	return p
}
