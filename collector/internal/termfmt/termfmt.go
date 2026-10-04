// Package termfmt lays out the CLI's reports: a title, sections, aligned
// key/value rows whose long values wrap under themselves, and a little
// colour (only on a terminal, never with NO_COLOR or TERM=dumb).
package termfmt

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"
)

// KeyWidth is the key column of a row.
const KeyWidth = 15

// Printer writes one report.
type Printer struct {
	w     io.Writer
	color bool
	width int
	first bool
}

// New prints to w: coloured and wrapped to the window when w is a terminal,
// plain and wrapped at 100 columns otherwise.
func New(w io.Writer) *Printer {
	p := &Printer{w: w, width: 100, first: true}
	if f, ok := w.(*os.File); ok {
		if cols, ok := termWidth(f); ok {
			p.width = max(60, cols-1)
			p.color = os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb" && enableColor(f)
		}
	}
	return p
}

// Plain prints without colour at a fixed width (tests).
func Plain(w io.Writer, width int) *Printer { return &Printer{w: w, width: width, first: true} }

const (
	reset  = "\x1b[0m"
	bold   = "\x1b[1m"
	dim    = "\x1b[2m"
	red    = "\x1b[31m"
	green  = "\x1b[32m"
	yellow = "\x1b[33m"
	cyan   = "\x1b[36m"
)

func (p *Printer) style(code, s string) string {
	if !p.color || s == "" {
		return s
	}
	return code + s + reset
}

// Bold, Dim, Good (green), Warn (yellow), Bad (red) and Accent (cyan) style
// a span of a value.
func (p *Printer) Bold(s string) string   { return p.style(bold, s) }
func (p *Printer) Dim(s string) string    { return p.style(dim, s) }
func (p *Printer) Good(s string) string   { return p.style(green, s) }
func (p *Printer) Warn(s string) string   { return p.style(yellow, s) }
func (p *Printer) Bad(s string) string    { return p.style(red, s) }
func (p *Printer) Accent(s string) string { return p.style(cyan, s) }

// Title is the report's first line: a bold name and dim details.
func (p *Printer) Title(name, detail string) {
	fmt.Fprintf(p.w, "%s  %s\n", p.Bold(name), p.Dim(detail))
	p.first = false
}

// Section starts a block with a blank line and a heading.
func (p *Printer) Section(name string) {
	if !p.first {
		fmt.Fprintln(p.w)
	}
	p.first = false
	fmt.Fprintln(p.w, p.style(bold+cyan, name))
}

// Row is "  key          value"; a value wider than the window wraps
// under itself, and each extra line is a continuation of the same row.
func (p *Printer) Row(key, value string, more ...string) {
	p.row(2, KeyWidth, key, value)
	for _, m := range more {
		p.row(2, KeyWidth, "", m)
	}
}

// Item is a row inside a list (sources, homes): a wider key column.
func (p *Printer) Item(key string, keyWidth int, value string) { p.row(2, keyWidth, key, value) }

// Line is an indented free line.
func (p *Printer) Line(s string) { fmt.Fprintf(p.w, "  %s\n", s) }

func (p *Printer) row(indent, keyWidth int, key, value string) {
	lead := strings.Repeat(" ", indent)
	k := key
	if pad := keyWidth - Width(key); pad > 0 {
		k += strings.Repeat(" ", pad)
	} else {
		k += " "
	}
	if key != "" {
		k = p.Dim(k)
	}
	hang := indent + max(keyWidth, Width(key)+1)
	lines := Wrap(value, p.width-hang)
	for i, l := range lines {
		if i == 0 {
			fmt.Fprintf(p.w, "%s%s%s\n", lead, k, l)
			continue
		}
		fmt.Fprintf(p.w, "%s%s\n", strings.Repeat(" ", hang), l)
	}
}

var ansi = regexp.MustCompile("\x1b\\[[0-9;]*m")

// Width is the visible width of s (colour codes excluded).
func Width(s string) int { return utf8.RuneCountInString(ansi.ReplaceAllString(s, "")) }

// Wrap breaks s at spaces into lines of at most width visible runes (a
// longer word, such as a path or URL, gets a line of its own).
func Wrap(s string, width int) []string {
	if width < 20 {
		width = 20
	}
	if Width(s) <= width {
		return []string{s}
	}
	var lines []string
	cur := ""
	for _, word := range strings.Split(s, " ") {
		switch {
		case cur == "":
			cur = word
		case Width(cur)+1+Width(word) <= width:
			cur += " " + word
		default:
			lines = append(lines, cur)
			cur = word
		}
	}
	return append(lines, cur)
}
