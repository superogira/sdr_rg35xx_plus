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
		{Grid: "OK04", IsCQ: true, Age: 1 * time.Second},
		{Grid: "JO65", FromGrid: "OK04", Age: 2 * time.Second}, // QSO arc DE→BE
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
	found := false
	for dy := -3; dy <= 3 && !found; dy++ {
		for dx := -3; dx <= 3; dx++ {
			mr, mg, mb, _ := u.img.At(wantX+dx, wantY+dy).RGBA()
			if mr>>8 == 255 && mg>>8 == 170 && mb>>8 == 60 {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatalf("no marker core near (%d,%d) for OK04", wantX, wantY)
	}

	// Arc endpoint: JO65 (Belgium) also gets a dot.
	lat2, lon2, _ := geo.GridToLatLon("JO65")
	x2 := int((lon2 + 180) * 640 / 360)
	y2 := int((90 - lat2) * 480 / 180)
	found2 := false
	for dy := -3; dy <= 3 && !found2; dy++ {
		for dx := -3; dx <= 3; dx++ {
			mr, mg, mb, _ := u.img.At(x2+dx, y2+dy).RGBA()
			if mr>>8 == 255 && mg>>8 == 170 && mb>>8 == 60 {
				found2 = true
				break
			}
		}
	}
	if !found2 {
		t.Fatalf("no marker core near (%d,%d) for JO65", x2, y2)
	}
}

// TestWorldMapArc: a QSO entry with a known FromGrid must draw dashed
// arc pixels along the sender→recipient line (regression: the arc
// condition never fired before the sender/recipient mix-up was fixed).
func TestWorldMapArc(t *testing.T) {
	u := New(640, 480)
	u.DrawWorldMap([]MapEntry{
		{Grid: "JO65", FromGrid: "OK04", Age: 3 * time.Second}, // Thailand → Belgium
	})

	arc := 0
	// Sample the line between the two endpoints for the arc colour.
	lat1, lon1, _ := geo.GridToLatLon("OK04")
	lat2, lon2, _ := geo.GridToLatLon("JO65")
	x1, y1 := u.latLonToScreen(lat1, lon1)
	x2, y2 := u.latLonToScreen(lat2, lon2)
	for i := 1; i < 16; i++ {
		t := float64(i) / 16
		x := x1 + int(float64(x2-x1)*t)
		y := y1 + int(float64(y2-y1)*t)
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
}

// TestWorldMapFallback covers the nil-basemap path (flat background,
// no panic, markers still drawn). The Once is left in its consumed
// state so worldMap() returns the nilled cache without re-decoding.
func TestWorldMapFallback(t *testing.T) {
	saved := worldMapImg
	worldMapImg = nil
	defer func() { worldMapImg = saved }()
	u := New(640, 480)
	u.DrawWorldMap([]MapEntry{{Grid: "OK04", IsCQ: true, Age: 0}})
	r, g, b, _ := u.img.At(320, 240).RGBA()
	if r>>8 != 15 || g>>8 != 17 || b>>8 != 23 {
		t.Fatalf("fallback background wrong: %d %d %d", r>>8, g>>8, b>>8)
	}
}
