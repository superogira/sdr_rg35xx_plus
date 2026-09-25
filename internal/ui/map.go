package ui

import (
	"bytes"
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/png" // decoder for the embedded basemap
	"math"
	"os"
	"sync"
	"time"
)

//go:embed world_map.png
var worldMapPNG []byte

var (
	worldMapOnce sync.Once
	worldMapImg  *image.RGBA
)

// worldMap decodes the embedded 640×480 equirectangular basemap into an
// RGBA copy ready for blitting. Calibration verified against landmark
// pixels: x = (lon+180)/360·W, y = (90-lat)/180·H.
func worldMap() *image.RGBA {
	worldMapOnce.Do(func() {
		img, _, err := image.Decode(bytes.NewReader(worldMapPNG))
		if err != nil {
			fmt.Fprintf(os.Stderr, "map: basemap decode failed: %v\n", err)
			return
		}
		if rgba, ok := img.(*image.RGBA); ok {
			worldMapImg = rgba
			return
		}
		rgba := image.NewRGBA(image.Rect(0, 0, img.Bounds().Dx(), img.Bounds().Dy()))
		draw.Draw(rgba, rgba.Bounds(), img, img.Bounds().Min, draw.Src)
		worldMapImg = rgba
	})
	return worldMapImg
}

// MapEntry describes one FT8 activity marker on the world map.
type MapEntry struct {
	Lat, Lon    float64       // marker position (exact grid or country centroid)
	Arc         bool          // draw a dashed arc from the sender's position
	FromLat     float64       // arc source
	FromLon     float64       // arc source
	IsCQ        bool          // true = CQ beacon, false = QSO exchange
	Approx      bool          // position is a country-level guess (hollow marker)
	Age         time.Duration // time since decoded
}

// DrawWorldMap renders the FT8 world map: photographic equirectangular
// basemap (blitted full-screen — it is 640×480, exactly the framebuffer)
// with CQ ripples and QSO arcs from the last 10 minutes on top. Markers
// whose position is only a country guess draw as hollow rings until the
// real grid is learned.
func (u *UI) DrawWorldMap(entries []MapEntry) {
	if !u.blitWorldMap() {
		// Decode failure fallback: plain dark background.
		u.fillBlend(0, 0, u.W, u.H, 15, 17, 23, 255)
	}

	// Draw activity markers.
	for _, e := range entries {
		x, y := u.latLonToScreen(e.Lat, e.Lon)

		// Linear fade across the whole 10-minute window.
		fade := 1.0 - (float64(e.Age) / (10 * float64(time.Minute)))
		if fade < 0 {
			fade = 0
		}

		if e.Arc {
			x1, y1 := u.latLonToScreen(e.FromLat, e.FromLon)
			u.drawArc(x1, y1, x, y, fade)
			u.marker(x, y, fade, e.Approx)
			u.marker(x1, y1, fade*0.7, false)
		} else if e.IsCQ {
			// CQ: expanding ripple (the rings self-cap at 55 px, so after
			// ~7 s the marker settles to a slowly fading dot).
			radius := float64(e.Age) / float64(time.Second) * 8 // 8px/second expansion
			if e.Approx {
				u.marker(x, y, fade, true)
			} else {
				u.drawRipple(x, y, radius, fade)
			}
		} else {
			u.marker(x, y, fade, e.Approx)
		}
	}

	// Title chip.
	u.fillBlend(8, 8, 220, 26, 0, 0, 0, 170)
	tf := Face(13, false)
	tf.DrawString(u.img, color.RGBA{255, 255, 255, 255}, 16, 26, "FT8 World Map - 10min")

	// Legend chip (below the title): exact vs approximate positions.
	// The markers are drawn graphically — the font has no circle glyphs.
	u.fillBlend(8, 38, 190, 20, 0, 0, 0, 150)
	u.marker(18, 47, 1.0, false)
	lf := Face(10, false)
	lf.DrawString(u.img, color.RGBA{220, 220, 220, 255}, 28, 52, "grid")
	u.marker(66, 47, 1.0, true)
	lf.DrawString(u.img, color.RGBA{220, 220, 220, 255}, 76, 52, "country (approx)")

	// Hint chip (bottom-right, clear of the map action).
	hint := "B close"
	hf := Face(11, false)
	hw := hf.TextWidth(hint)
	u.fillBlend(u.W-hw-24, u.H-30, hw+16, 22, 0, 0, 0, 170)
	hf.DrawString(u.img, color.RGBA{220, 220, 220, 255}, u.W-hw-16, u.H-13, hint)
}

// blitWorldMap copies the basemap over the whole frame, scaling if the
// screen size differs from the image (in practice 1:1 — both 640×480).
func (u *UI) blitWorldMap() bool {
	m := worldMap()
	if m == nil {
		return false
	}
	mw, mh := m.Bounds().Dx(), m.Bounds().Dy()
	if mw == u.W && mh == u.H && m.Stride == u.img.Stride {
		// Fast path: whole rows at once.
		copy(u.img.Pix, m.Pix)
		return true
	}
	for y := 0; y < u.H; y++ {
		sy := y * mh / u.H
		dp := y * u.img.Stride
		sp := sy * m.Stride
		for x := 0; x < u.W; x++ {
			sx := x * mw / u.W
			o := dp + x*4
			s := sp + sx*4
			u.img.Pix[o+0] = m.Pix[s+0]
			u.img.Pix[o+1] = m.Pix[s+1]
			u.img.Pix[o+2] = m.Pix[s+2]
			u.img.Pix[o+3] = 255
		}
	}
	return true
}

// latLonToScreen converts lat/lon to screen coordinates (equirectangular
// — matches the embedded basemap's projection).
func (u *UI) latLonToScreen(lat, lon float64) (int, int) {
	x := int((lon + 180) * float64(u.W) / 360)
	y := int((90 - lat) * float64(u.H) / 180)
	return x, y
}

// marker draws a station position: a filled amber dot when the grid is
// known, a hollow ring when the position is only a country guess (it
// moves to the exact spot once the station is heard with a grid).
func (u *UI) marker(x, y int, fade float64, approx bool) {
	if approx {
		u.drawRing(x, y, fade)
		return
	}
	u.drawDot(x, y, fade)
}

// drawRing draws a hollow circle (country-level approximate position).
func (u *UI) drawRing(cx, cy int, fade float64) {
	alpha := uint8(fade * 230)
	if alpha < 40 {
		alpha = 40
	}
	// Double ring for weight.
	u.drawCircle(cx, cy, 4, color.RGBA{255, 200, 100, alpha})
	u.drawCircle(cx, cy, 3, color.RGBA{60, 30, 0, alpha})
}

// drawRipple draws an expanding circle (CQ beacon indicator).
func (u *UI) drawRipple(cx, cy int, radius, fade float64) {
	if radius < 1 {
		radius = 1
	}
	alpha := uint8(fade * 220)
	if alpha < 25 {
		alpha = 25
	}

	// Three concentric rings trailing the wavefront.
	for _, r := range []float64{radius, radius + 4, radius + 8} {
		if r > 55 {
			continue
		}
		u.drawCircle(cx, cy, r, color.RGBA{34, 211, 238, alpha})
	}

	u.drawDot(cx, cy, fade)
}

// drawArc draws a dashed line between two points (QSO exchange).
func (u *UI) drawArc(x1, y1, x2, y2 int, fade float64) {
	alpha := uint8(fade * 230)
	if alpha < 35 {
		alpha = 35
	}
	c := color.RGBA{255, 223, 89, alpha}

	dx := x2 - x1
	dy := y2 - y1
	steps := int(math.Sqrt(float64(dx*dx + dy*dy)))
	if steps < 2 {
		steps = 2
	}

	// Dash pattern: 6px on, 6px off.
	for i := 0; i < steps; i += 6 {
		if i+3 > steps {
			break
		}
		t1 := float64(i) / float64(steps)
		t2 := float64(i+3) / float64(steps)
		sx := x1 + int(float64(dx)*t1)
		sy := y1 + int(float64(dy)*t1)
		ex := x1 + int(float64(dx)*t2)
		ey := y1 + int(float64(dy)*t2)
		u.drawLine(sx, sy, ex, ey, c)
	}

	u.drawDot(x1, y1, fade*0.7)
	u.drawDot(x2, y2, fade)
}

// drawDot draws a small filled circle with a dark rim so it reads on
// both ocean and land colours.
func (u *UI) drawDot(cx, cy int, fade float64) {
	alpha := uint8(fade * 255)
	if alpha < 45 {
		alpha = 45
	}
	rim := color.RGBA{0, 0, 0, alpha}
	core := color.RGBA{255, 170, 60, alpha}
	for dy := -3; dy <= 3; dy++ {
		for dx := -3; dx <= 3; dx++ {
			d := dx*dx + dy*dy
			if d > 9 {
				continue
			}
			if d >= 5 {
				u.setPixel(cx+dx, cy+dy, rim)
			} else {
				u.setPixel(cx+dx, cy+dy, core)
			}
		}
	}
}

// drawCircle draws a circle outline.
func (u *UI) drawCircle(cx, cy int, radius float64, c color.RGBA) {
	r := int(radius)
	if r < 1 {
		r = 1
	}

	x, y := 0, r
	d := 3 - 2*r

	for x <= y {
		u.setPixel(cx+x, cy+y, c)
		u.setPixel(cx-x, cy+y, c)
		u.setPixel(cx+x, cy-y, c)
		u.setPixel(cx-x, cy-y, c)
		u.setPixel(cx+y, cy+x, c)
		u.setPixel(cx-y, cy+x, c)
		u.setPixel(cx+y, cy-x, c)
		u.setPixel(cx-y, cy-x, c)

		if d < 0 {
			d += 4*x + 6
		} else {
			d += 4*(x-y) + 10
			y--
		}
		x++
	}
}

// drawLine draws a line between two points (Bresenham).
func (u *UI) drawLine(x1, y1, x2, y2 int, c color.RGBA) {
	dx := x2 - x1
	if dx < 0 {
		dx = -dx
	}
	dy := y2 - y1
	if dy < 0 {
		dy = -dy
	}

	sx := 1
	if x1 > x2 {
		sx = -1
	}
	sy := 1
	if y1 > y2 {
		sy = -1
	}

	err := dx - dy
	x, y := x1, y1

	for {
		u.setPixel(x, y, c)
		if x == x2 && y == y2 {
			break
		}
		e2 := 2 * err
		if e2 > -dy {
			err -= dy
			x += sx
		}
		if e2 < dx {
			err += dx
			y += sy
		}
	}
}

// setPixel sets a single pixel if within bounds.
func (u *UI) setPixel(x, y int, c color.RGBA) {
	if x < 0 || x >= u.W || y < 0 || y >= u.H {
		return
	}
	o := y*u.img.Stride + x*4
	u.img.Pix[o+0] = c.R
	u.img.Pix[o+1] = c.G
	u.img.Pix[o+2] = c.B
	u.img.Pix[o+3] = 255
}
