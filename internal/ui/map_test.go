package ui

import (
	"image"
	"math"
	"testing"
	"time"

	"sdr35/internal/geo"
)

// findCore scans ±3px for a role-coloured dot core (red sender /
// green receiver).
func findCore(u *UI, x, y int, role int) bool {
	for dy := -3; dy <= 3; dy++ {
		for dx := -3; dx <= 3; dx++ {
			r, g, b, _ := u.img.At(x+dx, y+dy).RGBA()
			if role == RoleReceiver {
				if r>>8 == 0 && g>>8 == 255 && b>>8 == 0 {
					return true
				}
			} else if r>>8 == 255 && g>>8 == 0 && b>>8 == 0 {
				return true
			}
		}
	}
	return false
}

// TestWorldMapBasemapAndMarkers: after DrawWorldMap the basemap must be
// blitted (pixel in the mid-Pacific is NOT the flat fallback colour) and
// a CQ marker for a known grid must land at the calibrated equirectangular
// position (x=(lon+180)/360·W, y=(90-lat)/180·H) with the red sender
// core.
func TestWorldMapBasemapAndMarkers(t *testing.T) {
	u := New(640, 480)

	// Thailand OK04 centre ≈ lat 14.0, lon 100.5.
	lat, lon, ok := geo.GridToLatLon("OK04")
	if !ok {
		t.Fatal("OK04 not a grid")
	}
	wantX := int((lon + 180) * 640 / 360)
	wantY := int((90 - lat) * 480 / 180)

	u.DrawWorldMap([]MapEntry{
		{Lat: lat, Lon: lon, IsCQ: true, Age: 0},
	}, nil)

	// Basemap present: mid-Pacific (lon -140, lat -20) must not be the
	// flat fallback dark-slate (15,17,23).
	px, py := int((-140+180)*640/360), int((90+20)*480/180)
	r, g, b, _ := u.img.At(px, py).RGBA()
	if r>>8 == 15 && g>>8 == 17 && b>>8 == 23 {
		t.Fatalf("pacific pixel (%d,%d) is the fallback background — basemap not blitted", px, py)
	}

	// Marker dot: the red core must appear within 3px of the computed
	// position (dot radius 2 + quantisation).
	if !findCore(u, wantX, wantY, RoleSender) {
		t.Fatalf("no sender core near (%d,%d) for OK04", wantX, wantY)
	}
}

// TestWorldMapArc: a QSO entry with Arc set must draw dashed arc pixels
// along the curved sender→recipient path (north-bowing Bézier), a red
// dot at the sender end and a green dot at the recipient end. The dash
// phase travels with wall time, so the assertion counts samples over
// the whole curve.
func TestWorldMapArc(t *testing.T) {
	u := New(640, 480)
	lat1, lon1, _ := geo.GridToLatLon("OK04") // Thailand (sender)
	lat2, lon2, _ := geo.GridToLatLon("JO65") // Denmark (recipient)
	u.DrawWorldMap([]MapEntry{
		{Lat: lat2, Lon: lon2, Arc: true, Role: RoleReceiver, FromLat: lat1, FromLon: lon1, Age: 0},
	}, nil)

	arc := 0
	orange := 0
	x1, y1 := u.latLonToScreen(lat1, lon1)
	x2, y2 := u.latLonToScreen(lat2, lon2)
	for _, p := range arcPoints(x1, y1, x2, y2) {
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				r, g, b, _ := u.img.At(p.X+dx, p.Y+dy).RGBA()
				if r>>8 == 255 && g>>8 == 223 && b>>8 == 89 {
					arc++
				}
				if r>>8 == 255 && g>>8 == 140 && b>>8 == 0 {
					orange++
				}
			}
		}
	}
	if arc < 8 {
		t.Fatalf("arc from OK04 to JO65 not drawn (only %d yellow samples)", arc)
	}
	if orange < 8 {
		t.Fatalf("alternating orange dashes missing (only %d samples) — direction colour pattern broken", orange)
	}
	if !findCore(u, x1, y1, RoleSender) && !findCoreTolerant(u, x1, y1, RoleSender) {
		t.Fatalf("no red marker at sender end")
	}
	if !findCore(u, x2, y2, RoleReceiver) {
		t.Fatalf("no green marker at recipient end")
	}
}

// findCoreTolerant scans ±3px for a role-tinted pixel: the arc's sender
// end is drawn at 70% fade, which blends with the basemap instead of
// hitting the pure role colour.
func findCoreTolerant(u *UI, x, y int, role int) bool {
	for dy := -3; dy <= 3; dy++ {
		for dx := -3; dx <= 3; dx++ {
			r, g, b, _ := u.img.At(x+dx, y+dy).RGBA()
			R, G, B := int(r>>8), int(g>>8), int(b>>8)
			if role == RoleReceiver {
				if G > 150 && R < 100 && B < 100 {
					return true
				}
			} else if R > 150 && G < 100 && B < 100 {
				return true
			}
		}
	}
	return false
}

// TestArcCurvature: the arc must bow north of the straight chord —
// higher for longer links — and stay clamped on screen.
func TestArcCurvature(t *testing.T) {
	// Short link (Thailand → Japan): mild bow.
	tx, ty := 499, 203 // OK04 screen pos
	jx, jy := 628, 180 // PM95-ish (Japan)
	short := arcPoints(tx, ty, jx, jy)
	shortLift := chordLift(short, tx, ty, jx, jy)

	// Long link (Thailand → JO65): the bow must be clearly higher.
	long := arcPoints(tx, ty, 343, 92)
	longLift := chordLift(long, tx, ty, 343, 92)

	if shortLift < 3 {
		t.Fatalf("short arc barely curves (lift %d px)", shortLift)
	}
	if longLift <= shortLift {
		t.Fatalf("long arc should bow higher than short: %d vs %d px", longLift, shortLift)
	}
	// Apex stays on screen.
	for _, p := range long {
		if p.Y < 0 || p.Y > 479 || p.X < 0 || p.X > 639 {
			t.Fatalf("arc point off screen: %v", p)
		}
	}
	// Endpoints preserved.
	if short[0].X != tx || short[0].Y != ty || short[len(short)-1].X != jx || short[len(short)-1].Y != jy {
		t.Fatalf("arc endpoints moved: %+v", short)
	}
}

// chordLift measures how far above the straight chord the arc's apex
// rises (pixels).
func chordLift(pts []image.Point, x1, y1, x2, y2 int) int {
	best := 0
	for _, p := range pts {
		// Distance from the chord line: |(y2-y1)x - (x2-x1)y + x2*y1 - y2*x1| / len
		num := abs((y2-y1)*p.X - (x2-x1)*p.Y + x2*y1 - y2*x1)
		d := num / int(math.Sqrt(float64((x2-x1)*(x2-x1)+(y2-y1)*(y2-y1))))
		if d > best {
			best = d
		}
	}
	return best
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// TestWorldMapApproxMarker: an Approx entry draws the hollow ring, not
// the filled dot, at the country centroid position.
func TestWorldMapApproxMarker(t *testing.T) {
	u := New(640, 480)
	// Unknown-grid station resolved via country centroid (Thailand 15,101).
	x, y := u.latLonToScreen(15, 101)
	u.DrawWorldMap([]MapEntry{
		{Lat: 15, Lon: 101, IsCQ: true, Approx: true, Age: 0},
	}, nil)
	// Hollow: the exact centre must not carry the role core colour
	// (the ring itself is the same red, so only the centre proves it).
	r, g, b, _ := u.img.At(x, y).RGBA()
	if r>>8 == 255 && g>>8 == 0 && b>>8 == 0 {
		t.Fatalf("approx marker drew a filled core — should be hollow")
	}
	// Ring core colour (sender-tinted red) must appear near the position.
	ring := false
	for dy := -6; dy <= 6 && !ring; dy++ {
		for dx := -6; dx <= 6; dx++ {
			r, g, b, _ := u.img.At(x+dx, y+dy).RGBA()
			if r>>8 == 255 && g>>8 == 0 && b>>8 == 0 {
				ring = true
				break
			}
		}
	}
	if !ring {
		t.Fatalf("no hollow ring near (%d,%d)", x, y)
	}
}

// TestWorldMapFallback covers the nil-basemap path (flat background, no
// panic, markers still drawn). mapStyle = -1 is never produced by
// CycleMap, so basemap() returns nil and the flat fill kicks in.
func TestWorldMapFallback(t *testing.T) {
	u := New(640, 480)
	u.mapStyle = -1
	u.DrawWorldMap([]MapEntry{{Lat: 15, Lon: 101, IsCQ: true, Age: 0}}, nil)
	r, g, b, _ := u.img.At(320, 240).RGBA()
	if r>>8 != 15 || g>>8 != 17 || b>>8 != 23 {
		t.Fatalf("fallback background wrong: %d %d %d", r>>8, g>>8, b>>8)
	}
}

// TestWorldMapFade: the age fade must actually show — a fresh marker
// paints full-strength colour over the basemap, an old one (age close
// to the 600 s window) blends most of the way back to the basemap, and
// at the window edge it is gone entirely.
func TestWorldMapFade(t *testing.T) {
	x, y := int((0+180)*640/360), int((90-0)*480/180) // equator, mid-Pacific

	uBase := New(640, 480)
	uBase.DrawWorldMap(nil, nil)
	br, bg, bb, _ := uBase.img.At(x, y).RGBA()

	uFresh := New(640, 480)
	uFresh.DrawWorldMap([]MapEntry{{Lat: 0, Lon: 0, Age: 0}}, nil)
	fr, fgn, fb, _ := uFresh.img.At(x, y).RGBA()

	uOld := New(640, 480)
	uOld.DrawWorldMap([]MapEntry{{Lat: 0, Lon: 0, Age: 540 * time.Second}}, nil)
	or, og, ob, _ := uOld.img.At(x, y).RGBA()

	dFresh := dist(int(br>>8), int(bg>>8), int(bb>>8), int(fr>>8), int(fgn>>8), int(fb>>8))
	dOld := dist(int(br>>8), int(bg>>8), int(bb>>8), int(or>>8), int(og>>8), int(ob>>8))
	if dFresh < 100 {
		t.Fatalf("fresh marker too faint (distance %d from basemap)", dFresh)
	}
	// At 540/600 s the fade is 0.1 → the marker must be ~10× closer to
	// the basemap than the fresh one.
	if dOld > dFresh/4 {
		t.Fatalf("aged marker not fading: distance %d vs fresh %d", dOld, dFresh)
	}
	if dOld < 1 {
		t.Fatalf("aged marker vanished entirely before the window ends")
	}

	// Past the window the marker is fully transparent.
	uGone := New(640, 480)
	uGone.DrawWorldMap([]MapEntry{{Lat: 0, Lon: 0, Age: 599 * time.Second}}, nil)
	gr, gg, gb, _ := uGone.img.At(x, y).RGBA()
	dGone := dist(int(br>>8), int(bg>>8), int(bb>>8), int(gr>>8), int(gg>>8), int(gb>>8))
	if dGone > 3 {
		t.Fatalf("marker at window edge still visible (distance %d)", dGone)
	}
}

// TestArcNoLeadingSolidSegment: regression for the solid yellow bar
// that used to cover the first stretch of every arc — math.Mod on a
// negative distance matched "m < 6" for the whole leading run. With a
// large phase the longest consecutive yellow run along the path must
// stay within one dash (≤ 7 px).
func TestArcNoLeadingSolidSegment(t *testing.T) {
	u := New(640, 480)
	u.mapStyle = -1 // flat fallback background
	for _, phase := range []float64{0, 7.3, 100, 390} {
		u.fillBlend(0, 0, u.W, u.H, 15, 17, 23, 255)
		u.drawArc(80, 400, 560, 100, 1.0, phase)
		run, best := 0, 0
		for _, p := range arcPoints(80, 400, 560, 100) {
			r, g, b, _ := u.img.At(p.X, p.Y).RGBA()
			if r>>8 == 255 && g>>8 == 223 && b>>8 == 89 {
				run++
				if run > best {
					best = run
				}
			} else {
				run = 0
			}
		}
		if best > 7 {
			t.Fatalf("phase %.1f: solid yellow run of %d px — leading-segment bug is back", phase, best)
		}
	}
}

func dist(r1, g1, b1, r2, g2, b2 int) int {
	dr, dg, db := r1-r2, g1-g2, b1-b2
	if dr < 0 {
		dr = -dr
	}
	if dg < 0 {
		dg = -dg
	}
	if db < 0 {
		db = -db
	}
	return dr + dg + db
}

// TestWorldMapCycleAndSelection: CycleMap wraps in both directions and
// visits every embedded style; a selection draws its highlight ring and
// the detail panel on the half opposite the station.
func TestWorldMapCycleAndSelection(t *testing.T) {
	u := New(640, 480)
	n := len(mapStyleNames)
	if n < 20 {
		t.Fatalf("expected >= 20 embedded map styles, got %d", n)
	}
	u.CycleMap(1)
	if u.mapStyle != 1 {
		t.Fatalf("CycleMap(1) → %d, want 1", u.mapStyle)
	}
	u.CycleMap(-1) // back to 0
	if u.mapStyle != 0 {
		t.Fatalf("CycleMap(-1) → %d, want 0", u.mapStyle)
	}
	u.CycleMap(-1) // wrap to last
	if u.mapStyle != n-1 {
		t.Fatalf("CycleMap(-1) wrap → %d, want %d", u.mapStyle, n-1)
	}
	if u.MapStyleName() == "" || u.MapStyleName() == "?" {
		t.Fatalf("MapStyleName empty for style %d", u.mapStyle)
	}

	// Station in Thailand (right half of screen) → panel on the left.
	lat, lon, _ := geo.GridToLatLon("OK04")
	sel := &MapSelection{
		Call: "HS0ZKO", Lat: lat, Lon: lon, Grid: "OK04", Country: "Thailand",
		Index: 1, Total: 3,
		Detail: []string{
			"12:00:15 > CQ HS0ZKO OK04",     // CQ → red
			"12:00:30 < ON4ABC HS0ZKO R-07", // report → green
			"12:00:45 > ON4ABC HS0ZKO RR73", // sign-off → blue
			"12:00:50 < ON4ABC HS0ZKO JO65", // directed → orange
		},
	}
	u.CycleMap(n - 1 - u.mapStyle) // back to style 0
	u.DrawWorldMap(nil, sel)
	// Panel border pixel near the left edge (x=6, panel spans y 64..H-40).
	border := false
	for y := 64; y < u.H-40 && !border; y++ {
		r, g, b, _ := u.img.At(6, y).RGBA()
		if r>>8 == 90 && g>>8 == 120 && b>>8 == 160 {
			border = true
		}
	}
	if !border {
		t.Fatal("detail panel not drawn on the left half for a right-half station")
	}
	// Message rows are coloured like the decode window: all four FT8
	// classes must appear inside the panel (left half, below the header).
	classes := map[string][3]uint8{
		"cq":      {255, 90, 90},
		"report":  {100, 255, 100},
		"signoff": {110, 170, 255},
		"call":    {255, 170, 60},
	}
	for name, want := range classes {
		found := false
		for y := 110; y < u.H-60 && !found; y++ {
			for x := 10; x < u.W/2-10; x++ {
				r, g, b, _ := u.img.At(x, y).RGBA()
				if uint8(r>>8) == want[0] && uint8(g>>8) == want[1] && uint8(b>>8) == want[2] {
					found = true
					break
				}
			}
		}
		if !found {
			t.Fatalf("detail panel row colour for %s (%v) not found", name, want)
		}
	}
}
