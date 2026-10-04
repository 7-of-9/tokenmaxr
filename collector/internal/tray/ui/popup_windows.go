//go:build windows

package ui

// The click popup on Windows: clicking the tray icon (left or right)
// opens this instead of the native menu (menu_windows.go asks systray for
// the clicks). It is the pinned panel's sheet with the actions below,
// drawn by the same renderer (sheet_windows.go): a topmost tool window
// (off the taskbar and Alt-Tab) that takes the focus while open, anchored
// to the icon and clamped to the work area (tray.PopupPos). It closes on
// Esc, on a click outside or any other focus loss, on the icon again, and
// after an action. Like the panel it runs its own message loop on a locked
// OS thread; the app's redraws (every second while open) are posted to it.

import (
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/7-of-9/tokenmaxr/collector/internal/tray"
)

const (
	popupClass = "d0m1CollectorPopup"
	// The systray window's class and the icon's id in fyne.io/systray
	// (v1.12), to ask the shell where the icon is. If they ever change,
	// the popup opens at the cursor instead.
	systrayClass  = "SystrayClass"
	systrayIconID = 100

	timerOutside = 1 // a click outside when the popup never got the focus
	timerCopied  = 2 // the "copied" feedback ends
	timerArmed   = 3 // "Click again to quit" gives up

	outsideEvery = 100 * time.Millisecond
	copiedFor    = 1500 * time.Millisecond
	armedFor     = 5 * time.Second
	// reopenGuard: a click on the icon while the popup is open first takes
	// the focus from it (closing it), then arrives as a tap; that tap must
	// not open it again.
	reopenGuard = 300 * time.Millisecond
)

// popup is the one click popup. mu guards the fields above the line; the
// rest belong to the window thread.
type popup struct {
	mu       sync.Mutex
	want     bool // open, as last asked
	running  bool // a window thread exists
	hwnd     uintptr
	base     []tray.PanelLine // tray.Popup, as the app last drew it
	st       tray.PopupState
	anchor   tray.Rect
	anchorBy string // "icon" or "cursor"
	closedAt time.Time
	closedBy string // why it last closed (app.dump)
	active   bool   // it has been the foreground window since it opened
	h        Handler

	sheet
	drawn    []tray.PanelLine // the rows as last laid out (hit-testing)
	tracking bool             // WM_MOUSELEAVE requested
	closing  bool
}

var (
	pop          = &popup{}
	popupOnce    sync.Once
	popupErr     error
	popupWndProc = windows.NewCallback(popupProc)
)

// setPopup is the app's latest rows (renderer.SetPopup): an open popup
// redraws with them.
func setPopup(lines []tray.PanelLine, h Handler) {
	p := pop
	p.mu.Lock()
	p.h = h
	changed := !slices.Equal(p.base, lines)
	if changed {
		p.base = slices.Clone(lines)
	}
	hwnd := p.hwnd
	p.mu.Unlock()
	if changed && hwnd != 0 {
		pPostMessageW.Call(hwnd, wmSync, 0, 0)
	}
}

// popupTap is a click on the tray icon (on systray's thread): it opens the
// popup, or closes it if it is open.
func popupTap(h Handler) {
	anchor, by := trayAnchor()
	p := pop
	p.mu.Lock()
	p.h = h
	switch {
	case p.running && p.want:
		p.want = false
		hwnd := p.hwnd
		p.mu.Unlock()
		if hwnd != 0 {
			pPostMessageW.Call(hwnd, wmSync, 0, 0)
		}
		return
	case time.Since(p.closedAt) < reopenGuard:
		p.mu.Unlock()
		return
	}
	p.want, p.anchor, p.anchorBy, p.st = true, anchor, by, tray.PopupState{}
	start := !p.running
	p.running = true
	p.mu.Unlock()
	if start {
		go p.loop()
	}
}

// closePopup closes the popup (the app quits).
func closePopup() {
	p := pop
	p.mu.Lock()
	p.want = false
	hwnd := p.hwnd
	p.mu.Unlock()
	if hwnd != 0 {
		pPostMessageW.Call(hwnd, wmSync, 0, 0)
	}
}

// loop is the window thread: create, pump until destroyed, and open again
// if the icon was clicked while it was closing.
func (p *popup) loop() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	created := p.create()
	if created {
		pump()
	}
	p.free()
	p.mu.Lock()
	p.hwnd, p.active = 0, false
	again := p.want && created
	p.running = again
	h := p.h
	p.mu.Unlock()
	if created && h.Popup != nil {
		h.Popup(false)
	}
	if again {
		go p.loop()
	}
}

func (p *popup) create() bool {
	dpiAwareThread()
	popupOnce.Do(func() { popupErr = registerClass(popupClass, popupWndProc) })
	if popupErr != nil {
		return false
	}
	p.mu.Lock()
	a := p.anchor
	h := p.h
	p.mu.Unlock()
	// Created at the icon, so it measures itself at that monitor's DPI.
	hwnd := newSheetWindow(popupClass, panelTitle, wsExTopmost|wsExToolWindow|wsExLayered,
		int32((a.Left+a.Right)/2), int32((a.Top+a.Bottom)/2))
	if hwnd == 0 {
		return false
	}
	p.mu.Lock()
	p.hwnd = hwnd
	p.mu.Unlock()
	p.closing, p.tracking = false, false
	p.layout(hwnd)
	pShowWindow.Call(hwnd, swShow)
	pSetForegroundWindow.Call(hwnd)
	pSetFocus.Call(hwnd)
	pSetTimer.Call(hwnd, timerOutside, uintptr(outsideEvery/time.Millisecond), 0)
	if h.Popup != nil {
		h.Popup(true) // the app redraws every second while it is open
	}
	// Closed while it was being created: the sync closes it again.
	pPostMessageW.Call(hwnd, wmSync, 0, 0)
	return true
}

func popupProc(hwnd, m, wp, lp uintptr) uintptr {
	p := pop
	switch m {
	case wmSync:
		p.mu.Lock()
		want := p.want
		p.mu.Unlock()
		if !want {
			p.close(hwnd, "asked (icon clicked again, or the app quit)")
			return 0
		}
		p.redraw(hwnd)
		return 0
	case wmPaint:
		p.paint(hwnd, p.drawn)
		return 0
	case wmEraseBkgnd:
		return 1
	case wmActivate:
		if wp&0xffff == waInactive {
			p.close(hwnd, "focus lost") // a click outside, Alt-Tab, another window
		}
		return 0
	case wmMouseMove:
		if !p.tracking {
			tme := struct {
				Size, Flags uint32
				Hwnd        uintptr
				Hover       uint32
			}{Flags: tmeLeave, Hwnd: hwnd}
			tme.Size = uint32(unsafe.Sizeof(tme))
			pTrackMouseEvent.Call(uintptr(unsafe.Pointer(&tme)))
			p.tracking = true
		}
		sel := p.m.HoverAt(p.drawn, int(int16(lp>>16)))
		if p.inClose(int32(int16(lp)), int32(int16(lp>>16))) {
			sel = tray.ActNone
		}
		p.select_(hwnd, sel)
		return 0
	case wmMouseLeave:
		p.tracking = false
		p.select_(hwnd, tray.ActNone)
		return 0
	case wmSetCursor:
		if lp&0xffff == htClient {
			var pt point
			pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
			pScreenToClient.Call(hwnd, uintptr(unsafe.Pointer(&pt)))
			cur := uintptr(idcArrow)
			if p.inClose(pt.X, pt.Y) || p.m.ActionAt(p.drawn, int(pt.Y)) != tray.ActNone {
				cur = idcHand
			}
			c, _, _ := pLoadCursorW.Call(0, cur)
			pSetCursor.Call(c)
			return 1
		}
	case wmLButtonUp, wmRButtonUp:
		x, y := int32(int16(lp)), int32(int16(lp>>16))
		if p.inClose(x, y) {
			p.close(hwnd, "the ×")
			return 0
		}
		p.do(hwnd, p.m.ActionAt(p.drawn, int(y)))
		return 0
	case wmKeyDown:
		p.mu.Lock()
		sel := p.st.Sel
		p.mu.Unlock()
		switch wp {
		case vkUp:
			p.select_(hwnd, tray.PopupNav(p.drawn, sel, -1))
		case vkDown, vkTab:
			p.select_(hwnd, tray.PopupNav(p.drawn, sel, 1))
		case vkReturn, vkSpace:
			p.do(hwnd, sel)
		case vkEscape:
			p.close(hwnd, "Esc")
		}
		return 0
	case wmTimer:
		switch wp {
		case timerOutside:
			p.outside(hwnd)
		case timerCopied:
			pKillTimer.Call(hwnd, timerCopied)
			p.mu.Lock()
			changed := tray.ClearCopied(&p.st)
			p.mu.Unlock()
			if changed {
				p.redraw(hwnd)
			}
		case timerArmed:
			pKillTimer.Call(hwnd, timerArmed)
			p.mu.Lock()
			changed := tray.DisarmQuit(&p.st)
			p.mu.Unlock()
			if changed {
				p.redraw(hwnd)
			}
		}
		return 0
	case wmDPIChanged:
		p.redraw(hwnd) // re-measured at the new DPI and anchored again
		return 0
	case wmDisplayChange, wmClose:
		p.close(hwnd, "display change or WM_CLOSE")
		return 0
	case wmDestroy:
		for _, t := range []uintptr{timerOutside, timerCopied, timerArmed} {
			pKillTimer.Call(hwnd, t)
		}
		pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, m, wp, lp)
	return r
}

// close destroys the popup (once: destroying an active window deactivates
// it, which asks again).
func (p *popup) close(hwnd uintptr, why string) {
	if p.closing {
		return
	}
	p.closing = true
	p.mu.Lock()
	p.want = false
	p.closedAt, p.closedBy = time.Now(), why
	p.mu.Unlock()
	pDestroyWindow.Call(hwnd)
}

// outside closes a popup that lost the foreground without a WM_ACTIVATE,
// and one that never got it (the shell did not let it take the
// foreground; ShowWindow still activates it within its own thread) on a
// click anywhere else.
func (p *popup) outside(hwnd uintptr) {
	if fg, _, _ := pGetForegroundWindow.Call(); fg == hwnd {
		p.mu.Lock()
		p.active = true
		p.mu.Unlock()
		return
	}
	p.mu.Lock()
	active := p.active
	p.mu.Unlock()
	if active {
		p.close(hwnd, "focus lost")
		return
	}
	down := false
	for _, vk := range []uintptr{vkLButton, vkRButton, vkMButton} {
		if s, _, _ := pGetAsyncKeyState.Call(vk); s&0x8000 != 0 {
			down = true
		}
	}
	if !down {
		return
	}
	var pt point
	var wr rect
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr)))
	if pt.X < wr.Left || pt.X >= wr.Right || pt.Y < wr.Top || pt.Y >= wr.Bottom {
		p.close(hwnd, "click outside")
	}
}

// select_ highlights the row with action sel (ActNone: none); leaving the
// armed Quit row disarms it.
func (p *popup) select_(hwnd uintptr, sel tray.Action) {
	p.mu.Lock()
	changed := tray.HoverPopup(&p.st, sel)
	p.mu.Unlock()
	if changed {
		p.redraw(hwnd)
	}
}

// do runs a row's action: the identity row copies and says so, Quit asks
// for a second click, anything else closes the popup and runs.
func (p *popup) do(hwnd uintptr, act tray.Action) {
	p.mu.Lock()
	h := p.h
	res := tray.ClickPopup(&p.st, act)
	p.mu.Unlock()
	if res.ArmCopied {
		pSetTimer.Call(hwnd, timerCopied, uintptr(copiedFor/time.Millisecond), 0)
	}
	if res.ArmQuit {
		pSetTimer.Call(hwnd, timerArmed, uintptr(armedFor/time.Millisecond), 0)
	}
	if res.Close {
		p.close(hwnd, "action: "+res.Run.String())
	}
	if res.Redraw {
		p.redraw(hwnd)
	}
	if res.Run != tray.ActNone && h.Click != nil {
		go h.Click(res.Run)
	}
}

// redraw lays the rows out again and repaints.
func (p *popup) redraw(hwnd uintptr) {
	p.layout(hwnd)
	pInvalidateRect.Call(hwnd, 0, 0)
}

// layout lays the rows out for the current state and, when the size
// changed, places the popup at the icon again.
func (p *popup) layout(hwnd uintptr) {
	p.mu.Lock()
	base, st, a := p.base, p.st, p.anchor
	p.mu.Unlock()
	p.drawn = tray.PopupView(base, st)
	wider := tray.PopupView(base, tray.PopupState{Copied: true, QuitArmed: true})
	if !p.measure(hwnd, p.drawn, wider) {
		return
	}
	ar := rect{int32(a.Left), int32(a.Top), int32(a.Right) + 1, int32(a.Bottom) + 1}
	x, y := int(ar.Left), int(ar.Top)-int(p.height)
	if mon, work, ok := monitorAt(ar); ok {
		x, y = tray.PopupPos(a, mon.model(), work.model(), int(p.width), int(p.height), int(p.scale(8)))
	}
	pSetWindowPos.Call(hwnd, hwndTopmost, uintptr(x), uintptr(y), uintptr(p.width), uintptr(p.height), swpNoActivate)
}

// trayAnchor is where the icon is, in physical pixels: its rectangle from
// the shell, or else the cursor (the click point).
func trayAnchor() (tray.Rect, string) {
	if old := dpiAwareThread(); old != 0 {
		defer pSetThreadDpiAwarenessContext.Call(old)
	}
	if r, ok := iconRect(); ok {
		return r.model(), "icon"
	}
	var pt point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	return tray.Rect{Left: int(pt.X), Top: int(pt.Y), Right: int(pt.X), Bottom: int(pt.Y)}, "cursor"
}

// iconRect asks the shell for the icon's rectangle (Shell_NotifyIconGetRect)
// by systray's window in this process and the icon's id.
func iconRect() (rect, bool) {
	if pShellNotifyIconGetRect.Find() != nil {
		return rect{}, false
	}
	hwnd := systrayWindow()
	if hwnd == 0 {
		return rect{}, false
	}
	id := struct {
		Size uint32
		Hwnd uintptr
		ID   uint32
		GUID windows.GUID
	}{Hwnd: hwnd, ID: systrayIconID}
	id.Size = uint32(unsafe.Sizeof(id))
	var r rect
	if hr, _, _ := pShellNotifyIconGetRect.Call(uintptr(unsafe.Pointer(&id)), uintptr(unsafe.Pointer(&r))); hr != 0 {
		return rect{}, false
	}
	return r, r.Right > r.Left && r.Bottom > r.Top
}

// systrayWindow is systray's hidden window in this process, or 0.
func systrayWindow() uintptr {
	class, _ := windows.UTF16PtrFromString(systrayClass)
	self := windows.GetCurrentProcessId()
	var after uintptr
	for range 64 {
		hwnd, _, _ := pFindWindowExW.Call(0, after, uintptr(unsafe.Pointer(class)), 0)
		if hwnd == 0 {
			return 0
		}
		var pid uint32
		pGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if pid == self {
			return hwnd
		}
		after = hwnd
	}
	return 0
}

// popupDebug describes the popup window and its rows for app.dump.
func popupDebug() string {
	p := pop
	p.mu.Lock()
	hwnd, base, st, a, by, active, closedBy := p.hwnd, p.base, p.st, p.anchor, p.anchorBy, p.active, p.closedBy
	p.mu.Unlock()
	var b strings.Builder
	if hwnd == 0 {
		if closedBy == "" {
			closedBy = "not opened yet"
		}
		b.WriteString("popup window: none (last closed by: " + closedBy + ")\n")
		return b.String()
	}
	var wr rect
	pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr)))
	fg, _, _ := pGetForegroundWindow.Call()
	fmt.Fprintf(&b, "popup window: %s; at %d,%d size %dx%d; dpi %d\n", windowFlags(hwnd),
		wr.Left, wr.Top, wr.Right-wr.Left, wr.Bottom-wr.Top, windowDPI(hwnd))
	fmt.Fprintf(&b, "popup focus: foreground now %v, was foreground %v\n", fg == hwnd, active)
	fmt.Fprintf(&b, "popup anchor (%s): %d,%d-%d,%d\n", by, a.Left, a.Top, a.Right, a.Bottom)
	fmt.Fprintf(&b, "popup state: selected %s, copied %v, quit armed %v\n", st.Sel, st.Copied, st.QuitArmed)
	b.WriteString("popup rows as drawn:\n" + tray.SheetText(tray.PopupView(base, st), "  "))
	return b.String()
}
