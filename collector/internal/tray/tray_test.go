package tray

import (
	"encoding/binary"
	"image/color"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/recent"
)

var now = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

// ok is a healthy machine: ticked 20 s ago (every minute, the next in
// 40 s), sent 12 s ago.
func ok() Input {
	return Input{
		Now: now, Enrolled: true, LastTick: now.Add(-20 * time.Second), LastUploadOK: now.Add(-12 * time.Second),
		Machine: "STUDIO", Fleet: "a1b2c3d4e5f60718", Endpoint: "https://d0m1.com",
		TickEvery: time.Minute, NextTick: now.Add(40 * time.Second),
	}
}

func TestEvaluate(t *testing.T) {
	cases := []struct {
		name    string
		edit    func(*Input)
		color   Color
		status  string
		tooltip string
	}{
		{"ok", func(*Input) {}, Green, "● Up to date", "tokenmaxr · Up to date"},
		{"backfill", func(in *Input) { in.Outbox, in.UploadPeak, in.Pending = 41000, 100000, 120 }, Green, "● Uploading ▰▰▰▰▰▱▱▱▱▱ 59% · 41,000 left", "tokenmaxr · Uploading ▰▰▰▰▰▱▱▱▱▱ 59% · 41,000 left"},
		{"backfill files", func(in *Input) { in.Pending = 1 }, Green, "● Up to date", ""},
		{"retry queue", func(in *Input) { in.Outbox, in.UploadPeak = 3, 3 }, Green, "● Uploading ▱▱▱▱▱▱▱▱▱▱ 0% · 3 left", ""},
		{"nothing to send yet", func(in *Input) { in.LastUploadOK = time.Time{} }, Green, "● Up to date", "tokenmaxr · Up to date"},
		{"harvest still pending", func(in *Input) { in.InitialScan = true }, Green, "● Up to date", ""},
		{"stale", func(in *Input) { in.LastTick = now.Add(-12 * time.Minute) }, Red, "● Error: no sync for 12 min · next sync in 40s", "tokenmaxr · ERROR: no sync for 12 min"},
		{"just stale", func(in *Input) { in.LastTick = now.Add(-Stale) }, Red, "● Error: no sync for 3 min · next sync in 40s", ""},
		{"ticking keeps it fresh", func(in *Input) {
			in.LastTick, in.Ticking, in.TickStarted = now.Add(-10*time.Minute), true, now.Add(-30*time.Second)
		}, Green, "● Up to date", ""},
		{"first tick running", func(in *Input) {
			in.LastTick, in.LastUploadOK, in.Ticking, in.TickStarted = time.Time{}, time.Time{}, true, now.Add(-5*time.Second)
		}, Green, "", ""},
		{"never ticked", func(in *Input) { in.LastTick = time.Time{} }, Red, "● Error: not synced yet · next sync in 40s", ""},
		{"offline", func(in *Input) {
			in.LastUploadErr = `Post "https://d0m1.com/api/ingest": dial tcp: lookup d0m1.com: no such host`
		}, Red, "● Error: offline · next sync in 40s", "tokenmaxr · ERROR: offline"},
		{"server", func(in *Input) { in.LastUploadErr = "ingest: HTTP 503" }, Red, "● Error: server error · next sync in 40s", ""},
		{"problem stays red during a tick", func(in *Input) { in.LastUploadErr, in.Ticking, in.TickStarted = "ingest: HTTP 503", true, now }, Red, "● Error: server error", ""},
		{"backoff", func(in *Input) { in.BackoffUntil = now.Add(time.Minute) }, Red, "● Error: upload paused · next sync in 40s", ""},
		{"401", func(in *Input) { in.Unauthorized, in.LastUploadErr = true, "token rejected (HTTP 401)" }, Red, "● Error: token rejected · next sync in 40s", "tokenmaxr · ERROR: token rejected"},
		{"not set up", func(in *Input) { in.Enrolled = false }, Red, "● Error: not set up", ""},
		{"config", func(in *Input) { in.ConfigErr = "config.json: invalid character" }, Red, "● Error: config.json is invalid", ""},
		{"tick failed", func(in *Input) { in.TickErr = "outbox: disk full" }, Red, "● Error: outbox · next sync in 40s", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := ok()
			c.edit(&in)
			v := Evaluate(in)
			if v.Color != c.color {
				t.Errorf("color %s, want %s (status %q)", v.Color, c.color, v.Status)
			}
			if c.status != "" && v.Status != c.status {
				t.Errorf("status %q, want %q", v.Status, c.status)
			}
			if c.tooltip != "" && v.Tooltip != c.tooltip {
				t.Errorf("tooltip %q, want %q", v.Tooltip, c.tooltip)
			}
			if n := len([]rune(v.Tooltip)); n > tooltipMaxRunes {
				t.Errorf("tooltip is %d runes", n)
			}
		})
	}
}

// vis shows the menu's padding spaces: _ a figure space, . a punctuation space.
func vis(s string) string {
	return strings.NewReplacer(FigureSpace, "_", PunctuationSpace, ".").Replace(s)
}

func TestProviderLines(t *testing.T) {
	in := ok()
	in.Providers = []recent.Summary{
		{Provider: "google", LastTS: now.Add(-3 * 24 * time.Hour), LastTokens: 7, PastMonth: 900},
		{Provider: "anthropic", LastTS: now.Add(-2 * time.Minute), LastTokens: 67_512, PastHour: 3_100_000, PastDay: 20_100_000_000, PastMonth: 13_400_000_000},
		{Provider: "xai", LastTS: now.Add(-30 * time.Second), LastTokens: 10, PastDay: 1234},
		{Provider: "openai"}, // never seen: hidden
	}
	v := Evaluate(in)
	want := []ProviderLine{
		{"xai", "Grok", "30s ago", "+10", "1.2K", "0", true},
		{"anthropic", "Claude", "2 min ago", "+67.5K", "20.1B", "13.4B", true},
		{"google", "Gemini", "3 d ago", "+7", "0", "900", false},
	}
	if len(v.Providers) != len(want) {
		t.Fatalf("lines %+v", v.Providers)
	}
	for i, w := range want {
		if v.Providers[i] != w {
			t.Errorf("line %d: %+v, want %+v", i, v.Providers[i], w)
		}
	}
	// The menu: left column, then the padded figures after the separator.
	for i, w := range []string{
		"Grok   30s ago   +10 | 24h _1.2K   30d __._0",
		"Claude   2 min ago   +67.5K | 24h 20.1B   30d 13.4B",
		"Gemini   3 d ago   +7 | 24h __._0   30d ._900",
	} {
		if got := vis(v.Providers[i].Text(" | ")); got != w {
			t.Errorf("menu line %d\n got %q\nwant %q", i, got, w)
		}
	}
	in.Providers[0].LastTS = now
	if got := Evaluate(in).Providers[0].Provider; got != "google" {
		t.Fatalf("latest provider did not move to the top: %s", got)
	}

}

func TestMenuFigure(t *testing.T) {
	// Every figure is padded to three digits, a point and a unit.
	for in, want := range map[string]string{
		"20.1B": "20.1B", "1.2K": "_1.2K", "123M": ".123M", "999": "._999", "7": "__._7", "0": "__._0", "1K": "__.1K",
	} {
		if got := vis(MenuFigure(in)); got != want {
			t.Errorf("MenuFigure(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAlign(t *testing.T) {
	cases := []struct {
		name string
		rows [][]string
		cols []Col
		want []string
	}{
		{"left and right", [][]string{{"a", "1"}, {"bbb", "22"}}, []Col{{}, {Right: true, Gap: " "}}, []string{"a    1", "bbb 22"}},
		{"gaps", [][]string{{"x", "y", "z"}}, []Col{{Gap: "!"}, {Gap: " | "}, {Gap: "-"}}, []string{"x | y-z"}},
		{"trailing space trimmed", [][]string{{"long", "a"}, {"s", "b"}}, []Col{{}, {Gap: " "}}, []string{"long a", "s    b"}},
		{"short row", [][]string{{"aa", "bb"}, {"c"}}, []Col{{Right: true}, {Gap: " "}}, []string{"aa bb", " c"}},
		{"runes, not bytes", [][]string{{"│", "x"}, {"ab", "y"}}, []Col{{}, {Gap: " "}}, []string{"│  x", "ab y"}},
		{"extra cells dropped", [][]string{{"a", "b", "c"}}, []Col{{}, {Gap: " "}}, []string{"a b"}},
		{"none", nil, []Col{{}}, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Align(c.rows, c.cols)
			if strings.Join(got, "\n") != strings.Join(c.want, "\n") || len(got) != len(c.want) {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestPanel(t *testing.T) {
	in := ok()
	in.Providers = []recent.Summary{
		{Provider: "anthropic", LastTS: now.Add(-2 * time.Minute), LastTokens: 67_512, PastDay: 20_100_000_000, PastMonth: 13_400_000_000},
		{Provider: "openai", LastTS: now.Add(-3 * time.Hour), LastTokens: 135_000, PastDay: 4_200_000, PastMonth: 611_000_000},
		{Provider: "xai", LastTS: now.Add(-30 * time.Second), LastTokens: 10, PastDay: 900, PastMonth: 900},
	}
	cases := []struct {
		name string
		edit func(*Input)
		want []PanelLine
	}{
		{"ok with providers", func(*Input) {}, []PanelLine{
			{Text: "● tokenmaxr · STUDIO", Kind: LineOK, HiEnd: 1},
			{Text: "Up to date", Kind: LineDim},
			{Text: "GitHub: not signed in · Settings… · server d0m1.com", Kind: LineDim},
			{Kind: LineRule},
			{Text: "Grok     30s ago        +10  │   24h   900   30d   900", Kind: LineText, Hi: 24, HiEnd: 27, Age: 9, AgeEnd: 16},
			{Text: "Claude   2 min ago   +67.5K  │   24h 20.1B   30d 13.4B", Kind: LineText, Hi: 21, HiEnd: 27, Age: 9, AgeEnd: 18},
			{Text: "OpenAI   3 h ago      +135K  │   24h  4.2M   30d  611M", Kind: LineText, Hi: 22, HiEnd: 27, Quiet: true, Age: 9, AgeEnd: 16},
		}},
		{"error, nothing seen", func(in *Input) { in.Providers, in.Enrolled = nil, false }, []PanelLine{
			{Text: "● tokenmaxr · STUDIO", Kind: LineError, HiEnd: 1},
			{Text: "Error: not set up", Kind: LineDim},
			{Text: "GitHub: not signed in · Settings…", Kind: LineDim},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := in
			c.edit(&in)
			got := Panel(Evaluate(in))
			if len(got) != len(c.want) {
				t.Fatalf("%d lines, want %d: %+v", len(got), len(c.want), got)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("line %d\n got %+v\nwant %+v", i, got[i], c.want[i])
				}
			}
			// The accent span is exactly the +value.
			for _, l := range got {
				if l.Kind == LineText && l.HiEnd > 0 {
					if r := []rune(l.Text)[l.Hi:l.HiEnd]; r[0] != '+' {
						t.Errorf("accent %q in %q", string(r), l.Text)
					}
				}
			}
		})
	}
}

func TestIdentity(t *testing.T) {
	in := ok()
	v := Evaluate(in)
	if v.Identity != "Machine STUDIO · fleet a1b2c3d4" || v.Fleet != "a1b2c3d4e5f60718" {
		t.Fatalf("identity %q fleet %q", v.Identity, v.Fleet)
	}
	in.Enrolled = false
	if v := Evaluate(in); v.Identity != "Machine STUDIO · not set up" || v.Fleet != "" || v.CanSync {
		t.Fatalf("unenrolled %+v", v)
	}
	for ep, want := range map[string]string{
		"https://d0m1.com":           "https://d0m1.com/tokens",
		"http://127.0.0.1:7071/":     "http://127.0.0.1:7071/tokens",
		"https://staging.d0m1.com/x": "https://staging.d0m1.com/tokens",
		"file:///etc/passwd":         "",
		"https://user@evil.example":  "",
		"":                           "",
	} {
		if got := DashboardURL(ep); got != want {
			t.Errorf("DashboardURL(%q) = %s, want %s", ep, got, want)
		}
	}
}

func TestMenu(t *testing.T) {
	in := ok()
	in.Providers = []recent.Summary{{Provider: "cursor", LastTS: now.Add(-time.Hour), LastTokens: 5}}
	items := Menu(Evaluate(in))
	var keys []string
	for _, it := range items {
		keys = append(keys, it.Key)
	}
	wantKeys := "machine sep-providers provider:cursor provider:anthropic provider:openai provider:xai provider:google sep-actions dashboard pin sync settings log quit"
	if got := strings.Join(keys, " "); got != wantKeys {
		t.Fatalf("keys\n got %s\nwant %s", got, wantKeys)
	}
	by := map[string]Item{}
	for _, it := range items {
		by[it.Key] = it
	}
	if c := by["provider:cursor"]; c.Hidden || vis(c.Label("\t")) != "Cursor   1 h ago   +5\t24h __._0   30d __._0" {
		t.Fatalf("cursor %+v", c)
	}
	if !by["provider:anthropic"].Hidden || by["sep-providers"].Hidden {
		t.Fatal("unseen provider shown or separator hidden")
	}
	if p := by["pin"]; p.Title != "Pin to screen" || p.Action != ActPin {
		t.Errorf("pin %+v", p)
	}
	for key, act := range map[string]Action{"dashboard": ActDashboard, "pin": ActPin, "sync": ActSyncNow, "log": ActOpenLog, "quit": ActQuit} {
		if by[key].Action != act || by[key].Disabled {
			t.Errorf("%s: %+v", key, by[key])
		}
	}

	// Same keys with nothing seen; a running tick greys Sync now; pinned,
	// the item unpins.
	in.Providers, in.Ticking, in.TickStarted, in.Pinned = nil, true, now, true
	items = Menu(Evaluate(in))
	if len(items) != len(keys) {
		t.Fatalf("menu changed shape: %d items", len(items))
	}
	for _, it := range items {
		switch {
		case it.Key == "sep-providers" && !it.Hidden:
			t.Error("separator shown with no providers")
		case it.Key == "sync" && !it.Hidden:
			t.Errorf("sync while ticking: %+v", it)
		case it.Key == "pin" && (it.Title != "Unpin" || it.Action != ActUnpin):
			t.Errorf("pin while pinned: %+v", it)
		}
	}
}

func TestFormat(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1K", 10_234: "10.2K", 999_950: "1M", 3_140_000: "3.1M", 123_400_000: "123M", 11_600_000_000: "11.6B", -1500: "-1.5K"} {
		if got := Compact(n); got != want {
			t.Errorf("Compact(%d) = %s, want %s", n, got, want)
		}
	}
	for d, want := range map[time.Duration]string{0: "0s", time.Minute: "60s", 10 * time.Second: "10s", 1500 * time.Millisecond: "2s", 119 * time.Second: "119s", 2 * time.Minute: "2 min", 150 * time.Second: "3 min", -time.Second: "0s"} {
		if got := Secs(d); got != want {
			t.Errorf("Secs(%s) = %s, want %s", d, got, want)
		}
	}
	for d, want := range map[time.Duration]string{-time.Minute: "just now", 12 * time.Second: "12s ago", 5 * time.Minute: "5 min ago", 3 * time.Hour: "3 h ago", 72 * time.Hour: "3 d ago"} {
		if got := Ago(now.Add(-d), now); got != want {
			t.Errorf("Ago(%s) = %s, want %s", d, got, want)
		}
	}
}

// lum is the WCAG relative luminance of an sRGB colour.
func lum(c color.NRGBA) float64 {
	f := func(v uint8) float64 {
		x := float64(v) / 255
		if x <= 0.04045 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	return 0.2126*f(c.R) + 0.7152*f(c.G) + 0.0722*f(c.B)
}

func contrast(a, b color.NRGBA) float64 {
	la, lb := lum(a), lum(b)
	return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)
}

func TestIcon(t *testing.T) {
	light, dark := color.NRGBA{0xf3, 0xf3, 0xf3, 255}, color.NRGBA{0x20, 0x20, 0x20, 255}
	for _, c := range []Color{Green, Red} {
		for _, size := range []int{16, 32} {
			img := DrawIcon(c, size, WindowsInset)
			if a := img.NRGBAAt(0, 0).A; a != 0 {
				t.Errorf("%s %d: corner alpha %d", c, size, a)
			}
			mid := img.NRGBAAt(size/2, size/2)
			if mid.A != 255 {
				t.Errorf("%s %d: centre alpha %d", c, size, mid.A)
			}
			// The fill stands out on a dark taskbar, the rim on a light one.
			if r := contrast(mid, dark); r < 3 {
				t.Errorf("%s %d: fill/dark contrast %.2f", c, size, r)
			}
			// The rim: the first opaque pixel down the centre column.
			var edge color.NRGBA
			for y := range size {
				if edge = img.NRGBAAt(size/2, y); edge.A == 255 {
					break
				}
			}
			if r := contrast(edge, light); r < 3 {
				t.Errorf("%s %d: rim/light contrast %.2f (%v)", c, size, r, edge)
			}
		}
	}
	// Green and red must differ in more than hue for red-green colour blind
	// users: the dots differ in lightness too.
	if d := math.Abs(lum(colGreen) - lum(colRed)); d < 0.15 {
		t.Errorf("green/red luminance differ by %.2f", d)
	}

	ico := IconICO(Green)
	if binary.LittleEndian.Uint16(ico[2:]) != 1 || binary.LittleEndian.Uint16(ico[4:]) != 5 {
		t.Fatalf("ico header % x", ico[:6])
	}
	for i := range 5 {
		e := ico[6+16*i:]
		size, off := int(binary.LittleEndian.Uint32(e[8:])), int(binary.LittleEndian.Uint32(e[12:]))
		if off+size > len(ico) || binary.LittleEndian.Uint32(ico[off:]) != 40 {
			t.Fatalf("entry %d: bad dib at %d+%d of %d", i, off, size, len(ico))
		}
	}
	if png := IconPNG(Red, 32, MenuBarInset); len(png) < 8 || string(png[1:4]) != "PNG" {
		t.Fatal("not a PNG")
	}
}

func TestLiveUploadAndIdleCountdown(t *testing.T) {
	// One state from the first event queued to the last one sent: uploading,
	// with progress against the largest queue of this upload.
	in := ok()
	in.UploadPeak = 22501
	for _, c := range []struct {
		left             int
		ticking, sending bool
		want             string
	}{
		{22501, true, true, "● Uploading ▱▱▱▱▱▱▱▱▱▱ 0% · 22,501 left"},
		{11250, true, true, "● Uploading ▰▰▰▰▰▱▱▱▱▱ 50% · 11,250 left"},
		{11250, false, false, "● Uploading ▰▰▰▰▰▱▱▱▱▱ 50% · 11,250 left"}, // between ticks: still uploading
		{2250, true, false, "● Uploading ▰▰▰▰▰▰▰▰▰▱ 90% · 2,250 left"},
	} {
		in.Outbox, in.Ticking, in.Uploading, in.TickStarted = c.left, c.ticking, c.sending, now
		v := Evaluate(in)
		if v.Status != c.want || v.Syncing != c.ticking {
			t.Fatalf("left %d: %+v, want %q", c.left, v, c.want)
		}
		if Panel(v)[0] != Popup(v)[0] {
			t.Fatal("pinned and popup status differ")
		}
	}
	in.Outbox, in.Ticking, in.Uploading = 0, false, false
	if v := Evaluate(in); v.Status != "● Up to date" {
		t.Fatalf("done %+v", v)
	}
	if UploadProgress(5, 0) != "▱▱▱▱▱▱▱▱▱▱ 0%" || UploadProgress(0, 10) != "▰▰▰▰▰▰▰▰▰▰ 100%" {
		t.Fatal("progress edges")
	}
}

// A machine without a server is green when it collects: GitHub-only opens
// its Pages dashboard, local-only has no dashboard; a GitHub failure is red.
func TestDestinationsWithoutAServer(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	in := Input{Now: now, SetUp: true, Machine: "STUDIO", Fleet: "a1b2c3d4e5", LastTick: now.Add(-time.Minute),
		LastUploadErr: "stale server error", Unauthorized: true, NextTick: now.Add(40 * time.Second)}

	local := Evaluate(in)
	if local.Color != Green || local.Dashboard != "" || !local.CanSync || local.Fleet == "" || local.Identity != "Machine STUDIO · fleet a1b2c3d4" {
		t.Fatalf("local-only %+v", local)
	}
	for _, it := range Menu(local) {
		if it.Key == "dashboard" && !it.Hidden {
			t.Fatal("local-only has no dashboard to open")
		}
	}

	in.GitHub, in.PagesURL = "octo/tokenmaxr-usage", "https://octo.github.io/tokenmaxr-usage/"
	gh := Evaluate(in)
	if gh.Color != Green || gh.Dashboard != in.PagesURL {
		t.Fatalf("github-only %+v", gh)
	}
	in.GitHubErr = "the tokenmaxor App cannot write to the repository: check it is installed with access to it"
	if bad := Evaluate(in); bad.Color != Red || !strings.HasPrefix(bad.Status, "● Error: GitHub: ") {
		t.Fatalf("github error %+v", bad)
	}

	if none := Evaluate(Input{Now: now, Machine: "STUDIO"}); none.Color != Red || none.Status != "● Error: not set up" || none.CanSync {
		t.Fatalf("not set up %+v", none)
	}
}
