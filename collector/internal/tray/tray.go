// Package tray is the desktop app's display logic (docs/agents/SPEC.md
// "Desktop app (v1.4)"): the collector's state in, the icon colour, tooltip
// and menu out. It is pure Go with no GUI dependency, so it builds
// everywhere and is tested as plain functions; internal/tray/ui draws it
// with fyne.io/systray.
package tray

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
	"github.com/7-of-9/tokenmaxr/collector/internal/recent"
)

// Color is the icon: green when all is well, red otherwise.
type Color int

const (
	Red Color = iota
	Green
)

func (c Color) String() string {
	if c == Green {
		return "green"
	}
	return "red"
}

// Stale is how old the last tick may be for green.
const Stale = 3 * time.Minute

const tooltipMaxRunes = 120 // Windows keeps 128 UTF-16 units

// Input is the collector as the app knows it.
type Input struct {
	Now time.Time
	// Enrolled: this machine uploads to a server (enrolled, not switched
	// off). SetUp: it holds a fleet key and collects, whatever it delivers to.
	Enrolled  bool
	SetUp     bool
	ConfigErr string
	// TickErr is a tick that failed outright (state or outbox not written).
	TickErr string
	// LastTick is when the last complete tick started; Ticking is set while
	// one runs, since TickStarted.
	LastTick    time.Time
	Ticking     bool
	TickStarted time.Time
	// Uploading is true only while an HTTP request is in flight.
	Uploading bool
	Phase     string

	LastUploadOK  time.Time
	LastUploadErr string
	Unauthorized  bool
	BackoffUntil  time.Time
	// Outbox is the events queued, Pending the files the backfill has left.
	Outbox      int
	Pending     int
	InitialScan bool

	Providers []recent.Summary
	Machine   string
	// GitHub is the repository this machine publishes to ("" when off),
	// GitHubErr its last publish failure and PagesURL its dashboard.
	GitHub    string
	GitHubErr string
	PagesURL  string
	// GitHubLogin is the signed-in GitHub account ("" when signed out).
	GitHubLogin string
	// Fleet is the fleet id, model.KFingerprint(K).
	Fleet    string
	Endpoint string
	// Pinned: the live panel is on screen (the menu offers Unpin).
	Pinned bool
	// TickEvery is the pause between ticks (shorter while pinned);
	// NextTick is when the next one is due (zero: not scheduled yet).
	TickEvery time.Duration
	NextTick  time.Time
	// Version and BuildTime (RFC 3339 UTC) identify the running binary.
	Version   string
	BuildTime string
}

// View is what the app shows for an Input.
type View struct {
	Color Color
	// Machine is the shared heading in the popup and pinned panel.
	Machine string
	// Status describes current activity below the machine heading.
	Status  string
	Tooltip string
	// Providers are ordered by latest activity, newest first.
	Providers []ProviderLine
	// Identity is "Machine STUDIO · fleet a1b2c3d4"; Fleet is what clicking
	// it copies ("" when there is none).
	Identity  string
	Fleet     string
	Dashboard string
	CanSync   bool
	Syncing   bool
	SyncLabel string
	// Pinned: the live panel is on screen.
	Pinned bool
	// Build is the recessive footer naming the running version and its
	// build time in UTC.
	Build string
	// Account says where this machine publishes: the signed-in GitHub
	// account and repository, and the server.
	Account string
}

// recentEvent is how new a provider's latest event must be for its age and
// +count to stay full ink. Older than this, those two are recessive.
const recentEvent = time.Hour

// ProviderLine is one provider row, as cells:
// "Claude", "2 min ago", "+67.5K" (the latest event and its tokens), then
// "20.1B" and "13.4B" (the rolling 24 h and 30 d totals).
// Fresh is set when that latest event is within the last hour.
type ProviderLine struct {
	Provider        string
	Name, Ago, Last string
	Day, Month      string
	Fresh           bool
}

// Left is the menu's first column: "Claude   2 min ago   +67.5K".
func (p ProviderLine) Left() string { return p.Name + "   " + p.Ago + "   " + p.Last }

// Right is the menu's second column, "24h 20.1B   30d 13.4B", with each
// figure padded to one width (MenuFigure) so the columns line up under
// each other in the menu's proportional font.
func (p ProviderLine) Right() string {
	return "24h " + MenuFigure(p.Day) + "   30d " + MenuFigure(p.Month)
}

// Text is the line as one string.
func (p ProviderLine) Text(sep string) string { return p.Left() + sep + p.Right() }

// Providers are the display names, in menu order.
var Providers = []struct{ ID, Name string }{
	{"anthropic", "Claude"},
	{"openai", "OpenAI"},
	{"xai", "Grok"},
	{"cursor", "Cursor"},
	{"google", "Gemini"},
}

func rank(provider string) int {
	for i, p := range Providers {
		if p.ID == provider {
			return i
		}
	}
	return len(Providers)
}

// Name is a provider's display name.
func Name(provider string) string {
	if i := rank(provider); i < len(Providers) {
		return Providers[i].Name
	}
	return provider
}

// Evaluate turns the state into the view: the first matching problem makes
// it red; otherwise it is green.
func Evaluate(in Input) View {
	in.SetUp = in.SetUp || in.Enrolled // a server enrolment holds the fleet key
	v := View{
		Machine:   machineName(in.Machine),
		Providers: providerLines(in.Providers, in.Now),
		Identity:  identity(in),
		Dashboard: dashboard(in),
		CanSync:   in.SetUp && in.ConfigErr == "",
		Syncing:   in.Ticking,
		SyncLabel: "Sync in progress…",
		Pinned:    in.Pinned,
		Build:     BuildLabel(in.Version, in.BuildTime),
		Account:   account(in),
	}
	if in.SetUp {
		v.Fleet = in.Fleet
	}
	if why := problem(in); why != "" {
		v.Color = Red
		v.Status = "● Error: " + why
		if !in.Ticking && in.SetUp && in.ConfigErr == "" {
			v.Status += " · " + nextSync(in)
		}
		v.Tooltip = clip(buildinfo.Product+" · ERROR: "+why, tooltipMaxRunes)
		return v
	}
	v.Color = Green
	activity := ""
	queue := exactCount(in.Outbox, "event", "events") + " queued"
	switch {
	case in.Ticking && in.Uploading && in.Outbox > 0:
		activity = "Uploading now · " + Count(in.Outbox) + " remaining"
		v.SyncLabel = "Uploading…"
	case in.Ticking && in.Uploading:
		activity = "Contacting server now"
		v.SyncLabel = "Contacting server…"
	case in.Ticking && in.Phase == "uploading":
		activity = "Preparing next upload · " + queue
		v.SyncLabel = "Preparing upload…"
	case in.Ticking && in.Phase == "finishing":
		activity = "Finishing sync"
		v.SyncLabel = "Finishing sync…"
	case in.Ticking && in.Phase == "account-history":
		activity = "Reading Codex account history"
		v.SyncLabel = "Reading account history…"
	case in.Ticking:
		activity = "Scanning local history"
		v.SyncLabel = "Scanning…"
	case in.Outbox > 0:
		activity = "Waiting · " + queue + " · " + nextSync(in)
	case in.Pending > 0:
		activity = "Waiting · " + exactCount(in.Pending, "file", "files") + " to scan · " + nextSync(in)
	case in.InitialScan:
		activity = "Initial scan pending · " + nextSync(in)
	default:
		activity = "Up to date · " + nextSync(in)
	}
	v.Status = "● " + activity
	v.Tooltip = clip(buildinfo.Product+" · "+activity, tooltipMaxRunes)
	return v
}

func exactCount(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return Count(n) + " " + many
}

func nextSync(in Input) string {
	next := in.NextTick
	if next.IsZero() {
		return "next sync not scheduled"
	}
	seconds := int(next.Sub(in.Now).Round(time.Second) / time.Second)
	if seconds <= 0 {
		return "next sync due now"
	}
	return "next sync in " + (time.Duration(seconds) * time.Second).String()
}

// problem names what keeps the icon red, in a few words ("" when green).
func problem(in Input) string {
	last := in.LastTick
	if in.Ticking && in.TickStarted.After(last) {
		// A tick in progress is the app at work, not a stale one.
		last = in.TickStarted
	}
	switch {
	case in.ConfigErr != "":
		return "config.json is invalid"
	case !in.SetUp:
		return "not set up"
	case in.Enrolled && in.Unauthorized:
		return "token rejected"
	case in.Enrolled && in.LastUploadErr != "":
		return ShortError(in.LastUploadErr)
	case in.Enrolled && in.Now.Before(in.BackoffUntil):
		return "upload paused"
	case in.GitHub != "" && in.GitHubErr != "":
		return "GitHub: " + ShortError(in.GitHubErr)
	case in.TickErr != "":
		return ShortError(in.TickErr)
	case last.IsZero():
		return "not synced yet"
	case in.Now.Sub(last) >= Stale:
		return "no sync for " + strings.TrimSuffix(Ago(last, in.Now), " ago")
	}
	return ""
}

// providerLines formats every provider seen locally, in menu order.
func providerLines(sums []recent.Summary, now time.Time) []ProviderLine {
	sums = slices.Clone(sums)
	slices.SortFunc(sums, func(a, b recent.Summary) int {
		return cmp.Or(b.LastTS.Compare(a.LastTS), cmp.Compare(rank(a.Provider), rank(b.Provider)), cmp.Compare(a.Provider, b.Provider))
	})
	var out []ProviderLine
	for _, s := range sums {
		if s.LastTS.IsZero() {
			continue
		}
		out = append(out, ProviderLine{
			Provider: s.Provider,
			Name:     Name(s.Provider),
			Ago:      Ago(s.LastTS, now),
			Last:     "+" + Compact(s.LastTokens),
			Day:      Compact(s.PastDay),
			Month:    Compact(s.PastMonth),
			Fresh:    now.Sub(s.LastTS) < recentEvent,
		})
	}
	return out
}

func machineName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "This machine"
	}
	return clip(name, 64)
}

func identity(in Input) string {
	m := "Machine " + clip(in.Machine, 32)
	if strings.TrimSpace(in.Machine) == "" {
		m = "This machine"
	}
	switch {
	case !in.SetUp:
		return m + " · not set up"
	case in.Fleet == "":
		return m
	}
	return m + " · fleet " + in.Fleet[:min(8, len(in.Fleet))]
}

// account is "GitHub 7-of-9 → tokenmaxr-usage · server d0m1.com", or says
// that GitHub is not signed in.
func account(in Input) string {
	gh := "GitHub: not signed in · Settings…"
	if in.GitHub != "" {
		repo := in.GitHub
		if owner, name, ok := strings.Cut(repo, "/"); ok && strings.EqualFold(owner, in.GitHubLogin) {
			repo = name
		}
		gh = "GitHub " + in.GitHubLogin + " → " + repo
		if in.GitHubLogin == "" {
			gh = "GitHub → " + in.GitHub
		}
	}
	if in.Enrolled {
		gh += " · server " + EndpointHost(in.Endpoint)
	}
	return gh
}

// dashboard is the server's dashboard, else the GitHub Pages one ("" when
// this machine publishes nowhere).
func dashboard(in Input) string {
	if in.Enrolled {
		return DashboardURL(in.Endpoint)
	}
	if in.GitHub != "" {
		return in.PagesURL
	}
	return ""
}

// DashboardURL is /tokens on the site behind the API endpoint (scheme and
// host only). Anything that is not a plain http(s) URL falls back to the
// production site.
func DashboardURL(endpoint string) string {
	if o := SiteOrigin(endpoint); o != "" {
		return o + "/tokens"
	}
	return ""
}

// EndpointHost is the endpoint's host as the menu names it: "d0m1.com",
// "127.0.0.1:7094".
func EndpointHost(endpoint string) string {
	_, host, _ := strings.Cut(SiteOrigin(endpoint), "://")
	return host
}

// SiteOrigin is the endpoint's scheme and host ("" for anything that is not
// a plain http(s) URL).
func SiteOrigin(endpoint string) string {
	e := strings.TrimSpace(endpoint)
	scheme, rest, ok := strings.Cut(e, "://")
	if !ok || (scheme != "https" && scheme != "http") {
		return ""
	}
	host, _, _ := strings.Cut(rest, "/")
	if host == "" || strings.ContainsAny(host, "@?#\\ ") {
		return ""
	}
	return scheme + "://" + host
}
