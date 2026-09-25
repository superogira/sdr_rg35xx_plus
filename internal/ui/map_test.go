package ui

import (
	"testing"
	"time"

	"sdr35/internal/geo"
)

// TestWorldMapBasemapAndMarkers: after DrawWorldMap the basemap must be
// blitted (pixel in the mid-Pacific is NOT the flat fallback colour) and
// a marker for a known grid must land at the calibrated equirectangular
// position (x=(lon+180)/360·W, y=(90-lat)/180·H).
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
	})

	// Basemap present: mid-Pacific (lon -140, lat -20) must not be the
	// flat fallback dark-slate (15,17,23).
	px, py := int((-140+180)*640/360), int((90+20)*480/180)
	r, g, b, _ := u.img.At(px, py).RGBA()
	if r>>8 == 15 && g>>8 == 17 && b>>8 == 23 {
		t.Fatalf("pacific pixel (%d,%d) is the fallback background — basemap not blitted", px, py)
	}

	// Marker dot: the amber core must appear within 3px of the computed
	// position (dot radius 2 + quantisation).
	found := findCore(u, wantX, wantY)
	if !found {
		t.Fatalf("no marker core near (%d,%d) for OK04", wantX, wantY)
	}
}

// findCore scans ±3px for the amber dot core colour.
func findCore(u *UI, x, y int) bool {
	for dy := -3; dy <= 3; dy++ {
		for dx := -3; dx <= 3; dx++ {
			r, g, b, _ := u.img.At(x+dx, y+dy).RGBA()
			if r>>8 == 255 && g>>8 == 170 && b>>8 == 60 {
				return true
			}
		}
	}
	return false
}

// TestWorldMapArc: a QSO entry with Arc set must draw dashed arc pixels
// along the sender→recipient line.
func TestWorldMapArc(t *testing.T) {
	u := New(640, 480)
	lat1, lon1, _ := geo.GridToLatLon("OK04") // Thailand
	lat2, lon2, _ := geo.GridToLatLon("JO65") // Belgium
	u.DrawWorldMap([]MapEntry{
		{Lat: lat2, Lon: lon2, Arc: true, FromLat: lat1, FromLon: lon1, Age: 3 * time.Second},
	})

	arc := 0
	x1, y1 := u.latLonToScreen(lat1, lon1)
	x2, y2 := u.latLonToScreen(lat2, lon2)
	for i := 1; i < 16; i++ {
		tt := float64(i) / 16
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
	if arc < 4 {
		t.Fatalf("arc from OK04 to JO65 not drawn (only %d coloured samples)", arc)
	}
	// Both endpoints get a marker dot.
	if !findCore(u, x1, y1) {
		t.Fatalf("no marker at sender end")
	}
	if !findCore(u, x2, y2) {
		t.Fatalf("no marker at recipient end")
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
	})
	if findCore(u, x, y) {
		t.Fatalf("approx marker drew a filled core — should be hollow")
	}
	// Ring colour (255,200,100) must appear near the position.
	ring := false
	for dy := -6; dy <= 6 && !ring; dy++ {
		for dx := -6; dx <= 6; dx++ {
			r, g, b, _ := u.img.At(x+dx, y+dy).RGBA()
			if r>>8 == 255 && g>>8 == 200 && b>>8 == 100 {
				ring = true
				break
			}
		}
	}
	if !ring {
		t.Fatalf("no hollow ring near (%d,%d)", x, y)
	}
}

// TestWorldMapFallback covers the nil-basemap path (flat background,
// no panic, markers still drawn). The Once is left in its consumed
// state so worldMap() returns the nilled cache without re-decoding.
func TestWorldMapFallback(t *testing.T) {
	saved := worldMapImg
	worldMapImg = nil
	defer func() { worldMapImg = saved }()
	u := New(640, 480)
	u.DrawWorldMap([]MapEntry{{Lat: 15, Lon: 101, IsCQ: true, Age: 0}})
	r, g, b, _ := u.img.At(320, 240).RGBA()
	if r>>8 != 15 || g>>8 != 17 || b>>8 != 23 {
		t.Fatalf("fallback background wrong: %d %d %d", r>>8, g>>8, b>>8)
	}
}
