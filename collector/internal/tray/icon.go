package tray

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

// The icon is a piece of the dashboard's contribution heatmap, drawn in code
// (owner's pick 2026-10-05, "1. Heatmap"), in one of three styles:
//
//   - Tile (the Dock, tokenmaxr.app at every size): a dark rounded tile
//     holding a 4×4 piece of the calendar in GitHub's four greens.
//   - Template (the macOS menu bar): a bare 3×3 grid in black at four
//     strengths, a template image that macOS paints in the menu bar's own
//     colour, on a light bar and a dark one.
//   - Grid (Windows: the notification area, the window and taskbar icons,
//     the Start-menu entry): the same grid in one green, which holds on a
//     light taskbar and a dark one.
//
// The red state adds a red badge, bottom right like the header icon's
// status dot (owner direction 2026-10-05), cut out of the grid (on the
// tile, at its corner), so it differs in shape, not only in hue.

// Style is how the icon is drawn, by where it is shown.
type Style int

const (
	Grid Style = iota
	Template
	Tile
)

var (
	colGreen = color.NRGBA{0x2e, 0xa0, 0x43, 0xff} // the Windows grid's green
	colRed   = color.NRGBA{0xff, 0x45, 0x3a, 0xff} // the badge
	// The header icon's status dot: the sheet's green and red.
	colDotGreen = color.NRGBA{0x56, 0xd3, 0x64, 0xff}
	colDotRed   = color.NRGBA{0xf8, 0x51, 0x49, 0xff}
	colBlack    = color.NRGBA{0, 0, 0, 0xff} // the template grid
	// GitHub's contribution levels 1-4, and the empty day.
	heatLevels = [5]color.NRGBA{{0x16, 0x1b, 0x22, 0xff}, {0x0e, 0x44, 0x29, 0xff}, {0x00, 0x6d, 0x32, 0xff}, {0x26, 0xa6, 0x41, 0xff}, {0x39, 0xd3, 0x53, 0xff}}
	// tileLevels is the 4×4 piece on the tile, row by row (0: empty day).
	tileLevels = [16]int{0, 1, 2, 4, 1, 2, 3, 3, 2, 3, 4, 2, 3, 4, 2, 1}
	// gridAlpha is each 3×3 cell's strength, row by row: the template's
	// (the design's), and the green grid's, lifted so its faintest cell
	// still shows on a dark taskbar.
	templateAlpha = [9]float64{0.3, 0.55, 1, 0.55, 1, 0.75, 1, 0.75, 0.4}
	gridAlpha     = [9]float64{0.45, 0.7, 1, 0.7, 1, 0.85, 1, 0.85, 0.55}
	tileTop       = color.NRGBA{0x1c, 0x23, 0x30, 0xff}
	tileBot       = color.NRGBA{0x0b, 0x0f, 0x14, 0xff}
	tileEdge      = color.NRGBA{0xf0, 0xf6, 0xfc, 0xff}
)

// RGBA is the state colour.
func (c Color) RGBA() color.NRGBA {
	if c == Green {
		return colGreen
	}
	return colRed
}

// tileInset is the margin around the tile as a fraction of the canvas
// (Apple's icon grid: 100 of 1024).
const tileInset = 100.0 / 1024

// roundRect is the signed distance from (px, py) to a rounded rectangle
// (negative inside).
func roundRect(px, py, x0, y0, x1, y1, r float64) float64 {
	cx, cy := (x0+x1)/2, (y0+y1)/2
	qx := math.Abs(px-cx) - ((x1-x0)/2 - r)
	qy := math.Abs(py-cy) - ((y1-y0)/2 - r)
	return math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - r
}

// over composites src (straight alpha a) over dst (premultiplied RGBA in 0..1).
func over(dst *[4]float64, src color.NRGBA, a float64) {
	if a <= 0 {
		return
	}
	k := 1 - a
	dst[0] = float64(src.R)/255*a + dst[0]*k
	dst[1] = float64(src.G)/255*a + dst[1]*k
	dst[2] = float64(src.B)/255*a + dst[2]*k
	dst[3] = a + dst[3]*k
}

// gridLayout is the 3×3 grid at size px, on whole pixels so every cell is
// crisp: margin m, cell c and gap g (3c + 2g + 2m = size, give or take one
// pixel of margin), in the design's proportions (18 px: 0.8, 4.4, 1.6).
func gridLayout(size int) (m, c, g int) {
	s := float64(size)
	g = max(1, int(math.Round(s*1.6/18)))
	m = max(1, int(math.Round(s*0.8/18)))
	if size < 16 {
		m = 0
	}
	c = (size - 2*g - 2*m) / 3
	m = (size - 3*c - 2*g) / 2
	return m, c, g
}

// badge is the red badge on the grid (r 3 of 18 at 15, 15, cut out to
// r 4.5) or on the tile (at its bottom-right corner, ringed in the
// tile's colour rather than cut out of it).
type badge struct{ x, y, r, cut float64 }

func badgeFor(size int, st Style) badge {
	s := float64(size)
	if st == Tile {
		m := tileInset * s
		w := s - 2*m
		return badge{x: s - m - 0.1*w, y: s - m - 0.1*w, r: 0.16 * w, cut: 0.16*w + math.Max(1, 0.035*w)}
	}
	return badge{x: s * 15 / 18, y: s * 15 / 18, r: s * 3 / 18, cut: s * 4.5 / 18}
}

// tile is the Dock tile's geometry at one size.
type tile struct{ m, r, w, cell, step, start, edge float64 }

func tileFor(size int, inset float64) tile {
	s := float64(size)
	m := inset * s
	w := s - 2*m
	return tile{m: m, r: 0.2245 * w, w: w, cell: 0.1505 * w, step: 0.1861 * w, start: m + 0.1456*w, edge: math.Max(1, s/256)}
}

// shade is the tile's top-lit gradient at row py: one shade per row.
func (t tile) shade(py float64) color.NRGBA {
	return mix(tileTop, tileBot, math.Min(1, math.Max(0, (math.Floor(py)+0.5-t.m)/t.w)))
}

// at paints the tile at (px, py): the rounded tile with its top-lit
// gradient, the 4×4 heatmap piece (empty days outlined) and a faint edge.
func (t tile) at(d *[4]float64, px, py float64) {
	e := roundRect(px, py, t.m, t.m, t.m+t.w, t.m+t.w, t.r)
	if e > 0 {
		return
	}
	over(d, t.shade(py), 1)
	// The one cell (px, py) can be in: cells are narrower than their step.
	col, row := math.Floor((px-t.start)/t.step), math.Floor((py-t.start)/t.step)
	if col >= 0 && col < 4 && row >= 0 && row < 4 {
		x0, y0 := t.start+col*t.step, t.start+row*t.step
		if ce := roundRect(px, py, x0, y0, x0+t.cell, y0+t.cell, 0.21*t.cell); ce <= 0 {
			lv := tileLevels[int(row)*4+int(col)]
			over(d, heatLevels[lv], 1)
			if lv == 0 && ce > -t.edge {
				over(d, tileEdge, 0.08)
			}
		}
	}
	// A faint edge, so the tile holds on a dark Dock.
	if e > -t.edge {
		over(d, tileEdge, 0.10)
	}
}

// DrawIcon renders the icon at size × size pixels in style st; c Red adds
// the badge.
func DrawIcon(c Color, size int, st Style) *image.NRGBA {
	return draw(size, st, c != Green, true)
}

// draw renders the icon; red cuts the badge's place out of the grid, and
// withBadge paints the badge there (in black on the template, which has
// no colours).
func draw(size int, st Style, red, withBadge bool) *image.NRGBA {
	b := badgeFor(size, st)
	var sample func(px, py float64) [4]float64
	if st == Tile {
		t := tileFor(size, tileInset)
		sample = func(px, py float64) [4]float64 {
			var d [4]float64
			t.at(&d, px, py)
			if red {
				dist := math.Hypot(px-b.x, py-b.y)
				if dist <= b.cut && d[3] > 0 {
					over(&d, t.shade(py), 1) // the ring: the tile's colour where it sits
				}
				if dist <= b.r && withBadge {
					over(&d, colRed, 1)
				}
			}
			return d
		}
	} else {
		m, cs, g := gridLayout(size)
		fill, alpha := colGreen, gridAlpha
		if st == Template {
			fill, alpha = colBlack, templateAlpha
		}
		cell := float64(cs)
		sample = func(px, py float64) [4]float64 {
			var d [4]float64
			col, row := (int(px)-m)/(cs+g), (int(py)-m)/(cs+g)
			if px >= float64(m) && py >= float64(m) && col < 3 && row < 3 {
				x0, y0 := float64(m+col*(cs+g)), float64(m+row*(cs+g))
				if roundRect(px, py, x0, y0, x0+cell, y0+cell, 0.25*cell) <= 0 {
					over(&d, fill, alpha[row*3+col])
				}
			}
			if red {
				dist := math.Hypot(px-b.x, py-b.y)
				if dist <= b.cut {
					d = [4]float64{}
				}
				if dist <= b.r && withBadge {
					badge := colRed
					if st == Template {
						badge = colBlack
					}
					over(&d, badge, 1)
				}
			}
			return d
		}
	}
	return render(size, sample)
}

// render draws sample (premultiplied RGBA at a point) into a size × size
// image.
func render(size int, sample func(px, py float64) [4]float64) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := range size {
		for x := range size {
			// 4×4 supersampling for clean edges at 16 px, where the pixel's
			// outer samples differ (an edge crosses it).
			fx, fy := float64(x), float64(y)
			acc := sample(fx+0.125, fy+0.125)
			if acc != sample(fx+0.875, fy+0.125) || acc != sample(fx+0.125, fy+0.875) || acc != sample(fx+0.875, fy+0.875) {
				acc = superSample(sample, fx, fy)
			}
			if acc[3] <= 0 {
				continue
			}
			un := func(v float64) uint8 { return uint8(math.Round(math.Min(1, v/acc[3]) * 255)) }
			img.SetNRGBA(x, y, color.NRGBA{un(acc[0]), un(acc[1]), un(acc[2]), uint8(math.Round(acc[3] * 255))})
		}
	}
	return img
}

// BrandIcon is the popup's and the main window's header icon at size ×
// size pixels (owner direction 2026-10-05: "BRAND the context menu nicer
// ... use the main icon in colour"): the heatmap tile filling the square,
// with the status dot over its bottom-right corner, green or red, cut out
// of the tile like a status badge on an avatar.
func BrandIcon(c Color, size int) *image.NRGBA {
	s := float64(size)
	t := tileFor(size, 0)
	dot := colDotGreen
	if c != Green {
		dot = colDotRed
	}
	x, y, r := 0.83*s, 0.83*s, 0.15*s
	cut := r + math.Max(1, 0.06*s)
	return render(size, func(px, py float64) [4]float64 {
		var d [4]float64
		dist := math.Hypot(px-x, py-y)
		if dist > cut {
			t.at(&d, px, py)
		}
		if dist <= r {
			over(&d, dot, 1)
		}
		return d
	})
}

// BrandPNG is BrandIcon as a PNG, BrandDIB as one icon image (IconDIB).
func BrandPNG(c Color, size int) []byte { return pngOf(BrandIcon(c, size)) }
func BrandDIB(c Color, size int) []byte { return dib(BrandIcon(c, size)) }

// superSample averages 4×4 samples of the pixel at (x, y).
func superSample(sample func(px, py float64) [4]float64, x, y float64) [4]float64 {
	var acc [4]float64
	for sy := range 4 {
		for sx := range 4 {
			v := sample(x+(float64(sx)+0.5)/4, y+(float64(sy)+0.5)/4)
			for k := range acc {
				acc[k] += v[k] / 16
			}
		}
	}
	return acc
}

func mix(a, b color.NRGBA, t float64) color.NRGBA {
	l := func(x, y uint8) uint8 { return uint8(math.Round(float64(x) + (float64(y)-float64(x))*t)) }
	return color.NRGBA{l(a.R, b.R), l(a.G, b.G), l(a.B, b.B), 255}
}

// IconPNG is the icon as a PNG (the macOS menu bar draws it at 16 pt, so 32
// px covers Retina).
func IconPNG(c Color, size int, st Style) []byte { return pngOf(DrawIcon(c, size, st)) }

// MenuBarRedParts are the red menu-bar icon's two layers, for a status item
// that paints the grid in the menu bar's colour itself (a template image
// cannot hold the red): the template grid with the badge's place cut out,
// and the red badge alone.
func MenuBarRedParts(size int) (grid, badge []byte) {
	g := draw(size, Template, true, false)
	b := image.NewNRGBA(g.Rect)
	full := draw(size, Grid, true, true)
	for i := 0; i < len(b.Pix); i += 4 {
		if p := full.Pix[i : i+4]; p[0] > 200 && p[1] < 120 { // the badge's pixels
			copy(b.Pix[i:i+4], p)
		}
	}
	return pngOf(g), pngOf(b)
}

func pngOf(img *image.NRGBA) []byte {
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

// IconICO is the icon as a Windows .ico with 16, 20, 24, 32 and 48 px images
// (32-bit DIBs), so the shell picks a crisp one at any DPI.
func IconICO(c Color) []byte {
	sizes := []int{16, 20, 24, 32, 48}
	var dibs [][]byte
	for _, s := range sizes {
		dibs = append(dibs, dib(DrawIcon(c, s, Grid)))
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

// IconDIB is the icon at size × size as one icon image (the bytes of an
// RT_ICON resource), for CreateIconFromResourceEx: the main window's
// title-bar and taskbar icons.
func IconDIB(c Color, size int) []byte { return dib(DrawIcon(c, size, Grid)) }

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
