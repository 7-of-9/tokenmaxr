//go:build windows

package ui

// The main window on Windows (window mode, the default): a normal
// top-level window (WS_EX_APPWINDOW, so it has a taskbar button and an
// Alt-Tab entry) titled "tokenmaxr", with the app's icon in its title bar
// and on the taskbar (the status dot, in the icon's colour). Its client
// area is the click popup's sheet: the same rows, drawn by the same
// renderer (sheet_windows.go), and the action rows work as the popup's
// (tray.ClickWindow), except that the window stays open. Its close button
// (and Alt-F4, Esc) minimizes it to the taskbar: collection goes on, and
// the Quit row exits. A second launch or the Start-menu entry restores it
// and brings it to the front (showMainWindow); at login it starts
// minimized, without taking the focus. Like the panel it runs its own
// message loop on a locked OS thread, and the app's redraws are posted.

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
	windowClass   = "d0m1CollectorWindow"
	windowStyle   = wsCaption | wsSysMenu | wsMinimizeBox // WS_OVERLAPPED (0) with no resizing
	windowExStyle = wsExAppWindow

	wmSize           = 0x0005
	wmSetIcon        = 0x0080
	wmShowReq        = 0x8000 + 2 // WM_APP+2: restore and bring to the front
	sizeRestored     = 0
	sizeMinimized    = 1
	iconSmall        = 0
	iconBig          = 1
	swShowNormal     = 1
	swMinimize       = 6
	swShowMinNoActiv = 7
	swRestore        = 9
	swpNoZOrder      = 0x0004
	smCxIcon         = 11
	smCxSmIcon       = 49
	dwmDarkMode      = 20 // DWMWA_USE_IMMERSIVE_DARK_MODE
	dwmBorderColor   = 34 // DWMWA_BORDER_COLOR (Windows 11)
	dwmCaptionColor  = 35 // DWMWA_CAPTION_COLOR
	dwmTextColor     = 36 // DWMWA_TEXT_COLOR
)

// windowPlacement is WINDOWPLACEMENT.
type windowPlacement struct {
	Length, Flags, ShowCmd uint32
	MinPos, MaxPos         point
	Normal                 rect
}

// mainWindow is the one main window. mu guards the fields above the line;
// the rest belong to the window thread.
type mainWindow struct {
	mu        sync.Mutex
	want      bool // the app has a window (window mode, until it quits)
	running   bool // a window thread exists
	hwnd      uintptr
	base      []tray.PanelLine // tray.Window, as the app last drew it
	st        tray.PopupState
	color     tray.Color
	minimized bool // start minimized (a launch at login)
	front     bool // a show request came before the window existed
	visible   bool // on screen and not minimized
	shows     int  // show requests (app.dump)
	h         Handler

	sheet
	drawn    []tray.PanelLine // the rows as last laid out (hit-testing)
	tracking bool
	icons    [2]uintptr // ICON_SMALL, ICON_BIG
	iconKey  string     // the colour and DPI they were made for
}

var (
	mwin          = &mainWindow{}
	windowOnce    sync.Once
	windowErr     error
	windowWndProc = windows.NewCallback(windowProc)
)

// startWindow turns window mode on (systray is ready). The window opens
// with the app's first rows.
func startWindow(h Handler) {
	w := mwin
	w.mu.Lock()
	w.want, w.h, w.minimized = true, h, h.Minimized
	w.mu.Unlock()
}

// setWindowLines is the app's latest rows (renderer.SetWindow): the first
// creates the window, later ones redraw it.
func setWindowLines(lines []tray.PanelLine, h Handler) {
	w := mwin
	w.mu.Lock()
	w.h = h
	changed := !slices.Equal(w.base, lines)
	if changed {
		w.base = slices.Clone(lines)
	}
	start := w.want && !w.running && len(w.base) > 0
	if start {
		w.running = true
	}
	hwnd := w.hwnd
	w.mu.Unlock()
	switch {
	case start:
		go w.loop()
	case changed && hwnd != 0:
		pPostMessageW.Call(hwnd, wmSync, 0, 0)
	}
}

// showMainWindow restores the window and brings it to the front (a second
// launch, the Start-menu entry).
func showMainWindow() {
	w := mwin
	w.mu.Lock()
	hwnd := w.hwnd
	w.shows++
	if hwnd == 0 {
		w.front = true
	}
	w.mu.Unlock()
	if hwnd != 0 {
		pPostMessageW.Call(hwnd, wmShowReq, 0, 0)
	}
}

// setWindowIcon gives the title bar and taskbar button the tray icon's
// colour.
func setWindowIcon(c tray.Color) {
	w := mwin
	w.mu.Lock()
	changed := w.color != c
	w.color = c
	hwnd := w.hwnd
	w.mu.Unlock()
	if changed && hwnd != 0 {
		pPostMessageW.Call(hwnd, wmSync, 0, 0)
	}
}

// closeMainWindow closes the window (the app quits) and waits, briefly,
// until it is gone: a restart's new window never overlaps the old one.
// It is never called on the window's own thread.
func closeMainWindow() {
	w := mwin
	w.mu.Lock()
	w.want = false
	hwnd := w.hwnd
	w.mu.Unlock()
	if hwnd == 0 {
		return
	}
	pPostMessageW.Call(hwnd, wmSync, 0, 0)
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		w.mu.Lock()
		gone := !w.running
		w.mu.Unlock()
		if gone {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// loop is the window thread: create, pump until destroyed, and create it
// again (minimized) should it go while the app still wants it.
func (w *mainWindow) loop() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	created := w.create()
	if created {
		pump()
	}
	w.freeIcons()
	w.free()
	w.mu.Lock()
	w.hwnd = 0
	wasVisible := w.visible
	w.visible = false
	again := w.want && created
	w.running = again
	if again {
		w.minimized = true
	}
	h := w.h
	w.mu.Unlock()
	if wasVisible && h.Shown != nil {
		h.Shown(false)
	}
	if again {
		go w.loop()
	}
}

func (w *mainWindow) create() bool {
	dpiAwareThread()
	windowOnce.Do(func() { windowErr = registerClassStyle(windowClass, windowWndProc, 0) })
	if windowErr != nil {
		return false
	}
	// On the monitor the pointer is on; layout centres it there.
	var pt point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	inst, _, _ := pGetModuleHandleW.Call(0)
	c, _ := windows.UTF16PtrFromString(windowClass)
	t, _ := windows.UTF16PtrFromString(tray.WindowTitle)
	hwnd, _, _ := pCreateWindowExW.Call(windowExStyle, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(t)), windowStyle,
		uintptr(pt.X), uintptr(pt.Y), 1, 1, 0, 0, inst, 0)
	if hwnd == 0 {
		return false
	}
	if pDwmSetWindowAttribute.Find() == nil {
		// A dark title bar in the sheet's colours (each is ignored where
		// Windows does not have it).
		on := int32(1)
		pDwmSetWindowAttribute.Call(hwnd, dwmDarkMode, uintptr(unsafe.Pointer(&on)), 4)
		for _, a := range []struct {
			attr uintptr
			col  uintptr
		}{{dwmCaptionColor, colBg}, {dwmTextColor, colInk}, {dwmBorderColor, colBorder}} {
			v := uint32(a.col)
			pDwmSetWindowAttribute.Call(hwnd, a.attr, uintptr(unsafe.Pointer(&v)), 4)
		}
	}
	w.mu.Lock()
	w.hwnd = hwnd
	minimized := w.minimized && !w.front
	w.minimized, w.front = false, false
	w.mu.Unlock()
	w.noClose, w.tracking, w.iconKey = true, false, ""
	w.updateIcons(hwnd)
	w.layout(hwnd, true)
	if minimized {
		// At login: a taskbar button, and the focus stays where it is.
		pShowWindow.Call(hwnd, swShowMinNoActiv)
	} else {
		pShowWindow.Call(hwnd, swShowNormal)
		pSetForegroundWindow.Call(hwnd)
	}
	w.syncVisible(hwnd)
	// Closed while it was being created: the sync closes it again.
	pPostMessageW.Call(hwnd, wmSync, 0, 0)
	return true
}

func windowProc(hwnd, m, wp, lp uintptr) uintptr {
	w := mwin
	switch m {
	case wmSync:
		w.mu.Lock()
		want := w.want
		w.mu.Unlock()
		if !want {
			pDestroyWindow.Call(hwnd)
			return 0
		}
		w.updateIcons(hwnd)
		w.redraw(hwnd)
		return 0
	case wmShowReq:
		if r, _, _ := pIsIconic.Call(hwnd); r != 0 {
			pShowWindow.Call(hwnd, swRestore)
		} else {
			pShowWindow.Call(hwnd, swShowNormal)
		}
		pSetForegroundWindow.Call(hwnd)
		w.syncVisible(hwnd)
		return 0
	case wmPaint:
		w.paint(hwnd, w.drawn)
		return 0
	case wmEraseBkgnd:
		return 1
	case wmSize:
		w.syncVisible(hwnd)
		return 0
	case wmActivate:
		if wp&0xffff == waInactive {
			w.select_(hwnd, tray.ActNone)
		}
		// DefWindowProc gives the window the keyboard focus (the arrows).
	case wmMouseMove:
		if !w.tracking {
			tme := struct {
				Size, Flags uint32
				Hwnd        uintptr
				Hover       uint32
			}{Flags: tmeLeave, Hwnd: hwnd}
			tme.Size = uint32(unsafe.Sizeof(tme))
			pTrackMouseEvent.Call(uintptr(unsafe.Pointer(&tme)))
			w.tracking = true
		}
		w.select_(hwnd, w.m.HoverAt(w.drawn, int(int16(lp>>16))))
		return 0
	case wmMouseLeave:
		w.tracking = false
		w.select_(hwnd, tray.ActNone)
		return 0
	case wmSetCursor:
		if lp&0xffff == htClient {
			var pt point
			pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
			pScreenToClient.Call(hwnd, uintptr(unsafe.Pointer(&pt)))
			cur := uintptr(idcArrow)
			if w.m.ClickAt(w.drawn, int(pt.X), int(pt.Y)) != tray.ActNone {
				cur = idcHand
			}
			c, _, _ := pLoadCursorW.Call(0, cur)
			pSetCursor.Call(c)
			return 1
		}
	case wmLButtonUp, wmRButtonUp:
		w.do(hwnd, w.m.ClickAt(w.drawn, int(int16(lp)), int(int16(lp>>16))))
		return 0
	case wmKeyDown:
		w.mu.Lock()
		sel := w.st.Sel
		w.mu.Unlock()
		switch wp {
		case vkUp:
			w.select_(hwnd, tray.PopupNav(w.drawn, sel, -1))
		case vkDown, vkTab:
			w.select_(hwnd, tray.PopupNav(w.drawn, sel, 1))
		case vkReturn, vkSpace:
			w.do(hwnd, sel)
		case vkEscape:
			pShowWindow.Call(hwnd, swMinimize)
		}
		return 0
	case wmTimer:
		var changed bool
		pKillTimer.Call(hwnd, wp)
		w.mu.Lock()
		switch wp {
		case timerCopied:
			changed = tray.ClearCopied(&w.st)
		case timerArmed:
			changed = tray.DisarmQuit(&w.st)
		}
		w.mu.Unlock()
		if changed {
			w.redraw(hwnd)
		}
		return 0
	case wmClose:
		// The close button keeps the app running: it minimizes to the
		// taskbar. Quit is the Quit row.
		pShowWindow.Call(hwnd, swMinimize)
		return 0
	case wmDPIChanged:
		// Windows' suggested rectangle at the new DPI; the layout then
		// re-measures the sheet and fits the window to it.
		r := *(*rect)(*(*unsafe.Pointer)(unsafe.Pointer(&lp)))
		pSetWindowPos.Call(hwnd, 0, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), swpNoZOrder|swpNoActivate)
		w.updateIcons(hwnd)
		w.redraw(hwnd)
		return 0
	case wmDestroy:
		for _, t := range []uintptr{timerCopied, timerArmed} {
			pKillTimer.Call(hwnd, t)
		}
		pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, m, wp, lp)
	return r
}

// syncVisible records whether the window is on screen (shown, not
// minimized) and tells the app when that changes.
func (w *mainWindow) syncVisible(hwnd uintptr) {
	vis, _, _ := pIsWindowVisible.Call(hwnd)
	iconic, _, _ := pIsIconic.Call(hwnd)
	v := vis != 0 && iconic == 0
	w.mu.Lock()
	changed := w.visible != v
	w.visible = v
	h := w.h
	w.mu.Unlock()
	if changed && h.Shown != nil {
		go h.Shown(v)
	}
}

// select_ highlights the row with action sel (ActNone: none); leaving the
// armed Quit row disarms it.
func (w *mainWindow) select_(hwnd uintptr, sel tray.Action) {
	w.mu.Lock()
	changed := tray.HoverPopup(&w.st, sel)
	w.mu.Unlock()
	if changed {
		w.redraw(hwnd)
	}
}

// do runs a row's action as the popup does, but the window stays.
func (w *mainWindow) do(hwnd uintptr, act tray.Action) {
	w.mu.Lock()
	h := w.h
	res := tray.ClickWindow(&w.st, act)
	w.mu.Unlock()
	if res.ArmCopied {
		pSetTimer.Call(hwnd, timerCopied, uintptr(copiedFor/time.Millisecond), 0)
	}
	if res.ArmQuit {
		pSetTimer.Call(hwnd, timerArmed, uintptr(armedFor/time.Millisecond), 0)
	}
	if res.Redraw {
		w.redraw(hwnd)
	}
	if res.Run != tray.ActNone && h.Click != nil {
		go h.Click(res.Run)
	}
}

// redraw lays the rows out again and repaints.
func (w *mainWindow) redraw(hwnd uintptr) {
	w.layout(hwnd, false)
	pInvalidateRect.Call(hwnd, 0, 0)
}

// layout lays the rows out for the current state and fits the window to
// the sheet, keeping its top-left corner; place (the first layout) centres
// it on its monitor's work area instead.
func (w *mainWindow) layout(hwnd uintptr, place bool) {
	w.mu.Lock()
	base, st := w.base, w.st
	w.mu.Unlock()
	w.drawn = tray.PopupView(base, st)
	wider := tray.PopupView(base, tray.PopupState{Copied: true, QuitArmed: true})
	if !w.measure(hwnd, w.drawn, wider) && !place {
		return
	}
	ow, oh := w.outerSize()
	if place {
		var wr rect
		pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr)))
		x, y := wr.Left, wr.Top
		if _, wa, ok := monitorAt(wr); ok {
			x = max(wa.Left, wa.Left+(wa.Right-wa.Left-ow)/2)
			y = max(wa.Top, wa.Top+(wa.Bottom-wa.Top-oh)/3)
		}
		pSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), uintptr(ow), uintptr(oh), swpNoZOrder|swpNoActivate)
		return
	}
	if r, _, _ := pIsIconic.Call(hwnd); r != 0 {
		// Minimized: resize the restored rectangle, and stay minimized
		// without taking the focus.
		wp := windowPlacement{}
		wp.Length = uint32(unsafe.Sizeof(wp))
		if r, _, _ := pGetWindowPlacement.Call(hwnd, uintptr(unsafe.Pointer(&wp))); r != 0 {
			wp.Normal.Right, wp.Normal.Bottom = wp.Normal.Left+ow, wp.Normal.Top+oh
			wp.Flags, wp.ShowCmd = 0, swShowMinNoActiv
			pSetWindowPlacement.Call(hwnd, uintptr(unsafe.Pointer(&wp)))
		}
		return
	}
	pSetWindowPos.Call(hwnd, 0, 0, 0, uintptr(ow), uintptr(oh), swpNoMove|swpNoZOrder|swpNoActivate)
}

// outerSize is the window's size for the sheet as its client area, at the
// window's DPI.
func (w *mainWindow) outerSize() (int32, int32) {
	r := rect{0, 0, w.width, w.height}
	if pAdjustWindowRectExForDpi.Find() == nil {
		pAdjustWindowRectExForDpi.Call(uintptr(unsafe.Pointer(&r)), windowStyle, 0, windowExStyle, uintptr(w.dpi))
	} else {
		pAdjustWindowRectEx.Call(uintptr(unsafe.Pointer(&r)), windowStyle, 0, windowExStyle)
	}
	return r.Right - r.Left, r.Bottom - r.Top
}

// updateIcons sets the title-bar and taskbar icons: the status dot in the
// tray icon's colour, sized for the window's DPI.
func (w *mainWindow) updateIcons(hwnd uintptr) {
	w.mu.Lock()
	c := w.color
	w.mu.Unlock()
	dpi := windowDPI(hwnd)
	key := fmt.Sprintf("%s@%d", c, dpi)
	if key == w.iconKey {
		return
	}
	var made [2]uintptr
	for i, size := range []int{systemMetric(smCxSmIcon, dpi, 16), systemMetric(smCxIcon, dpi, 32)} {
		b := tray.IconDIB(c, size)
		made[i], _, _ = pCreateIconFromResourceEx.Call(uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), 1, 0x00030000,
			uintptr(size), uintptr(size), 0)
	}
	if made[0] == 0 || made[1] == 0 {
		for _, h := range made {
			if h != 0 {
				pDestroyIcon.Call(h)
			}
		}
		return
	}
	pSendMessageW.Call(hwnd, wmSetIcon, iconSmall, made[0])
	pSendMessageW.Call(hwnd, wmSetIcon, iconBig, made[1])
	w.freeIcons()
	w.icons, w.iconKey = made, key
}

func (w *mainWindow) freeIcons() {
	for i, h := range w.icons {
		if h != 0 {
			pDestroyIcon.Call(h)
		}
		w.icons[i] = 0
	}
	w.iconKey = ""
}

// systemMetric is GetSystemMetricsForDpi(idx, dpi), or def at 96 DPI scaled.
func systemMetric(idx int, dpi uint32, def int) int {
	if pGetSystemMetricsForDpi.Find() == nil {
		if v, _, _ := pGetSystemMetricsForDpi.Call(uintptr(idx), uintptr(dpi)); v != 0 {
			return int(v)
		}
	}
	return def * int(dpi) / 96
}

// windowDebug describes the main window and its rows for app.dump.
func windowDebug() string {
	w := mwin
	w.mu.Lock()
	hwnd, base, st, visible, want, shows := w.hwnd, w.base, w.st, w.visible, w.want, w.shows
	w.mu.Unlock()
	var b strings.Builder
	if hwnd == 0 {
		fmt.Fprintf(&b, "main window: not open (wanted %v; it opens with the first rows)\n", want)
		return b.String()
	}
	var wr rect
	pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr)))
	vis, _, _ := pIsWindowVisible.Call(hwnd)
	iconic, _, _ := pIsIconic.Call(hwnd)
	fg, _, _ := pGetForegroundWindow.Call()
	title := make([]uint16, 128)
	n, _, _ := pGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&title[0])), uintptr(len(title)))
	fmt.Fprintf(&b, "main window: %q (class %s); %s; visible %v, minimized %v, foreground %v; at %d,%d size %dx%d; dpi %d\n",
		windows.UTF16ToString(title[:n]), windowClass, windowFlags(hwnd), vis != 0, iconic != 0, fg == hwnd,
		wr.Left, wr.Top, wr.Right-wr.Left, wr.Bottom-wr.Top, windowDPI(hwnd))
	fmt.Fprintf(&b, "main window state: on screen %v, show requests %d, selected %s, quit armed %v\n", visible, shows, st.Sel, st.QuitArmed)
	b.WriteString("main window rows as drawn:\n" + tray.SheetText(tray.PopupView(base, st), "  "))
	return b.String()
}
