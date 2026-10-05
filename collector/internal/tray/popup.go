package tray

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
)

// The click popup: the pinned panel's sheet, then a separator and the
// actions, drawn by the same renderer on each platform. This file is its
// pure model: the rows, the state the popup keeps while open (the row
// under the mouse or keyboard, the "copied" and "click again to quit"
// feedback), the row geometry for hit-testing, keyboard navigation, and
// where it opens.

// QuitArmedText is the Quit row once clicked: a second click quits.
const QuitArmedText = "Click again to quit (stops until next login)"

// CopiedText follows the identity row briefly after it copied the fleet id.
const CopiedText = "  copied"

// DashboardText is the action row that opens the GitHub dashboard.
const DashboardText = "Open GitHub dashboard"

// Popup is the machine heading and providers, then a rule and actions:
// the GitHub dashboard, the Settings section (SettingsRows), Open log and
// Quit, which stays last.
// There is no Sync row (showing the UI syncs) and no Pin row (the main
// window replaced the pinned panel; owner direction 2026-10-05: "remove the
// "sync now" from ui menu - not needed; remove pin to screen").
func Popup(v View) []PanelLine {
	out := Panel(v)
	out = append(out, PanelLine{Kind: LineRule})
	if v.Dashboard != "" {
		out = append(out, PanelLine{Text: DashboardText, Kind: LineAction, Action: ActDashboard})
	}
	out = append(out, SettingsRows(v)...)
	out = append(out,
		PanelLine{Text: "Open log", Kind: LineAction, Action: ActOpenLog},
		PanelLine{Text: "Quit", Kind: LineAction, Action: ActQuit},
	)
	// Recessive and inert (not hoverable or clickable): the running build.
	if v.Build != "" {
		out = append(out, PanelLine{Text: v.Build, Kind: LineDim})
	}
	return out
}

// BuildLabel is "v0.2.6 · built 2026-10-04 08:15 UTC", or "dev build".
func BuildLabel(version, buildTime string) string {
	if version == "" || version == "dev" {
		return "dev build"
	}
	t, err := time.Parse(time.RFC3339, buildTime)
	if buildTime == "" || err != nil {
		return "v" + version
	}
	return "v" + version + " · built " + t.UTC().Format("2006-01-02 15:04") + " UTC"
}

// Hoverable reports whether the row is highlighted under the mouse or
// keyboard: an enabled action row.
func (l PanelLine) Hoverable() bool {
	return (l.Kind == LineAction || l.Kind == LineArmed) && l.Action != ActNone
}

// Clickable reports whether clicking the row does something: an enabled
// action row, or the identity row (it copies the fleet id).
func (l PanelLine) Clickable() bool {
	return l.Action != ActNone && l.Kind != LineActionOff
}

// PopupState is what the open popup remembers between redraws: the row
// under the mouse or keyboard (by its action, so a refresh that adds a
// provider row keeps it), and the feedback states: copied, and the Quit
// and Stop publishing rows waiting for their second click.
type PopupState struct {
	Sel       Action
	Copied    bool
	QuitArmed bool
	StopArmed bool
}

// PopupView is the rows as drawn for a state: the selected row marked,
// the identity row showing "copied", the Quit row armed.
func PopupView(lines []PanelLine, s PopupState) []PanelLine {
	out := make([]PanelLine, len(lines))
	copy(out, lines)
	for i := range out {
		l := &out[i]
		switch {
		case l.Action == ActCopyFleet && s.Copied:
			n := len([]rune(l.Text))
			l.Text += CopiedText
			l.Hi, l.HiEnd = n+2, n+len([]rune(CopiedText))
		case l.Action == ActQuit && s.QuitArmed:
			l.Text, l.Kind = QuitArmedText, LineArmed
		case l.Action == ActStopPublishing && s.StopArmed:
			// Armed, the row runs the stop (it keeps its indent).
			indent := l.Text[:len(l.Text)-len(strings.TrimLeft(l.Text, " "))]
			armed := l.Armed
			if armed == "" {
				armed = StopArmedText
			}
			l.Text, l.Kind, l.Action = indent+armed, LineArmed, ActStopPublishingNow
			l.Selected = s.Sel == ActStopPublishing || s.Sel == ActStopPublishingNow
			continue
		}
		l.Selected = s.Sel != ActNone && sameRow(l.Action, s.Sel) && l.Hoverable()
	}
	return out
}

// sameRow reports whether two actions are one row's: Stop publishing's row
// runs ActStopPublishing, and ActStopPublishingNow while armed, so the
// selection and the keyboard follow it across the arming and the disarm.
func sameRow(a, b Action) bool {
	stop := func(x Action) bool { return x == ActStopPublishing || x == ActStopPublishingNow }
	return a == b || stop(a) && stop(b)
}

// PopupNav moves the selection among the hoverable rows: dir > 0 down, < 0
// up, wrapping around. With nothing selected, down picks the first row and
// up the last.
func PopupNav(lines []PanelLine, sel Action, dir int) Action {
	var rows []Action
	cur := -1
	for _, l := range lines {
		if l.Hoverable() {
			if sameRow(l.Action, sel) {
				cur = len(rows)
			}
			rows = append(rows, l.Action)
		}
	}
	if len(rows) == 0 {
		return ActNone
	}
	switch {
	case cur < 0 && dir < 0:
		return rows[len(rows)-1]
	case cur < 0:
		return rows[0]
	case dir < 0:
		return rows[(cur+len(rows)-1)%len(rows)]
	case dir > 0:
		return rows[(cur+1)%len(rows)]
	}
	return sel
}

// Metrics are a sheet's row geometry in pixels, at the window's DPI: the
// padding inside the border, a text line, a rule, and an action row (a
// text line with room for its highlight).
type Metrics struct {
	Pad, LineH, RuleH, ActionH int
	// CharW is the monospace font's advance, for the column under x
	// (LinkAt); 0 means links are not clickable.
	CharW float64
}

// Brand is the sheet's header (owner direction 2026-10-05: "BRAND the
// context menu nicer, make it prettier, use the main icon in colour"): the
// colour icon (BrandIcon, its dot the status), about two text lines and a
// half high, left of the header's lines (PanelLine.Header: the heading, the
// status and account lines), which are taller than other lines and start
// Indent after Pad. The icon's dot replaces the heading's "● ": renderers
// draw HeadingText.
type Brand struct {
	Rows   int // rows the icon spans; 0: no heading
	Size   int // the icon's side
	Indent int
}

// Brand is the header of lines: its leading Header lines.
func (m Metrics) Brand(lines []PanelLine) Brand {
	rows, h := 0, 0
	for rows < len(lines) && lines[rows].Header {
		h += m.RowH(lines[rows])
		rows++
	}
	if rows == 0 {
		return Brand{}
	}
	size := h - max(4, m.LineH/3)
	return Brand{Rows: rows, Size: size, Indent: size + m.Pad}
}

// HeaderH is the extra height of a header line.
func (m Metrics) HeaderH() int { return m.LineH * 2 / 5 }

// HeadingText is the heading as the header draws it, without the dot
// ("tokenmaxr · AC MBP 2"), and the rune length of its product name, which
// is set strong.
func HeadingText(l PanelLine) (text string, name int) {
	text = strings.TrimPrefix(l.Text, "● ")
	if strings.HasPrefix(text, buildinfo.Product) {
		name = utf8.RuneCountInString(buildinfo.Product)
	}
	return text, name
}

// RowH is the height of a row.
func (m Metrics) RowH(l PanelLine) int {
	if l.Header && l.Kind != LineRule {
		return m.LineH + m.HeaderH()
	}
	switch l.Kind {
	case LineRule:
		return m.RuleH
	case LineAction, LineActionOff, LineArmed:
		return m.ActionH
	}
	return m.LineH
}

// Height is the sheet's height: the rows and the padding above and below.
func (m Metrics) Height(lines []PanelLine) int {
	h := 2 * m.Pad
	for _, l := range lines {
		h += m.RowH(l)
	}
	return h
}

// RowTop is the y of row i's top edge.
func (m Metrics) RowTop(lines []PanelLine, i int) int {
	y := m.Pad
	for _, l := range lines[:min(i, len(lines))] {
		y += m.RowH(l)
	}
	return y
}

// RowAt is the row under y (window coordinates), or -1 in the padding.
func (m Metrics) RowAt(lines []PanelLine, y int) int {
	top := m.Pad
	for i, l := range lines {
		h := m.RowH(l)
		if y >= top && y < top+h {
			return i
		}
		top += h
	}
	return -1
}

// ActionAt is what clicking at y does (ActNone off the clickable rows).
func (m Metrics) ActionAt(lines []PanelLine, y int) Action {
	if i := m.RowAt(lines, y); i >= 0 && lines[i].Clickable() {
		return lines[i].Action
	}
	return ActNone
}

// LinkAt is the link under x, y (window coordinates; the text starts at
// Pad), or ActNone.
func (m Metrics) LinkAt(lines []PanelLine, x, y int) Action {
	i := m.RowAt(lines, y)
	if i < 0 || m.CharW <= 0 || x < m.Pad {
		return ActNone
	}
	x -= m.Pad
	if i < m.Brand(lines).Rows {
		x -= m.Brand(lines).Indent
	}
	col := int(float64(x) / m.CharW)
	if x < 0 {
		return ActNone
	}
	for _, l := range lines[i].Links {
		if l.Action != ActNone && col >= l.From && col < l.To {
			return l.Action
		}
	}
	return ActNone
}

// ClickAt is what a click at x, y does: the link there, else its row's
// action.
func (m Metrics) ClickAt(lines []PanelLine, x, y int) Action {
	if a := m.LinkAt(lines, x, y); a != ActNone {
		return a
	}
	return m.ActionAt(lines, y)
}

// HoverAt is the row the mouse highlights at y (ActNone off the action
// rows).
func (m Metrics) HoverAt(lines []PanelLine, y int) Action {
	if i := m.RowAt(lines, y); i >= 0 && lines[i].Hoverable() {
		return lines[i].Action
	}
	return ActNone
}

// Rect is a screen rectangle (right and bottom exclusive).
type Rect struct{ Left, Top, Right, Bottom int }

// Edge is the side of the screen the taskbar is on.
type Edge int

const (
	EdgeBottom Edge = iota
	EdgeTop
	EdgeLeft
	EdgeRight
)

// TaskbarEdge is the side the taskbar takes from the monitor (the side the
// work area is short of). With none (an auto-hidden taskbar) it is the
// monitor edge nearest the anchor.
func TaskbarEdge(anchor, monitor, work Rect) Edge {
	switch {
	case work.Bottom < monitor.Bottom:
		return EdgeBottom
	case work.Top > monitor.Top:
		return EdgeTop
	case work.Left > monitor.Left:
		return EdgeLeft
	case work.Right < monitor.Right:
		return EdgeRight
	}
	cx, cy := (anchor.Left+anchor.Right)/2, (anchor.Top+anchor.Bottom)/2
	edge, best := EdgeBottom, monitor.Bottom-cy
	for _, c := range []struct {
		e Edge
		d int
	}{{EdgeTop, cy - monitor.Top}, {EdgeLeft, cx - monitor.Left}, {EdgeRight, monitor.Right - cx}} {
		if c.d < best {
			edge, best = c.e, c.d
		}
	}
	return edge
}

// PopupPos is the top-left corner of a w×h popup opened from the tray
// icon at anchor (its rectangle, or the click point as an empty one): on
// the work-area side of the taskbar, gap away from it, centred on the icon
// along the taskbar, then clamped gap inside the work area (a popup larger
// than the work area keeps its top-left corner on screen).
func PopupPos(anchor, monitor, work Rect, w, h, gap int) (x, y int) {
	cx, cy := (anchor.Left+anchor.Right)/2, (anchor.Top+anchor.Bottom)/2
	switch TaskbarEdge(anchor, monitor, work) {
	case EdgeTop:
		x, y = cx-w/2, max(anchor.Bottom, work.Top)+gap
	case EdgeLeft:
		x, y = max(anchor.Right, work.Left)+gap, cy-h/2
	case EdgeRight:
		x, y = min(anchor.Left, work.Right)-gap-w, cy-h/2
	default:
		x, y = cx-w/2, min(anchor.Top, work.Bottom)-gap-h
	}
	x = max(work.Left+gap, min(x, work.Right-gap-w))
	y = max(work.Top+gap, min(y, work.Bottom-gap-h))
	return x, y
}

// RowOf is the index of the row that runs act (Stop publishing armed or
// not), or -1.
func RowOf(lines []PanelLine, act Action) int {
	if act == ActNone {
		return -1
	}
	for i, l := range lines {
		if sameRow(l.Action, act) {
			return i
		}
	}
	return -1
}

// KeepRowY is the top of an open h-pixel-high popup whose clicked row
// stays where it was on screen (at rowY) now that the rows above it put
// its top rowTop below the popup's: a popup anchored to a bottom taskbar
// would otherwise grow upward and slide another row under the pointer.
// It stays gap inside the work area.
func KeepRowY(rowY, rowTop, h int, work Rect, gap int) int {
	return max(work.Top+gap, min(rowY-rowTop, work.Bottom-gap-h))
}

// HoverPopup moves the highlight to sel (ActNone: none). Leaving the Quit
// row disarms it. changed is false when the highlight was already sel.
func HoverPopup(st *PopupState, sel Action) (changed bool) {
	if sameRow(st.Sel, sel) {
		return false // the same row (Stop publishing, armed or not)
	}
	st.Sel = sel
	if sel != ActQuit {
		st.QuitArmed = false
	}
	if sel != ActStopPublishing && sel != ActStopPublishingNow {
		st.StopArmed = false
	}
	return true
}

// PopupClick is what one row click asks the window to do.
type PopupClick struct {
	// Run is the action handed to the app. ActNone means the click only
	// changed the popup (the first Quit click, or a miss).
	Run Action
	// Close means the popup goes away before Run.
	Close bool
	// ArmCopied starts the "copied" timer; ArmQuit the disarm timer.
	ArmCopied bool
	ArmQuit   bool
	// Redraw means the rows changed and the window should paint.
	Redraw bool
}

// ClickPopup applies a row click. The identity row copies and says so.
// The first Quit click arms the row; the second returns ActQuitNow and
// closes. Stop publishing arms the same way (the armed row runs
// ActStopPublishingNow), and the popup stays open. The Settings section's
// other rows run with the popup open (Action.Stays); any other action
// closes, then runs.
func ClickPopup(st *PopupState, act Action) PopupClick {
	switch act {
	case ActNone:
		return PopupClick{}
	case ActStopPublishing, ActStopPublishingNow:
		// The armed row's action is ActStopPublishingNow; the keyboard's
		// selection stays ActStopPublishing, so Return arms, then runs.
		if !st.StopArmed {
			st.StopArmed, st.Sel = true, ActStopPublishing
			return PopupClick{ArmQuit: true, Redraw: true}
		}
		st.StopArmed = false
		return PopupClick{Run: ActStopPublishingNow, Redraw: true}
	case ActCopyFleet:
		st.Copied = true
		return PopupClick{Run: ActCopyFleet, ArmCopied: true, Redraw: true}
	case ActQuit:
		if !st.QuitArmed {
			st.QuitArmed, st.Sel = true, ActQuit
			return PopupClick{ArmQuit: true, Redraw: true}
		}
		return PopupClick{Run: ActQuitNow, Close: true}
	default:
		return PopupClick{Run: act, Close: !act.Stays()}
	}
}

// ClearCopied drops the "copied" feedback. changed is false if it was
// already gone.
func ClearCopied(st *PopupState) (changed bool) {
	changed = st.Copied
	st.Copied = false
	return changed
}

// DisarmQuit drops the armed Quit (and Stop publishing) row. The highlight
// stays where it is.
func DisarmQuit(st *PopupState) (changed bool) {
	changed = st.QuitArmed || st.StopArmed
	st.QuitArmed, st.StopArmed = false, false
	return changed
}

// FlipDown turns a y-up rectangle (origin at its bottom-left) into the
// y-down rectangle PopupPos uses. flip is the y-up coordinate that is
// y-down 0, the primary screen's top.
func FlipDown(x, y, w, h, flip int) Rect {
	top := flip - (y + h)
	return Rect{Left: x, Top: top, Right: x + w, Bottom: top + h}
}

// FlipUp is the y-up origin (bottom-left) of a window h tall whose y-down
// top-left is (x, top).
func FlipUp(x, top, h, flip int) (ox, oy int) {
	return x, flip - top - h
}

// String names an action (diagnostics).
func (a Action) String() string {
	names := map[Action]string{
		ActNone: "none", ActCopyFleet: "copy-fleet", ActDashboard: "dashboard", ActSyncNow: "sync",
		ActOpenLog: "log", ActQuit: "quit", ActPin: "pin", ActUnpin: "unpin", ActQuitNow: "quit-now",
		ActSettings: "settings", ActSettingsToggle: "settings-toggle", ActTrayOnly: "tray-only",
		ActStopPublishing: "stop-publishing", ActStopPublishingNow: "stop-publishing-now",
		ActConnectGitHub: "connect-github", ActAdvanced: "advanced",
	}
	if n, ok := names[a]; ok {
		return n
	}
	return "action-" + strconv.Itoa(int(a))
}

// SheetText is lines as plain text for app.dump, one per line after
// indent: a rule is "----"; a row that does something is tagged with its
// action ("action" rows are hover-highlighted, "click" rows are not), a
// greyed-out one "disabled", the highlighted one "selected".
func SheetText(lines []PanelLine, indent string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(indent)
		if l.Kind == LineRule {
			b.WriteString("----\n")
			continue
		}
		b.WriteString(l.Text)
		switch {
		case l.Hoverable():
			b.WriteString("   [action: " + l.Action.String() + "]")
		case l.Clickable():
			b.WriteString("   [click: " + l.Action.String() + "]")
		case l.Kind == LineActionOff:
			b.WriteString("   [disabled]")
		}
		if l.Selected {
			b.WriteString(" [selected]")
		}
		b.WriteString("\n")
	}
	return b.String()
}
