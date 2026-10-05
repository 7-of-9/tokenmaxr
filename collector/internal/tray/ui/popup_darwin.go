//go:build darwin && cgo

package ui

// The click popup on macOS. Clicking the menu-bar icon, left or right,
// opens the pinned panel's sheet with the action rows under it
// (tray.Popup). The window and the painting live in native_darwin.go, one
// sheet view shared with the panel; this file is the same state machine
// the Windows popup runs (tray.HoverPopup, tray.ClickPopup).

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/tray"
)

const (
	copiedFor   = 1500 * time.Millisecond
	armedFor    = 5 * time.Second
	reopenGuard = 300 * time.Millisecond
)

// macAnchor is the icon's rectangle in y-up screen points (a cursor point
// has w = h = 0).
type macAnchor struct {
	x, y, w, h int
	by         string
}

// macPopup is the one click popup.
type macPopup struct {
	mu       sync.Mutex
	open     bool
	base     []tray.PanelLine
	lines    []tray.PanelLine // as last drawn, for hit-testing
	st       tray.PopupState
	m        tray.Metrics
	anchor   macAnchor
	lastW    int
	lastH    int
	closedAt time.Time
	closedBy string
	h        Handler
	copiedT  *time.Timer
	armedT   *time.Timer
}

var macPop macPopup

// setPopup is the app's latest rows (renderer.SetPopup). An open popup
// redraws with them.
func setPopup(lines []tray.PanelLine, h Handler) {
	macPop.mu.Lock()
	macPop.h = h
	macPop.base = append([]tray.PanelLine(nil), lines...)
	open := macPop.open
	macPop.mu.Unlock()
	if open {
		macPop.redraw(false)
	}
}

// popupTap toggles the popup. A click that closes it by taking the focus
// arrives again as a tap; that tap does not reopen it.
func popupTap(h Handler) {
	ax, ay, aw, ah, by := statusAnchor()
	macPop.mu.Lock()
	macPop.h = h
	if macPop.open {
		macPop.mu.Unlock()
		macPop.close("icon clicked again")
		return
	}
	if time.Since(macPop.closedAt) < reopenGuard {
		macPop.mu.Unlock()
		return
	}
	macPop.open = true
	macPop.st = tray.PopupState{}
	macPop.anchor = macAnchor{ax, ay, aw, ah, by}
	macPop.lastW, macPop.lastH = 0, 0
	macPop.mu.Unlock()
	if h.Popup != nil {
		h.Popup(true) // the app redraws every second while it is open
	}
	macPop.redraw(true)
}

// revealPopup opens the popup unless it is open (tray only, a second launch):
// at the icon, or at the pointer when a full menu bar hides the icon.
func revealPopup(h Handler) {
	macPop.mu.Lock()
	open := macPop.open
	macPop.mu.Unlock()
	if !open {
		popupTap(h)
	}
}

// closePopup closes the popup (the app quits).
func closePopup() { macPop.close("app quit") }

func (p *macPopup) close(why string) {
	p.mu.Lock()
	if !p.open {
		p.mu.Unlock()
		return
	}
	p.open = false
	p.closedAt = time.Now()
	p.closedBy = why
	p.stopTimersLocked()
	h := p.h
	p.mu.Unlock()
	hidePopupWindow()
	if h.Popup != nil {
		h.Popup(false)
	}
}

func (p *macPopup) stopTimersLocked() {
	if p.copiedT != nil {
		p.copiedT.Stop()
		p.copiedT = nil
	}
	if p.armedT != nil {
		p.armedT.Stop()
		p.armedT = nil
	}
}

func (p *macPopup) arm(copied bool) {
	d := armedFor
	if copied {
		d = copiedFor
	}
	t := time.AfterFunc(d, func() {
		p.mu.Lock()
		var changed bool
		if copied {
			changed = tray.ClearCopied(&p.st)
		} else {
			changed = tray.DisarmQuit(&p.st)
		}
		open := p.open
		p.mu.Unlock()
		if changed && open {
			p.redraw(false)
		}
	})
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.open {
		t.Stop()
		return
	}
	if copied {
		if p.copiedT != nil {
			p.copiedT.Stop()
		}
		p.copiedT = t
	} else {
		if p.armedT != nil {
			p.armedT.Stop()
		}
		p.armedT = t
	}
}

// redraw paints the current rows. place anchors the window on the icon;
// a size change places it again so the longer Quit line fits without a jump
// of the top edge.
func (p *macPopup) redraw(place bool) {
	p.mu.Lock()
	if !p.open {
		p.mu.Unlock()
		return
	}
	lines := tray.PopupView(p.base, p.st)
	wider := tray.PopupView(p.base, tray.PopupState{Copied: true, QuitArmed: true, StopArmed: true})
	anchor := p.anchor
	p.lines = lines
	p.mu.Unlock()

	text, kinds, spans, m, w, h := sheetBox(lines, wider, true)
	p.mu.Lock()
	if !p.open {
		p.mu.Unlock()
		return
	}
	p.m = m
	p.lines = lines
	if w != p.lastW || h != p.lastH {
		place = true
		p.lastW, p.lastH = w, h
	}
	p.mu.Unlock()

	x, y := 0, 0
	if place {
		mon, work, flip := screenAt(anchor.x+anchor.w/2, anchor.y+anchor.h/2)
		xd, yd := tray.PopupPos(tray.FlipDown(anchor.x, anchor.y, anchor.w, anchor.h, flip), mon, work, w, h, 8)
		x, y = tray.FlipUp(xd, yd, h, flip)
	}
	showPopupWindow(text, kinds, spans, m, w, h, x, y, place)
}

func (p *macPopup) do(act tray.Action) {
	p.mu.Lock()
	if !p.open {
		p.mu.Unlock()
		return
	}
	h := p.h
	res := tray.ClickPopup(&p.st, act)
	p.mu.Unlock()
	if res.ArmCopied {
		p.arm(true)
	}
	if res.ArmQuit {
		p.arm(false)
	}
	if res.Close {
		p.close("action: " + res.Run.String())
	}
	if res.Redraw {
		p.redraw(false)
	}
	if res.Run != tray.ActNone && h.Click != nil {
		go h.Click(res.Run)
	}
}

func popupHover(x, y int) int {
	macPop.mu.Lock()
	if !macPop.open {
		macPop.mu.Unlock()
		return 0
	}
	sel := macPop.m.HoverAt(macPop.lines, y)
	changed := tray.HoverPopup(&macPop.st, sel)
	clickable := macPop.m.ClickAt(macPop.lines, x, y) != tray.ActNone
	macPop.mu.Unlock()
	if changed {
		macPop.redraw(false)
	}
	if clickable {
		return 1
	}
	return 0
}

func popupClick(x, y int) {
	macPop.mu.Lock()
	if !macPop.open {
		macPop.mu.Unlock()
		return
	}
	act := macPop.m.ClickAt(macPop.lines, x, y)
	macPop.mu.Unlock()
	macPop.do(act)
}

func popupKey(key int) {
	macPop.mu.Lock()
	if !macPop.open {
		macPop.mu.Unlock()
		return
	}
	sel := macPop.st.Sel
	lines := macPop.lines
	macPop.mu.Unlock()
	switch key {
	case 1:
		macPop.mu.Lock()
		changed := tray.HoverPopup(&macPop.st, tray.PopupNav(lines, sel, -1))
		macPop.mu.Unlock()
		if changed {
			macPop.redraw(false)
		}
	case 2:
		macPop.mu.Lock()
		changed := tray.HoverPopup(&macPop.st, tray.PopupNav(lines, sel, 1))
		macPop.mu.Unlock()
		if changed {
			macPop.redraw(false)
		}
	case 3:
		macPop.do(sel)
	case 4:
		macPop.close("Esc")
	}
}

func popupClose(why string) { macPop.close(why) }

func popupDebug() string {
	macPop.mu.Lock()
	defer macPop.mu.Unlock()
	var b strings.Builder
	if !macPop.open {
		why := macPop.closedBy
		if why == "" {
			why = "not opened yet"
		}
		fmt.Fprintf(&b, "popup window: none (last closed by: %s)\n", why)
		return b.String()
	}
	fmt.Fprintf(&b, "popup window: owner-drawn sheet; anchor %s at %d,%d size %dx%d\n",
		macPop.anchor.by, macPop.anchor.x, macPop.anchor.y, macPop.anchor.w, macPop.anchor.h)
	fmt.Fprintf(&b, "popup state: selected %s, copied %v, quit armed %v\n", macPop.st.Sel, macPop.st.Copied, macPop.st.QuitArmed)
	b.WriteString("popup rows as drawn:\n" + tray.SheetText(macPop.lines, "  "))
	return b.String()
}
