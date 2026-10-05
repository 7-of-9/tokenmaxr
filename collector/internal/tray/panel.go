package tray

import (
	"strings"
	"unicode/utf8"

	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
)

// LineKind is how the panel draws a line.
type LineKind int

const (
	// LineText is ink: the provider rows.
	LineText LineKind = iota
	// LineOK and LineError are the machine heading, with a green or red dot.
	LineOK
	LineError
	// LineDim is muted: the machine and fleet line.
	LineDim
	// LineRule is a thin divider (no text).
	LineRule
	// LineAction is a popup action row: clickable, highlighted under the
	// mouse or keyboard. LineActionOff is one that is greyed out (Syncing…),
	// LineArmed the Quit row waiting for its second click, in red.
	LineAction
	LineActionOff
	LineArmed
)

// PanelLine is one line of the pinned panel (or the click popup), laid out
// for a monospace font. Hi..HiEnd (runes) is a span drawn in the accent
// green: a provider row's "+67.5K", the tokens its latest event added.
// Age..AgeEnd is that row's age ("2 min ago"). Quiet means the event is
// more than an hour old, so those two spans are gray at a lighter weight
// instead, and the +value is not the accent green.
// Action is what clicking the line does in the popup (ActNone: nothing);
// Selected marks the row under the mouse or keyboard. Links are spans that
// act on their own click (Metrics.LinkAt), drawn as links: the account
// line's repository, server host and Settings….
type PanelLine struct {
	Text        string
	Kind        LineKind
	Hi, HiEnd   int
	Action      Action
	Selected    bool
	Quiet       bool
	Age, AgeEnd int
	Links       [2]Link
}

// Link is a clickable span of a line: runes From..To run Action.
type Link struct {
	From, To int
	Action   Action
}

// PanelState is what the renderer shows for the pinned panel.
type PanelState struct {
	Shown bool
	Lines []PanelLine
	// X, Y is where the panel was left (screen coordinates, the platform's
	// own); Placed is false until it has been moved once, for the default
	// spot.
	X, Y   int
	Placed bool
}

// ProviderCols are the panel's provider columns: name, age, +latest │ 24h
// total, 30d total.
var ProviderCols = []Col{
	{}, {Gap: "   "}, {Right: true, Gap: "   "},
	{Gap: "  "}, {Gap: "   "}, {Right: true, Gap: " "}, {Gap: "   "}, {Right: true, Gap: " "},
}

// cells are a provider row's cells for ProviderCols.
func (p ProviderLine) cells() []string {
	return []string{p.Name, p.Ago, p.Last, "│", "24h", p.Day, "30d", p.Month}
}

// ProviderRows are the provider lines in aligned columns:
// "Claude   2 min ago   +67.5K  │   24h 20.1B   30d 13.4B".
func ProviderRows(lines []ProviderLine) []string {
	rows := make([][]string, len(lines))
	for i, l := range lines {
		rows[i] = l.cells()
	}
	return Align(rows, ProviderCols)
}

// Panel shows the machine, current work and local provider totals.
func Panel(v View) []PanelLine {
	heading := PanelLine{Text: "● " + buildinfo.Product + " · " + v.Machine, Kind: LineOK, HiEnd: 1}
	if v.Color != Green {
		heading.Kind = LineError
	}
	out := []PanelLine{heading}
	if status := strings.TrimPrefix(v.Status, "● "); status != "" {
		out = append(out, PanelLine{Text: status, Kind: LineDim})
	}
	if v.Account != "" {
		out = append(out, PanelLine{Text: v.Account, Kind: LineDim, Links: accountLinks(v)})
	}
	if len(v.Providers) > 0 {
		out = append(out, PanelLine{Kind: LineRule})
		for i, row := range ProviderRows(v.Providers) {
			p := v.Providers[i]
			l := PanelLine{Text: row, Quiet: !p.Fresh}
			if start := runeIndex(row, p.Ago); start >= 0 {
				l.Age, l.AgeEnd = start, start+utf8.RuneCountInString(p.Ago)
			}
			// The +value is the third column: after the name and age
			// columns, right aligned, so it ends where the "│" gap starts.
			if end := runeIndex(row, "  │"); end > 0 {
				n := utf8.RuneCountInString(p.Last)
				l.Hi, l.HiEnd = end-n, end
			}
			out = append(out, l)
		}
	}
	return out
}

// accountLinks make the account line's names open what they name, one click
// from the UI (owner direction 2026-10-05): the repository after "→ " the
// GitHub dashboard, the host after "server " the server's dashboard, and
// "Settings…" (GitHub not signed in) the settings page.
func accountLinks(v View) [2]Link {
	var links [2]Link
	n := 0
	add := func(from, to int, act Action) {
		if act != ActNone && from >= 0 && to > from && n < len(links) {
			links[n] = Link{From: from, To: to, Action: act}
			n++
		}
	}
	text := v.Account
	end := utf8.RuneCountInString(text)
	server := runeIndex(text, " · server ")
	if i := runeIndex(text, "→ "); i >= 0 {
		to := end
		if server > i {
			to = server
		}
		// Published to both, GitHub has its own dashboard; to GitHub only, it is the dashboard.
		act := ActNone
		switch {
		case v.GitHubDashboard != "":
			act = ActGitHubDashboard
		case server < 0 && v.Dashboard != "":
			act = ActDashboard
		}
		add(i+2, to, act)
	} else if i := runeIndex(text, "Settings…"); i >= 0 {
		add(i, i+utf8.RuneCountInString("Settings…"), ActSettings)
	}
	if server >= 0 && v.Dashboard != "" {
		add(server+utf8.RuneCountInString(" · server "), end, ActDashboard)
	}
	return links
}

// runeIndex is the rune offset of sub in s, or -1.
func runeIndex(s, sub string) int {
	for i := range s {
		if len(s[i:]) >= len(sub) && s[i:i+len(sub)] == sub {
			return utf8.RuneCountInString(s[:i])
		}
	}
	return -1
}
