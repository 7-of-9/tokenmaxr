//go:build darwin && cgo

package ui

// The NSPanel's callbacks into Go. They live apart from native_darwin.go
// because a cgo file with //export may only declare C in its preamble,
// and that one defines the Objective-C panel.

import "C"

import "github.com/7-of-9/tokenmaxr/collector/internal/tray"

//export d0m1PanelMoved
func d0m1PanelMoved(x, top C.int) {
	if h := panelHandler(); h.Moved != nil {
		go h.Moved(int(x), int(top))
	}
}

//export d0m1PanelClosed
func d0m1PanelClosed() {
	if h := panelHandler(); h.Click != nil {
		go h.Click(tray.ActUnpin)
	}
}

//export d0m1PopupHover
func d0m1PopupHover(y C.int) C.int { return C.int(popupHover(int(y))) }

//export d0m1PopupClick
func d0m1PopupClick(y C.int) { popupClick(int(y)) }

//export d0m1PopupKey
func d0m1PopupKey(key C.int) { popupKey(int(key)) }

//export d0m1PopupResign
func d0m1PopupResign() { popupClose("focus lost") }

//export d0m1PopupClosed
func d0m1PopupClosed() { popupClose("the ×") }

//export d0m1PopupOutside
func d0m1PopupOutside() { popupClose("click outside") }
