//go:build darwin && cgo

package ui

// The main window on macOS (window mode, the default): the app is a
// regular app with a Dock icon (the status dot, in the menu-bar icon's
// colour) and a normal titled window, "tokenmaxr", hosting the same sheet
// view as the popup with the same rows and actions (tray.ClickWindow: the
// window stays open after an action). Clicking the Dock icon shows it
// (applicationShouldHandleReopen), closing it hides it, Cmd-Q and the Dock
// menu's Quit quit (applicationShouldTerminate), and Hide (Cmd-H) counts as
// off screen. At login it starts hidden, with its Dock icon. The window itself lives in
// native_darwin.go; this file is its state, as popup_darwin.go is the
// popup's.

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/tray"
)

// macWindow is the one main window.
type macWindow struct {
	mu      sync.Mutex
	enabled bool // window mode
	created bool
	visible bool // on screen (not hidden or miniaturized)
	front   bool // show it with the first rows (a normal start, or a reopen before them)
	base    []tray.PanelLine
	lines   []tray.PanelLine // as last drawn, for hit-testing
	st      tray.PopupState
	m       tray.Metrics
	h       Handler
	color   tray.Color
	colored bool
	shows   int
	copiedT *time.Timer
	armedT  *time.Timer
}

var macWin macWindow

// startWindow turns window mode on (systray is ready).
func startWindow(h Handler) {
	macWin.mu.Lock()
	macWin.enabled, macWin.h, macWin.front = true, h, !h.Minimized
	macWin.mu.Unlock()
}

// setWindowLines is the app's latest rows (renderer.SetWindow). The first
// creates the window (shown unless the app started minimized); later ones
// redraw it while it is on screen.
func setWindowLines(lines []tray.PanelLine, h Handler) {
	w := &macWin
	w.mu.Lock()
	w.h = h
	w.base = slices.Clone(lines)
	if !w.enabled || len(w.base) == 0 {
		w.mu.Unlock()
		return
	}
	front := w.front
	w.front = false
	draw := front || w.visible || !w.created
	w.mu.Unlock()
	if draw {
		w.redraw(front)
	}
}

// showMainWindow shows the window, deminiaturized, in front (the Dock icon,
// a second launch).
func showMainWindow() {
	w := &macWin
	w.mu.Lock()
	w.shows++
	if !w.enabled {
		w.mu.Unlock()
		return
	}
	if len(w.base) == 0 {
		w.front = true // with the first rows
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()
	w.redraw(true)
}

// setWindowIcon draws the Dock icon in the menu-bar icon's colour.
func setWindowIcon(c tray.Color) {
	w := &macWin
	w.mu.Lock()
	if !w.enabled || (w.colored && w.color == c) {
		w.mu.Unlock()
		return
	}
	w.color, w.colored = c, true
	w.mu.Unlock()
	setDockIcon(c)
}

// closeMainWindow hides the window (the app quits).
func closeMainWindow() {
	w := &macWin
	w.mu.Lock()
	w.stopTimersLocked()
	created := w.created
	w.mu.Unlock()
	if created {
		hideMainNative()
	}
	w.setVisible(false)
}

// redraw paints the current rows; front brings the window to the front.
func (w *macWindow) redraw(front bool) {
	w.mu.Lock()
	lines := tray.PopupView(w.base, w.st)
	wider := tray.PopupView(w.base, tray.PopupState{Copied: true, QuitArmed: true})
	w.mu.Unlock()
	text, kinds, spans, m, wd, ht := sheetBox(lines, wider, false)
	w.mu.Lock()
	w.lines, w.m, w.created = lines, m, true
	w.mu.Unlock()
	showMainNative(text, kinds, spans, m, wd, ht, front)
	if front && !w.setVisible(true) {
		// Already on screen and brought to the front (the Dock icon, a
		// second launch): the app checks again whenever its UI is shown.
		w.mu.Lock()
		h := w.h
		w.mu.Unlock()
		if h.Shown != nil {
			go h.Shown(true)
		}
	}
}

// setVisible records whether the window is on screen, tells the app when
// that changes and reports whether it did.
func (w *macWindow) setVisible(v bool) bool {
	w.mu.Lock()
	changed := w.visible != v
	w.visible = v
	h := w.h
	if !v {
		tray.HoverPopup(&w.st, tray.ActNone)
	}
	w.mu.Unlock()
	if changed && h.Shown != nil {
		go h.Shown(v)
	}
	return changed
}

func (w *macWindow) stopTimersLocked() {
	if w.copiedT != nil {
		w.copiedT.Stop()
		w.copiedT = nil
	}
	if w.armedT != nil {
		w.armedT.Stop()
		w.armedT = nil
	}
}

// arm starts the "copied" (or armed-Quit) timer that clears that state.
func (w *macWindow) arm(copied bool) {
	d := armedFor
	if copied {
		d = copiedFor
	}
	t := time.AfterFunc(d, func() {
		w.mu.Lock()
		var changed bool
		if copied {
			changed = tray.ClearCopied(&w.st)
		} else {
			changed = tray.DisarmQuit(&w.st)
		}
		visible := w.visible
		w.mu.Unlock()
		if changed && visible {
			w.redraw(false)
		}
	})
	w.mu.Lock()
	defer w.mu.Unlock()
	if copied {
		if w.copiedT != nil {
			w.copiedT.Stop()
		}
		w.copiedT = t
	} else {
		if w.armedT != nil {
			w.armedT.Stop()
		}
		w.armedT = t
	}
}

// do runs a row's action as the popup does, but the window stays.
func (w *macWindow) do(act tray.Action) {
	w.mu.Lock()
	h := w.h
	res := tray.ClickWindow(&w.st, act)
	w.mu.Unlock()
	if res.ArmCopied {
		w.arm(true)
	}
	if res.ArmQuit {
		w.arm(false)
	}
	if res.Redraw {
		w.redraw(false)
	}
	if res.Run != tray.ActNone && h.Click != nil {
		go h.Click(res.Run)
	}
}

// windowHover highlights the action row at y (-1: none) and reports
// whether the row is clickable (the pointer becomes a hand).
func windowHover(x, y int) int {
	w := &macWin
	w.mu.Lock()
	sel, clickable := tray.ActNone, false
	if y >= 0 {
		sel = w.m.HoverAt(w.lines, y)
		clickable = w.m.ClickAt(w.lines, x, y) != tray.ActNone
	}
	changed := tray.HoverPopup(&w.st, sel)
	w.mu.Unlock()
	if changed {
		w.redraw(false)
	}
	if clickable {
		return 1
	}
	return 0
}

func windowClick(x, y int) {
	w := &macWin
	w.mu.Lock()
	act := w.m.ClickAt(w.lines, x, y)
	w.mu.Unlock()
	w.do(act)
}

// windowKey: 1 up, 2 down (Tab), 3 run (Return, Space), 4 Esc (hide).
func windowKey(key int) {
	w := &macWin
	w.mu.Lock()
	sel, lines := w.st.Sel, w.lines
	w.mu.Unlock()
	switch key {
	case 1, 2:
		dir := -1
		if key == 2 {
			dir = 1
		}
		w.mu.Lock()
		changed := tray.HoverPopup(&w.st, tray.PopupNav(lines, sel, dir))
		w.mu.Unlock()
		if changed {
			w.redraw(false)
		}
	case 3:
		w.do(sel)
	case 4:
		hideMainNative()
		w.setVisible(false)
	}
}

// windowResign drops the highlight when the window loses the keyboard.
func windowResign() {
	w := &macWin
	w.mu.Lock()
	changed := tray.HoverPopup(&w.st, tray.ActNone)
	visible := w.visible
	w.mu.Unlock()
	if changed && visible {
		w.redraw(false)
	}
}

// windowQuit is Cmd-Q, the Dock menu's Quit and a logout: quit as the Quit
// row does (no second question). False: the app is not up yet (no window
// mode started), so nothing quits it.
func windowQuit() bool {
	w := &macWin
	w.mu.Lock()
	h := w.h
	w.mu.Unlock()
	if h.Click == nil {
		return false
	}
	go h.Click(tray.ActQuitNow)
	return true
}

// windowDebug describes the main window for app.dump.
func windowDebug() string {
	exists, visible, mini, key := mainNativeState()
	w := &macWin
	w.mu.Lock()
	defer w.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "main window: NSWindow %q, Dock icon (regular app); exists %v, visible %v, miniaturized %v, key %v\n",
		tray.WindowTitle, exists, visible, mini, key)
	fmt.Fprintf(&b, "main window state: on screen %v, show requests %d, selected %s, quit armed %v\n", w.visible, w.shows, w.st.Sel, w.st.QuitArmed)
	b.WriteString("main window rows as drawn:\n" + tray.SheetText(tray.PopupView(w.base, w.st), "  "))
	return b.String()
}
