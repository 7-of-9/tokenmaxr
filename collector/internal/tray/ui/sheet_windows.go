//go:build windows

package ui

// The sheet: the dark pane every Windows window draws, the pinned panel,
// the click popup and the main window, in pure Go (no cgo) with double-buffered GDI in Consolas
// so the model's columns line up. One renderer, so the two look the same:
// the grey-and-green double border, the status line in green or red, the
// muted lines, the provider rows with their +value in the accent green,
// the rules, the ×, and (popup only) the action rows with their hover
// highlight. Row geometry is tray.Metrics, so hit-testing is the model's.

import (
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/7-of-9/tokenmaxr/collector/internal/tray"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	dwmapi   = windows.NewLazySystemDLL("dwmapi.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")

	pRegisterClassExW             = user32.NewProc("RegisterClassExW")
	pCreateWindowExW              = user32.NewProc("CreateWindowExW")
	pDefWindowProcW               = user32.NewProc("DefWindowProcW")
	pDestroyWindow                = user32.NewProc("DestroyWindow")
	pShowWindow                   = user32.NewProc("ShowWindow")
	pSetWindowPos                 = user32.NewProc("SetWindowPos")
	pGetWindowRect                = user32.NewProc("GetWindowRect")
	pGetClientRect                = user32.NewProc("GetClientRect")
	pGetMessageW                  = user32.NewProc("GetMessageW")
	pTranslateMessage             = user32.NewProc("TranslateMessage")
	pDispatchMessageW             = user32.NewProc("DispatchMessageW")
	pPostMessageW                 = user32.NewProc("PostMessageW")
	pPostQuitMessage              = user32.NewProc("PostQuitMessage")
	pInvalidateRect               = user32.NewProc("InvalidateRect")
	pBeginPaint                   = user32.NewProc("BeginPaint")
	pEndPaint                     = user32.NewProc("EndPaint")
	pFillRect                     = user32.NewProc("FillRect")
	pGetDC                        = user32.NewProc("GetDC")
	pReleaseDC                    = user32.NewProc("ReleaseDC")
	pLoadCursorW                  = user32.NewProc("LoadCursorW")
	pSetCursor                    = user32.NewProc("SetCursor")
	pMonitorFromRect              = user32.NewProc("MonitorFromRect")
	pMonitorFromPoint             = user32.NewProc("MonitorFromPoint")
	pGetMonitorInfoW              = user32.NewProc("GetMonitorInfoW")
	pSetLayeredWindowAttributes   = user32.NewProc("SetLayeredWindowAttributes")
	pGetDpiForWindow              = user32.NewProc("GetDpiForWindow")
	pSetThreadDpiAwarenessContext = user32.NewProc("SetThreadDpiAwarenessContext")
	pGetWindowLongPtrW            = user32.NewProc("GetWindowLongPtrW")
	pGetForegroundWindow          = user32.NewProc("GetForegroundWindow")
	pSetForegroundWindow          = user32.NewProc("SetForegroundWindow")
	pSetFocus                     = user32.NewProc("SetFocus")
	pGetCursorPos                 = user32.NewProc("GetCursorPos")
	pScreenToClient               = user32.NewProc("ScreenToClient")
	pSetTimer                     = user32.NewProc("SetTimer")
	pKillTimer                    = user32.NewProc("KillTimer")
	pTrackMouseEvent              = user32.NewProc("TrackMouseEvent")
	pGetAsyncKeyState             = user32.NewProc("GetAsyncKeyState")
	pFindWindowExW                = user32.NewProc("FindWindowExW")
	pGetWindowThreadProcessId     = user32.NewProc("GetWindowThreadProcessId")
	pIsIconic                     = user32.NewProc("IsIconic")
	pIsWindowVisible              = user32.NewProc("IsWindowVisible")
	pGetWindowPlacement           = user32.NewProc("GetWindowPlacement")
	pSetWindowPlacement           = user32.NewProc("SetWindowPlacement")
	pAdjustWindowRectEx           = user32.NewProc("AdjustWindowRectEx")
	pAdjustWindowRectExForDpi     = user32.NewProc("AdjustWindowRectExForDpi")
	pCreateIconFromResourceEx     = user32.NewProc("CreateIconFromResourceEx")
	pDestroyIcon                  = user32.NewProc("DestroyIcon")
	pSendMessageW                 = user32.NewProc("SendMessageW")
	pGetSystemMetrics             = user32.NewProc("GetSystemMetrics")
	pGetSystemMetricsForDpi       = user32.NewProc("GetSystemMetricsForDpi")
	pGetWindowTextW               = user32.NewProc("GetWindowTextW")
	pCreateFontW                  = gdi32.NewProc("CreateFontW")
	pSelectObject                 = gdi32.NewProc("SelectObject")
	pDeleteObject                 = gdi32.NewProc("DeleteObject")
	pCreateSolidBrush             = gdi32.NewProc("CreateSolidBrush")
	pSetTextColor                 = gdi32.NewProc("SetTextColor")
	pSetBkMode                    = gdi32.NewProc("SetBkMode")
	pTextOutW                     = gdi32.NewProc("TextOutW")
	pGetTextExtentPoint32W        = gdi32.NewProc("GetTextExtentPoint32W")
	pGetDeviceCaps                = gdi32.NewProc("GetDeviceCaps")
	pCreateCompatibleDC           = gdi32.NewProc("CreateCompatibleDC")
	pCreateCompatibleBitmap       = gdi32.NewProc("CreateCompatibleBitmap")
	pDeleteDC                     = gdi32.NewProc("DeleteDC")
	pBitBlt                       = gdi32.NewProc("BitBlt")
	pGetModuleHandleW             = kernel32.NewProc("GetModuleHandleW")
	pDwmSetWindowAttribute        = dwmapi.NewProc("DwmSetWindowAttribute")
	pShellNotifyIconGetRect       = shell32.NewProc("Shell_NotifyIconGetRect")
)

const (
	wsPopup         = 0x80000000
	wsCaption       = 0x00C00000
	wsSysMenu       = 0x00080000
	wsMinimizeBox   = 0x00020000
	wsExTopmost     = 0x00000008
	wsExToolWindow  = 0x00000080
	wsExAppWindow   = 0x00040000
	wsExLayered     = 0x00080000
	wsExNoActivate  = 0x08000000
	csDropShadow    = 0x00020000
	wmDestroy       = 0x0002
	wmActivate      = 0x0006
	wmPaint         = 0x000F
	wmClose         = 0x0010
	wmEraseBkgnd    = 0x0014
	wmSetCursor     = 0x0020
	wmMouseActivate = 0x0021
	wmDisplayChange = 0x007E
	wmNCHitTest     = 0x0084
	wmKeyDown       = 0x0100
	wmTimer         = 0x0113
	wmMouseMove     = 0x0200
	wmLButtonUp     = 0x0202
	wmRButtonUp     = 0x0205
	wmExitSizeMove  = 0x0232
	wmMouseLeave    = 0x02A3
	wmDPIChanged    = 0x02E0
	wmSync          = 0x8000 + 1 // WM_APP+1: lines changed or close wanted
	waInactive      = 0
	htClient        = 1
	htCaption       = 2
	maNoActivate    = 3
	swShow          = 5
	swShowNoActive  = 4
	swpNoSize       = 0x0001
	swpNoMove       = 0x0002
	swpNoActivate   = 0x0010
	lwaAlpha        = 0x2
	idcArrow        = 32512
	idcHand         = 32649
	monitorNearest  = 2
	bkTransparent   = 1
	srcCopy         = 0x00CC0020
	logPixelsY      = 90
	errClassExists  = 1410
	dwmCornerPref   = 33 // DWMWA_WINDOW_CORNER_PREFERENCE (Windows 11)
	dwmCornerRound  = 2
	sheetAlpha      = 245
	gwlStyle        = ^uintptr(15) // GWL_STYLE, -16
	gwlExStyle      = ^uintptr(19) // GWL_EXSTYLE, -20
	hwndTopmost     = ^uintptr(0)  // (HWND)-1
	dpiPerMonitorV2 = ^uintptr(3)  // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2, (HANDLE)-4
	vkLButton       = 0x01
	vkRButton       = 0x02
	vkMButton       = 0x04
	vkReturn        = 0x0D
	vkEscape        = 0x1B
	vkSpace         = 0x20
	vkUp            = 0x26
	vkDown          = 0x28
	vkTab           = 0x09
	tmeLeave        = 0x00000002
)

// Colours (COLORREF 0x00BBGGRR): the site's GitHub-dark sheet on a
// near-black green pane, with its green accent. The action rows are a
// softer ink that brightens to white on a subtle green under the mouse.
func rgb(r, g, b uint8) uintptr { return uintptr(r) | uintptr(g)<<8 | uintptr(b)<<16 }

var (
	colBg       = rgb(0x0d, 0x14, 0x10)
	colBorder   = rgb(0x3d, 0x44, 0x4d)
	colInner    = rgb(0x19, 0x6c, 0x2e)
	colInk      = rgb(0xf0, 0xf6, 0xfc)
	colMuted    = rgb(0x91, 0x98, 0xa1)
	colAccent   = rgb(0x56, 0xd3, 0x64)
	colError    = rgb(0xf8, 0x51, 0x49)
	colAction   = rgb(0xc9, 0xd1, 0xd9)
	colOff      = rgb(0x6e, 0x76, 0x81)
	colHover    = rgb(0x17, 0x33, 0x1f)
	colHot      = rgb(0xff, 0xff, 0xff)
	colArmedHot = rgb(0xff, 0x7b, 0x72)
	colLink     = rgb(0x58, 0xa6, 0xff)
)

type point struct{ X, Y int32 }
type rect struct{ Left, Top, Right, Bottom int32 }
type size struct{ CX, CY int32 }

func (r rect) model() tray.Rect {
	return tray.Rect{Left: int(r.Left), Top: int(r.Top), Right: int(r.Right), Bottom: int(r.Bottom)}
}

type wndClassEx struct {
	Size, Style        uint32
	WndProc            uintptr
	ClsExtra, WndExtra int32
	Instance, Icon     uintptr
	Cursor, Background uintptr
	MenuName           *uint16
	ClassName          *uint16
	IconSm             uintptr
}

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	Private uint32
}

type paintStruct struct {
	Hdc       uintptr
	Erase     int32
	Paint     rect
	Restore   int32
	IncUpdate int32
	Reserved  [32]byte
}

type monitorInfo struct {
	Size    uint32
	Monitor rect
	Work    rect
	Flags   uint32
}

// registerClass registers a window class once (a second call, or a class
// left by an earlier window thread, is fine).
func registerClass(name string, proc uintptr) error {
	return registerClassStyle(name, proc, csDropShadow)
}

// registerClassStyle registers a window class with its class style (the
// main window has none: the drop shadow is for the borderless sheets).
func registerClassStyle(name string, proc uintptr, style uint32) error {
	inst, _, _ := pGetModuleHandleW.Call(0)
	class, _ := windows.UTF16PtrFromString(name)
	arrow, _, _ := pLoadCursorW.Call(0, idcArrow)
	wc := wndClassEx{Style: style, WndProc: proc, Instance: inst, Cursor: arrow, ClassName: class}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if r, _, err := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 && err != windows.Errno(errClassExists) {
		return err
	}
	return nil
}

// newSheetWindow creates a hidden sheet window of class at x, y (its DPI
// is that monitor's): translucent, with Windows 11's rounded corners.
func newSheetWindow(class, title string, exStyle uintptr, x, y int32) uintptr {
	inst, _, _ := pGetModuleHandleW.Call(0)
	c, _ := windows.UTF16PtrFromString(class)
	t, _ := windows.UTF16PtrFromString(title)
	hwnd, _, _ := pCreateWindowExW.Call(exStyle, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(t)), wsPopup,
		uintptr(x), uintptr(y), 1, 1, 0, 0, inst, 0)
	if hwnd == 0 {
		return 0
	}
	pSetLayeredWindowAttributes.Call(hwnd, 0, sheetAlpha, lwaAlpha)
	if pDwmSetWindowAttribute.Find() == nil {
		pref := int32(dwmCornerRound)
		pDwmSetWindowAttribute.Call(hwnd, dwmCornerPref, uintptr(unsafe.Pointer(&pref)), 4)
	}
	return hwnd
}

// dpiAwareThread makes the calling thread per-monitor DPI aware (v2): its
// windows get physical pixels and WM_DPICHANGED. It returns the previous
// context to restore, or 0.
func dpiAwareThread() uintptr {
	if pSetThreadDpiAwarenessContext.Find() != nil {
		return 0
	}
	old, _, _ := pSetThreadDpiAwarenessContext.Call(dpiPerMonitorV2)
	return old
}

// sheet is one window's drawing state. It belongs to the window's thread.
// noClose leaves out the × (the main window closes from its title bar).
type sheet struct {
	noClose   bool
	dpi       uint32
	font      uintptr
	fontLight uintptr // quieter weight for an event older than an hour
	m         tray.Metrics
	closeSize int32
	width     int32
	height    int32
}

// scale is v at 96 DPI in the sheet's DPI.
func (s *sheet) scale(v int32) int32 { return int32(int64(v) * int64(s.dpi) / 96) }

func (s *sheet) closeRect() rect {
	m := s.scale(6)
	return rect{s.width - m - s.closeSize, m, s.width - m, m + s.closeSize}
}

func (s *sheet) inClose(x, y int32) bool {
	c := s.closeRect()
	return x >= c.Left && x < c.Right && y >= c.Top && y < c.Bottom
}

// free releases the font (the window is gone).
func (s *sheet) free() {
	if s.font != 0 {
		pDeleteObject.Call(s.font)
	}
	s.font, s.dpi, s.width, s.height = 0, 0, 0, 0
}

func windowDPI(hwnd uintptr) uint32 {
	if pGetDpiForWindow.Find() == nil {
		if d, _, _ := pGetDpiForWindow.Call(hwnd); d != 0 {
			return uint32(d)
		}
	}
	hdc, _, _ := pGetDC.Call(0)
	d, _, _ := pGetDeviceCaps.Call(hdc, logPixelsY)
	pReleaseDC.Call(0, hdc)
	if d == 0 {
		return 96
	}
	return uint32(d)
}

func textExtent(hdc uintptr, s string) size {
	var sz size
	u, err := windows.UTF16FromString(s)
	if err != nil || len(u) <= 1 {
		return sz
	}
	pGetTextExtentPoint32W.Call(hdc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&sz)))
	return sz
}

func textOut(hdc uintptr, x, y int32, s string, col uintptr) int32 {
	u, err := windows.UTF16FromString(s)
	if err != nil || len(u) <= 1 {
		return 0
	}
	pSetTextColor.Call(hdc, col)
	pTextOutW.Call(hdc, uintptr(x), uintptr(y), uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1))
	return textExtent(hdc, s).CX
}

func fill(hdc uintptr, r rect, col uintptr) {
	br, _, _ := pCreateSolidBrush.Call(col)
	pFillRect.Call(hdc, uintptr(unsafe.Pointer(&r)), br)
	pDeleteObject.Call(br)
}

// measure sets the font and metrics for the window's DPI and the sheet's
// size for lines (the × beside the first); wider are more lines that only
// count for the width (a popup row's longer states, so it never resizes
// while open). It reports whether the size changed.
func (s *sheet) measure(hwnd uintptr, lines, wider []tray.PanelLine) bool {
	if dpi := windowDPI(hwnd); dpi != s.dpi || s.font == 0 {
		if s.font != 0 {
			pDeleteObject.Call(s.font)
		}
		if s.fontLight != 0 {
			pDeleteObject.Call(s.fontLight)
		}
		s.dpi = dpi
		face, _ := windows.UTF16PtrFromString("Consolas")
		const fwNormal, fwLight, defaultCharset, outTTPrecis, clearType, fixedModern = 400, 300, 1, 4, 5, 0x31
		s.font, _, _ = pCreateFontW.Call(uintptr(-s.scale(14)), 0, 0, 0, fwNormal, 0, 0, 0,
			defaultCharset, outTTPrecis, 0, clearType, fixedModern, uintptr(unsafe.Pointer(face)))
		s.fontLight, _, _ = pCreateFontW.Call(uintptr(-s.scale(14)), 0, 0, 0, fwLight, 0, 0, 0,
			defaultCharset, outTTPrecis, 0, clearType, fixedModern, uintptr(unsafe.Pointer(face)))
	}
	hdc, _, _ := pGetDC.Call(hwnd)
	old, _, _ := pSelectObject.Call(hdc, s.font)
	lineH := textExtent(hdc, "M").CY + s.scale(3)
	// The monospace advance, for the column under the pointer (links).
	charW := float64(textExtent(hdc, "0000000000").CX) / 10
	s.m = tray.Metrics{Pad: int(s.scale(12)), LineH: int(lineH), RuleH: int(s.scale(9)), ActionH: int(lineH + s.scale(8)), CharW: charW}
	s.closeSize = lineH
	var textW int32
	for i, l := range append(append([]tray.PanelLine(nil), lines...), wider...) {
		if l.Kind == tray.LineRule {
			continue
		}
		w := textExtent(hdc, l.Text).CX
		if i == 0 && !s.noClose {
			w += s.scale(10) + s.closeSize // the × beside the status line
		}
		textW = max(textW, w)
	}
	pSelectObject.Call(hdc, old)
	pReleaseDC.Call(hwnd, hdc)
	w, h := textW+2*int32(s.m.Pad), int32(s.m.Height(lines))
	resized := w != s.width || h != s.height
	s.width, s.height = w, h
	return resized
}

// lineColors are a row's text and accent colours.
// hasLinks reports whether a line has a link to draw.
func hasLinks(l tray.PanelLine) bool {
	for _, ln := range l.Links {
		if ln.Action != tray.ActNone {
			return true
		}
	}
	return false
}

func lineColors(l tray.PanelLine) (text, accent uintptr) {
	switch l.Kind {
	case tray.LineOK:
		return colInk, colAccent
	case tray.LineError:
		return colInk, colError
	case tray.LineDim:
		return colMuted, colAccent
	case tray.LineAction:
		if l.Selected {
			return colHot, colAccent
		}
		return colAction, colAccent
	case tray.LineActionOff:
		return colOff, colOff
	case tray.LineArmed:
		if l.Selected {
			return colArmedHot, colAccent
		}
		return colError, colAccent
	}
	return colInk, colAccent
}

// paint draws lines off screen, then copies them in one go (no flicker).
func (s *sheet) paint(hwnd uintptr, lines []tray.PanelLine) {
	var ps paintStruct
	hdc, _, _ := pBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	defer pEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	var cr rect
	pGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&cr)))
	w, h := cr.Right, cr.Bottom
	if w <= 0 || h <= 0 {
		return
	}
	mem, _, _ := pCreateCompatibleDC.Call(hdc)
	bmp, _, _ := pCreateCompatibleBitmap.Call(hdc, uintptr(w), uintptr(h))
	oldBmp, _, _ := pSelectObject.Call(mem, bmp)
	oldFont, _, _ := pSelectObject.Call(mem, s.font)
	pSetBkMode.Call(mem, bkTransparent)

	// The pane with the /projects tiles' grey-and-green double border.
	one := max(1, s.scale(1))
	fill(mem, rect{0, 0, w, h}, colBorder)
	fill(mem, rect{one, one, w - one, h - one}, colInner)
	fill(mem, rect{2 * one, 2 * one, w - 2*one, h - 2*one}, colBg)

	pad, lineH := int32(s.m.Pad), int32(s.m.LineH)
	y := pad
	for _, l := range lines {
		rowH := int32(s.m.RowH(l))
		if l.Kind == tray.LineRule {
			mid := y + rowH/2
			fill(mem, rect{pad, mid, w - pad, mid + one}, colBorder)
			y += rowH
			continue
		}
		if l.Selected {
			in := s.scale(6)
			fill(mem, rect{pad - in, y, w - pad + in, y + rowH}, colHover)
		}
		ty := y + (rowH-lineH)/2 // action rows centre their line
		col, accent := lineColors(l)
		r := []rune(l.Text)
		if l.Quiet && l.AgeEnd > l.Age && l.HiEnd > l.Hi && l.Age >= 0 && l.HiEnd <= len(r) && l.Hi >= l.AgeEnd {
			// Age, +latest and rolling totals of older activity: gray, light.
			light := s.fontLight
			if light == 0 {
				light = s.font
			}
			x := pad
			pSelectObject.Call(mem, s.font)
			x += textOut(mem, x, ty, string(r[:l.Age]), col)
			pSelectObject.Call(mem, light)
			x += textOut(mem, x, ty, string(r[l.Age:l.AgeEnd]), colMuted)
			pSelectObject.Call(mem, s.font)
			x += textOut(mem, x, ty, string(r[l.AgeEnd:l.Hi]), col)
			pSelectObject.Call(mem, light)
			x += textOut(mem, x, ty, string(r[l.Hi:l.HiEnd]), colMuted)
			textOut(mem, x, ty, string(r[l.HiEnd:]), colMuted)
			pSelectObject.Call(mem, s.font)
		} else if l.HiEnd > l.Hi && l.Hi >= 0 && l.HiEnd <= len(r) {
			x := pad
			x += textOut(mem, x, ty, string(r[:l.Hi]), col)
			x += textOut(mem, x, ty, string(r[l.Hi:l.HiEnd]), accent)
			textOut(mem, x, ty, string(r[l.HiEnd:]), col)
		} else if l.Kind == tray.LineDim && hasLinks(l) {
			// The account line's names are links (tray.Link).
			x, at := pad, 0
			for _, ln := range l.Links {
				if ln.Action == tray.ActNone || ln.From < at || ln.To <= ln.From || ln.To > len(r) {
					continue
				}
				x += textOut(mem, x, ty, string(r[at:ln.From]), col)
				x += textOut(mem, x, ty, string(r[ln.From:ln.To]), colLink)
				at = ln.To
			}
			textOut(mem, x, ty, string(r[at:]), col)
		} else {
			textOut(mem, pad, ty, l.Text, col)
		}
		y += rowH
	}
	if !s.noClose {
		c := s.closeRect()
		xs := textExtent(mem, "×")
		textOut(mem, c.Left+(c.Right-c.Left-xs.CX)/2, c.Top+(c.Bottom-c.Top-xs.CY)/2, "×", colMuted)
	}

	pBitBlt.Call(hdc, 0, 0, uintptr(w), uintptr(h), mem, 0, 0, srcCopy)
	pSelectObject.Call(mem, oldFont)
	pSelectObject.Call(mem, oldBmp)
	pDeleteObject.Call(bmp)
	pDeleteDC.Call(mem)
}

// monitorAt is the monitor and work area nearest r.
func monitorAt(r rect) (monitor, work rect, ok bool) {
	mon, _, _ := pMonitorFromRect.Call(uintptr(unsafe.Pointer(&r)), monitorNearest)
	mi := monitorInfo{}
	mi.Size = uint32(unsafe.Sizeof(mi))
	if r, _, _ := pGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); r == 0 {
		return rect{}, rect{}, false
	}
	return mi.Monitor, mi.Work, true
}

// windowFlags describes a window's style for app.dump.
func windowFlags(hwnd uintptr) string {
	st, _, _ := pGetWindowLongPtrW.Call(hwnd, gwlStyle)
	ex, _, _ := pGetWindowLongPtrW.Call(hwnd, gwlExStyle)
	var f []byte
	add := func(on bool, s string) {
		if on {
			if len(f) > 0 {
				f = append(f, ", "...)
			}
			f = append(f, s...)
		}
	}
	add(st&wsPopup != 0, "popup")
	add(st&wsCaption == wsCaption, "caption")
	add(st&wsMinimizeBox != 0, "minimize box")
	add(ex&wsExAppWindow != 0, "app window")
	add(ex&wsExTopmost != 0, "topmost")
	add(ex&wsExToolWindow != 0, "tool window")
	add(ex&wsExLayered != 0, "layered")
	add(ex&wsExNoActivate != 0, "no-activate")
	return string(f)
}
