package app

import (
	"fmt"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
	"github.com/7-of-9/tokenmaxr/collector/internal/configfix"
	"github.com/7-of-9/tokenmaxr/collector/internal/lock"
	"github.com/7-of-9/tokenmaxr/collector/internal/outbox"
	"github.com/7-of-9/tokenmaxr/collector/internal/paths"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
	"github.com/7-of-9/tokenmaxr/collector/internal/termfmt"
	"github.com/7-of-9/tokenmaxr/collector/internal/tray"
	"github.com/7-of-9/tokenmaxr/collector/internal/upload"
)

// since is "3 min ago · 20:07" (the clock time dim), or "never".
func since(p *termfmt.Printer, t, now time.Time) string {
	if t.IsZero() {
		return p.Dim("never")
	}
	d := now.Sub(t)
	rel := ""
	switch {
	case d < 0:
		rel = "in the future"
	case d < time.Minute:
		rel = "just now"
	case d < time.Hour:
		rel = fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		rel = fmt.Sprintf("%d h ago", int(d.Hours()))
	default:
		rel = fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
	clock := t.Local().Format("15:04")
	if now.Sub(t) >= 20*time.Hour {
		clock = t.Local().Format("2006-01-02 15:04")
	}
	return rel + p.Dim(" · "+clock)
}

func every(d time.Duration) string {
	switch {
	case d%time.Hour == 0 && d >= time.Hour:
		if d == time.Hour {
			return "every hour"
		}
		return fmt.Sprintf("every %d h", int(d.Hours()))
	default:
		return fmt.Sprintf("every %d min", int(d.Minutes()))
	}
}

func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return tray.Count(n) + " " + many
}

// Status prints the local health summary: what this machine publishes and
// where, how collection is going, its sources and scan roots, and its
// health checks.
func (a *App) Status() error {
	now := a.Now()
	cfg, cerr := store.LoadConfig(a.Home)
	sec, _ := store.LoadSecrets(a.Home)
	st, serr := store.LoadState(a.Home)
	p := termfmt.New(a.Out)

	built := ""
	if a.BuildTime != "" {
		if t, err := time.Parse(time.RFC3339, a.BuildTime); err == nil {
			built = " · built " + t.UTC().Format("2006-01-02 15:04") + " UTC"
		}
	}
	p.Title(buildinfo.Product+" "+a.Version, runtime.GOOS+"/"+runtime.GOARCH+built)
	if cerr != nil {
		p.Row("config", p.Bad("invalid: "+cerr.Error()))
	}
	if serr != nil {
		p.Row("state", p.Bad(serr.Error()))
	}
	if !sec.HasFleet() {
		p.Row("set up", p.Bad("no")+": run `"+buildinfo.Product+" install`")
	}

	// Publishing: GitHub, the server, the queue.
	p.Section("Publishing")
	a.githubRows(p, cfg, sec, st, now)
	switch {
	case cfg.Server() == "":
		p.Row("Server", p.Dim("none"))
	case !sec.Enrolled():
		p.Row("Server", cfg.Server()+" · "+p.Bad("not enrolled")+": run `"+buildinfo.Product+" install`")
	default:
		up := "last upload " + since(p, st.LastUploadOK, now)
		switch {
		case st.Unauthorized:
			up = p.Bad("PAUSED") + ": token rejected (401) " + since(p, st.UnauthorizedAt, now) + "; collection continues. Re-run install with a fresh join code."
		case st.LastUploadErr != "":
			up = p.Bad("upload error: " + st.LastUploadErr)
		}
		more := []string{up, "heartbeat " + since(p, st.LastHeartbeat, now) + p.Dim(fmt.Sprintf(" · clock skew %.1fs", float64(st.ClockSkewMs)/1000))}
		if st.Backoff.Failures > 0 {
			more = append(more, p.Warn(fmt.Sprintf("backing off: %d failures, next try %s, batch limit %d", st.Backoff.Failures, st.Backoff.Until.Local().Format("15:04:05"), st.Backoff.Limit)))
		}
		p.Row("Server", tray.EndpointHost(cfg.Server())+" · "+p.Good("enrolled")+p.Dim(" as "+sec.MachineID), more...)
	}
	ob := outbox.New(paths.Outbox(a.Home))
	files, events := ob.Count()
	if events == 0 {
		p.Row("Queue", p.Good("empty"))
	} else {
		p.Row("Queue", count(events, "event", "events")+" to upload"+p.Dim(" · "+count(files, "file", "files")))
	}
	if dfiles, devents := ob.DeadCount(); devents > 0 {
		p.Row("Dead letters", p.Warn(count(devents, "event", "events")+" in "+count(dfiles, "file", "files")), p.Dim(fmt.Sprintf("outbox/%s: rejected by the server or retried %d times; inspect or delete by hand", outbox.DeadDir, upload.MaxAttempts)))
	}

	// Collection.
	p.Section("Collection")
	tick := since(p, st.LastTick, now) + p.Dim(fmt.Sprintf(" · took %.1fs", float64(st.LastTickMs)/1000))
	if lock.Held(paths.Lock(a.Home)) {
		tick += " · " + p.Accent("running now")
	}
	p.Row("Last sync", tick)
	p.Row("Machine", p.Bold(cfg.MachineLabel), p.Dim(publicNote+"; rename: "+buildinfo.Product+" label NEW_NAME"))
	p.Row("Time zone", tzLine(machineTZ()))
	prompts := onOff(cfg.Prompts)
	if n := len(cfg.PromptExcludeAccts); n > 0 {
		prompts += p.Dim(fmt.Sprintf(" · %d accounts excluded", n))
	}
	p.Row("Prompts", prompts)
	if h := st.AccountHistory; !h.LastAttempt.IsZero() {
		if h.LastError != "" {
			p.Row("Codex history", p.Warn(h.LastError), p.Dim("checked "+since(p, h.LastAttempt, now)+"; local collection continues"))
		} else {
			p.Row("Codex history", fmt.Sprintf("%d UTC days · %s account tokens", h.Days, tray.Compact(h.TotalTokens)), p.Dim("read ")+since(p, h.LastSuccess, now)+p.Dim(" · automatic every 15 min"))
		}
	}

	// Sources.
	p.Section("Sources")
	names := make([]string, 0, len(st.Sources))
	for n := range st.Sources {
		names = append(names, n)
	}
	slices.Sort(names)
	if len(names) == 0 {
		p.Line(p.Dim("not scanned yet"))
	}
	for _, n := range names {
		s := st.Sources[n]
		v := fmt.Sprintf("%12s", count(s.Files, "file", "files"))
		switch {
		case !cfg.SourceEnabled(n):
			v += "  " + p.Dim("disabled")
		case s.LastEventTS != nil:
			v += "  last event " + since(p, *s.LastEventTS, now)
		}
		if s.Pending > 0 {
			v += "  " + p.Accent(fmt.Sprintf("backfill: %d files left", s.Pending))
		}
		if s.LastError != "" {
			v += "  " + p.Bad("error: "+s.LastError)
		}
		p.Item(n, 14, v)
	}

	// Scan roots.
	p.Section("Scan roots")
	if len(st.Homes) == 0 {
		p.Line(p.Dim("not scanned yet"))
	}
	for _, h := range st.Homes {
		p.Item(h.Kind+labelSuffix(h.Distro), 20, h.Path+p.Dim(" · "+count(h.Files, "file", "files")))
	}
	for _, d := range st.WSLSkipped {
		p.Item("wsl:"+d, 20, p.Dim("not running, skipped (never started by the collector)"))
	}

	// Health.
	p.Section("Health")
	p.Row("Autostart", a.healthText(p, a.autostartLine(&cfg)))
	p.Row("App", a.healthText(p, a.appLine(&cfg)))
	keys := make([]string, 0, len(st.Checks))
	var bad []string
	for k, v := range st.Checks {
		keys = append(keys, k)
		if v != "ok" && v != "fixed" && v != "off" {
			bad = append(bad, k+": "+v)
		}
	}
	slices.Sort(keys)
	slices.Sort(bad)
	checks := p.Good(fmt.Sprintf("all %d ok", len(keys)))
	if len(keys) == 0 {
		checks = p.Dim("not run yet")
	} else if len(bad) > 0 {
		checks = p.Warn(fmt.Sprintf("%d of %d need attention", len(bad), len(keys)))
	}
	p.Row("Checks", checks+p.Dim(" · ")+since(p, st.LastConfigCheck, now), bad...)
	if sac, detail, ok := configfix.SmartAppControl(); ok {
		p.Row("Smart App Ctl", strings.TrimSpace(sac+" "+p.Dim(detail)))
	}
	upd := "checked " + since(p, st.LastUpdateCheck, now)
	if a.Version == "dev" {
		upd += p.Dim(" · dev build: never self-updates")
	}
	if st.LastUpdateErr != "" {
		upd += " · " + p.Bad("error: "+st.LastUpdateErr)
	}
	p.Row("Updates", upd)
	p.Row("Accounts", p.Dim(evidenceLine(st)))
	p.Row("State", p.Dim(a.Home))
	return nil
}

// healthText colours a health line by its first word.
func (a *App) healthText(p *termfmt.Printer, s string) string {
	switch {
	case strings.HasPrefix(s, "running"), strings.HasPrefix(s, "app at login"), strings.HasPrefix(s, "headless, a tick"):
		return p.Good(s)
	case strings.HasPrefix(s, "missing"), strings.Contains(s, "re-run install"):
		return p.Bad(s)
	case strings.HasPrefix(s, "off"), strings.HasPrefix(s, "not running"):
		return p.Warn(s)
	}
	return s
}

// githubRows are the GitHub publisher's rows (status and github status).
func (a *App) githubRows(p *termfmt.Printer, cfg store.Config, sec store.Secrets, st *store.State, now time.Time) {
	if !githubEnabled(&cfg, sec) {
		more := []string{}
		if serverOn(&cfg, sec) {
			if cfg.GitHubFleetOptOut {
				more = append(more, p.Dim("opted out of the sign-in your fleet shares (signing in here undoes it)"))
			} else {
				more = append(more, p.Dim("publishes with the sign-in your fleet shares through your server, once one of your machines signs in"))
			}
		}
		if st != nil && st.GitHub.Fleet.LastError != "" {
			more = append(more, p.Warn(st.GitHub.Fleet.LastError))
		}
		p.Row("GitHub", p.Dim("off")+" · publish to your own GitHub: "+p.Bold(buildinfo.Product+" github login"), more...)
		return
	}
	more := []string{}
	if l := fleetLine(&cfg, sec, st); l != "" {
		more = append(more, p.Dim(l))
	}
	if cfg.GitHub.Adopted {
		more = append(more, p.Dim("stop publishing here ("+buildinfo.Product+" github logout) to opt this machine out"))
	}
	if st != nil && st.GitHub.Fleet.LastError != "" {
		more = append(more, p.Warn(st.GitHub.Fleet.LastError))
	}
	if st != nil {
		pub := "published " + since(p, st.GitHub.LastPublish, now) + p.Dim(" · "+every(cfg.GitHub.PublishEvery()))
		if st.GitHub.Rebuild {
			pub += " · " + p.Accent("re-reading history for the dashboard")
		}
		more = append(more, pub)
		if st.GitHub.LastError != "" {
			more = append(more, p.Bad("error: "+st.GitHub.LastError)+p.Dim(" · ")+since(p, st.GitHub.LastAttempt, now))
		}
		if st.GitHub.PagesURL != "" {
			more = append(more, p.Accent(st.GitHub.PagesURL))
		}
	}
	if cfg.GitHub.NoQuota {
		more = append(more, p.Dim("quota meters not published"))
	}
	if cfg.GitHub.ShowCountry {
		more = append(more, p.Dim("country (flag) published"))
	}
	if cfg.GitHub.ShowAccountHistory {
		more = append(more, p.Dim("Codex account history published"))
	}
	who := " · signed in as " + sec.GitHub.Login
	if cfg.GitHub.Adopted {
		who = "" // the fleet line says whose sign-in it is
	}
	p.Row("GitHub", cfg.GitHub.Repo+" as "+p.Bold(fmt.Sprintf("%q", cfg.GitHub.Label))+p.Dim(who), more...)
}
