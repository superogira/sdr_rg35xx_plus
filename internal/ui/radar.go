// Radar screen: classic green phosphor ATC display — the receiver sits
// at the centre (no basemap yet), range rings every quarter of the
// scale, aircraft as blips with callsign + flight level.
package ui

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"sort"
	"time"

	"sdr35/internal/i18n"
)

// RadarDot is one breadcrumb of an aircraft's past track, positioned
// relative to the receiver like the blip itself.
type RadarDot struct {
	BrngDeg      float64
	DistKm       float64
	AgeSec       int
	AltFt        int     // per-point altitude; -1 = unknown (age-tinted green)
	MercX, MercY float64 // Web-Mercator world pixels of the breadcrumb
}

// MercView carries what the radar needs to project targets onto the
// Web-Mercator basemap: the receiver's world-pixel position and the
// metres-per-pixel scale there. Positions then land EXACTLY on the
// tiles (screen = mercPx − rx + centre), so ships sit on the river
// and aircraft on their true ground track. The old polar
// (bearing+distance) projection silently disagreed with Mercator —
// ~3% stretch at Thai latitudes pushed river traffic onto land.
type MercView struct {
	Zoom   int
	Rx, Ry float64 // receiver world pixels at Zoom
	MPerPx float64 // ground metres per world pixel at the receiver latitude
}

// project returns the screen offset from the radar centre for a target
// at Mercator world pixels (mx, my) — computed at the SAME zoom as the
// receiver reference, so a world-pixel difference IS the offset on the
// mosaic tiles. The sign convention matches osm.Mosaic exactly
// (screen = worldPx − receiverPx + centre, y down), so a target north
// of the receiver draws ABOVE the centre.
func (m *MercView) project(mx, my float64) (int, int) {
	return int(mx - m.Rx), int(my - m.Ry)
}

// pxPerKm is the map-true pixels per ground kilometre — drives the
// range rings and the Phosphor fallback scale.
func (m *MercView) pxPerKm() float64 { return 1000 / m.MPerPx }

// RadarBlip is one tracked object (aircraft or vessel) positioned
// relative to the receiver.
type RadarBlip struct {
	Vessel       bool    // true = ship (AIS), false = aircraft (ADS-B)
	AtoN         bool    // AIS type 6/21 static aid: yellow rhombus
	MercX, MercY float64 // Web-Mercator world pixels (see MercView)
	Call         string
	ICAO         string
	Country      string // ISO 3166-1 alpha-2 country code
	BrngDeg      float64
	DistKm       float64
	TrackDeg     int
	AltFt        int
	SpdKt        int
	SogKt        float64
	VrateFpm     int
	HasPos       bool
	Trail        []RadarDot
	Seen         time.Time // last position update; newest draws on top
}

// Radar label display modes (A cycles them).
const (
	LabelFlagText = 0 // flag + name/reg text (normal)
	LabelFlagOnly = 1 // flag only, text hidden
	LabelNone     = 2 // no flag, no text — bare targets
)

// DrawRadar renders the ADS-B radar. Targets beyond rangeKm are not
// drawn at all — they appear when the user zooms out to their range.
func (u *UI) DrawRadar(blips []RadarBlip, rangeKm float64, host string, connected bool, rxLat, rxLon float64, cpuPct float64, basemap *image.RGBA, mapName, mapAttr string, merc *MercView, flagDir string, labelMode int) {
	// Phosphor palette.
	bg := color.RGBA{2, 10, 4, 255}
	dim := color.RGBA{0, 110, 55, 255}
	green := color.RGBA{0, 230, 120, 255}
	bright := color.RGBA{140, 255, 180, 255}
	if basemap != nil {
		for y := 0; y < u.H; y++ {
			src := y * basemap.Stride
			dst := y * u.img.Stride
			copy(u.img.Pix[dst:dst+u.W*4], basemap.Pix[src:src+u.W*4])
		}
	} else {
		u.fillBlend(0, 0, u.W, u.H, bg.R, bg.G, bg.B, 255)
	}

	cx, cy := u.W/2, u.H/2+6
	maxR := float64(u.H)/2 - 46
	if float64(u.W)/2-16 < maxR {
		maxR = float64(u.W)/2 - 16
	}

	// Ring scale: map-true pixels per km when a basemap is shown
	// (rings then sit on the geography at their real distance); polar
	// screen fraction on the phosphor fallback.
	pxPerKm := maxR / rangeKm
	if merc != nil {
		pxPerKm = merc.pxPerKm()
	}
	// Map mode rings are TRUE kilometres at a round step; phosphor
	// keeps the quartered screen rings.
	ringStep := rangeKm / 4
	if merc != nil {
		ringStep = niceStep(rangeKm / 4)
	}

	// Range rings + crosshair. On a map background the rings blend at
	// ~50% so the geography stays readable; on the dark fallback they
	// draw solid as before.
	for k := 1; ; k++ {
		r := ringStep * float64(k) * pxPerKm
		if r > maxR {
			break
		}
		for a2 := 0.0; a2 < 360; a2 += 0.5 {
			px := cx + int(r*math.Sin(a2*math.Pi/180))
			py := cy - int(r*math.Cos(a2*math.Pi/180))
			if basemap != nil {
				u.blendPx(px, py, 0, 230, 120, 120)
			} else {
				u.setPixel(px, py, dim)
			}
		}
	}
	u.drawCircle(cx, cy, 2, green)
	cross := color.RGBA{0, 60, 30, 255}
	if basemap != nil {
		cross = color.RGBA{0, 200, 100, 90}
	}
	for y := 0; y < u.H; y++ {
		u.setPixel(cx, y, cross)
	}
	for x := 0; x < u.W; x++ {
		u.setPixel(x, cy, cross)
	}

	// Ring distance labels.
	lf := Face(10, false)
	for k := 1; ; k++ {
		d := ringStep * float64(k)
		r := d * pxPerKm
		if r > maxR {
			break
		}
		label := fmt.Sprintf("%.0f", d)
		if d >= rangeKm-0.5 {
			label = fmt.Sprintf("%.0f km", d)
		}
		lf.DrawString(u.img, dim, cx+4, cy-int(r)+10, label)
	}

	// Blips.
	tf := Face(11, true)
	for _, b := range blips {
		if !b.HasPos {
			continue
		}
		// Projection: on a Mercator basemap the target's world pixels
		// land EXACTLY on the tiles; on the phosphor fallback the polar
		// (bearing+distance) rings apply.
		var x, y int
		if merc != nil {
			dx, dy := merc.project(b.MercX, b.MercY)
			if math.Hypot(float64(dx), float64(dy)) > maxR {
				continue // beyond the selected range: wait for zoom-out
			}
			x, y = cx+dx, cy+dy
		} else {
			r := b.DistKm / rangeKm * maxR
			if r > maxR {
				continue // beyond the selected range: wait for zoom-out
			}
			ang := b.BrngDeg * 3.14159265 / 180
			x = cx + int(r*math.Sin(ang))
			y = cy - int(r*math.Cos(ang))
		}
		col := green
		// Flown track: breadcrumbs every 5 s of flight within a
		// five-minute window. Every dot is a full 2x2 body — 1-px dots
		// proved invisible on the handheld screen.
		for _, d := range b.Trail {
			var dx, dy int
			if merc != nil {
				dx, dy = merc.project(d.MercX, d.MercY)
				if math.Hypot(float64(dx), float64(dy)) > maxR {
					continue // off-scope history stays off
				}
			} else {
				tr := d.DistKm / rangeKm * maxR
				if tr > maxR {
					continue // off-scope history stays off
				}
				ta := d.BrngDeg * math.Pi / 180
				dx = cx + int(tr*math.Sin(ta)) - cx
				dy = cy - int(tr*math.Cos(ta)) - cy
			}
			sx2 := cx + dx
			sy2 := cy + dy
			// Aircraft breadcrumbs paint in the same altitude colours
			// as the labels (tar1090 convention); points without
			// altitude (or vessel history) fall back to age-tinted
			// green.
			var tc color.RGBA
			if !b.Vessel && d.AltFt >= 0 {
				tc = altitudeColor(d.AltFt)
			} else {
				tc = color.RGBA{0, 255, 130, 255} // recent (< 1 min)
				if d.AgeSec > 150 {
					tc = color.RGBA{0, 150, 75, 255} // old
				} else if d.AgeSec > 60 {
					tc = color.RGBA{0, 200, 100, 255}
				}
			}
			u.setPixel(sx2, sy2, tc)
			if rangeKm < 100 {
				// Zoomed in: 2x2 bodies. At 100 km and beyond the 5-minute
				// trail gets dense — single pixels keep the picture clean.
				u.setPixel(sx2+1, sy2, tc)
				u.setPixel(sx2, sy2+1, tc)
				u.setPixel(sx2+1, sy2+1, tc)
			}
		}

		if b.Vessel {
			// Vessel: cyan square + name/MMSI + SOG. Static aids (AIS
			// base stations, buoys, lighthouses — types 6/21) draw as a
			// yellow RHOMBUS so they never read as traffic.
			if b.AtoN {
				col := color.RGBA{255, 215, 0, 255}
				for _, d := range [][2]int{{-4, 0}, {-3, -1}, {-3, 1}, {-2, -2}, {-2, 2}, {-1, -2}, {-1, 2}, {0, -2}, {0, 0}, {0, 2}, {1, -2}, {1, 2}, {2, -1}, {2, 1}, {3, 0}} {
					u.setPixel(x+d[0], y+d[1], col)
				}
			} else {
				for dy := -2; dy <= 2; dy++ {
					for dx := -2; dx <= 2; dx++ {
						u.setPixel(x+dx, y+dy, color.RGBA{0, 220, 220, 255})
					}
				}
			}
			call := b.Call
			if call == "" {
				call = b.ICAO
			}
			label := call
			if b.SogKt > 0.5 && !b.AtoN {
				label = fmt.Sprintf("%s %.1fkt", call, b.SogKt)
			}
			var flag image.Image
			fw := 0
			if b.Country != "" && labelMode != LabelNone {
				flag = GetFlag(b.Country, flagDir)
				if flag != nil {
					fw = flag.Bounds().Dx() + 3
				}
			}
			tx := x + 6
			if x > u.W-120 {
				tx = x - 6 - tf.TextWidth(label) - fw
			}
			if flag != nil {
				tx += fw // flag leads the label
			}
			if labelMode == LabelNone {
				continue
			}
			ty := y + 4
			if y < 60 {
				ty = y + 14
			}
			// Unconditional: the chip also dims any older label this one
			// lands on (labels sort oldest-first, newest on top).
			u.fillBlend(tx-3, ty-11, tf.TextWidth(label)+fw+6, 13, 0, 0, 0, 170)
			if labelMode == LabelFlagText {
				lcol := color.RGBA{180, 255, 255, 255}
				if b.AtoN {
					lcol = color.RGBA{255, 235, 130, 255}
				}
				tf.DrawString(u.img, lcol, tx, ty, label)
			}
			if flag != nil {
				drawImage(u.img, flag, tx-fw, ty-10)
			}
			continue
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
		var flag image.Image
		fw := 0
		if b.Country != "" && labelMode != LabelNone {
			flag = GetFlag(b.Country, flagDir)
			if flag != nil {
				fw = flag.Bounds().Dx() + 3
			}
		}
		tx := x + 8
		if x > u.W-120 {
			tx = x - 8 - tf.TextWidth(label) - fw
		}
		if flag != nil {
			tx += fw // flag leads the label
		}
		if labelMode == LabelNone {
			continue
		}
		ty := y + 4
		if y < 60 {
			ty = y + 14
		}
		// Unconditional: the chip also dims any older label this one
		// lands on (labels sort oldest-first, newest on top).
		u.fillBlend(tx-3, ty-11, tf.TextWidth(label)+fw+6, 13, 0, 0, 0, 170)
		if labelMode == LabelFlagText {
			tf.DrawString(u.img, altitudeColor(b.AltFt), tx, ty, label)
		}
		if flag != nil {
			drawImage(u.img, flag, tx-fw, ty-10)
		}
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
	// Deterministic z-order: oldest first so the freshest target paints
	// LAST, on top. Blips used to arrive in Go map order, which reshuffles
	// every frame — overlapping labels flickered as they fought for the
	// top spot. Callsign breaks ties so the order never wobbles.
	ordered := make([]RadarBlip, len(blips))
	copy(ordered, blips)
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].Seen.Equal(ordered[j].Seen) {
			return ordered[i].Seen.Before(ordered[j].Seen)
		}
		return ordered[i].Call < ordered[j].Call
	})
	blips = ordered
	n := 0
	for _, b := range blips {
		if b.HasPos {
			n++
		}
	}
	sf.DrawString(u.img, green, 10, 58, fmt.Sprintf(i18n.T("radar_count"), n))
	if mapName != "" {
		sf.DrawString(u.img, green, 10, 74, "MAP "+mapName)
	}

	// Button hints: top-right (clear of the aircraft labels).
	hint := i18n.T("radar_hint")
	hw := sf.TextWidth(hint)
	u.fillBlend(u.W-hw-14, 12, hw+10, 20, 2, 14, 7, 200)
	sf.DrawString(u.img, bright, u.W-hw-9, 27, hint)

	if basemap != nil && mapAttr != "" {
		// Attribution per layer (tile usage policy) — bottom-left.
		sf.DrawString(u.img, dim, 8, u.H-9, mapAttr)
	}

	// Clock stacked above the CPU badge: bottom-right.
	clock := time.Now().Format("15:04:05")
	kw := sf.TextWidth(clock)
	u.fillBlend(u.W-kw-14, u.H-46, kw+10, 20, 2, 14, 7, 200)
	sf.DrawString(u.img, bright, u.W-kw-9, u.H-31, clock)

	// CPU load: bottom-right.
	cpu := fmt.Sprintf("CPU %.0f%%", cpuPct)
	cw := sf.TextWidth(cpu)
	u.fillBlend(u.W-cw-14, u.H-24, cw+10, 20, 2, 14, 7, 200)
	sf.DrawString(u.img, bright, u.W-cw-9, u.H-9, cpu)
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

// blendPx alpha-blends a colour onto one pixel (map-overlay rings).
func (u *UI) blendPx(x, y int, r, g, b uint8, a uint8) {
	if x < 0 || x >= u.W || y < 0 || y >= u.H {
		return
	}
	o := y*u.img.Stride + x*4
	ia := 255 - a
	u.img.Pix[o+0] = uint8((int(r)*int(a) + int(u.img.Pix[o+0])*int(ia)) / 255)
	u.img.Pix[o+1] = uint8((int(g)*int(a) + int(u.img.Pix[o+1])*int(ia)) / 255)
	u.img.Pix[o+2] = uint8((int(b)*int(a) + int(u.img.Pix[o+2])*int(ia)) / 255)
	u.img.Pix[o+3] = 255
}

// niceStep rounds km to the nearest 1-2-5 decade value so ring labels
// read well (e.g. 6.25 → 5, 12.5 → 10, 25 → 20).
func niceStep(km float64) float64 {
	if km <= 0 {
		return 1
	}
	pow := math.Pow(10, math.Floor(math.Log10(km)))
	for _, m := range []float64{1, 2, 5, 10} {
		if m*pow >= km*0.9 {
			return m * pow
		}
	}
	return 10 * pow
}

// altitudeColor returns HSL→RGB color for ADS-B labels, matching tar1090.
func altitudeColor(altFt int) color.RGBA {
	if altFt < 0 {
		altFt = 0
	}
	h := 20.0
	switch {
	case altFt <= 2000:
		h = 20
	case altFt <= 4000:
		h = 32.5 + (43-32.5)*float64(altFt-2000)/2000
	case altFt <= 6000:
		h = 43 + (54-43)*float64(altFt-4000)/2000
	case altFt <= 8000:
		h = 54 + (72-54)*float64(altFt-6000)/2000
	case altFt <= 9000:
		h = 72 + (85-72)*float64(altFt-8000)/1000
	case altFt <= 11000:
		h = 85 + (140-85)*float64(altFt-9000)/2000
	case altFt <= 40000:
		h = 140 + (300-140)*float64(altFt-11000)/29000
	case altFt <= 51000:
		h = 300 + (360-300)*float64(altFt-40000)/11000
	default:
		h = 360
	}
	s, l := 88.0, 50.0
	if h >= 200 {
		l = 58
	} else if h >= 100 {
		l = 41
	} else if h >= 80 {
		l = 41
	} else if h >= 60 {
		l = 43
	} else if h >= 50 {
		l = 46
	} else if h >= 46 {
		l = 51
	} else if h >= 40 {
		l = 52
	} else if h >= 32 {
		l = 54
	} else if h >= 20 {
		l = 50
	} else {
		l = 53
	}
	return hslToRGB(h, s, l)
}

func hslToRGB(h, s, l float64) color.RGBA {
	s /= 100
	l /= 100
	c := (1 - absF64(2*l-1)) * s
	hp := h / 60
	x := c * (1 - absF64(mod(hp, 2)-1))
	var r1, g1, b1 float64
	switch {
	case hp < 1:
		r1, g1, b1 = c, x, 0
	case hp < 2:
		r1, g1, b1 = x, c, 0
	case hp < 3:
		r1, g1, b1 = 0, c, x
	case hp < 4:
		r1, g1, b1 = 0, x, c
	case hp < 5:
		r1, g1, b1 = x, 0, c
	default:
		r1, g1, b1 = c, 0, x
	}
	m := l - c/2
	return color.RGBA{
		uint8((r1 + m) * 255),
		uint8((g1 + m) * 255),
		uint8((b1 + m) * 255),
		255,
	}
}

func absF64(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func mod(a, b float64) float64 {
	r := a - b*float64(int(a/b))
	if r < 0 {
		r += b
	}
	return r
}
