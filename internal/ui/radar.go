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

// RadarBlip is one aircraft positioned relative to the receiver.
type RadarBlip struct {
	Call    string
	ICAO    string
	BrngDeg float64
	DistKm  float64
	AltFt   int
	SpdKt   int
	HasPos  bool
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
		// Blip: filled 3x3 square.
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				u.setPixel(x+dx, y+dy, col)
			}
		}
		// Label: callsign + flight level (hundreds of feet).
		call := b.Call
		if call == "" {
			call = b.ICAO
		}
		label := fmt.Sprintf("%s %d", call, b.AltFt/100)
		if b.SpdKt > 0 {
			label = fmt.Sprintf("%s %d %dkt", call, b.AltFt/100, b.SpdKt)
		}
		lx := x + 6
		if x > u.W-120 {
			lx = x - 6 - tf.TextWidth(label)
		}
		ly := y + 4
		if y < 60 {
			ly = y + 14
		}
		tf.DrawString(u.img, bright, lx, ly, label)
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
