package tray

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Compact formats a token count like the site: 999, 1.2K, 18.9B, 123M.
func Compact(n int64) string {
	if n < 0 {
		return "-" + Compact(-n)
	}
	if n < 1000 {
		return strconv.FormatInt(n, 10)
	}
	units := []string{"K", "M", "B", "T"}
	v := float64(n)
	u := -1
	for v >= 999.95 && u < len(units)-1 {
		v /= 1000
		u++
	}
	s := strconv.FormatFloat(v, 'f', 1, 64)
	if v >= 100 {
		s = strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strings.TrimSuffix(s, ".0") + units[u]
}

// Count formats a whole number with thousands separators: 41,000.
func Count(n int) string {
	s := strconv.Itoa(max(n, -n))
	var b strings.Builder
	if n < 0 {
		b.WriteByte('-')
	}
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Plural is "1 file" / "2K files".
func Plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return Compact(int64(n)) + " " + many
}

// Menu padding: Unicode spaces as wide as a digit and a full stop in the
// menu font (its figures are tabular), so padded figures end in line.
const (
	FigureSpace      = " "
	PunctuationSpace = " "
)

// MenuFigure left-pads a Compact figure to the width of "20.1B" (three
// digits, a point and a unit): "900" gets two spaces for the missing point
// and unit, "1.2K" one for the missing digit. After the tab of a Windows
// menu item every row's text starts at the same x, so padded figures line
// up on the right.
func MenuFigure(s string) string {
	digits, point, unit := 0, false, false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '.':
			point = true
		default:
			unit = true
		}
	}
	pad := strings.Repeat(FigureSpace, max(0, 3-digits))
	if !point {
		pad += PunctuationSpace
	}
	if !unit {
		pad += FigureSpace
	}
	return pad + s
}

// Col is one column for Align: Right aligns it to the right, Gap is what
// separates it from the column before (ignored for the first).
type Col struct {
	Right bool
	Gap   string
}

// Align lays rows of cells out in a monospace font: each column padded with
// spaces to its widest cell (in runes), left or right aligned, joined by the
// gaps, trailing spaces trimmed. A row may have fewer cells than cols.
func Align(rows [][]string, cols []Col) []string {
	width := make([]int, len(cols))
	for _, r := range rows {
		for i, c := range r[:min(len(r), len(cols))] {
			width[i] = max(width[i], utf8.RuneCountInString(c))
		}
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		var b strings.Builder
		for i, c := range r[:min(len(r), len(cols))] {
			if i > 0 {
				b.WriteString(cols[i].Gap)
			}
			pad := strings.Repeat(" ", width[i]-utf8.RuneCountInString(c))
			if cols[i].Right {
				b.WriteString(pad + c)
			} else {
				b.WriteString(c + pad)
			}
		}
		out = append(out, strings.TrimRight(b.String(), " "))
	}
	return out
}

// Ago is a short age: "12s ago", "5 min ago", "3 h ago", "2 d ago".
func Ago(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d d ago", int(d.Hours()/24))
}

// Secs is a short interval, rounded up to whole seconds: "60s", "9s",
// "5 min" (two minutes and over, in whole minutes, rounded up).
func Secs(d time.Duration) string {
	s := int64((max(d, 0) + time.Second - 1) / time.Second)
	if s >= 120 {
		return fmt.Sprintf("%d min", (s+59)/60)
	}
	return fmt.Sprintf("%ds", s)
}

// ShortError reduces an error to a few words (errors never hold secrets,
// but URLs and wrapped detail do not fit a menu line).
func ShortError(err string) string {
	e := strings.ToLower(err)
	switch {
	case strings.Contains(e, "401"):
		return "token rejected"
	case strings.Contains(e, "timeout"), strings.Contains(e, "timed out"), strings.Contains(e, "deadline"):
		return "timed out"
	case strings.Contains(e, "no such host"), strings.Contains(e, "dial"), strings.Contains(e, "network is unreachable"), strings.Contains(e, "connection refused"):
		return "offline"
	case strings.Contains(e, "http 5"):
		return "server error"
	case strings.Contains(e, "http 4"):
		return "request refused"
	}
	if i := strings.Index(err, ": "); i > 0 && i < 40 {
		err = err[:i]
	}
	return clip(err, 32)
}

// clip shortens s to at most n runes, marking the cut with an ellipsis.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "…"
}
