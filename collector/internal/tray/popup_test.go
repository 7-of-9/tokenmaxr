package tray

import (
	"strings"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/recent"
)

func popupInput() Input {
	in := ok()
	in.Providers = []recent.Summary{
		{Provider: "anthropic", LastTS: now.Add(-8 * time.Hour), LastTokens: 787_000, PastDay: 194_000_000, PastMonth: 13_500_000_000},
	}
	return in
}

func TestPopupRows(t *testing.T) {
	v := Evaluate(popupInput())
	got := Popup(v)
	want := []PanelLine{
		{Text: "● tokenmaxr · STUDIO", Kind: LineOK, HiEnd: 1},
		{Text: "Up to date · next sync in 40s", Kind: LineDim},
		{Text: "GitHub: not signed in · Settings… · server d0m1.com", Kind: LineDim},
		{Kind: LineRule},
		{Text: "Claude   8 h ago   +787K  │   24h 194M   30d 13.5B", Kind: LineText, Hi: 19, HiEnd: 24, Quiet: true, Age: 9, AgeEnd: 16},
		{Kind: LineRule},
		{Text: "Open dashboard", Kind: LineAction, Action: ActDashboard},
		{Text: "Pin to screen", Kind: LineAction, Action: ActPin},
		{Text: "Sync now", Kind: LineAction, Action: ActSyncNow},
		{Text: "Settings…", Kind: LineAction, Action: ActSettings},
		{Text: "Open log", Kind: LineAction, Action: ActOpenLog},
		{Text: "Quit", Kind: LineAction, Action: ActQuit},
		{Text: "dev build", Kind: LineDim},
	}
	if len(got) != len(want) {
		t.Fatalf("%d rows, want %d:\n%s", len(got), len(want), SheetText(got, ""))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
	// The popup's content is the pinned panel's, line for line.
	for i, l := range Panel(v) {
		if l.Text != got[i].Text || l.Kind != got[i].Kind || l.Hi != got[i].Hi {
			t.Errorf("row %d differs from the panel: %+v vs %+v", i, got[i], l)
		}
	}
	// Only the action rows highlight; the identity row is clickable too.
	for _, l := range got {
		hover := l.Kind == LineAction
		if l.Hoverable() != hover {
			t.Errorf("%q hoverable %v", l.Text, l.Hoverable())
		}
		click := hover || l.Action == ActCopyFleet
		if l.Clickable() != click {
			t.Errorf("%q clickable %v", l.Text, l.Clickable())
		}
	}

	// Pinned: Unpin. Ticking: Syncing…, greyed out. Not enrolled: no copy,
	// no sync. The rows end with Quit, then the inert build footer.
	in := popupInput()
	in.Pinned, in.Ticking, in.TickStarted = true, true, now
	rows := Popup(Evaluate(in))
	if p := rows[len(rows)-6]; p.Text != "Unpin" || p.Action != ActUnpin {
		t.Errorf("pinned row %+v", p)
	}
	if s := rows[len(rows)-5]; s.Text != "Scanning…" || s.Kind != LineActionOff || s.Clickable() || s.Hoverable() {
		t.Errorf("syncing row %+v", s)
	}
	in = popupInput()
	in.Enrolled = false
	rows = Popup(Evaluate(in))
	for _, l := range rows {
		if l.Action == ActCopyFleet || l.Action == ActSyncNow || l.Action == ActDashboard {
			t.Errorf("unenrolled row %+v", l)
		}
	}
	if s := rows[len(rows)-5]; s.Text != "Sync now" || s.Kind != LineActionOff {
		t.Errorf("unenrolled sync row %+v", s)
	}
}

func TestPopupView(t *testing.T) {
	base := Popup(Evaluate(popupInput()))
	// Nothing selected: the rows as they are.
	for i, l := range PopupView(base, PopupState{}) {
		if l != base[i] {
			t.Fatalf("row %d changed: %+v", i, l)
		}
	}
	v := PopupView(base, PopupState{Sel: ActOpenLog, Copied: true})
	var sel []string
	for _, l := range v {
		if l.Selected {
			sel = append(sel, l.Text)
		}
	}
	if strings.Join(sel, ",") != "Open log" {
		t.Errorf("selected %q", sel)
	}
	for _, l := range v {
		if l.Action == ActCopyFleet || strings.Contains(l.Text, "fleet") {
			t.Fatalf("redundant footer remains: %+v", l)
		}
	}
	quitRow := func(lines []PanelLine) PanelLine {
		for _, l := range lines {
			if l.Action == ActQuit {
				return l
			}
		}
		t.Fatal("no Quit row")
		return PanelLine{}
	}
	q := PopupView(base, PopupState{Sel: ActQuit, QuitArmed: true})
	if armed := quitRow(q); armed.Text != QuitArmedText || armed.Kind != LineArmed || !armed.Selected || !armed.Hoverable() {
		t.Errorf("armed quit %+v", armed)
	}
	if foot := q[len(q)-1]; foot.Kind != LineDim || foot.Selected || foot.Hoverable() {
		t.Errorf("the build footer must stay inert while Quit is armed: %+v", foot)
	}
	if quitRow(base).Text != "Quit" {
		t.Error("PopupView changed its input")
	}
}

func TestPopupNav(t *testing.T) {
	rows := Popup(Evaluate(popupInput()))
	order := []Action{ActDashboard, ActPin, ActSyncNow, ActSettings, ActOpenLog, ActQuit}
	cases := []struct {
		sel  Action
		dir  int
		want Action
	}{
		{ActNone, 1, ActDashboard},
		{ActNone, -1, ActQuit},
		{ActDashboard, 1, ActPin},
		{ActQuit, 1, ActDashboard}, // wraps
		{ActDashboard, -1, ActQuit},
		{ActSyncNow, -1, ActPin},
		{ActCopyFleet, 1, ActDashboard}, // not a hover row: starts over
		{ActOpenLog, 0, ActOpenLog},
	}
	for _, c := range cases {
		if got := PopupNav(rows, c.sel, c.dir); got != c.want {
			t.Errorf("PopupNav(%s, %d) = %s, want %s", c.sel, c.dir, got, c.want)
		}
	}
	// Down through every row and back to the start.
	sel := ActNone
	for i := range len(order) + 1 {
		sel = PopupNav(rows, sel, 1)
		if want := order[i%len(order)]; sel != want {
			t.Fatalf("step %d: %s, want %s", i, sel, want)
		}
	}
	// A greyed-out Sync is skipped.
	in := popupInput()
	in.Ticking, in.TickStarted = true, now
	busy := Popup(Evaluate(in))
	if got := PopupNav(busy, ActPin, 1); got != ActSettings {
		t.Errorf("skip Syncing…: %s", got)
	}
	if got := PopupNav(nil, ActNone, 1); got != ActNone {
		t.Errorf("no rows: %s", got)
	}
}

func TestPopupHitTest(t *testing.T) {
	rows := Popup(Evaluate(popupInput()))
	m := Metrics{Pad: 12, LineH: 20, RuleH: 9, ActionH: 28}
	// heading, status, account, rule, Claude, rule, 6 actions, build footer.
	if h := m.Height(rows); h != 12+20+20+20+9+20+9+6*28+20+12 {
		t.Fatalf("height %d", h)
	}
	if top := m.RowTop(rows, 6); top != 12+20+20+20+9+20+9 {
		t.Fatalf("first action at %d", top)
	}
	first := m.RowTop(rows, 6) // 110
	cases := []struct {
		y            int
		click, hover Action
	}{
		{0, ActNone, ActNone},  // padding
		{11, ActNone, ActNone}, // padding
		{12, ActNone, ActNone}, // status
		{first, ActDashboard, ActDashboard},
		{first + 27, ActDashboard, ActDashboard},
		{first + 28, ActPin, ActPin},
		{first + 2*28 + 5, ActSyncNow, ActSyncNow},
		{first + 3*28, ActSettings, ActSettings},
		{first + 4*28, ActOpenLog, ActOpenLog},
		{first + 5*28 + 27, ActQuit, ActQuit},
		{first + 6*28, ActNone, ActNone}, // build footer
		{-5, ActNone, ActNone},
	}
	for _, c := range cases {
		if got := m.ActionAt(rows, c.y); got != c.click {
			t.Errorf("ActionAt(%d) = %s, want %s", c.y, got, c.click)
		}
		if got := m.HoverAt(rows, c.y); got != c.hover {
			t.Errorf("HoverAt(%d) = %s, want %s", c.y, got, c.hover)
		}
	}
	// Syncing…: neither clicks nor highlights.
	in := popupInput()
	in.Ticking, in.TickStarted = true, now
	busy := Popup(Evaluate(in))
	if a, h := m.ActionAt(busy, first+2*28+5), m.HoverAt(busy, first+2*28+5); a != ActNone || h != ActNone {
		t.Errorf("syncing row: %s %s", a, h)
	}
}

func TestPopupClick(t *testing.T) {
	var st PopupState
	if !HoverPopup(&st, ActPin) || st.Sel != ActPin {
		t.Fatalf("hover %+v", st)
	}
	if HoverPopup(&st, ActPin) {
		t.Fatal("hover unchanged")
	}
	st.QuitArmed = true
	if !HoverPopup(&st, ActOpenLog) || st.QuitArmed || st.Sel != ActOpenLog {
		t.Fatalf("leaving quit: %+v", st)
	}

	st = PopupState{}
	if r := ClickPopup(&st, ActNone); r != (PopupClick{}) {
		t.Fatalf("miss %+v", r)
	}
	r := ClickPopup(&st, ActCopyFleet)
	if !st.Copied || !r.ArmCopied || !r.Redraw || r.Close || r.Run != ActCopyFleet {
		t.Fatalf("copy %+v %+v", r, st)
	}
	r = ClickPopup(&st, ActQuit)
	if !st.QuitArmed || st.Sel != ActQuit || r.Run != ActNone || !r.ArmQuit || !r.Redraw || r.Close {
		t.Fatalf("arm %+v %+v", r, st)
	}
	r = ClickPopup(&st, ActQuit)
	if r.Run != ActQuitNow || !r.Close || r.Redraw {
		t.Fatalf("quit %+v", r)
	}
	st = PopupState{}
	r = ClickPopup(&st, ActSyncNow)
	if r.Run != ActSyncNow || !r.Close || r.Redraw {
		t.Fatalf("sync %+v", r)
	}
	st.Copied, st.QuitArmed = true, true
	if !ClearCopied(&st) || st.Copied || ClearCopied(&st) {
		t.Fatal("clear copied")
	}
	if !DisarmQuit(&st) || st.QuitArmed || DisarmQuit(&st) {
		t.Fatal("disarm")
	}
}

func TestFlipScreen(t *testing.T) {
	// A 1000-point primary screen, menu bar 24, dock 80, icon in the bar.
	const flip = 1000
	mon := FlipDown(0, 0, 1440, 1000, flip)
	work := FlipDown(0, 80, 1440, 896, flip)
	anchor := FlipDown(1200, 976, 24, 24, flip)
	if mon.Top != 0 || mon.Bottom != 1000 || work.Top != 24 || work.Bottom != 920 {
		t.Fatalf("screen mon %+v work %+v", mon, work)
	}
	if anchor.Top != 0 || anchor.Bottom != 24 {
		t.Fatalf("anchor %+v", anchor)
	}
	const w, h, gap = 400, 300, 8
	x, top := PopupPos(anchor, mon, work, w, h, gap)
	ox, oy := FlipUp(x, top, h, flip)
	// Centred on the icon, 8 points under the menu bar.
	if x != 1212-w/2 || oy != 976-gap-h || ox != x {
		t.Fatalf("popup at %d,%d (y-down top %d)", ox, oy, top)
	}
}

func TestPopupPos(t *testing.T) {
	mon := Rect{0, 0, 1920, 1080}
	const w, h, gap = 400, 300, 8
	cases := []struct {
		name   string
		anchor Rect
		mon    Rect
		work   Rect
		x, y   int
		edge   Edge
	}{
		// Bottom taskbar, icon near the right: above the taskbar, centred
		// on the icon, then pulled inside the right edge.
		{"bottom, right corner", Rect{1800, 1044, 1824, 1068}, mon, Rect{0, 0, 1920, 1032}, 1920 - gap - w, 1032 - gap - h, EdgeBottom},
		{"bottom, centred", Rect{900, 1044, 924, 1068}, mon, Rect{0, 0, 1920, 1032}, 912 - w/2, 1032 - gap - h, EdgeBottom},
		// The click point alone (no icon rectangle).
		{"bottom, cursor only", Rect{1000, 1050, 1000, 1050}, mon, Rect{0, 0, 1920, 1032}, 1000 - w/2, 1032 - gap - h, EdgeBottom},
		// An icon in the overflow flyout, above the taskbar: above the icon.
		{"bottom, overflow flyout", Rect{1700, 900, 1732, 932}, mon, Rect{0, 0, 1920, 1032}, 1920 - gap - w, 900 - gap - h, EdgeBottom},
		{"top", Rect{1800, 10, 1824, 34}, mon, Rect{0, 48, 1920, 1080}, 1920 - gap - w, 48 + gap, EdgeTop},
		{"left", Rect{10, 900, 34, 924}, mon, Rect{48, 0, 1920, 1080}, 48 + gap, 912 - h/2, EdgeLeft},
		{"left, middle", Rect{10, 500, 34, 524}, mon, Rect{48, 0, 1920, 1080}, 48 + gap, 512 - h/2, EdgeLeft},
		{"right", Rect{1890, 1000, 1914, 1024}, mon, Rect{0, 0, 1872, 1080}, 1872 - gap - w, 1080 - gap - h, EdgeRight},
		// Auto-hidden taskbar: the work area is the monitor; the nearest
		// edge to the icon decides.
		{"autohide bottom", Rect{1800, 1060, 1824, 1080}, mon, mon, 1920 - gap - w, 1060 - gap - h, EdgeBottom},
		{"autohide right", Rect{1900, 600, 1920, 624}, mon, mon, 1900 - gap - w, 612 - h/2, EdgeRight},
		// A second monitor to the left, with negative coordinates.
		{"second monitor", Rect{-200, 1044, -176, 1068}, Rect{-1920, 0, 0, 1080}, Rect{-1920, 0, 0, 1032}, -gap - w, 1032 - gap - h, EdgeBottom},
		// A popup taller than the work area keeps its top on screen.
		{"too tall", Rect{900, 1044, 924, 1068}, Rect{0, 0, 1920, 280}, Rect{0, 0, 1920, 240}, 912 - w/2, gap, EdgeBottom},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if e := TaskbarEdge(c.anchor, c.mon, c.work); e != c.edge {
				t.Errorf("edge %d, want %d", e, c.edge)
			}
			x, y := PopupPos(c.anchor, c.mon, c.work, w, h, gap)
			if x != c.x || y != c.y {
				t.Errorf("at %d,%d, want %d,%d", x, y, c.x, c.y)
			}
			if c.name != "too tall" && (x < c.work.Left || y < c.work.Top || x+w > c.work.Right || y+h > c.work.Bottom) {
				t.Errorf("%d,%d %dx%d is off the work area %+v", x, y, w, h, c.work)
			}
		})
	}
}

func TestSheetText(t *testing.T) {
	rows := PopupView(Popup(Evaluate(popupInput())), PopupState{Sel: ActPin})
	got := SheetText(rows, "  ")
	for _, want := range []string{
		"  ----\n",
		"  ● tokenmaxr · STUDIO\n",
		"  Open dashboard   [action: dashboard]\n",
		"  Pin to screen   [action: pin] [selected]\n",
		"  Quit   [action: quit]\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}
