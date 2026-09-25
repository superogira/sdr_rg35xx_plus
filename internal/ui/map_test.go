package ui

import (
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
		{Lat: lat, Lon: lon, IsCQ: true, Age: 1 * time.Second},
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
// along the sender→recipient line, a red dot at the sender end and a
// green dot at the recipient end. The dash phase travels with wall
// time, so the assertion counts samples over the whole line.
func TestWorldMapArc(t *testing.T) {
	u := New(640, 480)
	lat1, lon1, _ := geo.GridToLatLon("OK04") // Thailand (sender)
	lat2, lon2, _ := geo.GridToLatLon("JO65") // Belgium (recipient)
	u.DrawWorldMap([]MapEntry{
		{Lat: lat2, Lon: lon2, Arc: true, Role: RoleReceiver, FromLat: lat1, FromLon: lon1, Age: 3 * time.Second},
	}, nil)

	arc := 0
	x1, y1 := u.latLonToScreen(lat1, lon1)
	x2, y2 := u.latLonToScreen(lat2, lon2)
	for i := 1; i < 40; i++ {
		tt := float64(i) / 40
		x := x1 + int(float64(x2-x1)*tt)
		y := y1 + int(float64(y2-y1)*tt)
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				r, g, b, _ := u.img.At(x+dx, y+dy).RGBA()
				if r>>8 == 255 && g>>8 == 223 && b>>8 == 89 {
					arc++
				}
			}
		}
	}
	if arc < 8 {
		t.Fatalf("arc from OK04 to JO65 not drawn (only %d coloured samples)", arc)
	}
	if !findCore(u, x1, y1, RoleSender) {
		t.Fatalf("no red marker at sender end")
	}
	if !findCore(u, x2, y2, RoleReceiver) {
		t.Fatalf("no green marker at recipient end")
	}
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
		Detail: []string{"12:00:15 > CQ HS0ZKO OK04"},
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
}
