// Radar screen: classic green phosphor ATC display — the receiver sits
// at the centre (no basemap yet), range rings every quarter of the
// scale, aircraft as blips with callsign + flight level.
package ui

import (
	"fmt"
	"image/color"
	"math"

	"sdr35/internal/i18n"
)

// RadarDot is one breadcrumb of an aircraft's past track, positioned
// relative to the receiver like the blip itself.
type RadarDot struct {
	BrngDeg float64
	DistKm  float64
	AgeSec  int
}

// RadarBlip is one aircraft positioned relative to the receiver.
type RadarBlip struct {
	Call     string
	ICAO     string
	BrngDeg  float64
	DistKm   float64
	TrackDeg int
	AltFt    int
	SpdKt    int
	VrateFpm int
	HasPos   bool
	Trail    []RadarDot
}

// DrawRadar renders the ADS-B radar. Blips beyond rangeKm clamp to the
// outer ring at half brightness.
func (u *UI) DrawRadar(blips []RadarBlip, rangeKm float64, host string, connected bool, rxLat, rxLon float64) {
	// Phosphor palette.
	bg := color.RGBA{2, 10, 4, 255}
	dim := color.RGBA{0, 110, 55, 255}
	green := color.RGBA{0, 230, 120, 255}
	bright := color.RGBA{140, 255, 180, 255}
	u.fillBlend(0, 0, u.W, u.H, bg.R, bg.G, bg.B, 255)

	cx, cy := u.W/2, u.H/2+6
	maxR := float64(u.H)/2 - 46
	if float64(u.W)/2-16 < maxR {
		maxR = float64(u.W)/2 - 16
	}

	// Range rings + crosshair.
	for k := 1; k <= 4; k++ {
		u.drawCircle(cx, cy, maxR*float64(k)/4, dim)
	}
	u.drawCircle(cx, cy, 2, green)
	for y := 0; y < u.H; y++ {
		u.setPixel(cx, y, color.RGBA{0, 60, 30, 255})
	}
	for x := 0; x < u.W; x++ {
		u.setPixel(x, cy, color.RGBA{0, 60, 30, 255})
	}

	// Ring distance labels.
	lf := Face(10, false)
	for k := 1; k <= 4; k++ {
		d := rangeKm * float64(k) / 4
		label := fmt.Sprintf("%.0f", d)
		if k == 4 {
			label = fmt.Sprintf("%.0f km", d)
		}
		lf.DrawString(u.img, dim, cx+4, cy-int(maxR*float64(k)/4)+10, label)
	}

	// Blips.
	tf := Face(11, true)
	for _, b := range blips {
		if !b.HasPos {
			continue
		}
		r := b.DistKm / rangeKm * maxR
		clamped := false
		if r > maxR {
			r = maxR
			clamped = true
		}
		ang := b.BrngDeg * 3.14159265 / 180
		x := cx + int(r*math.Sin(ang))
		y := cy - int(r*math.Cos(ang))
		col := green
		if clamped {
			col = dim
		}
		// Flown track: one-minute breadcrumbs, fading with age.
		for _, d := range b.Trail {
			tr := d.DistKm / rangeKm * maxR
			if tr > maxR {
				continue // off-scope history stays off
			}
			ta := d.BrngDeg * math.Pi / 180
			dx := cx + int(tr*math.Sin(ta))
			dy := cy - int(tr*math.Cos(ta))
			tc := color.RGBA{0, 160, 80, 255} // recent
			if d.AgeSec > 40 {
				tc = color.RGBA{0, 80, 40, 255} // old
			} else if d.AgeSec > 20 {
				tc = color.RGBA{0, 120, 60, 255}
			}
			u.setPixel(dx, dy, tc)
			if d.AgeSec <= 20 {
				// Dots fresh enough to matter get a 2x2 body.
				u.setPixel(dx+1, dy, tc)
				u.setPixel(dx, dy+1, tc)
				u.setPixel(dx+1, dy+1, tc)
			}
		}

		// Velocity leader: where the aircraft will be in one minute
		// (knots → km/min: kt·1.852/60), drawn before the triangle so
		// the icon stays on top.
		if b.SpdKt > 0 {
			kmPerMin := float64(b.SpdKt) * 1.852 / 60.0
			lead := kmPerMin / rangeKm * maxR
			if lead > maxR {
				lead = maxR
			}
			rad := float64(b.TrackDeg) * math.Pi / 180
			ex := x + int(lead*math.Sin(rad))
			ey := y - int(lead*math.Cos(rad))
			u.radarLine(x, y, ex, ey, dim)
		}

		// Aircraft icon: a triangle pointing along the track.
		rad := float64(b.TrackDeg) * math.Pi / 180
		dir := func(degOff, r float64) (int, int) {
			a := rad + degOff*math.Pi/180
			return x + int(r*math.Sin(a)), y - int(r*math.Cos(a))
		}
		nx, ny := dir(0, 6)
		lx, ly := dir(150, 5)
		rx, ry := dir(210, 5)
		u.radarLine(nx, ny, lx, ly, col)
		u.radarLine(lx, ly, rx, ry, col)
		u.radarLine(rx, ry, nx, ny, col)
		u.setPixel(x, y, col)
		// Label: callsign + flight level (hundreds of feet).
		call := b.Call
		if call == "" {
			call = b.ICAO
		}
		vr := ""
		if b.VrateFpm > 300 {
			vr = "+" // climbing
		} else if b.VrateFpm < -300 {
			vr = "-" // descending
		}
		label := fmt.Sprintf("%s %dft%s", call, b.AltFt, vr)
		if b.SpdKt > 0 {
			label = fmt.Sprintf("%s %dft%s %dkt", call, b.AltFt, vr, b.SpdKt)
		}
		tx := x + 8
		if x > u.W-120 {
			tx = x - 8 - tf.TextWidth(label)
		}
		ty := y + 4
		if y < 60 {
			ty = y + 14
		}
		tf.DrawString(u.img, bright, tx, ty, label)
	}

	// Header: host + connection + aircraft count.
	state := i18n.T("radar_noconn")
	if connected {
		state = i18n.T("radar_conn")
	}
	hf := Face(12, true)
	hf.DrawString(u.img, bright, 10, 24, fmt.Sprintf("ADS-B  %s", host))
	sf := Face(11, false)
	sf.DrawString(u.img, green, 10, 42, fmt.Sprintf("%s  %d km  RX %.4f %.4f", state, int(rangeKm), rxLat, rxLon))
	n := 0
	for _, b := range blips {
		if b.HasPos {
			n++
		}
	}
	sf.DrawString(u.img, green, 10, 58, fmt.Sprintf("%d aircraft", n))

	// Hint bar.
	hint := "L1/R1 range  B close"
	hw := sf.TextWidth(hint)
	u.fillBlend(u.W-hw-20, u.H-24, hw+14, 20, 0, 20, 8, 180)
	sf.DrawString(u.img, dim, u.W-hw-13, u.H-9, hint)
}

// radarLine draws a Bresenham line (radar-local helper; map.go's
// drawLine was removed when the arcs went per-pixel).
func (u *UI) radarLine(x1, y1, x2, y2 int, c color.RGBA) {
	dx := x2 - x1
	if dx < 0 {
		dx = -dx
	}
	dy := y2 - y1
	if dy < 0 {
		dy = -dy
	}
	sx, sy := 1, 1
	if x1 > x2 {
		sx = -1
	}
	if y1 > y2 {
		sy = -1
	}
	err := dx - dy
	x, y := x1, y1
	for {
		u.setPixel(x, y, c)
		if x == x2 && y == y2 {
			return
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
