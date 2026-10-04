//go:build windows

package ui

// The pinned live panel on Windows: a borderless, always-on-top tool
// window (off the taskbar and Alt-Tab) that never takes the focus, drawn
// by the shared sheet (sheet_windows.go). It runs its own message loop on
// a locked OS thread, apart from systray's. Other goroutines only change
// its lines under a mutex and post it a message.

import (
	"fmt"
	"runtime"
	"slices"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
	"github.com/7-of-9/tokenmaxr/collector/internal/tray"
)

const (
	panelClass = "d0m1CollectorPanel"
	panelTitle = buildinfo.Product
)

// panel is the one pinned panel. mu guards the fields above the line; the
// rest belong to the window thread.
type panel struct {
	mu      sync.Mutex
	want    bool // shown, as the app last asked
	running bool // a window thread exists (creating, shown or closing)
	hwnd    uintptr
	lines   []tray.PanelLine
	x, y    int
	placed  bool
	h       Handler

	sheet
}

var (
	pnl          = &panel{}
	panelOnce    sync.Once
	panelErr     error
	panelWndProc = windows.NewCallback(panelProc)
)

// setPanel shows, updates or closes the panel (renderer.SetPanel).
func setPanel(s tray.PanelState, h Handler) {
	p := pnl
	p.mu.Lock()
	p.h = h
	changed := p.want != s.Shown || (s.Shown && !slices.Equal(p.lines, s.Lines))
	p.want = s.Shown
	if s.Shown {
		p.lines = slices.Clone(s.Lines)
	}
	start := s.Shown && !p.running
	if start {
		p.running = true
		p.x, p.y, p.placed = s.X, s.Y, s.Placed
	}
	hwnd := p.hwnd
	p.mu.Unlock()
	switch {
	case start:
		go p.loop()
	case changed && hwnd != 0:
		pPostMessageW.Call(hwnd, wmSync, 0, 0)
	}
}

// closePanel closes the panel (the app quits).
func closePanel() {
	p := pnl
	p.mu.Lock()
	p.want = false
	hwnd := p.hwnd
	p.mu.Unlock()
	if hwnd != 0 {
		pPostMessageW.Call(hwnd, wmSync, 0, 0)
	}
}

// loop is the window thread: create, pump messages until destroyed, and
// start again if the panel was wanted back meanwhile.
func (p *panel) loop() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	created := p.create()
	if created {
		pump()
	}
	p.free()
	p.mu.Lock()
	p.hwnd = 0
	again := p.want && created // a failed create does not retry forever
	p.running = again
	p.mu.Unlock()
	if again {
		go p.loop()
	}
}

// pump runs the calling thread's message loop until WM_QUIT.
func pump() {
	var m msg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func (p *panel) create() bool {
	dpiAwareThread()
	panelOnce.Do(func() { panelErr = registerClass(panelClass, panelWndProc) })
	if panelErr != nil {
		return false
	}
	p.mu.Lock()
	x, y := int32(0), int32(0)
	if p.placed {
		x, y = int32(p.x), int32(p.y)
	}
	p.mu.Unlock()
	hwnd := newSheetWindow(panelClass, panelTitle, wsExTopmost|wsExToolWindow|wsExLayered|wsExNoActivate, x, y)
	if hwnd == 0 {
		return false
	}
	p.mu.Lock()
	p.hwnd = hwnd
	p.mu.Unlock()
	p.layout(hwnd, true)
	pShowWindow.Call(hwnd, swShowNoActive)
	// Closed while it was being created: the sync closes it again.
	pPostMessageW.Call(hwnd, wmSync, 0, 0)
	return true
}

func panelProc(hwnd, m, wp, lp uintptr) uintptr {
	p := pnl
	switch m {
	case wmSync:
		p.mu.Lock()
		want := p.want
		p.mu.Unlock()
		if !want {
			pDestroyWindow.Call(hwnd)
			return 0
		}
		p.layout(hwnd, false)
		pInvalidateRect.Call(hwnd, 0, 0)
		return 0
	case wmPaint:
		p.mu.Lock()
		lines := p.lines
		p.mu.Unlock()
		p.paint(hwnd, lines)
		return 0
	case wmEraseBkgnd:
		return 1
	case wmNCHitTest:
		// Anywhere but the × drags the panel.
		var wr rect
		pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr)))
		if p.inClose(int32(int16(lp))-wr.Left, int32(int16(lp>>16))-wr.Top) {
			return htClient
		}
		return htCaption
	case wmSetCursor:
		if lp&0xffff == htClient {
			hand, _, _ := pLoadCursorW.Call(0, idcHand)
			pSetCursor.Call(hand)
			return 1
		}
	case wmMouseActivate:
		return maNoActivate
	case wmLButtonUp:
		if p.inClose(int32(int16(lp)), int32(int16(lp>>16))) {
			p.unpin()
		}
		return 0
	case wmClose:
		p.unpin()
		return 0
	case wmExitSizeMove:
		var wr rect
		pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr)))
		p.mu.Lock()
		p.x, p.y, p.placed = int(wr.Left), int(wr.Top), true
		h := p.h
		p.mu.Unlock()
		if h.Moved != nil {
			go h.Moved(int(wr.Left), int(wr.Top))
		}
		return 0
	case wmDPIChanged:
		// The suggested rectangle keeps the panel under the cursor on the
		// new monitor; the layout then re-measures at the new DPI.
		r := *(*rect)(*(*unsafe.Pointer)(unsafe.Pointer(&lp)))
		pSetWindowPos.Call(hwnd, hwndTopmost, uintptr(r.Left), uintptr(r.Top), 0, 0, swpNoSize|swpNoActivate)
		p.layout(hwnd, false)
		pInvalidateRect.Call(hwnd, 0, 0)
		return 0
	case wmDisplayChange:
		p.layout(hwnd, true) // back on screen if its monitor went away
		return 0
	case wmDestroy:
		pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, m, wp, lp)
	return r
}

// unpin is the × (and WM_CLOSE): the app unpins, which closes the panel.
func (p *panel) unpin() {
	p.mu.Lock()
	h := p.h
	p.mu.Unlock()
	if h.Click != nil {
		go h.Click(tray.ActUnpin)
	}
}

// layout measures the lines at the window's DPI and sizes the panel; place
// also positions it: its saved spot kept on a monitor, or by default the
// bottom-right corner of the work area, above the notification area.
func (p *panel) layout(hwnd uintptr, place bool) {
	p.mu.Lock()
	lines := p.lines
	x, y, placed := int32(p.x), int32(p.y), p.placed
	p.mu.Unlock()
	resized := p.measure(hwnd, lines, nil)
	w, h := p.width, p.height
	if !place {
		if resized {
			pSetWindowPos.Call(hwnd, hwndTopmost, 0, 0, uintptr(w), uintptr(h), swpNoMove|swpNoActivate)
		}
		return
	}
	if _, wa, ok := monitorAt(rect{x, y, x + w, y + h}); ok {
		m := p.scale(16)
		if !placed {
			x, y = wa.Right-w-m, wa.Bottom-h-m
		}
		x = max(wa.Left, min(x, wa.Right-w))
		y = max(wa.Top, min(y, wa.Bottom-h))
	}
	pSetWindowPos.Call(hwnd, hwndTopmost, uintptr(x), uintptr(y), uintptr(w), uintptr(h), swpNoActivate)
}

// panelDebug describes the panel window for app.dump.
func panelDebug() string {
	p := pnl
	p.mu.Lock()
	hwnd, want := p.hwnd, p.want
	p.mu.Unlock()
	if hwnd == 0 {
		return fmt.Sprintf("panel window: none (wanted %v)\n", want)
	}
	var wr rect
	pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr)))
	return fmt.Sprintf("panel window: %s; at %d,%d size %dx%d\n", windowFlags(hwnd),
		wr.Left, wr.Top, wr.Right-wr.Left, wr.Bottom-wr.Top)
}
