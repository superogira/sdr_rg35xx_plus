package ui

import (
	"bytes"
	"embed"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/png" // decoder for the embedded basemaps
	"io/fs"
	"math"
	"sort"
	"strings"
	"time"
)

//go:embed maps/*.png
var mapFS embed.FS

// mapStyleNames lists every embedded basemap in lexical order. All of
// them are the same 640×480 equirectangular template (verified by
// cmd/mapcheck — coastline edge correlation peaks at offset (0,0)), so
// one projection formula serves every style:
// x = (lon+180)/360·W, y = (90-lat)/180·H.
var mapStyleNames = func() []string {
	names, err := fs.Glob(mapFS, "maps/*.png")
	if err != nil {
		return nil
	}
	sort.Strings(names)
	return names
}()

// Station roles colour the markers: the SENDING station is red, the
// RECEIVING station is green. Zero value = sender so bare literals keep
// the common (CQ / plain QSO) meaning.
const (
	RoleSender = iota
	RoleReceiver
)

// MapEntry describes one FT8 activity marker on the world map.
type MapEntry struct {
	Lat, Lon    float64       // marker position (exact grid or country centroid)
	Arc         bool          // draw a dashed arc from the sender's position
	FromLat     float64       // arc source (sender, red)
	FromLon     float64       // arc source
	Role        int           // RoleSender / RoleReceiver (arc endpoint colour)
	IsCQ        bool          // true = CQ beacon, false = QSO exchange
	Approx      bool          // position is a country-level guess (hollow marker)
	Age         time.Duration // time since decoded
}

// MapSelection is the station picked with Left/Right on the map screen.
// Detail (non-nil) opens the info panel with the station's message
// history; the panel lands on the half of the screen opposite the
// station marker so it never covers it.
type MapSelection struct {
	Call      string
	Lat, Lon  float64 // station position (grid or country centroid)
	Approx    bool
	Grid      string // known Maidenhead grid ("" when only country known)
	Country   string
	Index     int      // 1-based position in the sorted station list
	Total     int      // station count
	Detail    []string // message-history rows; nil = panel closed
}

// CycleMap switches the basemap style (L1 = previous, R1 = next).
func (u *UI) CycleMap(dir int) {
	n := len(mapStyleNames)
	if n == 0 {
		return
	}
	u.mapStyle = ((u.mapStyle + dir) % n + n) % n
	u.mapImg = nil // force re-decode of the new style
}

// MapStyleName returns a short label for the active basemap.
func (u *UI) MapStyleName() string {
	if u.mapStyle < 0 || u.mapStyle >= len(mapStyleNames) {
		return "?"
	}
	n := mapStyleNames[u.mapStyle]
	n = n[len("maps/world_map"):]
	n = n[:len(n)-len(".png")]
	if n == "" {
		return "base"
	}
	return n[1:] // strip the leading '_' / '.'
}

// basemap decodes the active style into an RGBA copy, caching only the
// most recent one (switching styles re-decodes, ~tens of ms — fine for
// a button action, and 21 full-res RGBA copies would be 25 MB).
func (u *UI) basemap() *image.RGBA {
	if u.mapImg != nil && u.mapIdx == u.mapStyle {
		return u.mapImg
	}
	if u.mapStyle < 0 || u.mapStyle >= len(mapStyleNames) {
		return nil
	}
	data, err := mapFS.ReadFile(mapStyleNames[u.mapStyle])
	if err != nil {
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		fmt.Printf("map: basemap decode failed: %v\n", err)
		return nil
	}
	rgba, ok := img.(*image.RGBA)
	if !ok {
		rgba = image.NewRGBA(image.Rect(0, 0, img.Bounds().Dx(), img.Bounds().Dy()))
		draw.Draw(rgba, rgba.Bounds(), img, img.Bounds().Min, draw.Src)
	}
	u.mapImg, u.mapIdx = rgba, u.mapStyle
	return rgba
}

// DrawWorldMap renders the FT8 world map: the active equirectangular
// basemap with red sender / green receiver markers, CQ ripples and
// animated dashed QSO arcs from the last 10 minutes on top. Markers
// whose position is only a country guess draw as hollow rings until the
// real grid is learned.
func (u *UI) DrawWorldMap(entries []MapEntry, sel *MapSelection) {
	if !u.blitWorldMap() {
		// Decode failure fallback: plain dark background.
		u.fillBlend(0, 0, u.W, u.H, 15, 17, 23, 255)
	}

	// Travelling-dash phase in [0, 24): 1 px per 30 ms, wrapping exactly
	// at the pattern period (24 px × 30 ms = 720 ms) so the animation
	// never jumps — the old %12000 ms wrap left 400 mod 24 = 16 px of
	// backwards skip every 12 s, which read as a fresh long yellow bar
	// charging out of the sender.
	phase := float64(time.Now().UnixMilli()%720) / 30.0

	for _, e := range entries {
		x, y := u.latLonToScreen(e.Lat, e.Lon)

		// Linear fade across the whole 10-minute window.
		fade := 1.0 - (float64(e.Age) / (10 * float64(time.Minute)))
		if fade < 0 {
			fade = 0
		}

		if e.Arc {
			x1, y1 := u.latLonToScreen(e.FromLat, e.FromLon)
			u.drawArc(x1, y1, x, y, fade, phase)
			u.marker(x1, y1, fade*0.7, false, RoleSender)
			u.marker(x, y, fade, e.Approx, e.Role)
		} else if e.IsCQ {
			// CQ: expanding ripple (the rings self-cap at 55 px, so after
			// ~7 s the marker settles to a slowly fading dot).
			radius := float64(e.Age) / float64(time.Second) * 8 // 8px/second expansion
			if e.Approx {
				u.marker(x, y, fade, true, RoleSender)
			} else {
				u.drawRipple(x, y, radius, fade)
			}
		} else {
			u.marker(x, y, fade, e.Approx, e.Role)
		}
	}

	// Selection highlight on top of the markers.
	if sel != nil {
		x, y := u.latLonToScreen(sel.Lat, sel.Lon)
		u.drawCircle(x, y, 8, color.RGBA{255, 255, 255, 230})
		u.drawCircle(x, y, 9, color.RGBA{0, 0, 0, 160})
		// Callsign chip above the marker.
		cf := Face(12, true)
		cw := cf.TextWidth(sel.Call)
		cx := x - cw/2 - 5
		if cx < 2 {
			cx = 2
		}
		cy := y - 26
		if cy < 30 {
			cy = y + 12
		}
		u.fillBlend(cx, cy, cw+10, 17, 0, 0, 0, 200)
		cf.DrawString(u.img, color.RGBA{255, 255, 255, 255}, cx+5, cy+13, sel.Call)
	}

	// Title chip.
	u.fillBlend(8, 8, 220, 26, 0, 0, 0, 170)
	tf := Face(13, false)
	tf.DrawString(u.img, color.RGBA{255, 255, 255, 255}, 16, 26, "FT8 World Map - 10min")

	// Active basemap chip (top-right).
	mn := fmt.Sprintf("map %d/%d %s", u.mapStyle+1, len(mapStyleNames), u.MapStyleName())
	mw := tf.TextWidth(mn)
	u.fillBlend(u.W-mw-24, 8, mw+16, 26, 0, 0, 0, 170)
	tf.DrawString(u.img, color.RGBA{200, 220, 255, 255}, u.W-mw-16, 26, mn)

	// Legend chip (below the title): sender / receiver / approximate.
	// The markers are drawn graphically — the font has no circle glyphs.
	u.fillBlend(8, 38, 210, 20, 0, 0, 0, 150)
	lf := Face(10, false)
	u.marker(18, 47, 1.0, false, RoleSender)
	lf.DrawString(u.img, color.RGBA{220, 220, 220, 255}, 28, 52, "TX")
	u.marker(52, 47, 1.0, false, RoleReceiver)
	lf.DrawString(u.img, color.RGBA{220, 220, 220, 255}, 62, 52, "RX")
	u.marker(86, 47, 1.0, true, RoleSender)
	lf.DrawString(u.img, color.RGBA{220, 220, 220, 255}, 96, 52, "approx")

	// Station counter + hints (bottom, clear of the map action).
	hf := Face(11, false)
	if sel != nil {
		cnt := fmt.Sprintf("%s  %d/%d", sel.Call, sel.Index, sel.Total)
		cw := hf.TextWidth(cnt)
		u.fillBlend(8, u.H-30, cw+16, 22, 0, 0, 0, 170)
		hf.DrawString(u.img, color.RGBA{120, 255, 120, 255}, 16, u.H-13, cnt)
	}
	hint := "L/R station  A info  B close"
	hw := hf.TextWidth(hint)
	u.fillBlend(u.W-hw-24, u.H-30, hw+16, 22, 0, 0, 0, 170)
	hf.DrawString(u.img, color.RGBA{220, 220, 220, 255}, u.W-hw-16, u.H-13, hint)
	styleHint := "L1/R1 map style"
	sw := hf.TextWidth(styleHint)
	sx := u.W - hw - 24 - sw - 16
	if sx > 4 {
		u.fillBlend(sx, u.H-30, sw+16, 22, 0, 0, 0, 170)
		hf.DrawString(u.img, color.RGBA{220, 220, 220, 255}, sx+8, u.H-13, styleHint)
	}

	// Detail panel last, over everything.
	if sel != nil && len(sel.Detail) > 0 {
		u.drawMapDetail(sel)
	}
}

// drawMapDetail renders the station info panel on the half of the
// screen opposite the station marker.
func (u *UI) drawMapDetail(sel *MapSelection) {
	x, _ := u.latLonToScreen(sel.Lat, sel.Lon)

	pw := u.W/2 - 12 // panel width
	px := 6          // station on the right half → panel on the left
	if x <= u.W/2 {
		px = u.W/2 + 6 // station on the left → panel on the right
	}
	py := 64 // below title + legend
	ph := u.H - py - 40

	// Panel background + border.
	u.fillBlend(px, py, pw, ph, 8, 10, 14, 235)
	for d := 0; d < pw; d++ {
		u.setPixel(px+d, py, color.RGBA{90, 120, 160, 255})
		u.setPixel(px+d, py+ph-1, color.RGBA{90, 120, 160, 255})
	}
	for d := 0; d < ph; d++ {
		u.setPixel(px, py+d, color.RGBA{90, 120, 160, 255})
		u.setPixel(px+pw-1, py+d, color.RGBA{90, 120, 160, 255})
	}

	txf := px + 10
	ty := py + 22
	// Callsign header.
	hf := Face(16, true)
	hf.DrawString(u.img, color.RGBA{255, 255, 255, 255}, txf, ty, sel.Call)
	ty += 20

	sf := Face(11, false)
	g := sel.Grid
	if g == "" {
		g = "- (country approx)"
	}
	sf.DrawString(u.img, color.RGBA{200, 200, 200, 255}, txf, ty, "Grid: "+g)
	ty += 16
	if sel.Country != "" {
		sf.DrawString(u.img, color.RGBA{200, 200, 200, 255}, txf, ty, sel.Country)
		ty += 16
	}
	// Separator.
	for d := 0; d < pw-20; d++ {
		u.setPixel(txf+d, ty, color.RGBA{90, 120, 160, 200})
	}
	ty += 8

	// Message history: newest at the bottom (chat style). Show as many
	// as fit; the caller pre-sorts and pre-truncates the rows.
	rowH := 15
	maxRows := (py + ph - 12 - ty) / rowH
	rows := sel.Detail
	if len(rows) > maxRows {
		rows = rows[len(rows)-maxRows:]
	}
	for _, ln := range rows {
		// Message rows ("HH:MM:SS > text") carry the same colouring as
		// the FT8 decode window, classified on the message part alone.
		col := color.RGBA{230, 230, 230, 255}
		msg := ""
		if i := strings.Index(ln, " > "); i >= 0 {
			msg = ln[i+3:]
		} else if i := strings.Index(ln, " < "); i >= 0 {
			msg = ln[i+3:]
		}
		if msg != "" {
			col = ft8TextColor(msg)
		}
		sf.DrawString(u.img, col, txf, ty, ln)
		ty += rowH
	}
}

// blitWorldMap copies the active basemap over the whole frame, scaling
// if the screen size differs from the image (in practice 1:1 — both
// 640×480).
func (u *UI) blitWorldMap() bool {
	m := u.basemap()
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
// — matches every embedded basemap's projection).
func (u *UI) latLonToScreen(lat, lon float64) (int, int) {
	x := int((lon + 180) * float64(u.W) / 360)
	y := int((90 - lat) * float64(u.H) / 180)
	return x, y
}

// roleColour returns (core, rim) for a station role.
func roleColour(role int) (color.RGBA, color.RGBA) {
	if role == RoleReceiver {
		return color.RGBA{0, 255, 0, 255}, color.RGBA{0, 60, 0, 255}
	}
	return color.RGBA{255, 0, 0, 255}, color.RGBA{70, 0, 0, 255}
}

// marker draws a station position: a filled dot when the grid is known,
// a hollow ring when the position is only a country guess (it moves to
// the exact spot once the station is heard with a grid).
func (u *UI) marker(x, y int, fade float64, approx bool, role int) {
	if approx {
		u.drawRing(x, y, fade, role)
		return
	}
	u.drawDot(x, y, fade, role)
}

// drawRing draws a hollow circle (country-level approximate position),
// tinted by the station role. Alpha follows the age fade (1.0 → 0 over
// the 10-minute window).
func (u *UI) drawRing(cx, cy int, fade float64, role int) {
	alpha := uint8(fade * 255)
	core, _ := roleColour(role)
	rim := color.RGBA{core.R / 3, core.G / 3, core.B / 3, alpha}
	core.A = alpha
	// Double ring for weight.
	u.drawCircle(cx, cy, 4, core)
	u.drawCircle(cx, cy, 3, rim)
}

// drawRipple draws an expanding circle (CQ beacon indicator).
func (u *UI) drawRipple(cx, cy int, radius, fade float64) {
	if radius < 1 {
		radius = 1
	}
	alpha := uint8(fade * 255)

	// Three concentric rings trailing the wavefront.
	for _, r := range []float64{radius, radius + 4, radius + 8} {
		if r > 55 {
			continue
		}
		u.drawCircle(cx, cy, r, color.RGBA{34, 211, 238, alpha})
	}

	u.drawDot(cx, cy, fade, RoleSender)
}

// arcPoints samples a quadratic Bézier from (x1,y1) to (x2,y2) that
// bows towards the top of the screen (north), rising higher the longer
// the link — the classic long-path radio look. The control point sits
// on the chord's perpendicular at height h = 22% of the distance
// (capped at 140 px and clamped so the arc's apex stays on screen; a
// quadratic's apex reaches h/2 above the chord midpoint).
func arcPoints(x1, y1, x2, y2 int) []image.Point {
	fx1, fy1 := float64(x1), float64(y1)
	fx2, fy2 := float64(x2), float64(y2)
	dx, dy := fx2-fx1, fy2-fy1
	length := math.Hypot(dx, dy)
	if length < 2 {
		return []image.Point{{x1, y1}, {x2, y2}}
	}

	nx, ny := -dy/length, dx/length // unit perpendicular
	if ny > 0 {                     // prefer bowing "up"
		nx, ny = -nx, -ny
	}
	h := 0.22 * length
	if h > 140 {
		h = 140
	}
	if ny < 0 { // apex (midpoint − h/2·|ny|) must stay on screen
		if apex := (fy1+fy2)/2 + ny*h/2; apex < 4 {
			h = 2 * ((fy1+fy2)/2 - 4) / -ny
		}
	}
	cx := (fx1+fx2)/2 + nx*h
	cy := (fy1+fy2)/2 + ny*h

	steps := int(length)
	if steps < 8 {
		steps = 8
	}
	pts := make([]image.Point, 0, steps+1)
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		mt := 1 - t
		bx := mt*mt*fx1 + 2*mt*t*cx + t*t*fx2
		by := mt*mt*fy1 + 2*mt*t*cy + t*t*fy2
		pts = append(pts, image.Point{int(bx), int(by)})
	}
	return pts
}

// drawArc draws a curved dashed link between two points (QSO
// exchange). The arc bows north, higher for longer distances. The
// dashes travel from (x1,y1) — the sender — towards (x2,y2) and
// ALTERNATE COLOURS every dash (yellow, orange, yellow, …) so the
// direction of travel stays readable even where arcs overlap: the
// colour sequence orders the dashes along the path. Pattern period is
// 24 px of arc length: 6 on (yellow), 6 off, 6 on (orange), 6 off;
// phase advances the pattern forward. dist+phase is always ≥ 0, so
// math.Mod never returns the negative values that used to paint a
// solid yellow bar over the first stretch of the arc.
func (u *UI) drawArc(x1, y1, x2, y2 int, fade, phase float64) {
	yellow := color.RGBA{255, 223, 89, uint8(fade * 255)}
	orange := color.RGBA{255, 140, 0, uint8(fade * 255)}

	pts := arcPoints(x1, y1, x2, y2)
	dist := 0.0 // arc length travelled so far
	px, py := float64(pts[0].X), float64(pts[0].Y)
	for _, p := range pts {
		dist += math.Hypot(float64(p.X)-px, float64(p.Y)-py)
		px, py = float64(p.X), float64(p.Y)
		m := math.Mod(dist+phase, 24)
		switch {
		case m < 6:
			u.setPixel(p.X, p.Y, yellow)
		case m >= 12 && m < 18:
			u.setPixel(p.X, p.Y, orange)
		}
	}
}

// drawDot draws a small filled circle with a dark rim so it reads on
// both ocean and land colours; the core colour encodes the role. Alpha
// follows the age fade (1.0 → 0 over the 10-minute window).
func (u *UI) drawDot(cx, cy int, fade float64, role int) {
	alpha := uint8(fade * 255)
	core, rim := roleColour(role)
	core.A = alpha
	rim.A = alpha
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

// setPixel sets a single pixel if within bounds. Semi-transparent
// colours blend with what is already there — this is what makes the
// age fade (alpha 1.0 → 0 over the 10-minute window) visible: the
// markers and arcs dissolve into the basemap as they age.
func (u *UI) setPixel(x, y int, c color.RGBA) {
	if x < 0 || x >= u.W || y < 0 || y >= u.H {
		return
	}
	o := y*u.img.Stride + x*4
	if c.A == 255 {
		u.img.Pix[o+0] = c.R
		u.img.Pix[o+1] = c.G
		u.img.Pix[o+2] = c.B
	} else {
		a := float64(c.A) / 255.0
		ia := 1.0 - a
		u.img.Pix[o+0] = uint8(float64(c.R)*a + float64(u.img.Pix[o+0])*ia)
		u.img.Pix[o+1] = uint8(float64(c.G)*a + float64(u.img.Pix[o+1])*ia)
		u.img.Pix[o+2] = uint8(float64(c.B)*a + float64(u.img.Pix[o+2])*ia)
	}
	u.img.Pix[o+3] = 255
}
