package tray

import (
	"encoding/binary"
	"slices"
	"testing"
)

func syncingInput() Input {
	in := popupInput()
	in.Ticking, in.TickStarted = true, now
	return in
}

func pinnedInput() Input {
	in := popupInput()
	in.Pinned = true
	return in
}

// The main window shows the popup's rows: the panel's heading, status,
// account and provider lines, then the same action rows (and the build).
func TestWindowRows(t *testing.T) {
	for _, in := range []Input{popupInput(), syncingInput(), pinnedInput()} {
		v := Evaluate(in)
		got := Window(v)
		if !slices.Equal(got, Popup(v)) {
			t.Fatalf("window rows differ from the popup's:\n%s\nvs\n%s", SheetText(got, "  "), SheetText(Popup(v), "  "))
		}
		for i, l := range Panel(v) {
			if got[i] != l {
				t.Errorf("row %d is not the panel's: %+v vs %+v", i, got[i], l)
			}
		}
	}
	v := Evaluate(popupInput())
	var acts []Action
	for _, l := range Window(v) {
		if l.Hoverable() {
			acts = append(acts, l.Action)
		}
	}
	want := []Action{ActDashboard, ActSettings, ActOpenLog, ActQuit}
	if !slices.Equal(acts, want) {
		t.Fatalf("window actions %v, want %v", acts, want)
	}
	// Pinned, no Unpin row either (the panel's own × closes it).
	pinned := Window(Evaluate(pinnedInput()))
	if slices.ContainsFunc(pinned, func(l PanelLine) bool { return l.Action == ActUnpin || l.Action == ActPin }) {
		t.Fatalf("pinned window rows:\n%s", SheetText(pinned, "  "))
	}
	if WindowTitle != "tokenmaxr" {
		t.Fatalf("title %q", WindowTitle)
	}
}

// Actions in the window run as in the popup, but never close it; Quit
// still takes two clicks.
func TestClickWindow(t *testing.T) {
	var st PopupState
	for _, act := range []Action{ActDashboard, ActPin, ActUnpin, ActSyncNow, ActSettings, ActOpenLog} {
		if res := ClickWindow(&st, act); res.Run != act || res.Close {
			t.Fatalf("%s: %+v", act, res)
		}
	}
	if res := ClickWindow(&st, ActNone); res != (PopupClick{}) {
		t.Fatalf("a miss: %+v", res)
	}
	res := ClickWindow(&st, ActQuit)
	if res.Run != ActNone || res.Close || !res.ArmQuit || !res.Redraw || !st.QuitArmed || st.Sel != ActQuit {
		t.Fatalf("first Quit click: %+v %+v", res, st)
	}
	view := PopupView(Window(Evaluate(popupInput())), st)
	if !slices.ContainsFunc(view, func(l PanelLine) bool { return l.Kind == LineArmed && l.Text == QuitArmedText }) {
		t.Fatalf("armed Quit row not drawn:\n%s", SheetText(view, "  "))
	}
	res = ClickWindow(&st, ActQuit)
	if res.Run != ActQuitNow || res.Close {
		t.Fatalf("second Quit click: %+v", res)
	}
	st = PopupState{}
	if res := ClickWindow(&st, ActCopyFleet); res.Run != ActCopyFleet || res.Close || !res.ArmCopied || !st.Copied {
		t.Fatalf("copy: %+v %+v", res, st)
	}
}

// IconDIB is one RT_ICON image: a BITMAPINFOHEADER with the doubled
// height, 32-bit BGRA rows, then the AND mask.
func TestIconDIB(t *testing.T) {
	for _, size := range []int{16, 24, 32, 48} {
		b := IconDIB(Green, size)
		if len(b) != 40+4*size*size+((size+31)/32)*4*size {
			t.Fatalf("%d px: %d bytes", size, len(b))
		}
		hdr := binary.LittleEndian
		if hdr.Uint32(b[0:]) != 40 || int32(hdr.Uint32(b[4:])) != int32(size) || int32(hdr.Uint32(b[8:])) != int32(2*size) || hdr.Uint16(b[14:]) != 32 {
			t.Fatalf("%d px header % x", size, b[:16])
		}
		// The centre pixel is opaque green (BGRA).
		row, col := size/2, size/2
		p := b[40+4*((size-1-row)*size+col):]
		if p[3] != 255 || p[1] < p[2] || p[1] < p[0] {
			t.Fatalf("%d px centre % x", size, p[:4])
		}
	}
}
