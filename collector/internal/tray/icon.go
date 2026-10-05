package tray

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

// The icon is a status dot drawn in code: the state colour with a soft top
// sheen and a darker rim, so it holds its edge on a light taskbar and stays
// bright on a dark one.

var (
	colGreen = color.NRGBA{0x34, 0xc0, 0x6a, 0xff}
	colRed   = color.NRGBA{0xe5, 0x48, 0x4d, 0xff}
)

// RGBA is the state colour.
func (c Color) RGBA() color.NRGBA {
	if c == Green {
		return colGreen
	}
	return colRed
}

// Inset is the margin around the dot as a fraction of the canvas: Windows
// nearly fills its notification-area slot; the macOS menu bar wants air;
// the Dock tile (and tokenmaxr.app's icon) sits between them.
const (
	WindowsInset = 1.5 / 16
	MenuBarInset = 3.0 / 16
	DockInset    = 2.0 / 16
)

// DrawIcon renders the dot at size × size pixels.
func DrawIcon(c Color, size int, inset float64) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	base := c.RGBA()
	rim := mix(base, color.NRGBA{0, 0, 0, 255}, 0.45)
	s := float64(size)
	m := math.Max(1, math.Round(inset*s*2)/2)
	cx, cy, r := s/2, s/2, s/2-m
	rimW := math.Max(1, s/16)
	for y := range size {
		for x := range size {
			// 4x4 supersampling for a smooth edge at 16 px.
			var cover, inner float64
			for sy := range 4 {
				for sx := range 4 {
					px := float64(x) + (float64(sx)+0.5)/4
					py := float64(y) + (float64(sy)+0.5)/4
					d := math.Hypot(px-cx, py-cy) - r
					if d <= 0 {
						cover++
						if d <= -rimW {
							inner++
						}
					}
				}
			}
			if cover == 0 {
				continue
			}
			t := (float64(y) + 0.5 - (cy - r)) / (2 * r)
			fill := mix(base, color.NRGBA{255, 255, 255, 255}, 0.22*(1-math.Min(1, math.Max(0, t))))
			p := mix(rim, fill, inner/cover)
			p.A = uint8(math.Round(255 * cover / 16))
			img.SetNRGBA(x, y, p)
		}
	}
	return img
}

func mix(a, b color.NRGBA, t float64) color.NRGBA {
	l := func(x, y uint8) uint8 { return uint8(math.Round(float64(x) + (float64(y)-float64(x))*t)) }
	return color.NRGBA{l(a.R, b.R), l(a.G, b.G), l(a.B, b.B), 255}
}

// IconPNG is the dot as a PNG (the macOS menu bar draws it at 16 pt, so 32
// px covers Retina).
func IconPNG(c Color, size int, inset float64) []byte {
	var b bytes.Buffer
	png.Encode(&b, DrawIcon(c, size, inset))
	return b.Bytes()
}

// IconICO is the dot as a Windows .ico with 16, 20, 24, 32 and 48 px images
// (32-bit DIBs), so the shell picks a crisp one at any DPI.
func IconICO(c Color) []byte {
	sizes := []int{16, 20, 24, 32, 48}
	var dibs [][]byte
	for _, s := range sizes {
		dibs = append(dibs, dib(DrawIcon(c, s, WindowsInset)))
	}
	var b bytes.Buffer
	le := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	le(uint16(0))
	le(uint16(1)) // icon
	le(uint16(len(sizes)))
	offset := 6 + 16*len(sizes)
	for i, s := range sizes {
		b.Write([]byte{byte(s), byte(s), 0, 0}) // no palette
		le(uint16(1))                           // planes
		le(uint16(32))                          // bpp
		le(uint32(len(dibs[i])))
		le(uint32(offset))
		offset += len(dibs[i])
	}
	for _, d := range dibs {
		b.Write(d)
	}
	return b.Bytes()
}

// IconDIB is the dot at size × size as one icon image (the bytes of an
// RT_ICON resource), for CreateIconFromResourceEx: the main window's
// title-bar and taskbar icons.
func IconDIB(c Color, size int) []byte { return dib(DrawIcon(c, size, WindowsInset)) }

// dib encodes img as an icon DIB: BITMAPINFOHEADER with doubled height,
// bottom-up BGRA rows, then a 1-bit AND mask (all zero: alpha decides).
func dib(img *image.NRGBA) []byte {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	var b bytes.Buffer
	le := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	le(uint32(40))
	le(int32(w))
	le(int32(2 * h))
	le(uint16(1))
	le(uint16(32))
	le(uint32(0)) // BI_RGB
	le(uint32(0))
	le(int32(0))
	le(int32(0))
	le(uint32(0))
	le(uint32(0))
	for y := h - 1; y >= 0; y-- {
		for x := range w {
			p := img.NRGBAAt(x, y)
			b.Write([]byte{p.B, p.G, p.R, p.A})
		}
	}
	b.Write(make([]byte, ((w+31)/32)*4*h))
	return b.Bytes()
}
